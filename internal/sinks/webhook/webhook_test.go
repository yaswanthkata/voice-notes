package webhook

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"voice-notes/internal/domain"
)

func TestPushSignsPayload(t *testing.T) {
	secret := []byte("whsec")
	var gotBody []byte
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotHeader = r.Header
	}))
	defer srv.Close()

	s, err := New(Config{URL: srv.URL, Secret: string(secret)}) // httptest uses 127.0.0.1, allowed over http
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Push(context.Background(), domain.Note{Title: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	// Receiver-side verification.
	want := "sha256=" + Sign(secret, gotHeader.Get("X-Timestamp"), gotBody)
	if !hmac.Equal([]byte(gotHeader.Get("X-Signature-256")), []byte(want)) {
		t.Fatal("signature does not verify")
	}
	var ev Event
	json.Unmarshal(gotBody, &ev)
	if ev.Type != "note.created" || ev.Data.Title != "hi" || ev.ID != r.ID || gotHeader.Get("X-Event-Id") != ev.ID {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestNewRejectsInsecureURL(t *testing.T) {
	for _, u := range []string{"", "not a url", "http://example.com/hook", "ftp://example.com"} {
		if _, err := New(Config{URL: u}); err == nil {
			t.Errorf("New(%q) should fail", u)
		}
	}
	for _, u := range []string{"https://example.com/hook", "http://localhost:9000/hook", "http://127.0.0.1/hook", "http://[::1]/hook"} {
		if _, err := New(Config{URL: u}); err != nil {
			t.Errorf("New(%q) should be allowed: %v", u, err)
		}
	}
}
