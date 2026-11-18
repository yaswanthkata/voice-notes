package sheets

import (
	"fmt"
	"strings"
	"time"

	"voice-notes/internal/domain"
)

// Pure mapping from a note to one spreadsheet row. No I/O, so it is unit-tested
// on its own.

// header is written once to an empty sheet, so a fresh spreadsheet is readable
// without any manual setup.
var header = []any{
	"Created (UTC)", "Title", "Summary", "Contact", "Company", "Email",
	"Sentiment", "Next steps", "Insights", "Tags", "Language", "Transcript",
}

// columns and headerRange span exactly the header; keep them in sync.
const (
	columns     = "A:L"
	headerRange = "A1:L1"
)

// maxCell is Google Sheets' limit on characters per cell.
const maxCell = 50000

// row renders the note in header order. Lists become one line per item so the
// cells stay readable with text wrapping on.
func row(n domain.Note, now time.Time) []any {
	steps := make([]string, 0, len(n.NextSteps))
	for _, t := range n.NextSteps {
		details := []string{t.Type, t.Priority + " priority"}
		if t.DueDate != "" {
			details = append(details, "due "+t.DueDate)
		}
		steps = append(steps, fmt.Sprintf("• %s (%s)", t.Title, strings.Join(details, ", ")))
	}
	insights := make([]string, 0, len(n.Insights))
	for _, in := range n.Insights {
		insights = append(insights, "• "+domain.InsightLabels[in.Type]+": "+in.Text)
	}

	cells := []string{
		now.UTC().Format("2006-01-02 15:04"),
		n.Title,
		n.Summary,
		n.Contact.Name,
		n.Contact.Company,
		n.Contact.Email,
		n.Sentiment,
		strings.Join(steps, "\n"),
		strings.Join(insights, "\n"),
		strings.Join(n.Tags, ", "),
		n.Language,
		n.Transcript,
	}
	out := make([]any, len(cells))
	for i, c := range cells {
		out[i] = truncate(c, maxCell)
	}
	return out
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
