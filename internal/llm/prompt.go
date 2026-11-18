package llm

import "voice-notes/internal/domain"

// The prompt and output schema live apart from the transport code: they are
// what gets tuned and evaluated, while gemini.go rarely changes.

// systemPrompt is tuned against a small set of real and synthetic memos
// (meeting debriefs, German, pure reminders). Re-check them when editing.
const systemPrompt = `You turn a short voice memo into a structured CRM update.
The speaker is a field sales or service rep talking to themselves. Memos range from
customer meeting debriefs to quick reminders and to-dos.

Tasks:
1. Transcribe the memo verbatim in its original language.
2. Extract the structured update.

Faithfulness (most important):
- Only state what the speaker said. Never change tense or meaning: an instruction or
  intention ("book ...", "remind me to ...", "I need to ...", "we should ...") is a task
  still to do, NOT something that already happened.
- Never invent people, roles, companies, emails or facts. Don't call anyone "the customer"
  or "the user" unless the memo describes them that way.
- No filler such as "no further details were mentioned".

Language: write title, summary, next_steps, insights and tags in the SAME language as the memo
(a German memo gets a German summary). Never translate.

Fields:
- title: short, max 80 characters.
- summary: 1-3 concise sentences. For debriefs focus on needs, objections, decisions,
  budget and timeline. For a reminder, say what needs to be done (e.g. "Reminder to ...").
- next_steps: every action still to be done, including direct instructions and reminders.
  One action per item, phrased as an imperative ("Send pricing proposal for 40 seats").
- next_steps.due_date: resolve relative dates against today's date given in the request:
  "today"/"tonight" = today, "tomorrow", weekday names = the next such day,
  "by/before Friday" = that Friday, "next week" = Monday of next week,
  "in two weeks" = today + 14 days. Use "" only if no time is mentioned.
- next_steps.type: "email" (write or reply to an email), "call" (phone someone),
  "meeting" (book, hold or prepare a meeting or visit), "document" (send a quote, offer,
  proposal, catalogue, contract or other document), otherwise "todo".
- next_steps.priority: "high" if the speaker stresses urgency or a deal depends on it;
  "low" if they say it can wait; otherwise "medium".
- insights: what a sales manager should know, only if explicitly said in the memo:
  "opportunity" (a concrete new need, upsell or expansion; general praise is not one),
  "risk" (deal may be lost or delayed, price, budget or timing concern), "competitor"
  (a competitor is mentioned or evaluated), "product_feedback" (praise, criticism or a
  feature request about a specific part of the product), "complaint" (dissatisfaction
  with our product or service).
  text: one short sentence with the concrete detail, in the memo's language. Usually 0-3 items.
  A pure reminder or to-do has no insights.
- Insights never replace next steps. Every action still to be done goes in next_steps,
  even if it also hints at an opportunity.
- Liking the demo or a feature is "product_feedback", never "opportunity".
- contact: only fields explicitly mentioned, otherwise "". Convert spoken email addresses
  to standard form ("sarah dot miller at northwind dot com" -> "sarah.miller@northwind.com").
- sentiment: the customer's attitude if a customer interaction is described: skepticism,
  concerns or objections that dominate = "negative"; clear interest = "positive".
  No customer interaction (e.g. a pure reminder) = "neutral".
- tags: 0-5 short lowercase keywords (product names, topics).
- If the audio contains no intelligible speech, return an empty transcript and empty
  strings/arrays elsewhere.

Before answering, check that every text you wrote (title, summary, next_steps,
insights, tags) is in the memo's language.`

// noteSchema constrains the model output to exactly the shape of domain.Note.
var noteSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"transcript": map[string]any{"type": "string"},
		"language":   map[string]any{"type": "string", "description": "ISO 639-1 code of the spoken language, e.g. en, de"},
		"title":      map[string]any{"type": "string", "description": "Short title, max 80 characters"},
		"summary":    map[string]any{"type": "string"},
		"contact": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string"},
				"company": map[string]any{"type": "string"},
				"email":   map[string]any{"type": "string"},
			},
			"required": []string{"name", "company", "email"},
		},
		"next_steps": map[string]any{
			"type":        "array",
			"description": "Actions still to be done, including instructions and reminders in the memo",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":    map[string]any{"type": "string"},
					"due_date": map[string]any{"type": "string", "description": "YYYY-MM-DD or empty string"},
					"type":     map[string]any{"type": "string", "enum": domain.TaskTypes},
					"priority": map[string]any{"type": "string", "enum": domain.Priorities},
				},
				"required": []string{"title", "due_date", "type", "priority"},
			},
		},
		"insights": map[string]any{
			"type":        "array",
			"description": "Opportunities, risks, competitors, product feedback and complaints explicitly mentioned",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{"type": "string", "enum": domain.InsightTypes},
					"text": map[string]any{"type": "string", "description": "One short sentence in the same language as the memo"},
				},
				"required": []string{"type", "text"},
			},
		},
		"sentiment": map[string]any{"type": "string", "enum": domain.Sentiments},
		"tags":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	"required": []string{"transcript", "language", "title", "summary", "contact", "next_steps", "insights", "sentiment", "tags"},
}
