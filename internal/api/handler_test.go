package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"voice-notes/internal/domain"
)

func init() { gin.SetMode(gin.TestMode) }

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeAnalyzer struct {
	note  domain.Note
	err   error
	got   domain.Audio
	block chan struct{} // if set, Analyze waits on it
}

func (f *fakeAnalyzer) Analyze(_ context.Context, a domain.Audio) (domain.Note, error) {
	f.got = a
	if f.block != nil {
		<-f.block
	}
	return f.note, f.err
}

type fakeDispatcher struct{ results []domain.Delivery }

func (f fakeDispatcher) Dispatch(context.Context, domain.Note, []string) []domain.Delivery {
	return f.results
}

var allOK = fakeDispatcher{[]domain.Delivery{{Sink: "a", OK: true}, {Sink: "b", OK: true}}}

// minimalWAV returns a valid 44-byte WAV header followed by silence.
func minimalWAV() []byte {
	var b bytes.Buffer
	dataLen := uint32(1600)
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, 36+dataLen)
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))     // PCM
	binary.Write(&b, binary.LittleEndian, uint16(1))     // mono
	binary.Write(&b, binary.LittleEndian, uint32(8000))  // sample rate
	binary.Write(&b, binary.LittleEndian, uint32(16000)) // byte rate
	binary.Write(&b, binary.LittleEndian, uint16(2))     // block align
	binary.Write(&b, binary.LittleEndian, uint16(16))    // bits per sample
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, dataLen)
	b.Write(make([]byte, dataLen))
	return b.Bytes()
}

func uploadRequest(t *testing.T, field string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(field, "memo.wav")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/notes", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func newRouter(a Analyzer, d Dispatcher, maxBytes int64, rc RouterConfig) http.Handler {
	h := NewHandler(a, d, HandlerConfig{MaxUploadBytes: maxBytes, AnalyzeTimeout: 5 * time.Second, DeliveryTimeout: 5 * time.Second}, discard)
	if rc.RateLimitPerMinute == 0 {
		rc.RateLimitPerMinute = 1000
	}
	if rc.MaxConcurrentJobs == 0 {
		rc.MaxConcurrentJobs = 10
	}
	rc.Log = discard
	r, err := NewRouter(h, rc)
	if err != nil {
		panic(err)
	}
	return r
}

func TestCreateNote(t *testing.T) {
	wav := minimalWAV()
	partial := fakeDispatcher{[]domain.Delivery{{Sink: "a", OK: true}, {Sink: "b", Error: "delivery failed"}}}
	allFail := fakeDispatcher{[]domain.Delivery{{Sink: "a", Error: "delivery failed"}}}

	tests := []struct {
		name       string
		field      string
		content    []byte
		maxBytes   int64
		analyzer   *fakeAnalyzer
		dispatcher fakeDispatcher
		wantStatus int
		wantNote   bool
	}{
		{"success", "audio", wav, 1 << 20, &fakeAnalyzer{note: domain.Note{Title: "Call with ACME"}}, allOK, http.StatusCreated, true},
		{"missing field", "file", wav, 1 << 20, &fakeAnalyzer{}, allOK, http.StatusBadRequest, false},
		{"not audio", "audio", []byte("hello, I am a text file"), 1 << 20, &fakeAnalyzer{}, allOK, http.StatusUnsupportedMediaType, false},
		{"too large", "audio", wav, 512, &fakeAnalyzer{}, allOK, http.StatusRequestEntityTooLarge, false},
		{"body over limit plus overhead", "audio", append(wav, make([]byte, multipartOverhead+1024)...), 512, &fakeAnalyzer{}, allOK, http.StatusRequestEntityTooLarge, false},
		{"analyzer fails", "audio", wav, 1 << 20, &fakeAnalyzer{err: errors.New("boom")}, allOK, http.StatusBadGateway, false},
		{"no speech", "audio", wav, 1 << 20, &fakeAnalyzer{err: domain.ErrNoSpeech}, allOK, http.StatusUnprocessableEntity, false},
		{"analyzer timeout", "audio", wav, 1 << 20, &fakeAnalyzer{err: context.DeadlineExceeded}, allOK, http.StatusGatewayTimeout, false},
		{"analysis only (no sinks)", "audio", wav, 1 << 20, &fakeAnalyzer{note: domain.Note{Title: "x"}}, fakeDispatcher{[]domain.Delivery{}}, http.StatusCreated, true},
		{"partial delivery", "audio", wav, 1 << 20, &fakeAnalyzer{note: domain.Note{Title: "x"}}, partial, http.StatusMultiStatus, true},
		{"all deliveries fail keeps note", "audio", wav, 1 << 20, &fakeAnalyzer{note: domain.Note{Title: "x"}}, allFail, http.StatusBadGateway, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newRouter(tc.analyzer, tc.dispatcher, tc.maxBytes, RouterConfig{}).ServeHTTP(rec, uploadRequest(t, tc.field, tc.content))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body)
			}
			var resp createNoteResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			if gotNote := resp.Note.Title != ""; gotNote != tc.wantNote {
				t.Fatalf("note present = %v, want %v; body=%s", gotNote, tc.wantNote, rec.Body)
			}
			if resp.RequestID == "" {
				t.Fatal("response missing request_id")
			}
			if tc.wantStatus == http.StatusCreated && tc.analyzer.got.MIMEType != "audio/wav" {
				t.Fatalf("mime = %q, want audio/wav", tc.analyzer.got.MIMEType)
			}
		})
	}
}

func TestAnalyzeErrorResponses(t *testing.T) {
	tests := []struct {
		err        error
		status     int
		code       string
		retryable  bool
		retryAfter string
	}{
		{domain.ErrNoSpeech, http.StatusUnprocessableEntity, "no_speech", false, ""},
		{fmt.Errorf("gemini x: %w: 503 high demand", domain.ErrModelBusy), http.StatusServiceUnavailable, "model_busy", true, "10"},
		{fmt.Errorf("gemini x: %w", domain.ErrQuotaExceeded), http.StatusTooManyRequests, "model_quota_exceeded", true, "60"},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "model_timeout", true, "5"},
		{errors.New("secret provider detail"), http.StatusBadGateway, "analysis_failed", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newRouter(&fakeAnalyzer{err: tc.err}, allOK, 1<<20, RouterConfig{}).ServeHTTP(rec, uploadRequest(t, "audio", minimalWAV()))

			var resp errorResponse
			json.Unmarshal(rec.Body.Bytes(), &resp)
			if rec.Code != tc.status || resp.Code != tc.code || resp.Retryable != tc.retryable {
				t.Fatalf("got %d %q retryable=%v; want %d %q %v", rec.Code, resp.Code, resp.Retryable, tc.status, tc.code, tc.retryable)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.retryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, tc.retryAfter)
			}
			if resp.Error == "" || strings.Contains(resp.Error, "secret") || strings.Contains(resp.Error, "503") {
				t.Fatalf("client message missing or leaks internals: %q", resp.Error)
			}
		})
	}
}

// analyzerFunc adapts a function to Analyzer.
type analyzerFunc func(context.Context, domain.Audio) (domain.Note, error)

func (f analyzerFunc) Analyze(ctx context.Context, a domain.Audio) (domain.Note, error) {
	return f(ctx, a)
}

func TestCreateNoteClientDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	waitForCancel := analyzerFunc(func(ctx context.Context, _ domain.Audio) (domain.Note, error) {
		cancel() // the client hangs up mid-analysis
		<-ctx.Done()
		return domain.Note{}, ctx.Err()
	})
	dispatched := false
	d := dispatcherFunc(func() { dispatched = true })

	rec := httptest.NewRecorder()
	newRouter(waitForCancel, d, 1<<20, RouterConfig{}).ServeHTTP(rec, uploadRequest(t, "audio", minimalWAV()).WithContext(ctx))

	if rec.Code != statusClientClosed || rec.Body.Len() != 0 {
		t.Fatalf("got %d %q; want %d with no body (not a 502)", rec.Code, rec.Body, statusClientClosed)
	}
	if dispatched {
		t.Fatal("nothing should be delivered when analysis was cancelled")
	}
}

type dispatcherFunc func()

func (f dispatcherFunc) Dispatch(context.Context, domain.Note, []string) []domain.Delivery {
	f()
	return nil
}

type recordingDispatcher struct {
	called bool
	only   []string
}

func (r *recordingDispatcher) Dispatch(_ context.Context, _ domain.Note, only []string) []domain.Delivery {
	r.called, r.only = true, only
	return []domain.Delivery{}
}

func TestCreateNoteSinkSelection(t *testing.T) {
	tests := []struct {
		name       string
		sinks      *string // nil: field not sent
		wantStatus int
		wantOnly   []string // nil: all destinations
		analyzed   bool
	}{
		{name: "field absent means all", wantStatus: http.StatusCreated, analyzed: true},
		{name: "subset", sinks: ptr("sheets"), wantStatus: http.StatusCreated, wantOnly: []string{"sheets"}, analyzed: true},
		{name: "case, spaces and duplicates", sinks: ptr(" Notion , sheets,notion"), wantStatus: http.StatusCreated, wantOnly: []string{"notion", "sheets"}, analyzed: true},
		{name: "none means analysis only", sinks: ptr("none"), wantStatus: http.StatusCreated, wantOnly: []string{}, analyzed: true},
		{name: "unknown is rejected before analysis", sinks: ptr("notion,salesforce"), wantStatus: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			analyzer := &fakeAnalyzer{note: domain.Note{Title: "x"}}
			d := &recordingDispatcher{}
			h := NewHandler(analyzer, d, HandlerConfig{Sinks: []string{"notion", "sheets"}, MaxUploadBytes: 1 << 20,
				AnalyzeTimeout: time.Second, DeliveryTimeout: time.Second}, discard)
			r, _ := NewRouter(h, RouterConfig{RateLimitPerMinute: 100, MaxConcurrentJobs: 1, Log: discard})

			var buf bytes.Buffer
			w := multipart.NewWriter(&buf)
			fw, _ := w.CreateFormFile("audio", "memo.wav")
			fw.Write(minimalWAV())
			if tc.sinks != nil {
				w.WriteField("sinks", *tc.sinks)
			}
			w.Close()
			req := httptest.NewRequest(http.MethodPost, "/v1/notes", &buf)
			req.Header.Set("Content-Type", w.FormDataContentType())
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body)
			}
			if analyzed := analyzer.got.MIMEType != ""; analyzed != tc.analyzed {
				t.Fatalf("analyzed = %v, want %v", analyzed, tc.analyzed)
			}
			if !tc.analyzed {
				if !strings.Contains(rec.Body.String(), "unknown_destination") {
					t.Fatalf("body = %s", rec.Body)
				}
				return
			}
			if (d.only == nil) != (tc.wantOnly == nil) || strings.Join(d.only, ",") != strings.Join(tc.wantOnly, ",") {
				t.Fatalf("dispatched to %#v, want %#v", d.only, tc.wantOnly)
			}
		})
	}
}

func TestSinkSelectionWithoutDestinations(t *testing.T) {
	h := NewHandler(&fakeAnalyzer{}, &recordingDispatcher{}, HandlerConfig{Sinks: []string{}, MaxUploadBytes: 1 << 20,
		AnalyzeTimeout: time.Second, DeliveryTimeout: time.Second}, discard)
	r, _ := NewRouter(h, RouterConfig{RateLimitPerMinute: 100, MaxConcurrentJobs: 1, Log: discard})

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("audio", "memo.wav")
	fw.Write(minimalWAV())
	w.WriteField("sinks", "hubspot")
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/notes", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var resp errorResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != http.StatusBadRequest || strings.Contains(resp.Error, "Available: .") || !strings.Contains(resp.Error, "no destinations") {
		t.Fatalf("got %d %q; want a clear 400 when nothing is configured", rec.Code, resp.Error)
	}
}

func ptr(s string) *string { return &s }
