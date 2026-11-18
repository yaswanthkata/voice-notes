package console

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"voice-notes/internal/domain"
)

func TestPushWritesNoteAsJSON(t *testing.T) {
	var buf bytes.Buffer
	if _, err := New(&buf).Push(context.Background(), domain.Note{Title: "Call with ACME"}); err != nil {
		t.Fatal(err)
	}
	var got domain.Note
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil || got.Title != "Call with ACME" {
		t.Fatalf("output %q: %v", buf.String(), err)
	}
}
