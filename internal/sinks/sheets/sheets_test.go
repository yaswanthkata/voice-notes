package sheets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"voice-notes/internal/domain"
	"voice-notes/internal/httpx"
)

const sheetID = "1AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"

var fixedNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

func sampleNote() domain.Note {
	return domain.Note{
		Title:      "Northwind intro",
		Summary:    "Liked the demo.",
		Transcript: "I met Sarah.",
		Language:   "en",
		Contact:    domain.Contact{Name: "Sarah Miller", Company: "Northwind", Email: "sarah@northwind.com"},
		NextSteps: []domain.Task{
			{Title: "Send pricing", DueDate: "2026-11-20", Type: "document", Priority: "high"},
			{Title: "Book IT call", Type: "call", Priority: "medium"},
		},
		Insights:  []domain.Insight{{Type: "competitor", Text: "Also evaluating Contoso."}},
		Sentiment: "positive",
		Tags:      []string{"pricing", "it"},
	}
}

// --- mapper (pure) ---

func TestRow(t *testing.T) {
	got := row(sampleNote(), fixedNow)
	want := []any{
		"2026-11-16 09:30", "Northwind intro", "Liked the demo.", "Sarah Miller", "Northwind", "sarah@northwind.com",
		"positive",
		"• Send pricing (document, high priority, due 2026-11-20)\n• Book IT call (call, medium priority)",
		"• Competitor: Also evaluating Contoso.",
		"pricing, it", "en", "I met Sarah.",
	}
	if len(got) != len(header) {
		t.Fatalf("row has %d cells, header has %d", len(got), len(header))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cell %d (%s) = %q, want %q", i, header[i], got[i], want[i])
		}
	}
}

func TestRowRespectsCellLimit(t *testing.T) {
	n := sampleNote()
	n.Transcript = strings.Repeat("ä", maxCell+100)
	cell := row(n, fixedNow)[11].(string)
	if l := len([]rune(cell)); l != maxCell {
		t.Fatalf("transcript cell has %d characters, want %d", l, maxCell)
	}
}

func TestNewParsesSpreadsheetID(t *testing.T) {
	tok := func(context.Context) (string, error) { return "t", nil }
	tests := []struct {
		in      string
		wantErr bool
	}{
		{in: sheetID},
		{in: "https://docs.google.com/spreadsheets/d/" + sheetID + "/edit#gid=0"},
		{in: "", wantErr: true},
		{in: "not an id", wantErr: true},
		{in: "https://example.com/sheet", wantErr: true},
	}
	for _, tc := range tests {
		s, err := New(Config{Spreadsheet: tc.in, Token: tok})
		if (err != nil) != tc.wantErr {
			t.Errorf("New(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if err == nil && s.id != sheetID {
			t.Errorf("New(%q) id = %q", tc.in, s.id)
		}
	}
}

// --- API flow against a fake Sheets API ---

type fakeSheets struct {
	mu         sync.Mutex
	calls      []string
	auth       []string
	hasHeader  bool
	appended   [][]any
	failWith   int // status for the append call, if non-zero
	failHeader int // status for the header GET, if non-zero
}

func (f *fakeSheets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	var body struct {
		Values [][]any `json:"values"`
	}
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &body)

	switch {
	case r.Method == http.MethodGet && f.failHeader != 0:
		w.WriteHeader(f.failHeader)
	case r.Method == http.MethodGet:
		if f.hasHeader {
			io.WriteString(w, `{"values":[["Created (UTC)"]]}`)
		} else {
			io.WriteString(w, `{}`)
		}
	case r.Method == http.MethodPut:
		f.hasHeader = true
		io.WriteString(w, `{}`)
	case r.Method == http.MethodPost && f.failWith != 0:
		w.WriteHeader(f.failWith)
	case r.Method == http.MethodPost:
		f.appended = append(f.appended, body.Values...)
		io.WriteString(w, `{"updates":{"updatedRange":"Sheet1!A2:L2"}}`)
	}
}

func newTestSink(t *testing.T, f *fakeSheets, tab string) *Sink {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s, err := New(Config{Spreadsheet: sheetID, Tab: tab, BaseURL: srv.URL,
		Token: func(context.Context) (string, error) { return "tok", nil }})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return fixedNow }
	return s
}

func TestPushWritesHeaderOnceThenAppends(t *testing.T) {
	f := &fakeSheets{}
	s := newTestSink(t, f, "")

	for range 2 {
		r, err := s.Push(context.Background(), sampleNote())
		if err != nil {
			t.Fatal(err)
		}
		if r.ID != "Sheet1!A2:L2" || r.URL != "https://docs.google.com/spreadsheets/d/"+sheetID+"/edit" {
			t.Fatalf("receipt = %+v", r)
		}
	}

	want := []string{
		"GET /v4/spreadsheets/" + sheetID + "/values/A1:L1?",
		"PUT /v4/spreadsheets/" + sheetID + "/values/A1:L1?valueInputOption=RAW",
		"POST /v4/spreadsheets/" + sheetID + "/values/A:L:append?valueInputOption=RAW&insertDataOption=INSERT_ROWS",
		"POST /v4/spreadsheets/" + sheetID + "/values/A:L:append?valueInputOption=RAW&insertDataOption=INSERT_ROWS",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	for _, a := range f.auth {
		if a != "Bearer tok" {
			t.Fatalf("Authorization = %q", a)
		}
	}
	if len(f.appended) != 2 || f.appended[0][1] != "Northwind intro" {
		t.Fatalf("appended rows = %v", f.appended)
	}
}

func TestPushKeepsExistingHeaderAndQuotesTab(t *testing.T) {
	f := &fakeSheets{hasHeader: true}
	s := newTestSink(t, f, "Voice notes")
	if _, err := s.Push(context.Background(), sampleNote()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || !strings.HasPrefix(f.calls[0], "GET /v4/spreadsheets/"+sheetID+"/values/%27Voice%20notes%27%21A1:L1") {
		t.Fatalf("want GET with quoted tab then append, no PUT; got %v", f.calls)
	}
}

func TestPushErrors(t *testing.T) {
	t.Run("sheet not shared with the service account", func(t *testing.T) {
		f := &fakeSheets{hasHeader: true, failWith: http.StatusForbidden}
		_, err := newTestSink(t, f, "").Push(context.Background(), sampleNote())
		if httpx.StatusOf(err) != http.StatusForbidden {
			t.Fatalf("err = %v, want 403", err)
		}
	})
	t.Run("server error on append is not retried (no duplicate rows)", func(t *testing.T) {
		f := &fakeSheets{hasHeader: true, failWith: http.StatusServiceUnavailable}
		_, err := newTestSink(t, f, "").Push(context.Background(), sampleNote())
		if httpx.StatusOf(err) != http.StatusServiceUnavailable {
			t.Fatalf("err = %v, want 503", err)
		}
		if n := strings.Count(strings.Join(f.calls, "\n"), "POST"); n != 1 {
			t.Fatalf("append was sent %d times; a POST must not be retried", n)
		}
	})
	t.Run("header failure appends nothing and is retried on the next note", func(t *testing.T) {
		f := &fakeSheets{failHeader: http.StatusForbidden}
		s := newTestSink(t, f, "")
		if _, err := s.Push(context.Background(), sampleNote()); err == nil || !strings.Contains(err.Error(), "header row") {
			t.Fatalf("err = %v, want a header row error", err)
		}
		if len(f.appended) != 0 {
			t.Fatal("a row was appended despite the header failure")
		}
		f.failHeader = 0
		if _, err := s.Push(context.Background(), sampleNote()); err != nil || len(f.appended) != 1 {
			t.Fatalf("second note: err = %v, rows = %d; the header check should be retried", err, len(f.appended))
		}
	})
	t.Run("token failure", func(t *testing.T) {
		s := newTestSink(t, &fakeSheets{}, "")
		s.token = func(context.Context) (string, error) { return "", errors.New("no credentials") }
		if _, err := s.Push(context.Background(), sampleNote()); err == nil || !strings.Contains(err.Error(), "access token") {
			t.Fatalf("err = %v", err)
		}
	})
}
