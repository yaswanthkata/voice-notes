// Package hubspot is a native CRM connector: it upserts the contact (by
// email), creates a Note on the contact's timeline and one Task per next step.
//
// Config:
//
//	HUBSPOT_TOKEN      required. A service key (Development → Keys → Service keys)
//	                   with scopes crm.objects.contacts.read + crm.objects.contacts.write
//	HUBSPOT_PORTAL_ID  optional; enables a deep link to the contact in the response
//	HUBSPOT_APP_URL    optional; default https://app.hubspot.com (EU: https://app-eu1.hubspot.com)
//	HUBSPOT_BASE_URL   optional; default https://api.hubapi.com
package hubspot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"voice-notes/internal/domain"
	"voice-notes/internal/httpx"
	"voice-notes/internal/sinks"
)

// API version for date-versioned CRM object endpoints.
const objectsPath = "/crm/objects/2026-09"

func init() {
	sinks.Register("hubspot", func(env sinks.Env) (sinks.Sink, error) {
		return New(Config{
			Token:    env("HUBSPOT_TOKEN"),
			PortalID: env("HUBSPOT_PORTAL_ID"),
			AppURL:   env("HUBSPOT_APP_URL"),
			BaseURL:  env("HUBSPOT_BASE_URL"),
		})
	})
}

// Config is the HubSpot connector's configuration.
type Config struct {
	Token    string
	PortalID string
	AppURL   string
	BaseURL  string
}

// Sink writes notes, contacts and tasks to one HubSpot account.
type Sink struct {
	api *httpx.Client
	cfg Config
	now func() time.Time
}

// New validates cfg and fills in defaults.
func New(cfg Config) (*Sink, error) {
	if cfg.Token == "" {
		return nil, errors.New("HUBSPOT_TOKEN is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.hubapi.com"
	}
	if cfg.AppURL == "" {
		cfg.AppURL = "https://app.hubspot.com"
	}
	api := httpx.New(cfg.BaseURL, http.Header{"Authorization": {"Bearer " + cfg.Token}})
	return &Sink{api: api, cfg: cfg, now: time.Now}, nil
}

// Name implements sinks.Sink.
func (*Sink) Name() string { return "hubspot" }

type object struct {
	ID string `json:"id"`
}

// Push upserts the contact, creates the note and then the tasks. On a later
// failure the receipt still lists what was created, so it can be found.
func (s *Sink) Push(ctx context.Context, note domain.Note) (domain.Receipt, error) {
	now := s.now()
	var r domain.Receipt

	contactID, err := s.upsertContact(ctx, note.Contact)
	if err != nil {
		return r, fmt.Errorf("upsert contact: %w", err)
	}
	if contactID != "" {
		r.Refs = append(r.Refs, "contact:"+contactID)
		if s.cfg.PortalID != "" {
			r.URL = fmt.Sprintf("%s/contacts/%s/record/0-1/%s", s.cfg.AppURL, s.cfg.PortalID, contactID)
		}
	}

	var created object
	if err := s.api.Do(ctx, httpx.Request{Method: http.MethodPost, Path: objectsPath + "/notes",
		Body: noteInput(note, contactID, now)}, &created); err != nil {
		return r, fmt.Errorf("create note: %w", err)
	}
	r.ID = created.ID

	if tasks := taskInputs(note, contactID, now); len(tasks) > 0 {
		var batch struct{ Results []object }
		if err := s.api.Do(ctx, httpx.Request{Method: http.MethodPost, Path: objectsPath + "/tasks/batch/create",
			Body: map[string]any{"inputs": tasks}}, &batch); err != nil {
			return r, fmt.Errorf("create tasks: %w", err)
		}
		for _, t := range batch.Results {
			r.Refs = append(r.Refs, "task:"+t.ID)
		}
	}
	return r, nil
}

// upsertContact finds the contact by email or creates it. Without a valid
// email we don't guess: matching on names would risk attaching the note to
// the wrong person, so the note is created unassociated instead.
func (s *Sink) upsertContact(ctx context.Context, c domain.Contact) (string, error) {
	addr, err := mail.ParseAddress(c.Email)
	if err != nil {
		return "", nil
	}
	c.Email = strings.ToLower(addr.Address) // normalise so lookups don't create duplicates

	id, err := s.findContact(ctx, c.Email)
	if err != nil || id != "" {
		return id, err
	}

	var created object
	err = s.api.Do(ctx, httpx.Request{Method: http.MethodPost, Path: objectsPath + "/contacts",
		Body: objectInput{Properties: contactProperties(c)}}, &created)
	if httpx.StatusOf(err) == http.StatusConflict {
		return s.findContact(ctx, c.Email) // created concurrently by someone else
	}
	return created.ID, err
}

func (s *Sink) findContact(ctx context.Context, email string) (string, error) {
	var found object
	err := s.api.Do(ctx, httpx.Request{Method: http.MethodGet,
		Path: objectsPath + "/contacts/" + url.PathEscape(email) + "?idProperty=email"}, &found)
	if httpx.StatusOf(err) == http.StatusNotFound {
		return "", nil
	}
	return found.ID, err
}
