// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config is the core service configuration. Connector settings are not part
// of it: each connector reads its own prefixed variables (see package sinks).
type Config struct {
	Port           string
	MaxUploadBytes int64
	AnalyzeTimeout time.Duration // budget for one model call

	// Security / load protection
	APIKey             string   // optional; if set, /v1 requires it
	RateLimitPerMinute int      // per client IP
	MaxConcurrentJobs  int      // concurrent audio analyses
	TrustedProxies     []string // IPs/CIDRs allowed to set X-Forwarded-For

	GeminiAPIKey string
	GeminiModel  string

	// Enabled connectors, e.g. ["hubspot", "webhook"]. Each connector reads
	// its own settings (HUBSPOT_*, WEBHOOK_*, ...) — the core config doesn't
	// know about any vendor. Empty for SINKS=none (analysis only).
	Sinks []string
}

// Load reads .env (if present) and then the process environment.
func Load() (Config, error) {
	_ = godotenv.Load() // optional; real env vars take precedence

	var errs []error
	intVar := func(key string, def int) int {
		n, err := intEnv(key, def)
		errs = append(errs, err)
		return n
	}

	cfg := Config{
		Port:               env("PORT", "8080"),
		MaxUploadBytes:     int64(intVar("MAX_UPLOAD_MB", 20)) << 20,
		AnalyzeTimeout:     time.Duration(intVar("ANALYZE_TIMEOUT_SECONDS", 90)) * time.Second,
		APIKey:             os.Getenv("API_KEY"),
		RateLimitPerMinute: intVar("RATE_LIMIT_PER_MINUTE", 10),
		MaxConcurrentJobs:  intVar("MAX_CONCURRENT_JOBS", 4),
		TrustedProxies:     list(os.Getenv("TRUSTED_PROXIES")),
		GeminiAPIKey:       os.Getenv("GEMINI_API_KEY"),
		GeminiModel:        env("GEMINI_MODEL", "gemini-3.5-flash-lite"),
		Sinks:              list(env("SINKS", "console")),
	}

	if cfg.GeminiAPIKey == "" {
		errs = append(errs, errors.New("GEMINI_API_KEY is required"))
	}
	switch {
	case len(cfg.Sinks) == 1 && cfg.Sinks[0] == "none":
		// Analysis only: nothing is delivered or stored anywhere. Used for
		// public demos, where the console sink would put notes into the logs.
		cfg.Sinks = []string{}
	case len(cfg.Sinks) == 0:
		errs = append(errs, errors.New(`SINKS must list at least one sink, or "none"`))
	}
	return cfg, errors.Join(errs...)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", key, v)
	}
	return n, nil
}

// list splits a comma-separated value, trimming and lowercasing items.
func list(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}
