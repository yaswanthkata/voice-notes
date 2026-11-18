package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRetryPolicy(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		status    int
		wantCalls int32
	}{
		{"GET 503 is retried", http.MethodGet, 503, 3},
		{"POST 503 is not retried (could duplicate records)", http.MethodPost, 503, 1},
		{"POST 429 is retried (not processed)", http.MethodPost, 429, 3},
		{"GET 404 is not retried", http.MethodGet, 404, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			c := New(srv.URL, nil)
			err := c.Do(context.Background(), Request{Method: tc.method, Path: "/x"}, nil)
			if StatusOf(err) != tc.status {
				t.Fatalf("err = %v, want status %d", err, tc.status)
			}
			if calls.Load() != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls.Load(), tc.wantCalls)
			}
		})
	}
}

// Error strings end up in logs, so they must not carry URLs or response
// bodies, which can contain PII.
func TestErrorsDoNotLeakPII(t *testing.T) {
	const email = "sarah.miller@northwind.com"

	t.Run("network error omits URL", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		srv.Close() // connection refused
		c := New(srv.URL, nil)
		c.MaxRetries = 0
		err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/contacts/" + email}, nil)
		if err == nil || strings.Contains(err.Error(), email) {
			t.Fatalf("err = %v; want error without the URL", err)
		}
	})

	t.Run("http error carries only the status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"message":"invalid email `+email+`"}`)
		}))
		defer srv.Close()
		err := New(srv.URL, nil).Do(context.Background(), Request{Method: http.MethodPost, Path: "/x"}, nil)
		if StatusOf(err) != http.StatusBadRequest || strings.Contains(err.Error(), email) {
			t.Fatalf("err = %v; want 400 without the body", err)
		}
	})
}

// A redirect must not be followed: for the webhook it could re-send a signed
// note to another host, even over plain http.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var reached atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer elsewhere.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()

	err := New(redirecting.URL, nil).Do(context.Background(), Request{Method: http.MethodPost, Body: map[string]string{"a": "b"}}, nil)
	if StatusOf(err) != http.StatusTemporaryRedirect || reached.Load() {
		t.Fatalf("err = %v, redirect followed = %v; want a 307 error and no second request", err, reached.Load())
	}
}
