// Package notion is a note-taking connector: each note becomes a Notion page
// (title, summary, contact, next steps as to-dos, transcript) under a parent
// page that has been shared with the integration.
//
// Config:
//
//	NOTION_TOKEN           required. Internal integration secret
//	                       (notion.so/profile/integrations), capability "Insert content"
//	NOTION_PARENT_PAGE_ID  required. ID or URL of a page shared with the
//	                       integration (page menu → Connections)
//	NOTION_BASE_URL        optional; default https://api.notion.com
package notion

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"voice-notes/internal/domain"
	"voice-notes/internal/httpx"
	"voice-notes/internal/sinks"
)

// Notion versions its API by header; pin it so behaviour doesn't change under us.
const apiVersion = "2026-03-11"

func init() {
	sinks.Register("notion", func(env sinks.Env) (sinks.Sink, error) {
		return New(Config{
			Token:        env("NOTION_TOKEN"),
			ParentPageID: env("NOTION_PARENT_PAGE_ID"),
			BaseURL:      env("NOTION_BASE_URL"),
		})
	})
}

// Config is the Notion connector's configuration.
type Config struct {
	Token        string
	ParentPageID string
	BaseURL      string
}

// Sink creates one Notion page per note under a parent page.
type Sink struct {
	api      *httpx.Client
	parentID string
}

// New validates cfg; the parent may be given as a page ID or URL.
func New(cfg Config) (*Sink, error) {
	if cfg.Token == "" {
		return nil, errors.New("NOTION_TOKEN is required")
	}
	if cfg.ParentPageID == "" {
		return nil, errors.New("NOTION_PARENT_PAGE_ID is required")
	}
	parentID, err := parsePageID(cfg.ParentPageID)
	if err != nil {
		return nil, fmt.Errorf("NOTION_PARENT_PAGE_ID: %w", err)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.notion.com"
	}
	api := httpx.New(cfg.BaseURL, http.Header{
		"Authorization":  {"Bearer " + cfg.Token},
		"Notion-Version": {apiVersion},
	})
	return &Sink{api: api, parentID: parentID}, nil
}

// Name implements sinks.Sink.
func (*Sink) Name() string { return "notion" }

// Push creates the page in a single request (blocks are sent inline), so there
// is no partially written page to clean up if it fails.
func (s *Sink) Push(ctx context.Context, note domain.Note) (domain.Receipt, error) {
	var page struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := s.api.Do(ctx, httpx.Request{Method: http.MethodPost, Path: "/v1/pages",
		Body: pageInput(note, s.parentID)}, &page); err != nil {
		return domain.Receipt{}, fmt.Errorf("create page: %w", err)
	}
	return domain.Receipt{ID: page.ID, URL: page.URL}, nil
}
