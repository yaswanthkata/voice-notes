package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

const requestIDKey = "request_id"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestID propagates a sane incoming X-Request-ID or generates one, so a
// request can be traced across our logs and the client's.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if !validRequestID.MatchString(id) {
			id = rand.Text()
		}
		c.Set(requestIDKey, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// Recovery turns a panic into the standard JSON error and one structured log
// line. gin.Recovery is not used because it dumps request headers to stderr,
// masking Authorization but not X-API-Key.
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, err any) {
		log.Error("panic recovered",
			"request_id", c.GetString(requestIDKey),
			"panic", fmt.Sprint(err),
			"stack", string(debug.Stack()))
		writeError(c, errInternal)
	})
}

// AccessLog writes one structured line per request. Bodies, query strings
// and headers are never logged (audio/transcripts are PII; headers hold keys).
func AccessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("request",
			"request_id", c.GetString(requestIDKey),
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}

// SecurityHeaders sets a strict baseline. The test page only loads its own
// static files, so the CSP needs no inline script or third-party origins.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "microphone=(self), camera=(), geolocation=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"media-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		c.Next()
	}
}

// RequireAPIKey accepts "Authorization: Bearer <key>" or "X-API-Key: <key>".
// Keys are compared as SHA-256 digests in constant time, which avoids
// leaking the key (or its length) through timing.
func RequireAPIKey(key string) gin.HandlerFunc {
	want := sha256.Sum256([]byte(key))
	return func(c *gin.Context) {
		got := c.GetHeader("X-API-Key")
		if bearer, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer "); ok {
			got = bearer
		}
		sum := sha256.Sum256([]byte(got))
		if got == "" || subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
			c.Header("WWW-Authenticate", `Bearer realm="voice-notes"`)
			writeError(c, errUnauthorized)
			return
		}
		c.Next()
	}
}

// RateLimit applies a token bucket per client IP. It runs before auth so
// key-guessing is throttled too. In-memory: fine for one instance; behind
// a load balancer this moves to the gateway or Redis.
func RateLimit(perMinute int) gin.HandlerFunc {
	type entry struct {
		lim  *rate.Limiter
		seen time.Time
	}
	var (
		mu      sync.Mutex
		clients = map[string]*entry{}
		sweep   = time.Now()
	)
	burst := max(1, perMinute/4)

	return func(c *gin.Context) {
		now := time.Now()
		mu.Lock()
		if now.Sub(sweep) > time.Minute { // drop idle clients to bound memory
			for ip, e := range clients {
				if now.Sub(e.seen) > 10*time.Minute {
					delete(clients, ip)
				}
			}
			sweep = now
		}
		e, ok := clients[c.ClientIP()]
		if !ok {
			e = &entry{lim: rate.NewLimiter(rate.Limit(float64(perMinute)/60), burst)}
			clients[c.ClientIP()] = e
		}
		e.seen = now
		allowed := e.lim.Allow()
		mu.Unlock()

		if !allowed {
			writeError(c, errRateLimited)
			return
		}
		c.Next()
	}
}

// LimitConcurrency caps in-flight requests. Each analysis holds up to
// MAX_UPLOAD_MB in memory and costs model tokens, so we shed load early with
// 503 instead of queueing unboundedly. It runs before the body is read.
func LimitConcurrency(n int) gin.HandlerFunc {
	sem := make(chan struct{}, n)
	return func(c *gin.Context) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			c.Next()
		default:
			writeError(c, errServerBusy)
		}
	}
}
