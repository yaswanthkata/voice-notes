package notion

import (
	"errors"
	"fmt"
	"strings"

	"voice-notes/internal/domain"
)

// Pure mapping from the CRM-neutral domain.Note to Notion blocks. No I/O, so it
// is unit-tested on its own; notion.go does the transport.

// Notion API limits (developers.notion.com/reference/request-limits).
const (
	maxTextLen  = 2000 // characters per rich_text item
	maxChildren = 100  // blocks per request
)

// pageInput maps a note to a POST /v1/pages body: a sub-page of parentID whose
// title is the note title and whose body holds everything else as blocks.
// Text goes in rich_text "content", which Notion renders literally, so model
// output can't inject markup or links.
func pageInput(note domain.Note, parentID string) map[string]any {
	title := note.Title
	if title == "" {
		title = "Voice memo"
	}
	return map[string]any{
		"parent": map[string]any{"type": "page_id", "page_id": parentID},
		"icon":   map[string]any{"type": "emoji", "emoji": "🎙️"},
		// "title" is the fixed property ID of a page's title, whatever its display name.
		"properties": map[string]any{"title": map[string]any{"title": richText(seg{text: title})}},
		"children":   blocks(note),
	}
}

// Colours mirror the web page so a note looks the same everywhere. Notion has
// no pill component in page bodies; text with a background colour is the
// idiomatic substitute.
var (
	sentimentColors = map[string]string{"positive": "green_background", "negative": "red_background", "neutral": "gray_background"}
	priorityColors  = map[string]string{"high": "red_background", "medium": "yellow_background", "low": "gray_background"}
	taskTypes       = map[string]struct{ label, color string }{
		"email":    {"Email", "blue_background"},
		"call":     {"Call", "green_background"},
		"meeting":  {"Meeting", "purple_background"},
		"document": {"Send document", "orange_background"},
		"todo":     {"To-do", "gray_background"},
	}
	insightStyles = map[string]struct{ emoji, color string }{
		"opportunity":      {"📈", "green_background"},
		"risk":             {"⚠️", "red_background"},
		"competitor":       {"🏁", "purple_background"},
		"product_feedback": {"💬", "blue_background"},
		"complaint":        {"😟", "orange_background"},
	}
)

func blocks(note domain.Note) []map[string]any {
	var out []map[string]any

	// Status line: sentiment, language and tags as coloured "pills".
	var status []seg
	if c, ok := sentimentColors[note.Sentiment]; ok {
		status = append(status, pill(capitalize(note.Sentiment), c))
	}
	if note.Language != "" {
		status = append(status, seg{text: strings.ToUpper(note.Language), color: "gray"})
	}
	for _, t := range note.Tags {
		status = append(status, pill("#"+t, "gray_background"))
	}
	if len(status) > 0 {
		out = append(out, block("paragraph", spaced(status)...))
	}

	if note.Summary != "" {
		out = append(out, callout("📝", "gray_background", seg{text: note.Summary}))
	}

	if len(note.NextSteps) > 0 {
		out = append(out, block("heading_2", seg{text: "Next steps"}))
		for _, t := range note.NextSteps {
			out = append(out, todo(t))
		}
	}

	if len(note.Insights) > 0 {
		out = append(out, block("heading_2", seg{text: "Insights"}))
		for _, in := range note.Insights {
			st, ok := insightStyles[in.Type]
			if !ok {
				continue
			}
			out = append(out, callout(st.emoji, st.color, seg{text: domain.InsightLabels[in.Type] + ": ", bold: true}, seg{text: in.Text}))
		}
	}

	if c := note.Contact; c.Name != "" || c.Company != "" || c.Email != "" {
		out = append(out, block("heading_2", seg{text: "Contact"}))
		parts := []seg{{text: orDefault(c.Name, "Unknown name"), bold: true}}
		for _, v := range []string{c.Company, c.Email} {
			if v != "" {
				parts = append(parts, seg{text: "  ·  " + v, color: "gray"})
			}
		}
		out = append(out, callout("👤", "default", parts...))
	}

	if note.Transcript != "" {
		out = append(out, map[string]any{"object": "block", "type": "divider", "divider": map[string]any{}})
		var paras []map[string]any
		for _, chunk := range chunks(note.Transcript, maxTextLen) {
			paras = append(paras, block("paragraph", seg{text: chunk, color: "gray"}))
		}
		out = append(out, toggle(seg{text: "Transcript", bold: true}, capBlocks(paras)))
	}

	out = append(out, block("paragraph", seg{text: "Created by Voice Notes", italic: true, color: "gray"}))
	return capBlocks(out)
}

// todo renders a next step as a checkbox: title, then type, priority and due
// date as coloured pills.
func todo(t domain.Task) map[string]any {
	var details []seg
	if tt, ok := taskTypes[t.Type]; ok {
		details = append(details, pill(tt.label, tt.color))
	}
	if c, ok := priorityColors[t.Priority]; ok {
		details = append(details, pill(capitalize(t.Priority)+" priority", c))
	}
	if t.DueDate != "" {
		details = append(details, seg{text: "due " + t.DueDate, color: "gray"})
	}
	b := block("to_do", spaced(append([]seg{{text: t.Title}}, details...))...)
	b["to_do"].(map[string]any)["checked"] = false
	return b
}

// capBlocks keeps a block list within Notion's per-array limit, marking the cut
// instead of failing the delivery.
func capBlocks(bs []map[string]any) []map[string]any {
	if len(bs) <= maxChildren {
		return bs
	}
	return append(bs[:maxChildren-1], block("paragraph", seg{text: "… (truncated)", italic: true, color: "gray"}))
}

// seg is one rich-text run with optional formatting.
type seg struct {
	text         string
	bold, italic bool
	color        string // Notion colour, e.g. "gray" or "red_background"
}

// spaced puts a gap between segments, never after the last one.
func spaced(segs []seg) []seg {
	out := make([]seg, 0, 2*len(segs))
	for i, s := range segs {
		if i > 0 {
			out = append(out, seg{text: "  "})
		}
		out = append(out, s)
	}
	return out
}

// pill is a short label on a coloured background, padded so it reads as a tag.
func pill(label, color string) seg { return seg{text: " " + label + " ", bold: true, color: color} }

func richText(segs ...seg) []map[string]any {
	out := make([]map[string]any, 0, len(segs))
	for _, s := range segs {
		if s.text == "" {
			continue
		}
		item := map[string]any{"type": "text", "text": map[string]any{"content": truncate(s.text, maxTextLen)}}
		if s.bold || s.italic || s.color != "" {
			item["annotations"] = map[string]any{"bold": s.bold, "italic": s.italic, "color": orDefault(s.color, "default")}
		}
		out = append(out, item)
	}
	return out
}

func block(typ string, segs ...seg) map[string]any {
	return map[string]any{"object": "block", "type": typ, typ: map[string]any{"rich_text": richText(segs...)}}
}

func callout(emoji, color string, segs ...seg) map[string]any {
	return map[string]any{"object": "block", "type": "callout", "callout": map[string]any{
		"rich_text": richText(segs...),
		"icon":      map[string]any{"type": "emoji", "emoji": emoji},
		"color":     color,
	}}
}

func toggle(label seg, children []map[string]any) map[string]any {
	return map[string]any{"object": "block", "type": "toggle", "toggle": map[string]any{
		"rich_text": richText(label),
		"children":  children,
	}}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// chunks splits s into pieces of at most n runes, so multi-byte characters
// (German umlauts) are never cut in half.
func chunks(s string, n int) []string {
	r := []rune(s)
	var out []string
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return append(out, string(r))
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// parsePageID accepts a raw page ID (with or without dashes) or a page URL
// copied from Notion, e.g. https://www.notion.so/Sales-Notes-1a2b...?pvs=4,
// because pasting the URL is what users naturally do.
func parsePageID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "-", "")
	if len(s) < 32 {
		return "", errors.New("not a Notion page ID or URL")
	}
	id := strings.ToLower(s[len(s)-32:])
	if strings.Trim(id, "0123456789abcdef") != "" {
		return "", fmt.Errorf("not a Notion page ID or URL: %q", id)
	}
	return id, nil
}
