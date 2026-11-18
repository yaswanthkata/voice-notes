// Package httpx is a small JSON-over-HTTP client shared by connectors.
// It centralises timeouts, auth headers, safe retries and error handling so
// that each connector only has to implement its mapping.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Error is a non-2xx response. It deliberately carries only the status: vendors
// echo submitted values (names, emails) back in error bodies, and error strings
// end up in logs.
type Error struct {
	Status int
}

func (e *Error) Error() string { return fmt.Sprintf("http %d %s", e.Status, http.StatusText(e.Status)) }

// StatusOf returns the HTTP status of err, or 0 if it isn't an *Error.
func StatusOf(err error) int {
	if he, ok := errors.AsType[*Error](err); ok {
		return he.Status
	}
	return 0
}

// Client sends JSON requests to one base URL.
type Client struct {
	BaseURL    string
	Header     http.Header // sent with every request (e.g. Authorization)
	HTTP       *http.Client
	MaxRetries int
}

// New returns a client with sane defaults. The underlying transport pools
// and reuses connections across requests.
func New(baseURL string, header http.Header) *Client {
	return &Client{
		BaseURL: baseURL,
		Header:  header,
		HTTP: &http.Client{
			Timeout: 15 * time.Second,
			// None of our vendor APIs redirect. Following redirects would let a
			// webhook re-send a signed note to another host, even over plain http.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		MaxRetries: 2,
	}
}

// Request describes one API call relative to the client's BaseURL.
type Request struct {
	Method string
	Path   string
	Body   any         // JSON-encoded; json.RawMessage is sent as-is
	Header http.Header // per-request headers (e.g. signatures)
}

// Do sends req and decodes a JSON response into out (if non-nil).
//
// Retry policy: 429 is always retried (the request was not processed).
// Network errors and 5xx are retried only for idempotent methods, because
// replaying a POST could create duplicate CRM records.
func (c *Client) Do(ctx context.Context, req Request, out any) error {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = json.Marshal(req.Body); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	idempotent := req.Method == http.MethodGet || req.Method == http.MethodPut || req.Method == http.MethodDelete

	for attempt := 0; ; attempt++ {
		resp, err := c.send(ctx, req, body)
		retryable := false
		var wait time.Duration
		switch {
		case err != nil:
			retryable = idempotent && ctx.Err() == nil
		case resp.StatusCode == http.StatusTooManyRequests:
			retryable, wait = true, retryAfter(resp)
		case resp.StatusCode >= 500:
			retryable = idempotent
		}

		if err == nil && (!retryable || attempt >= c.MaxRetries) {
			return decode(resp, out)
		}
		if err == nil {
			drain(resp)
		}
		if !retryable || attempt >= c.MaxRetries {
			return err
		}
		if wait == 0 {
			wait = backoff(attempt)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (c *Client) send(ctx context.Context, req Request, body []byte) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, req.Method, c.BaseURL+req.Path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", req.Method, err)
	}
	for k, v := range c.Header {
		r.Header[k] = v
	}
	for k, v := range req.Header {
		r.Header[k] = v
	}
	r.Header.Set("Accept", "application/json")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(r)
	if err != nil {
		// *url.Error embeds the full URL, which can contain PII (e.g. a CRM that looks
		// contacts up by email in the path). Keep only the cause.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("%s request: %w", req.Method, err)
	}
	return resp, nil
}

func decode(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		drain(resp)
		return &Error{Status: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // lets the connection be reused
	resp.Body.Close()
}

func backoff(attempt int) time.Duration {
	base := 300 * time.Millisecond << attempt
	return base + rand.N(base/2) // jitter avoids synchronized retries
}

func retryAfter(resp *http.Response) time.Duration {
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		return min(time.Duration(s)*time.Second, 10*time.Second)
	}
	return 0
}
