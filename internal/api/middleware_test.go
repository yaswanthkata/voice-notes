package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"voice-notes/internal/domain"
)

func TestAPIKeyAuth(t *testing.T) {
	r := newRouter(&fakeAnalyzer{note: domain.Note{Title: "x"}}, allOK, 1<<20, RouterConfig{APIKey: "s3cret"})

	tests := []struct {
		name   string
		header string
		value  string
		want   int
	}{
		{"missing", "", "", http.StatusUnauthorized},
		{"wrong bearer", "Authorization", "Bearer nope", http.StatusUnauthorized},
		{"bearer", "Authorization", "Bearer s3cret", http.StatusCreated},
		{"x-api-key", "X-API-Key", "s3cret", http.StatusCreated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := uploadRequest(t, "audio", minimalWAV())
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}

	// Public endpoints stay open.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
}

func TestRateLimit(t *testing.T) {
	// 4/min => burst of 1: second immediate request is limited.
	r := newRouter(&fakeAnalyzer{note: domain.Note{Title: "x"}}, allOK, 1<<20, RouterConfig{RateLimitPerMinute: 4})
	codes := []int{}
	for range 2 {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, uploadRequest(t, "audio", minimalWAV()))
		codes = append(codes, rec.Code)
	}
	if codes[0] != http.StatusCreated || codes[1] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v, want [201 429]", codes)
	}
}

func TestConcurrencyLimitSheds(t *testing.T) {
	block := make(chan struct{})
	r := newRouter(&fakeAnalyzer{note: domain.Note{Title: "x"}, block: block}, allOK, 1<<20, RouterConfig{MaxConcurrentJobs: 1})

	var wg sync.WaitGroup
	first := httptest.NewRecorder()
	wg.Go(func() { r.ServeHTTP(first, uploadRequest(t, "audio", minimalWAV())) })
	time.Sleep(50 * time.Millisecond) // let the first request take the only slot

	second := httptest.NewRecorder()
	r.ServeHTTP(second, uploadRequest(t, "audio", minimalWAV()))
	close(block)
	wg.Wait()

	if second.Code != http.StatusServiceUnavailable || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second = %d, want 503 with Retry-After", second.Code)
	}
	if first.Code != http.StatusCreated {
		t.Fatalf("first = %d, want 201", first.Code)
	}
}

func TestSecurityHeadersAndRequestID(t *testing.T) {
	r := newRouter(&fakeAnalyzer{}, allOK, 1<<20, RouterConfig{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "abc-123")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	if got := rec.Header().Get("X-Request-ID"); got != "abc-123" {
		t.Errorf("request id = %q, want propagated abc-123", got)
	}

	// Malicious IDs (log injection) are replaced.
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "evil\nline")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-ID"); got == "" || got == "evil\nline" {
		t.Errorf("request id not sanitized: %q", got)
	}
}

func TestHealthEndpoints(t *testing.T) {
	r := newRouter(&fakeAnalyzer{}, allOK, 1<<20, RouterConfig{})
	for _, path := range []string{"/healthz", "/health"} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func TestUnknownRouteUsesErrorContract(t *testing.T) {
	rec := httptest.NewRecorder()
	newRouter(&fakeAnalyzer{}, allOK, 1<<20, RouterConfig{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/nope", nil))
	var resp errorResponse
	if json.Unmarshal(rec.Body.Bytes(), &resp) != nil || rec.Code != http.StatusNotFound || resp.Code != "not_found" {
		t.Fatalf("got %d %s; want 404 JSON error", rec.Code, rec.Body)
	}
}

func TestRecoveryReturnsContractAndHidesKeys(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	h := NewHandler(analyzerFunc(func(context.Context, domain.Audio) (domain.Note, error) { panic("bug") }),
		allOK, HandlerConfig{MaxUploadBytes: 1 << 20, AnalyzeTimeout: time.Second, DeliveryTimeout: time.Second}, log)
	r, err := NewRouter(h, RouterConfig{RateLimitPerMinute: 100, MaxConcurrentJobs: 1, Log: log})
	if err != nil {
		t.Fatal(err)
	}

	req := uploadRequest(t, "audio", minimalWAV())
	req.Header.Set("X-API-Key", "super-secret-key")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusInternalServerError || resp.Code != "internal_error" || resp.RequestID == "" {
		t.Fatalf("got %d %s; want 500 with the JSON error contract", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), "panic recovered") || strings.Contains(logs.String(), "super-secret-key") {
		t.Fatalf("panic must be logged without request headers: %s", logs.String())
	}
}
