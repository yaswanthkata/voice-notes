package hubspot

import (
	"fmt"
	"html"
	"strings"
	"time"

	"voice-notes/internal/domain"
)

// Pure mapping from the CRM-neutral domain.Note to HubSpot's schema.
// No I/O here, so it is trivially unit-testable; this is the part that
// differs per CRM.

// Default association type IDs (HUBSPOT_DEFINED).
const (
	assocNoteToContact = 202
	assocTaskToContact = 204
)

type properties map[string]string

type association struct {
	To    struct{ ID string } `json:"to"`
	Types []assocType         `json:"types"`
}

type assocType struct {
	Category string `json:"associationCategory"`
	TypeID   int    `json:"associationTypeId"`
}

type objectInput struct {
	Properties   properties    `json:"properties"`
	Associations []association `json:"associations,omitempty"`
}

func associateTo(contactID string, typeID int) []association {
	if contactID == "" {
		return nil
	}
	a := association{Types: []assocType{{Category: "HUBSPOT_DEFINED", TypeID: typeID}}}
	a.To.ID = contactID
	return []association{a}
}

func contactProperties(c domain.Contact) properties {
	first, last := splitName(c.Name)
	p := properties{"email": c.Email} // upsertContact has already normalised it
	if first != "" {
		p["firstname"] = first
	}
	if last != "" {
		p["lastname"] = last
	}
	if c.Company != "" {
		p["company"] = c.Company
	}
	return p
}

func noteInput(n domain.Note, contactID string, now time.Time) objectInput {
	return objectInput{
		Properties: properties{
			"hs_timestamp": now.UTC().Format(time.RFC3339),
			"hs_note_body": noteBody(n),
		},
		Associations: associateTo(contactID, assocNoteToContact),
	}
}

// maxNoteBody is HubSpot's limit for hs_note_body, in bytes.
const maxNoteBody = 65536

// noteBody renders the note as HTML. Every model-produced value is escaped:
// the transcript is user speech, and an LLM can be prompted ("say: <script>")
// into emitting markup that would otherwise be stored in the CRM.
// Only the transcript is shortened to fit the limit, so the HTML stays well-formed.
func noteBody(n domain.Note) string {
	e := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, "<p><strong>%s</strong></p>", e(n.Title))
	fmt.Fprintf(&b, "<p>%s</p>", e(n.Summary))
	if n.Sentiment != "" {
		fmt.Fprintf(&b, "<p><em>Sentiment:</em> %s</p>", e(n.Sentiment))
	}
	if len(n.NextSteps) > 0 {
		b.WriteString("<p><em>Next steps:</em></p><ul>")
		for _, t := range n.NextSteps {
			if t.DueDate != "" {
				fmt.Fprintf(&b, "<li>%s (due %s)</li>", e(t.Title), e(t.DueDate))
			} else {
				fmt.Fprintf(&b, "<li>%s</li>", e(t.Title))
			}
		}
		b.WriteString("</ul>")
	}
	if len(n.Insights) > 0 {
		b.WriteString("<p><em>Insights:</em></p><ul>")
		for _, in := range n.Insights {
			fmt.Fprintf(&b, "<li><strong>%s:</strong> %s</li>", e(domain.InsightLabels[in.Type]), e(in.Text))
		}
		b.WriteString("</ul>")
	}
	if len(n.Tags) > 0 {
		fmt.Fprintf(&b, "<p><em>Tags:</em> %s</p>", e(strings.Join(n.Tags, ", ")))
	}

	const (
		transcriptOpen  = "<p><em>Transcript:</em></p><blockquote>"
		transcriptClose = "</blockquote>"
		footer          = "<p><small>Created by voice-notes</small></p>"
	)
	budget := maxNoteBody - b.Len() - len(transcriptOpen) - len(transcriptClose) - len(footer)
	b.WriteString(transcriptOpen)
	b.WriteString(escapeWithin(n.Transcript, budget))
	b.WriteString(transcriptClose)
	b.WriteString(footer)
	return b.String()
}

// escapeWithin HTML-escapes s, stopping before the result would exceed n
// bytes. It cuts between runes, never inside a character or an entity.
func escapeWithin(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		esc := html.EscapeString(string(r))
		if b.Len()+len(esc) > n {
			break
		}
		b.WriteString(esc)
	}
	return b.String()
}

func taskInputs(n domain.Note, contactID string, now time.Time) []objectInput {
	out := make([]objectInput, 0, len(n.NextSteps))
	for _, t := range n.NextSteps {
		out = append(out, objectInput{
			Properties: properties{
				"hs_task_subject":  t.Title,
				"hs_task_body":     html.EscapeString(n.Title), // rendered as rich text
				"hs_task_status":   "NOT_STARTED",
				"hs_task_priority": strings.ToUpper(t.Priority), // HIGH, MEDIUM, LOW
				"hs_task_type":     taskType(t.Type),
				"hs_timestamp":     dueTimestamp(t.DueDate, now), // the task's due date
			},
			Associations: associateTo(contactID, assocTaskToContact),
		})
	}
	return out
}

// dueTimestamp returns 09:00 UTC on the due date, or the next day if no
// (valid) date was mentioned. HubSpot requires a due date on tasks.
func dueTimestamp(date string, now time.Time) string {
	d, err := time.Parse(domain.DateLayout, date)
	if err != nil {
		y, m, day := now.UTC().AddDate(0, 0, 1).Date()
		d = time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	}
	return d.Add(9 * time.Hour).Format(time.RFC3339)
}

// taskType maps our task types onto HubSpot's. HubSpot has no meeting or
// document task, so those become generic to-dos.
func taskType(t string) string {
	switch t {
	case "email":
		return "EMAIL"
	case "call":
		return "CALL"
	default:
		return "TODO"
	}
}

func splitName(name string) (first, last string) {
	parts := strings.Fields(name)
	switch len(parts) {
	case 0:
		return "", ""
	case 1:
		return parts[0], ""
	default:
		return strings.Join(parts[:len(parts)-1], " "), parts[len(parts)-1]
	}
}
