// Package sinks is the extension point for delivering analysed notes to
// external systems (CRMs, note tools, spreadsheets, webhooks, ...).
//
// The core knows nothing about any vendor. Each connector lives in its own
// package, owns its configuration (env vars with its own prefix), maps the
// CRM-neutral domain.Note to its vendor schema, and registers itself:
//
//	func init() { sinks.Register("salesforce", New) }
//
// It is enabled by blank-importing the package in main and listing it in
// SINKS (comma-separated to deliver to several systems at once).
package sinks

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"voice-notes/internal/domain"
)

// Sink stores a note in one external system.
type Sink interface {
	Name() string
	Push(ctx context.Context, note domain.Note) (domain.Receipt, error)
}

// Env looks up a configuration value (os.Getenv in production).
type Env func(key string) string

// Factory builds a connector, reading and validating its own config.
type Factory func(env Env) (Sink, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register makes a connector available under name. Called from init().
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[name]; dup {
		panic("sinks: duplicate registration of " + name)
	}
	registry[name] = f
}

// Build instantiates the named connectors, in order.
func Build(names []string, env Env) ([]Sink, error) {
	mu.RLock()
	defer mu.RUnlock()
	var out []Sink
	for i, name := range names {
		if slices.Contains(names[:i], name) {
			// Would deliver every note twice, i.e. duplicate CRM records.
			return nil, fmt.Errorf("sink %q is listed twice", name)
		}
		f, ok := registry[name]
		if !ok {
			return nil, fmt.Errorf("unknown sink %q (available: %s)", name, strings.Join(available(), ", "))
		}
		s, err := f(env)
		if err != nil {
			return nil, fmt.Errorf("sink %s: %w", name, err)
		}
		// Requests select sinks by registry name and Fanout matches on Name(),
		// so a mismatch would silently deliver nowhere.
		if s.Name() != name {
			return nil, fmt.Errorf("sink %s: Name() returns %q, want the registry name", name, s.Name())
		}
		out = append(out, s)
	}
	return out, nil
}

func available() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
