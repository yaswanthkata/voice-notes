package sinks

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"voice-notes/internal/domain"
)

// Fanout delivers a note to several sinks concurrently. One sink failing, or
// even panicking, does not affect the others; every outcome is reported.
type Fanout struct {
	sinks []Sink
}

// NewFanout returns a Fanout over sinks.
func NewFanout(sinks ...Sink) *Fanout {
	return &Fanout{sinks: sinks}
}

// Dispatch pushes note to the sinks named in only (nil means all of them) in
// parallel and returns one Delivery per sink, in configuration order. Callers
// validate the names; unknown ones are simply not matched.
func (f *Fanout) Dispatch(ctx context.Context, note domain.Note, only []string) []domain.Delivery {
	selected := f.sinks
	if only != nil {
		selected = make([]Sink, 0, len(only))
		for _, s := range f.sinks {
			if slices.Contains(only, s.Name()) {
				selected = append(selected, s)
			}
		}
	}

	out := make([]domain.Delivery, len(selected))
	var wg sync.WaitGroup
	for i, s := range selected {
		wg.Go(func() { out[i] = push(ctx, s, note) })
	}
	wg.Wait()
	return out
}

func push(ctx context.Context, s Sink, note domain.Note) (d domain.Delivery) {
	d.Sink = s.Name()
	defer func() {
		// Recovery middleware only covers the request goroutine; without this
		// a bug in one connector would crash the whole server.
		if r := recover(); r != nil {
			d.OK, d.Error, d.Err = false, "delivery failed", fmt.Errorf("panic: %v", r)
		}
	}()

	r, err := s.Push(ctx, note)
	// Keep partial receipts (e.g. note created, tasks failed) so they're visible.
	d.Receipt = r
	if err != nil {
		d.Error, d.Err = "delivery failed", err
		return d
	}
	d.OK = true
	return d
}
