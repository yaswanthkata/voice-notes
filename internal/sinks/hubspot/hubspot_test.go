package hubspot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"voice-notes/internal/domain"
)

var fixedNow = time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)

func sampleNote() domain.Note {
	return domain.Note{
		Title:      "Northwind intro",
		Summary:    "Liked the demo.",
		Transcript: `I met Sarah. She said "<script>alert(1)</script>"`,
		Contact:    domain.Contact{Name: "Sarah Miller", Company: "Northwind", Email: "Sarah.Miller@Northwind.com"},
		NextSteps: []domain.Task{
			{Title: "Send pricing", DueDate: "2026-11-14", Type: "document", Priority: "high"},
			{Title: "Book IT call", Type: "call", Priority: "medium"},
		},
		Insights:  []domain.Insight{{Type: "competitor", Text: "Also evaluating <Contoso>."}},
		Sentiment: "positive",
	}
}

// --- mapper (pure) ---

func TestNoteBodyEscapesModelOutput(t *testing.T) {
	body := noteBody(sampleNote())
	if strings.Contains(body, "<script>") {
		t.Fatal("transcript markup was not escaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("expected escaped transcript")
	}
	if !strings.Contains(body, "<li><strong>Competitor:</strong> Also evaluating &lt;Contoso&gt;.</li>") {
		t.Fatal("insight missing or not escaped")
	}
}

func TestNoteBodyFitsHubSpotLimit(t *testing.T) {
	n := sampleNote()
	n.Transcript = strings.Repeat("Grüße & <Tschüss> ", 10_000) // ~180 KB once escaped
	body := noteBody(n)
	if len(body) > maxNoteBody {
		t.Fatalf("body is %d bytes, limit %d", len(body), maxNoteBody)
	}
	if !utf8.ValidString(body) {
		t.Fatal("body was cut inside a UTF-8 character")
	}
	if !strings.HasSuffix(body, "</blockquote><p><small>Created by voice-notes</small></p>") {
		t.Fatal("closing markup lost: HTML is no longer well-formed")
	}
	if i := strings.LastIndex(body, "&"); strings.IndexByte(body[i:], ';') < 0 {
		t.Fatal("body was cut inside an HTML entity")
	}
}

func TestTaskInputs(t *testing.T) {
	tasks := taskInputs(sampleNote(), "42", fixedNow)
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks", len(tasks))
	}
	if got := tasks[0].Properties["hs_timestamp"]; got != "2026-11-14T09:00:00Z" {
		t.Errorf("due = %s", got)
	}
	for i, want := range [][2]string{{"TODO", "HIGH"}, {"CALL", "MEDIUM"}} {
		if p := tasks[i].Properties; p["hs_task_type"] != want[0] || p["hs_task_priority"] != want[1] {
			t.Errorf("task %d type/priority = %s/%s, want %s/%s", i, p["hs_task_type"], p["hs_task_priority"], want[0], want[1])
		}
	}
	if got := tasks[1].Properties["hs_timestamp"]; got != "2026-11-13T09:00:00Z" {
		t.Errorf("default due = %s, want next day 09:00", got)
	}
	if a := tasks[0].Associations; len(a) != 1 || a[0].To.ID != "42" || a[0].Types[0].TypeID != assocTaskToContact {
		t.Errorf("bad association: %+v", a)
	}
}

func TestSplitName(t *testing.T) {
	for in, want := range map[string][2]string{
		"":                {"", ""},
		"Cher":            {"Cher", ""},
		"Sarah Miller":    {"Sarah", "Miller"},
		"Anna Maria Lutz": {"Anna Maria", "Lutz"},
	} {
		f, l := splitName(in)
		if f != want[0] || l != want[1] {
			t.Errorf("splitName(%q) = %q,%q", in, f, l)
		}
	}
}

// --- API flow against a fake HubSpot ---

type fakeHubSpot struct {
	mu       sync.Mutex
	calls    []string
	bodies   map[string]map[string]any
	existing bool // contact already exists
	conflict bool // contact is created concurrently: POST returns 409, then GET finds it
}

func (f *fakeHubSpot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, key)
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		json.Unmarshal(b, &body)
		f.bodies[key] = body
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, objectsPath+"/contacts/"):
		if !f.existing {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"id":"100"}`)
	case key == "POST "+objectsPath+"/contacts" && f.conflict:
		f.existing = true // someone else created it in between
		w.WriteHeader(http.StatusConflict)
	case key == "POST "+objectsPath+"/contacts":
		io.WriteString(w, `{"id":"101"}`)
	case key == "POST "+objectsPath+"/notes":
		io.WriteString(w, `{"id":"500"}`)
	case key == "POST "+objectsPath+"/tasks/batch/create":
		io.WriteString(w, `{"status":"COMPLETE","results":[{"id":"900"},{"id":"901"}]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTestSink(t *testing.T, f *fakeHubSpot) *Sink {
	t.Helper()
	f.bodies = map[string]map[string]any{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s, err := New(Config{Token: "tok", BaseURL: srv.URL, PortalID: "777"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return fixedNow }
	return s
}

func TestPushCreatesContactNoteAndTasks(t *testing.T) {
	f := &fakeHubSpot{}
	r, err := newTestSink(t, f).Push(context.Background(), sampleNote())
	if err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{
		"GET " + objectsPath + "/contacts/sarah.miller@northwind.com",
		"POST " + objectsPath + "/contacts",
		"POST " + objectsPath + "/notes",
		"POST " + objectsPath + "/tasks/batch/create",
	}
	if strings.Join(f.calls, "|") != strings.Join(wantCalls, "|") {
		t.Fatalf("calls:\n got %v\nwant %v", f.calls, wantCalls)
	}
	if r.ID != "500" || r.URL != "https://app.hubspot.com/contacts/777/record/0-1/101" {
		t.Fatalf("receipt = %+v", r)
	}
	if strings.Join(r.Refs, ",") != "contact:101,task:900,task:901" {
		t.Fatalf("refs = %v", r.Refs)
	}
	props := f.bodies["POST "+objectsPath+"/contacts"]["properties"].(map[string]any)
	if props["email"] != "sarah.miller@northwind.com" || props["firstname"] != "Sarah" || props["lastname"] != "Miller" {
		t.Fatalf("contact props = %v", props)
	}
}

func TestPushReusesExistingContact(t *testing.T) {
	f := &fakeHubSpot{existing: true}
	r, err := newTestSink(t, f).Push(context.Background(), sampleNote())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c == "POST "+objectsPath+"/contacts" {
			t.Fatal("must not create a duplicate contact")
		}
	}
	if r.Refs[0] != "contact:100" {
		t.Fatalf("refs = %v", r.Refs)
	}
}

func TestPushContactCreatedConcurrently(t *testing.T) {
	f := &fakeHubSpot{conflict: true}
	r, err := newTestSink(t, f).Push(context.Background(), sampleNote())
	if err != nil {
		t.Fatal(err)
	}
	if r.Refs[0] != "contact:100" {
		t.Fatalf("after a 409 the existing contact should be used, got refs %v", r.Refs)
	}
}

func TestPushWithoutEmailSkipsContact(t *testing.T) {
	f := &fakeHubSpot{}
	n := sampleNote()
	n.Contact.Email = ""
	n.NextSteps = nil
	if _, err := newTestSink(t, f).Push(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0] != "POST "+objectsPath+"/notes" {
		t.Fatalf("calls = %v, want only note creation", f.calls)
	}
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("want error without token")
	}
}
