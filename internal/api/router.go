package api

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// RouterConfig configures routes and the middleware stack.
type RouterConfig struct {
	APIKey             string   // empty disables auth (local dev)
	TrustedProxies     []string // load balancers allowed to set X-Forwarded-For
	RateLimitPerMinute int
	MaxConcurrentJobs  int
	MaxUploadBytes     int64
	Sinks              []string
	Web                fs.FS // static test page; nil to disable
	Log                *slog.Logger
}

// NewRouter wires the middleware stack and routes. It returns an error for an
// invalid TrustedProxies entry.
func NewRouter(h *Handler, cfg RouterConfig) (*gin.Engine, error) {
	r := gin.New()
	// Only trust X-Forwarded-For from known proxies: otherwise any client could
	// spoof its IP and dodge the per-IP rate limit. nil trusts nobody.
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return nil, fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	r.MaxMultipartMemory = 8 << 20 // larger parts spill to temp files
	r.Use(RequestID(), Recovery(cfg.Log), AccessLog(cfg.Log), SecurityHeaders())

	health := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) }
	r.GET("/healthz", health)
	r.GET("/health", health) // Cloud Run reserves /healthz for its own front end
	r.GET("/favicon.ico", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	// Public, non-sensitive info the test page uses to render itself.
	r.GET("/meta", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"auth_required": cfg.APIKey != "",
			"sinks":         cfg.Sinks,
			"max_upload_mb": cfg.MaxUploadBytes >> 20,
		})
	})

	if cfg.Web != nil {
		// Serve index.html bytes directly: http.FileServer redirects
		// "/index.html" requests to "./", which would loop on "/".
		index, err := fs.ReadFile(cfg.Web, "index.html")
		if err != nil {
			return nil, fmt.Errorf("web files: %w", err)
		}
		r.GET("/", func(c *gin.Context) { c.Data(http.StatusOK, "text/html; charset=utf-8", index) })
		r.StaticFS("/static", http.FS(cfg.Web))
	}

	v1 := r.Group("/v1", RateLimit(cfg.RateLimitPerMinute))
	if cfg.APIKey != "" {
		v1.Use(RequireAPIKey(cfg.APIKey))
	}
	v1.POST("/notes", LimitConcurrency(cfg.MaxConcurrentJobs), h.CreateNote)
	r.NoRoute(func(c *gin.Context) { writeError(c, errNotFound) })

	return r, nil
}
