// Package api exposes the HTTP interface: routing, middleware and the
// POST /v1/notes handler. It depends only on the small interfaces below,
// never on a concrete AI provider or connector.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"voice-notes/internal/domain"
)

// Analyzer turns audio into a structured note (implemented by internal/llm).
type Analyzer interface {
	Analyze(ctx context.Context, audio domain.Audio) (domain.Note, error)
}

// Dispatcher delivers a note to the configured connectors (implemented by
// sinks.Fanout). only selects a subset by name; nil means all of them.
type Dispatcher interface {
	Dispatch(ctx context.Context, note domain.Note, only []string) []domain.Delivery
}

// multipartOverhead is headroom for multipart boundaries and headers on top of
// the file-size limit, so a file of exactly MaxUploadBytes is still accepted.
const multipartOverhead = 64 << 10

// statusClientClosed is the de-facto (nginx) status for a client that hung up
// before we could answer. It only ever appears in our access logs.
const statusClientClosed = 499

// HandlerConfig holds the limits the handler enforces per request.
type HandlerConfig struct {
	Sinks           []string // configured destinations a request may choose from
	MaxUploadBytes  int64
	AnalyzeTimeout  time.Duration
	DeliveryTimeout time.Duration
}

// Handler serves the notes API.
type Handler struct {
	analyzer   Analyzer
	dispatcher Dispatcher
	cfg        HandlerConfig
	log        *slog.Logger
}

// NewHandler returns a Handler that analyses with a and delivers with d.
func NewHandler(a Analyzer, d Dispatcher, cfg HandlerConfig, log *slog.Logger) *Handler {
	return &Handler{analyzer: a, dispatcher: d, cfg: cfg, log: log}
}

// createNoteResponse is returned once the audio has been analysed. When some
// deliveries failed, Code and Error explain it and Deliveries has the details.
type createNoteResponse struct {
	RequestID  string            `json:"request_id"`
	Note       domain.Note       `json:"note"`
	Deliveries []domain.Delivery `json:"deliveries"`
	Error      string            `json:"error,omitempty"`
	Code       string            `json:"code,omitempty"`
}

// CreateNote handles POST /v1/notes with a multipart "audio" file field and an
// optional "sinks" field choosing the destinations (see selectSinks).
//
// Responses: 201 all deliveries succeeded, 207 some failed, 502 all failed
// (the note is still returned so the analysis isn't lost), 4xx invalid input,
// 422 no speech, 429/503/504 model limits (retryable, with Retry-After).
func (h *Handler) CreateNote(c *gin.Context) {
	log := h.log.With("request_id", c.GetString(requestIDKey))

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.cfg.MaxUploadBytes+multipartOverhead)
	audio, err := readAudio(c, h.cfg.MaxUploadBytes)
	if err != nil {
		writeError(c, asAPIError(err))
		return
	}
	// Validated before the (paid) analysis, so a typo costs nothing.
	only, err := selectSinks(c, h.cfg.Sinks)
	if err != nil {
		writeError(c, asAPIError(err))
		return
	}

	note, ok := h.analyze(c, log, audio)
	if !ok {
		return
	}

	// Delivery is detached from the client: once we've paid for the analysis,
	// a dropped connection must not leave the CRM half-written.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), h.cfg.DeliveryTimeout)
	defer cancel()
	deliveries := h.dispatcher.Dispatch(ctx, note, only)
	for _, d := range deliveries {
		if d.Err != nil {
			// Full vendor detail for operators; clients only see d.Error.
			log.Error("delivery failed", "sink", d.Sink, "err", d.Err)
		}
	}

	resp := createNoteResponse{RequestID: c.GetString(requestIDKey), Note: note, Deliveries: deliveries}
	status := deliveryStatus(deliveries)
	switch status {
	case http.StatusMultiStatus:
		resp.Code = "delivery_failed"
		resp.Error = "The note was analyzed, but delivery to some destinations failed. Details are listed per destination."
	case http.StatusBadGateway:
		resp.Code = "delivery_failed"
		resp.Error = "The note was analyzed, but it could not be delivered to any destination. Details are listed per destination."
	}
	c.JSON(status, resp)
}

// analyze runs the analyzer and writes the error response itself on failure.
func (h *Handler) analyze(c *gin.Context, log *slog.Logger, audio domain.Audio) (domain.Note, bool) {
	// Analysis is tied to the client: if they disconnect, stop paying for it.
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.cfg.AnalyzeTimeout)
	defer cancel()

	start := time.Now()
	note, err := h.analyzer.Analyze(ctx, audio)
	if errors.Is(err, context.Canceled) && c.Request.Context().Err() != nil {
		// The client went away; nobody is left to read an error body.
		log.Info("client disconnected during analysis")
		c.AbortWithStatus(statusClientClosed)
		return domain.Note{}, false
	}
	if err != nil {
		e := analyzeError(err)
		log.Error("analyze failed", "code", e.code, "err", err)
		writeError(c, e)
		return domain.Note{}, false
	}
	// Metadata only: transcripts and note content are PII and never logged.
	log.Info("audio analyzed",
		"bytes", len(audio.Data),
		"mime", audio.MIMEType,
		"language", note.Language,
		"next_steps", len(note.NextSteps),
		"took_ms", time.Since(start).Milliseconds())
	return note, true
}

// selectSinks reads the optional "sinks" form field, a comma-separated list of
// configured destinations. Absent means all of them, "none" means analysis
// only; anything not configured is rejected.
func selectSinks(c *gin.Context, configured []string) ([]string, error) {
	raw, ok := c.GetPostForm("sinks")
	if !ok {
		return nil, nil
	}
	only := []string{}
	for name := range strings.SplitSeq(raw, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		switch {
		case name == "" || name == "none" || slices.Contains(only, name):
		case slices.Contains(configured, name):
			only = append(only, name)
		case len(configured) == 0:
			return nil, apiError{http.StatusBadRequest, "unknown_destination",
				"This server only analyses memos; it has no destinations configured.", 0}
		default:
			return nil, apiError{http.StatusBadRequest, "unknown_destination",
				fmt.Sprintf("Unknown destination %q. Available: %s.", name, strings.Join(configured, ", ")), 0}
		}
	}
	return only, nil
}

// deliveryStatus summarises per-sink outcomes as one HTTP status.
func deliveryStatus(ds []domain.Delivery) int {
	ok := 0
	for _, d := range ds {
		if d.OK {
			ok++
		}
	}
	switch ok {
	case len(ds):
		return http.StatusCreated
	case 0:
		return http.StatusBadGateway
	default:
		return http.StatusMultiStatus
	}
}
