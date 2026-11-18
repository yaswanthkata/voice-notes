package notion

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"voice-notes/internal/domain"
	"voice-notes/internal/httpx"
)

const pageID = "1a2b3c4d5e6f708192a3b4c5d6e7f809"

func sampleNote() domain.Note {
	return domain.Note{
		Title:      "Northwind intro",
		Summary:    "Liked the demo.",
		Transcript: "I met Sarah from Northwind.",
		Language:   "en",
		Contact:    domain.Contact{Name: "Sarah Miller", Company: "Northwind", Email: "sarah@northwind.com"},
		NextSteps: []domain.Task{
			{Title: "Send pricing", DueDate: "2026-11-14", Type: "document", Priority: "high"},
			{Title: "Book IT call", Type: "call", Priority: "medium"},
		},
		Insights:  []domain.Insight{{Type: "risk", Text: "Budget needs CFO approval."}},
		Sentiment: "positive",
		Tags:      []string{"pricing"},
	}
}

// text joins a block's rich-text runs, which keeps assertions readable.
func text(b map[string]any) string {
	typ := b["type"].(string)
	body, _ := b[typ].(map[string]any)
	rt, _ := body["rich_text"].([]map[string]any)
	var sb strings.Builder
	for _, r := range rt {
		sb.WriteString(r["text"].(map[string]any)["content"].(string))
	}
	return sb.String()
}

// summary renders blocks as "type:text" lines.
func summary(bs []map[string]any) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b["type"].(string)+":"+text(b))
	}
	return out
}

// colorOf returns the colour of the run whose text contains substr.
func colorOf(b map[string]any, substr string) string {
	typ := b["type"].(string)
	for _, r := range b[typ].(map[string]any)["rich_text"].([]map[string]any) {
		if strings.Contains(r["text"].(map[string]any)["content"].(string), substr) {
			if a, ok := r["annotations"].(map[string]any); ok {
				return a["color"].(string)
			}
			return "default"
		}
	}
	return "(not found)"
}

// --- mapper (pure) ---

func TestBlocks(t *testing.T) {
	tests := []struct {
		name string
		note domain.Note
		want []string
	}{
		{
			name: "full note",
			note: sampleNote(),
			want: []string{
				"paragraph: Positive   EN   #pricing ",
				"callout:Liked the demo.",
				"heading_2:Next steps",
				"to_do:Send pricing   Send document    High priority   due 2026-11-14",
				"to_do:Book IT call   Call    Medium priority ",
				"heading_2:Insights",
				"callout:Risk: Budget needs CFO approval.",
				"heading_2:Contact",
				"callout:Sarah Miller  ·  Northwind  ·  sarah@northwind.com",
				"divider:",
				"toggle:Transcript",
				"paragraph:Created by Voice Notes",
			},
		},
		{
			name: "reminder without contact, steps or insights omits those sections",
			note: domain.Note{Summary: "Reminder to book tickets.", Transcript: "Book tickets.", Sentiment: "neutral"},
			want: []string{
				"paragraph: Neutral ",
				"callout:Reminder to book tickets.",
				"divider:",
				"toggle:Transcript",
				"paragraph:Created by Voice Notes",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summary(blocks(tc.note))
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("blocks:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestBlocksUseMatchingColors(t *testing.T) {
	bs := blocks(sampleNote())
	checks := []struct {
		block  int
		substr string
		want   string
	}{
		{0, "Positive", "green_background"},
		{0, "#pricing", "gray_background"},
		{3, "Send document", "orange_background"},
		{3, "High priority", "red_background"},
		{4, "Call", "green_background"},
		{4, "Medium priority", "yellow_background"},
	}
	for _, c := range checks {
		if got := colorOf(bs[c.block], c.substr); got != c.want {
			t.Errorf("%q in block %d: colour %s, want %s", c.substr, c.block, got, c.want)
		}
	}
	if bs[6]["callout"].(map[string]any)["color"] != "red_background" {
		t.Errorf("risk insight callout colour = %v", bs[6]["callout"].(map[string]any)["color"])
	}
}

func TestBlocksRespectNotionLimits(t *testing.T) {
	// 300k runes of multi-byte text: split into ≤2000-rune chunks inside the
	// transcript toggle, valid UTF-8, and capped at 100 children per array.
	bs := blocks(domain.Note{Transcript: strings.Repeat("ä", 300_000)})
	if len(bs) > maxChildren {
		t.Fatalf("%d top-level blocks, limit %d", len(bs), maxChildren)
	}
	var tg map[string]any
	for _, b := range bs {
		if b["type"] == "toggle" {
			tg = b["toggle"].(map[string]any)
		}
	}
	children := tg["children"].([]map[string]any)
	if len(children) != maxChildren {
		t.Fatalf("toggle has %d children, want %d", len(children), maxChildren)
	}
	for _, c := range children {
		s := text(c)
		if n := utf8.RuneCountInString(s); n > maxTextLen || !utf8.ValidString(s) {
			t.Fatalf("child has %d runes (valid UTF-8: %v)", n, utf8.ValidString(s))
		}
	}
	if last := text(children[maxChildren-1]); last != "… (truncated)" {
		t.Errorf("last child = %q", last)
	}
}

func TestPageInputTitle(t *testing.T) {
	for _, tc := range []struct{ title, want string }{
		{"Northwind intro", "Northwind intro"},
		{"", "Voice memo"},
	} {
		in := pageInput(domain.Note{Title: tc.title}, pageID)
		title := in["properties"].(map[string]any)["title"].(map[string]any)["title"].([]map[string]any)
		if got := title[0]["text"].(map[string]any)["content"]; got != tc.want {
			t.Errorf("title(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestParsePageID(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: pageID, want: pageID},
		{in: "1a2b3c4d-5e6f-7081-92a3-b4c5d6e7f809", want: pageID},
		{in: "https://www.notion.so/Sales-Notes-" + pageID + "?pvs=4", want: pageID},
		{in: "https://www.notion.so/acme/" + strings.ToUpper(pageID) + "#section", want: pageID},
		{in: "", wantErr: true},
		{in: "https://www.notion.so/Sales-Notes", wantErr: true},
		{in: strings.Repeat("z", 32), wantErr: true},
	}
	for _, tc := range tests {
		got, err := parsePageID(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("parsePageID(%q) = %q, %v; want %q, err %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

// --- API flow against a fake Notion ---

func TestPush(t *testing.T) {
	var gotReq struct {
		method, path, auth, version string
		body                        map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq.method, gotReq.path = r.Method, r.URL.Path
		gotReq.auth, gotReq.version = r.Header.Get("Authorization"), r.Header.Get("Notion-Version")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotReq.body)
		io.WriteString(w, `{"object":"page","id":"page-1","url":"https://www.notion.so/page-1"}`)
	}))
	defer srv.Close()

	s, err := New(Config{Token: "secret", ParentPageID: "https://www.notion.so/Notes-" + pageID, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Push(context.Background(), sampleNote())
	if err != nil {
		t.Fatal(err)
	}

	if gotReq.method != http.MethodPost || gotReq.path != "/v1/pages" {
		t.Errorf("request = %s %s", gotReq.method, gotReq.path)
	}
	if gotReq.auth != "Bearer secret" || gotReq.version != apiVersion {
		t.Errorf("headers: auth=%q version=%q", gotReq.auth, gotReq.version)
	}
	if parent := gotReq.body["parent"].(map[string]any); parent["page_id"] != pageID {
		t.Errorf("parent = %v", parent)
	}
	if r.ID != "page-1" || r.URL != "https://www.notion.so/page-1" {
		t.Errorf("receipt = %+v", r)
	}
}

func TestPushVendorError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound) // parent page not shared with the integration
		io.WriteString(w, `{"object":"error","status":404,"code":"object_not_found"}`)
	}))
	defer srv.Close()

	s, _ := New(Config{Token: "t", ParentPageID: pageID, BaseURL: srv.URL})
	_, err := s.Push(context.Background(), sampleNote())
	if httpx.StatusOf(err) != http.StatusNotFound || calls != 1 {
		t.Fatalf("err = %v calls = %d; want 404 after a single call", err, calls)
	}
}

func TestNewRequiresConfig(t *testing.T) {
	for _, cfg := range []Config{
		{ParentPageID: pageID},
		{Token: "t"},
		{Token: "t", ParentPageID: "not-an-id"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v): expected error", cfg)
		}
	}
}
