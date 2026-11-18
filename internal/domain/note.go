// Package domain holds the core types shared across the pipeline.
// It has no dependencies on transport, AI providers or sinks, and it is
// deliberately CRM-agnostic: each sink maps a Note to its own schema.
package domain

import "errors"

// DateLayout is the format of all calendar dates in a Note (YYYY-MM-DD).
const DateLayout = "2006-01-02"

// Analyzer errors. Adapters wrap provider-specific failures in these so the
// API layer can map them to HTTP statuses without knowing the provider.
var (
	// ErrNoSpeech: the audio contains no intelligible speech.
	ErrNoSpeech = errors.New("no speech detected in audio")
	// ErrModelBusy: the model is temporarily overloaded or unavailable.
	ErrModelBusy = errors.New("model temporarily unavailable")
	// ErrQuotaExceeded: our account's rate limit or quota with the provider is used up.
	ErrQuotaExceeded = errors.New("model quota exceeded")
)

// Audio is a validated audio upload ready to be analysed.
type Audio struct {
	Data     []byte
	MIMEType string // as the model expects it, e.g. "audio/mp4"
}

// Note is the structured result extracted from a voice memo.
type Note struct {
	Transcript string    `json:"transcript"`
	Language   string    `json:"language"`
	Title      string    `json:"title"`
	Summary    string    `json:"summary"`
	Contact    Contact   `json:"contact"`
	NextSteps  []Task    `json:"next_steps"`
	Insights   []Insight `json:"insights"`
	Sentiment  string    `json:"sentiment"` // one of Sentiments
	Tags       []string  `json:"tags"`
}

// Contact is the customer the memo is about, if mentioned.
type Contact struct {
	Name    string `json:"name,omitempty"`
	Company string `json:"company,omitempty"`
	Email   string `json:"email,omitempty"`
}

// Task is a follow-up action mentioned in the memo.
type Task struct {
	Title    string `json:"title"`
	DueDate  string `json:"due_date,omitempty"` // DateLayout, or empty
	Type     string `json:"type"`               // one of TaskTypes
	Priority string `json:"priority"`           // one of Priorities
}

// Insight is something "said between the lines" that a sales manager should
// know about, such as a competitor mention or a complaint.
type Insight struct {
	Type string `json:"type"` // one of InsightTypes
	Text string `json:"text"` // one sentence, in the memo's language
}

// Allowed values. Plain strings keep the JSON contract readable for every
// connector; the first value of each list is the default.
var (
	TaskTypes    = []string{"todo", "email", "call", "meeting", "document"}
	Priorities   = []string{"medium", "high", "low"}
	InsightTypes = []string{"opportunity", "risk", "competitor", "product_feedback", "complaint"}
	Sentiments   = []string{"neutral", "positive", "negative"}
)

// InsightLabels are human-readable names for InsightTypes, shared by every
// connector so a note reads the same in each destination.
var InsightLabels = map[string]string{
	"opportunity":      "Opportunity",
	"risk":             "Risk",
	"competitor":       "Competitor",
	"product_feedback": "Product feedback",
	"complaint":        "Complaint",
}

// Receipt is what a sink returns after successfully storing a note.
type Receipt struct {
	ID   string   `json:"id,omitempty"`
	URL  string   `json:"url,omitempty"`
	Refs []string `json:"refs,omitempty"` // secondary records, e.g. "task:123"
}

// Delivery is the per-sink outcome reported back to the API client.
type Delivery struct {
	Sink  string `json:"sink"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"` // generic, safe to show to clients
	Err   error  `json:"-"`               // full detail, for logs only
	Receipt
}
