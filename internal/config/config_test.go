package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string // substring; empty means success
		check   func(t *testing.T, c Config)
	}{
		{
			name: "defaults",
			env:  map[string]string{"GEMINI_API_KEY": "k"},
			check: func(t *testing.T, c Config) {
				if c.Port != "8080" || c.MaxUploadBytes != 20<<20 || c.AnalyzeTimeout != 90*time.Second {
					t.Errorf("unexpected defaults: %+v", c)
				}
				if len(c.Sinks) != 1 || c.Sinks[0] != "console" || c.TrustedProxies != nil {
					t.Errorf("sinks = %v, proxies = %v", c.Sinks, c.TrustedProxies)
				}
			},
		},
		{
			name: "lists are trimmed, lowercased and skip empty items",
			env:  map[string]string{"GEMINI_API_KEY": "k", "SINKS": " Notion, ,webhook ", "TRUSTED_PROXIES": "10.0.0.0/8"},
			check: func(t *testing.T, c Config) {
				if strings.Join(c.Sinks, ",") != "notion,webhook" || strings.Join(c.TrustedProxies, ",") != "10.0.0.0/8" {
					t.Errorf("sinks = %v, proxies = %v", c.Sinks, c.TrustedProxies)
				}
			},
		},
		{name: "missing Gemini key", env: map[string]string{}, wantErr: "GEMINI_API_KEY is required"},
		{name: "non-numeric", env: map[string]string{"GEMINI_API_KEY": "k", "MAX_UPLOAD_MB": "lots"}, wantErr: "MAX_UPLOAD_MB"},
		{name: "zero", env: map[string]string{"GEMINI_API_KEY": "k", "RATE_LIMIT_PER_MINUTE": "0"}, wantErr: "RATE_LIMIT_PER_MINUTE"},
		{name: "negative", env: map[string]string{"GEMINI_API_KEY": "k", "MAX_CONCURRENT_JOBS": "-1"}, wantErr: "MAX_CONCURRENT_JOBS"},
		{
			name: "SINKS=none means analysis only",
			env:  map[string]string{"GEMINI_API_KEY": "k", "SINKS": "None"},
			check: func(t *testing.T, c Config) {
				if c.Sinks == nil || len(c.Sinks) != 0 {
					t.Errorf("sinks = %#v, want empty non-nil", c.Sinks)
				}
			},
		},
		{name: "only separators in SINKS", env: map[string]string{"GEMINI_API_KEY": "k", "SINKS": " , "}, wantErr: "SINKS"},
		{
			name:    "all problems are reported at once",
			env:     map[string]string{"MAX_UPLOAD_MB": "x"},
			wantErr: "MAX_UPLOAD_MB must be a positive integer, got \"x\"\nGEMINI_API_KEY is required",
		},
	}
	keys := []string{"PORT", "MAX_UPLOAD_MB", "ANALYZE_TIMEOUT_SECONDS", "API_KEY", "RATE_LIMIT_PER_MINUTE",
		"MAX_CONCURRENT_JOBS", "TRUSTED_PROXIES", "GEMINI_API_KEY", "GEMINI_MODEL", "SINKS"}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Set every key (empty = unset for our purposes) so a developer's
			// real environment can't leak into the test. t.Setenv also stops
			// godotenv from overriding these, and restores them afterwards.
			for _, k := range keys {
				t.Setenv(k, tc.env[k])
			}
			c, err := Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, c)
		})
	}
}
