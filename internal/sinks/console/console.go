// Package console is a connector that prints notes as JSON to stdout.
// Useful for local runs without any third-party credentials.
package console

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"

	"voice-notes/internal/domain"
	"voice-notes/internal/sinks"
)

func init() {
	sinks.Register("console", func(sinks.Env) (sinks.Sink, error) { return New(os.Stdout), nil })
}

// Sink writes each note to an io.Writer.
type Sink struct {
	mu sync.Mutex // keep concurrent notes from interleaving
	w  io.Writer
}

// New returns a Sink writing to w.
func New(w io.Writer) *Sink { return &Sink{w: w} }

// Name implements sinks.Sink.
func (*Sink) Name() string { return "console" }

// Push writes the note as indented JSON. It never fails on its own.
func (s *Sink) Push(_ context.Context, note domain.Note) (domain.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	enc := json.NewEncoder(s.w)
	enc.SetIndent("", "  ")
	return domain.Receipt{}, enc.Encode(note)
}
