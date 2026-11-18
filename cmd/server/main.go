package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"voice-notes/internal/api"
	"voice-notes/internal/config"
	"voice-notes/internal/llm"
	"voice-notes/internal/sinks"
	"voice-notes/web"

	// Connectors register themselves with the sinks registry. Adding a new
	// CRM or destination = one package + one import line here.
	_ "voice-notes/internal/sinks/console"
	_ "voice-notes/internal/sinks/notion"
	_ "voice-notes/internal/sinks/sheets"
	_ "voice-notes/internal/sinks/webhook"
)

// Timeouts around the analysis budget (AnalyzeTimeout, from config).
const (
	deliveryTimeout   = 30 * time.Second // all connectors, in parallel
	readHeaderTimeout = 10 * time.Second // slowloris protection
	readTimeout       = 60 * time.Second // uploading the audio
	writeSlack        = 15 * time.Second // encoding and sending the response
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// run wires the dependencies and serves until SIGINT/SIGTERM, then drains
// in-flight requests. main.go is the only place concrete types meet.
func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if os.Getenv(gin.EnvGinMode) == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	analyzer, err := llm.NewGemini(ctx, llm.GeminiConfig{APIKey: cfg.GeminiAPIKey, Model: cfg.GeminiModel})
	if err != nil {
		return fmt.Errorf("gemini: %w", err)
	}

	connectors, err := sinks.Build(cfg.Sinks, os.Getenv)
	if err != nil {
		return fmt.Errorf("connectors: %w", err)
	}

	if cfg.APIKey == "" {
		log.Warn("API_KEY not set: /v1 endpoints are unauthenticated (fine for local dev only)")
	}

	handler := api.NewHandler(analyzer, sinks.NewFanout(connectors...), api.HandlerConfig{
		Sinks:           cfg.Sinks,
		MaxUploadBytes:  cfg.MaxUploadBytes,
		AnalyzeTimeout:  cfg.AnalyzeTimeout,
		DeliveryTimeout: deliveryTimeout,
	}, log)

	router, err := api.NewRouter(handler, api.RouterConfig{
		APIKey:             cfg.APIKey,
		TrustedProxies:     cfg.TrustedProxies,
		RateLimitPerMinute: cfg.RateLimitPerMinute,
		MaxConcurrentJobs:  cfg.MaxConcurrentJobs,
		MaxUploadBytes:     cfg.MaxUploadBytes,
		Sinks:              cfg.Sinks,
		Web:                web.Static(),
		Log:                log,
	})
	if err != nil {
		return err
	}

	// The slowest legitimate request uploads, is analysed and then delivered;
	// the server must not cut it off after the CRM has already been written.
	maxRequest := readTimeout + cfg.AnalyzeTimeout + deliveryTimeout + writeSlack
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      maxRequest,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", "http://localhost:"+cfg.Port, "sinks", cfg.Sinks, "model", cfg.GeminiModel)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down, draining in-flight requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), maxRequest)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
