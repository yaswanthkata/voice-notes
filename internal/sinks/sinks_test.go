package sinks_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"voice-notes/internal/domain"
	"voice-notes/internal/sinks"
)

type stub struct {
	name  string
	err   error
	panic bool
}

func (s stub) Name() string { return s.name }
func (s stub) Push(context.Context, domain.Note) (domain.Receipt, error) {
	if s.panic {
		panic("connector bug")
	}
	return domain.Receipt{ID: s.name + "-1"}, s.err
}

func init() {
	sinks.Register("test-misnamed", func(sinks.Env) (sinks.Sink, error) { return stub{name: "other"}, nil })
	sinks.Register("test-ok", func(sinks.Env) (sinks.Sink, error) { return stub{name: "test-ok"}, nil })
	sinks.Register("test-needs-config", func(env sinks.Env) (sinks.Sink, error) {
		if env("TEST_TOKEN") == "" {
			return nil, errors.New("TEST_TOKEN is required")
		}
		return stub{name: "test-needs-config"}, nil
	})
}

func env(m map[string]string) sinks.Env { return func(k string) string { return m[k] } }

func TestBuild(t *testing.T) {
	if _, err := sinks.Build([]string{"test-ok", "test-needs-config"}, env(map[string]string{"TEST_TOKEN": "x"})); err != nil {
		t.Fatal(err)
	}
	if _, err := sinks.Build([]string{"test-needs-config"}, env(nil)); err == nil || !strings.Contains(err.Error(), "TEST_TOKEN") {
		t.Fatalf("want connector config error, got %v", err)
	}
	if _, err := sinks.Build([]string{"nope"}, env(nil)); err == nil || !strings.Contains(err.Error(), "available") {
		t.Fatalf("want unknown sink error listing available ones, got %v", err)
	}
	if _, err := sinks.Build([]string{"test-misnamed"}, env(nil)); err == nil || !strings.Contains(err.Error(), "registry name") {
		t.Fatalf("want error when Name() differs from the registry name, got %v", err)
	}
	if _, err := sinks.Build([]string{"test-ok", "test-ok"}, env(nil)); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("want duplicate sink error, got %v", err)
	}
}

func TestFanoutSelectsSinks(t *testing.T) {
	f := sinks.NewFanout(stub{name: "a"}, stub{name: "b"}, stub{name: "c"})
	tests := []struct {
		only []string
		want string
	}{
		{nil, "a,b,c"},
		{[]string{"c", "a"}, "a,c"}, // configuration order, not request order
		{[]string{}, ""},
		{[]string{"zzz"}, ""},
	}
	for _, tc := range tests {
		var got []string
		for _, d := range f.Dispatch(context.Background(), domain.Note{}, tc.only) {
			got = append(got, d.Sink)
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("only=%v: delivered to %v, want %s", tc.only, got, tc.want)
		}
	}
}

func TestFanoutIsolatesFailures(t *testing.T) {
	vendorErr := errors.New("secret vendor detail")
	f := sinks.NewFanout(stub{name: "a"}, stub{name: "b", err: vendorErr}, stub{name: "c", panic: true}, stub{name: "d"})

	got := f.Dispatch(context.Background(), domain.Note{}, nil)
	if len(got) != 4 || !got[0].OK || got[1].OK || got[2].OK || !got[3].OK {
		t.Fatalf("unexpected deliveries: %+v", got)
	}
	for _, d := range got[1:3] {
		if d.Error != "delivery failed" || d.Err == nil {
			t.Fatalf("%s: want generic client error and logged detail, got %q / %v", d.Sink, d.Error, d.Err)
		}
	}
	if !errors.Is(got[1].Err, vendorErr) {
		t.Fatalf("vendor error not kept for logs: %v", got[1].Err)
	}
	if got[0].ID != "a-1" || got[3].ID != "d-1" {
		t.Fatalf("receipts not preserved in order: %+v", got)
	}
	if got[1].ID != "b-1" {
		t.Fatalf("partial receipt dropped on failure: %+v", got[1])
	}
}
