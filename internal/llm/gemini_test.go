package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"voice-notes/internal/domain"
)

// geminiResponse builds a minimal generateContent response body.
func geminiResponse(t *testing.T, text, finishReason string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}},
			"finishReason": finishReason,
		}},
	})
	return string(b)
}

func newTestGemini(t *testing.T, handler http.HandlerFunc) *Gemini {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	g, err := NewGemini(context.Background(), GeminiConfig{APIKey: "test", Model: "test-model", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC) }
	return g
}

func TestAnalyzeSuccess(t *testing.T) {
	audio := domain.Audio{Data: []byte("fake-audio"), MIMEType: "audio/mp4"}
	modelOut := `{"transcript":" Met Anna from ACME. ","language":"en","title":"ACME intro","summary":"Good call.",
		"contact":{"name":"Anna","company":"ACME","email":""},
		"next_steps":[{"title":"Send pricing","due_date":"2026-11-14","type":"document","priority":"high"},
		              {"title":"Follow up","due_date":"next week","type":"fax","priority":""},
		              {"title":"Call back","due_date":"2026-11-13","type":"call","priority":"low"},
		              {"title":"  ","due_date":"","type":"todo","priority":"medium"}],
		"insights":[{"type":"competitor","text":" Also evaluating Contoso. "},{"type":"gossip","text":"x"},{"type":"risk","text":" "}],
		"sentiment":"positive","tags":[" Pricing ","pricing","","crm","a","b","c","d"]}`

	var reqBody map[string]any
	g := newTestGemini(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models/test-model:generateContent") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &reqBody)
		io.WriteString(w, geminiResponse(t, modelOut, "STOP"))
	})

	note, err := g.Analyze(context.Background(), audio)
	if err != nil {
		t.Fatal(err)
	}

	// Request: audio sent inline with the right MIME type, schema attached, date in prompt.
	raw, _ := json.Marshal(reqBody)
	s := string(raw)
	for _, want := range []string{
		`"mimeType":"audio/mp4"`,
		`"data":"` + base64.StdEncoding.EncodeToString(audio.Data) + `"`,
		`"responseJsonSchema"`,
		`"insights"`,
		`Today is 2026-11-12 (Thursday)`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("request missing %s", want)
		}
	}

	// Response: parsed and normalized.
	if note.Transcript != "Met Anna from ACME." {
		t.Errorf("transcript not trimmed: %q", note.Transcript)
	}
	if note.Contact.Company != "ACME" || len(note.NextSteps) != 3 {
		t.Errorf("unexpected note: %+v", note)
	}
	if note.NextSteps[0].DueDate != "2026-11-14" || note.NextSteps[1].DueDate != "" {
		t.Errorf("due dates not normalized: %+v", note.NextSteps)
	}
	if s := note.NextSteps; s[0].Type != "document" || s[0].Priority != "high" || s[1].Type != "todo" || s[1].Priority != "medium" {
		t.Errorf("type/priority not kept or defaulted: %+v", s)
	}
	if p := note.NextSteps[2].Priority; p != "high" {
		t.Errorf("task due tomorrow has priority %q, want high", p)
	}
	if len(note.Insights) != 1 || note.Insights[0] != (domain.Insight{Type: "competitor", Text: "Also evaluating Contoso."}) {
		t.Errorf("insights not cleaned (unknown types and empty text dropped): %+v", note.Insights)
	}
	if got, want := strings.Join(note.Tags, ","), "pricing,crm,a,b,c"; got != want {
		t.Errorf("tags = %q, want %q (trimmed, lowercased, deduplicated, max 5)", got, want)
	}
}

func TestAnalyzeErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		ctx     func() (context.Context, context.CancelFunc)
		wantIs  error
	}{
		{
			name: "no speech",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, geminiResponse(t, `{"transcript":"","next_steps":[],"tags":[]}`, "STOP"))
			},
			wantIs: domain.ErrNoSpeech,
		},
		{
			name: "truncated output",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, geminiResponse(t, `{"transcript":"abc`, "MAX_TOKENS"))
			},
		},
		{
			name: "api error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`)
			},
		},
		{
			name: "server-side deadline (504)",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusGatewayTimeout)
				io.WriteString(w, `{"error":{"code":504,"message":"Deadline expired before operation could complete.","status":"DEADLINE_EXCEEDED"}}`)
			},
			wantIs: context.DeadlineExceeded,
		},
		{
			name: "timeout",
			handler: func(w http.ResponseWriter, r *http.Request) {
				// Outlive the client deadline, but bounded so srv.Close doesn't block.
				select {
				case <-r.Context().Done():
				case <-time.After(time.Second):
				}
			},
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 50*time.Millisecond)
			},
			wantIs: context.DeadlineExceeded,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newTestGemini(t, tc.handler)
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if tc.ctx != nil {
				ctx, cancel = tc.ctx()
			}
			defer cancel()

			start := time.Now()
			_, err := g.Analyze(ctx, domain.Audio{Data: []byte("x"), MIMEType: "audio/wav"})
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("Analyze took %s; context cancellation not honoured", elapsed)
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("err = %v, want errors.Is %v", err, tc.wantIs)
			}
		})
	}
}

func TestAnalyzeRetriesOverloadButNotQuota(t *testing.T) {
	ok := geminiResponse(t, `{"transcript":"hello","language":"en","title":"t","summary":"s","contact":{"name":"","company":"","email":""},"next_steps":[],"sentiment":"neutral","tags":[]}`, "STOP")

	t.Run("503 then success is retried", func(t *testing.T) {
		var calls atomic.Int32
		g := newTestGemini(t, func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, `{"error":{"code":503,"message":"This model is currently experiencing high demand.","status":"UNAVAILABLE"}}`)
				return
			}
			io.WriteString(w, ok)
		})
		if _, err := g.Analyze(context.Background(), domain.Audio{Data: []byte("x"), MIMEType: "audio/wav"}); err != nil {
			t.Fatalf("want success after retry, got %v", err)
		}
		if calls.Load() != 2 {
			t.Fatalf("calls = %d, want 2", calls.Load())
		}
	})

	t.Run("persistent 503 maps to ErrModelBusy", func(t *testing.T) {
		g := newTestGemini(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":{"code":503,"message":"high demand","status":"UNAVAILABLE"}}`)
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := g.Analyze(ctx, domain.Audio{Data: []byte("x"), MIMEType: "audio/wav"})
		if !errors.Is(err, domain.ErrModelBusy) {
			t.Fatalf("err = %v, want ErrModelBusy", err)
		}
	})

	t.Run("429 is not retried and maps to ErrQuotaExceeded", func(t *testing.T) {
		var calls atomic.Int32
		g := newTestGemini(t, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`)
		})
		_, err := g.Analyze(context.Background(), domain.Audio{Data: []byte("x"), MIMEType: "audio/wav"})
		if !errors.Is(err, domain.ErrQuotaExceeded) || calls.Load() != 1 {
			t.Fatalf("err = %v calls = %d; want ErrQuotaExceeded after 1 call", err, calls.Load())
		}
	})
}
