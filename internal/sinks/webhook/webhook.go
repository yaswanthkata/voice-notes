// Package webhook is a generic connector: it POSTs each note as a signed,
// versioned JSON event to any URL. This integrates with Zapier, Make, n8n,
// Slack workflows or an in-house service without writing a new connector.
//
// Config:
//
//	WEBHOOK_URL     required, https (http allowed only for localhost)
//	WEBHOOK_SECRET  optional; if set, requests carry an HMAC-SHA256 signature
//
// Receivers verify X-Signature-256 = "sha256=" + hex(HMAC(secret, timestamp + "." + body))
// and reject stale X-Timestamp values to prevent replay.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"voice-notes/internal/domain"
	"voice-notes/internal/httpx"
	"voice-notes/internal/sinks"
)

func init() {
	sinks.Register("webhook", func(env sinks.Env) (sinks.Sink, error) {
		return New(Config{URL: env("WEBHOOK_URL"), Secret: env("WEBHOOK_SECRET")})
	})
}

// Config is the webhook connector's configuration.
type Config struct {
	URL    string
	Secret string // optional; enables request signing
}

// Event is the versioned payload contract. Additive changes only within v1.
type Event struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"` // "note.created"
	Version   string      `json:"version"`
	CreatedAt time.Time   `json:"created_at"`
	Data      domain.Note `json:"data"`
}

// Sink posts note.created events to one URL.
type Sink struct {
	api    *httpx.Client
	secret []byte
	now    func() time.Time
}

// New validates cfg and returns a Sink. Plain http is only allowed for
// loopback addresses, so notes never cross a network unencrypted.
func New(cfg Config) (*Sink, error) {
	u, err := url.Parse(cfg.URL)
	if cfg.URL == "" || err != nil || u.Host == "" {
		return nil, errors.New("WEBHOOK_URL must be an absolute URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, errors.New("WEBHOOK_URL must use https (http only for localhost)")
	}
	c := httpx.New(cfg.URL, nil)
	c.MaxRetries = 0 // receivers may not be idempotent; the event ID lets them dedupe if we add retries
	return &Sink{api: c, secret: []byte(cfg.Secret), now: time.Now}, nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// Name implements sinks.Sink.
func (*Sink) Name() string { return "webhook" }

// Push posts the note as a note.created event. The receipt ID is the event ID,
// which receivers can use to deduplicate.
func (s *Sink) Push(ctx context.Context, note domain.Note) (domain.Receipt, error) {
	ev := Event{ID: newID(), Type: "note.created", Version: "1", CreatedAt: s.now().UTC(), Data: note}
	body, err := json.Marshal(ev)
	if err != nil {
		return domain.Receipt{}, fmt.Errorf("encode event: %w", err)
	}

	h := http.Header{"X-Event-Id": {ev.ID}}
	if len(s.secret) > 0 {
		ts := strconv.FormatInt(ev.CreatedAt.Unix(), 10)
		h.Set("X-Timestamp", ts)
		h.Set("X-Signature-256", "sha256="+Sign(s.secret, ts, body))
	}

	if err := s.api.Do(ctx, httpx.Request{Method: http.MethodPost, Body: json.RawMessage(body), Header: h}, nil); err != nil {
		return domain.Receipt{}, fmt.Errorf("post event: %w", err)
	}
	return domain.Receipt{ID: ev.ID}, nil
}

// Sign computes the hex HMAC-SHA256 of "timestamp.body".
func Sign(secret []byte, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(timestamp + "."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func newID() string { return "evt_" + rand.Text() }
