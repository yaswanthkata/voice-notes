// Package llm turns audio into a structured note using Google Gemini.
// Gemini accepts audio natively, so transcription and extraction happen in a
// single call.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"google.golang.org/genai"

	"voice-notes/internal/domain"
)

// GeminiConfig configures the Gemini analyzer.
type GeminiConfig struct {
	APIKey  string
	Model   string // e.g. "gemini-3.5-flash-lite"
	BaseURL string // optional; points the client at a fake server in tests
}

// Gemini implements api.Analyzer with a single multimodal call: the audio goes
// in, a schema-constrained JSON note comes out.
type Gemini struct {
	client *genai.Client
	model  string
	now    func() time.Time
}

// NewGemini creates a client with retries for transient overload errors.
func NewGemini(ctx context.Context, cfg GeminiConfig) (*Gemini, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  cfg.APIKey,
		Backend: genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: cfg.BaseURL,
			// Retry short overload spikes ("high demand", 500/503). Safe because
			// generateContent has no side effects. Not 429 (quota won't recover
			// in seconds) and not 504 (the time budget is already spent).
			RetryOptions: &genai.HTTPRetryOptions{
				Attempts:        ptr[int32](3),
				InitialDelay:    ptr(1.0),
				MaxDelay:        ptr(4.0),
				HTTPStatusCodes: []int32{http.StatusInternalServerError, http.StatusServiceUnavailable},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}
	return &Gemini{client: client, model: cfg.Model, now: time.Now}, nil
}

// Analyze transcribes the audio and extracts a structured note. Provider
// failures are returned as domain errors (ErrNoSpeech, ErrModelBusy,
// ErrQuotaExceeded) or context.DeadlineExceeded, wrapping the original detail.
func (g *Gemini) Analyze(ctx context.Context, audio domain.Audio) (domain.Note, error) {
	today := g.now()
	contents := []*genai.Content{
		genai.NewContentFromParts([]*genai.Part{
			genai.NewPartFromText(fmt.Sprintf("Today is %s (%s). Process this voice memo.",
				today.Format(domain.DateLayout), today.Weekday())),
			genai.NewPartFromBytes(audio.Data, audio.MIMEType),
		}, genai.RoleUser),
	}
	cfg := &genai.GenerateContentConfig{
		SystemInstruction:  genai.NewContentFromText(systemPrompt, genai.RoleUser),
		ResponseMIMEType:   "application/json",
		ResponseJsonSchema: noteSchema,
		// Extraction is not reasoning-heavy; keep latency low for a synchronous request.
		ThinkingConfig: &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelLow},
	}

	resp, err := g.client.Models.GenerateContent(ctx, g.model, contents, cfg)
	if err != nil {
		return domain.Note{}, g.classify(err)
	}
	if err := checkResponse(resp); err != nil {
		return domain.Note{}, err
	}

	var note domain.Note
	if err := json.Unmarshal([]byte(resp.Text()), &note); err != nil {
		return domain.Note{}, fmt.Errorf("decode gemini output: %w", err)
	}
	normalize(&note, today)
	if note.Transcript == "" {
		return domain.Note{}, domain.ErrNoSpeech
	}
	return note, nil
}

// classify maps Gemini API errors onto provider-neutral domain errors,
// keeping the original detail for logs.
func (g *Gemini) classify(err error) error {
	var apiErr genai.APIError // returned by value, not pointer
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case http.StatusTooManyRequests:
			return fmt.Errorf("gemini %s: %w: %v", g.model, domain.ErrQuotaExceeded, err)
		case http.StatusInternalServerError, http.StatusServiceUnavailable:
			return fmt.Errorf("gemini %s: %w: %v", g.model, domain.ErrModelBusy, err)
		case http.StatusGatewayTimeout:
			// The SDK forwards our deadline as x-server-timeout; Google gave up on it.
			return fmt.Errorf("gemini %s: server-side deadline: %w (%v)", g.model, context.DeadlineExceeded, err)
		}
	}
	return fmt.Errorf("gemini %s generate content: %w", g.model, err)
}

func checkResponse(resp *genai.GenerateContentResponse) error {
	if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
		return fmt.Errorf("gemini blocked prompt: %s", resp.PromptFeedback.BlockReason)
	}
	if len(resp.Candidates) == 0 {
		return errors.New("gemini returned no candidates")
	}
	// Anything other than STOP (e.g. MAX_TOKENS, SAFETY) means the JSON may be truncated or missing.
	if fr := resp.Candidates[0].FinishReason; fr != genai.FinishReasonStop {
		return fmt.Errorf("gemini finished unexpectedly: %s", fr)
	}
	return nil
}

// normalize cleans up model output so downstream code can rely on it. The
// schema already constrains enums; this is the safety net if a model ignores it.
// Rules that are deterministic (like "due tomorrow is urgent") are applied here
// rather than left to the model, which applied them inconsistently.
func normalize(n *domain.Note, today time.Time) {
	tomorrow := today.AddDate(0, 0, 1).Format(domain.DateLayout)
	n.Transcript = strings.TrimSpace(n.Transcript)
	n.Title = strings.TrimSpace(n.Title)
	n.Summary = strings.TrimSpace(n.Summary)
	n.Sentiment = oneOf(n.Sentiment, domain.Sentiments)
	n.Contact.Name = strings.TrimSpace(n.Contact.Name)
	n.Contact.Company = strings.TrimSpace(n.Contact.Company)
	n.Contact.Email = strings.TrimSpace(n.Contact.Email)

	// A task without a title would become a blank to-do in every destination.
	tasks := []domain.Task{}
	for _, t := range n.NextSteps {
		t.Title = strings.TrimSpace(t.Title)
		if t.Title == "" {
			continue
		}
		if _, err := time.Parse(domain.DateLayout, t.DueDate); err != nil {
			t.DueDate = "" // drop anything that isn't a valid date
		}
		t.Type = oneOf(t.Type, domain.TaskTypes)
		t.Priority = oneOf(t.Priority, domain.Priorities)
		if t.DueDate != "" && t.DueDate <= tomorrow { // ISO dates compare as strings
			t.Priority = "high"
		}
		tasks = append(tasks, t)
	}
	n.NextSteps = tasks

	insights := []domain.Insight{}
	for _, in := range n.Insights {
		in.Text = strings.TrimSpace(in.Text)
		if in.Text != "" && slices.Contains(domain.InsightTypes, in.Type) {
			insights = append(insights, in)
		}
	}
	n.Insights = insights

	// Tags become labels in each destination; empty or case-variant duplicates
	// would show up as separate (or blank) pills.
	tags := []string{}
	for _, tag := range n.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && !slices.Contains(tags, tag) && len(tags) < maxTags {
			tags = append(tags, tag)
		}
	}
	n.Tags = tags
}

// maxTags matches the "0-5 tags" rule in the prompt.
const maxTags = 5

// oneOf returns v if it is allowed, otherwise the default (first) value.
func oneOf(v string, allowed []string) string {
	if slices.Contains(allowed, v) {
		return v
	}
	return allowed[0]
}

func ptr[T any](v T) *T { return &v }
