// Package sheets is a spreadsheet connector: each note becomes one row in a
// Google Sheet, under a header row it writes itself on first use.
//
// Authentication uses Google Application Default Credentials, so there is no
// secret to manage: on Cloud Run it is the service's own service account (share
// the sheet with that account's email as Editor). Locally, impersonate that
// same account: Google blocks the spreadsheets scope for gcloud's own OAuth
// client, so a plain `gcloud auth application-default login --scopes=...`
// fails with "This app is blocked".
//
// Config:
//
//	SHEETS_SPREADSHEET_ID  required. Spreadsheet ID or its URL
//	SHEETS_TAB             optional tab name; default is the first tab
//	SHEETS_BASE_URL        optional; default https://sheets.googleapis.com
package sheets

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/auth/credentials"

	"voice-notes/internal/domain"
	"voice-notes/internal/httpx"
	"voice-notes/internal/sinks"
)

const scope = "https://www.googleapis.com/auth/spreadsheets"

func init() {
	sinks.Register("sheets", func(env sinks.Env) (sinks.Sink, error) {
		return New(Config{
			Spreadsheet: env("SHEETS_SPREADSHEET_ID"),
			Tab:         env("SHEETS_TAB"),
			BaseURL:     env("SHEETS_BASE_URL"),
		})
	})
}

// Config is the Sheets connector's configuration.
type Config struct {
	Spreadsheet string // ID or URL
	Tab         string
	BaseURL     string
	// Token returns an OAuth access token. Nil means Application Default
	// Credentials; tests inject a fake.
	Token func(context.Context) (string, error)
}

// Sink appends notes as rows to one spreadsheet.
type Sink struct {
	api   *httpx.Client
	id    string
	tab   string
	token func(context.Context) (string, error)
	now   func() time.Time

	// headerLock is a one-slot channel rather than a sync.Mutex, so a push
	// waiting for another push's header check still honours its context.
	headerLock chan struct{}
	headerDone bool
}

var (
	urlID = regexp.MustCompile(`/spreadsheets/d/([A-Za-z0-9_-]+)`)
	rawID = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)
)

// New validates cfg and, unless a token func is given, looks up Google
// credentials so a missing login fails at startup rather than on first note.
func New(cfg Config) (*Sink, error) {
	id := strings.TrimSpace(cfg.Spreadsheet)
	if m := urlID.FindStringSubmatch(id); m != nil {
		id = m[1]
	}
	if !rawID.MatchString(id) {
		return nil, errors.New("SHEETS_SPREADSHEET_ID must be a spreadsheet ID or URL")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://sheets.googleapis.com"
	}
	if cfg.Token == nil {
		creds, err := credentials.DetectDefault(&credentials.DetectOptions{Scopes: []string{scope}})
		if err != nil {
			return nil, fmt.Errorf("google credentials (locally: gcloud auth application-default login --impersonate-service-account=<service account the sheet is shared with>; see README): %w", err)
		}
		cfg.Token = func(ctx context.Context) (string, error) {
			t, err := creds.Token(ctx) // cached and refreshed by the library
			if err != nil {
				return "", err
			}
			return t.Value, nil
		}
	}
	return &Sink{api: httpx.New(cfg.BaseURL, nil), id: id, tab: cfg.Tab, token: cfg.Token, now: time.Now,
		headerLock: make(chan struct{}, 1)}, nil
}

// Name implements sinks.Sink.
func (*Sink) Name() string { return "sheets" }

// Push appends the note as one row. valueInputOption=RAW stores every value as
// literal text, so model output such as "=IMPORTXML(...)" can never run as a
// formula in the sheet.
func (s *Sink) Push(ctx context.Context, note domain.Note) (domain.Receipt, error) {
	tok, err := s.token(ctx)
	if err != nil {
		return domain.Receipt{}, fmt.Errorf("get access token: %w", err)
	}
	auth := http.Header{"Authorization": {"Bearer " + tok}}

	if err := s.ensureHeader(ctx, auth); err != nil {
		return domain.Receipt{}, fmt.Errorf("write header row: %w", err)
	}

	var resp struct {
		Updates struct {
			UpdatedRange string `json:"updatedRange"`
		} `json:"updates"`
	}
	err = s.api.Do(ctx, httpx.Request{
		Method: http.MethodPost,
		Path:   s.valuesPath(columns) + ":append?valueInputOption=RAW&insertDataOption=INSERT_ROWS",
		Body:   map[string]any{"values": [][]any{row(note, s.now())}},
		Header: auth,
	}, &resp)
	if err != nil {
		return domain.Receipt{}, fmt.Errorf("append row: %w", err)
	}
	return domain.Receipt{
		ID:  resp.Updates.UpdatedRange,
		URL: "https://docs.google.com/spreadsheets/d/" + s.id + "/edit",
	}, nil
}

// ensureHeader writes the header into row 1 if it is empty. GET and PUT are
// idempotent, so unlike the append they are safe to retry. It runs once per
// process; a failure is retried on the next note.
func (s *Sink) ensureHeader(ctx context.Context, auth http.Header) error {
	select {
	case s.headerLock <- struct{}{}:
		defer func() { <-s.headerLock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.headerDone {
		return nil
	}
	var current struct {
		Values [][]any `json:"values"`
	}
	if err := s.api.Do(ctx, httpx.Request{Method: http.MethodGet, Path: s.valuesPath(headerRange), Header: auth}, &current); err != nil {
		return err
	}
	if len(current.Values) == 0 {
		err := s.api.Do(ctx, httpx.Request{
			Method: http.MethodPut,
			Path:   s.valuesPath(headerRange) + "?valueInputOption=RAW",
			Body:   map[string]any{"values": [][]any{header}},
			Header: auth,
		}, nil)
		if err != nil {
			return err
		}
	}
	s.headerDone = true
	return nil
}

// valuesPath builds the values endpoint for an A1 range, on the configured tab
// (quoted, so names with spaces work) or the first tab.
func (s *Sink) valuesPath(a1 string) string {
	if s.tab != "" {
		a1 = "'" + strings.ReplaceAll(s.tab, "'", "''") + "'!" + a1
	}
	return "/v4/spreadsheets/" + s.id + "/values/" + url.PathEscape(a1)
}
