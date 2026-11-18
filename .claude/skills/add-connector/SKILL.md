---
name: add-connector
description: Add a new delivery connector (CRM, note-taking tool, spreadsheet, chat, webhook-style destination) to voice-notes. Use when asked to integrate Notion, Google Sheets, Salesforce, Pipedrive, Slack, Airtable or any other system that should receive analysed notes.
---

# Add a connector

Connectors live in `internal/sinks/<name>/` and plug into the registry in
`internal/sinks/sink.go`. The core must not change. Follow `internal/sinks/hubspot/`
as the reference implementation.

## Steps

1. **Check the vendor docs first** (current API version, auth method, required
   scopes, rate limits, field limits). Don't rely on memory; APIs change.
   If a no-code route is enough, point out that `SINKS=webhook` + Zapier/Make/n8n
   already covers it, and ask before building a native connector.

2. **Create `internal/sinks/<name>/mapper.go`** — pure functions, no I/O:
   - `domain.Note` → vendor payload structs.
   - Escape model output if the vendor renders HTML/Markdown (`html.EscapeString`).
   - Respect vendor field limits (truncate, don't fail).
   - Missing optional data (no email, no due date) must degrade gracefully; never invent data.

3. **Create `internal/sinks/<name>/<name>.go`** — transport:
   ```go
   func init() {
       sinks.Register("<name>", func(env sinks.Env) (sinks.Sink, error) {
           return New(Config{Token: env("<NAME>_TOKEN"), BaseURL: env("<NAME>_BASE_URL")})
       })
   }
   ```
   - `New(cfg)` validates required config and returns a clear error naming the env var.
   - Default `BaseURL` to the vendor's; keep it overridable for tests.
   - Use `httpx.New(baseURL, header)` for all HTTP. Do not hand-roll retries.
   - `Name()` returns exactly the registry name (`sinks.Build` rejects a mismatch); `Push(ctx, note)` returns a `domain.Receipt`
     (primary record ID, optional deep-link URL, `Refs` like `"task:123"`).
   - Wrap errors with context: `fmt.Errorf("create page: %w", err)`.

4. **Enable it**: add `_ "voice-notes/internal/sinks/<name>"` to the import block in
   `cmd/server/main.go`. Nothing else in the core changes.

5. **Tests** in `internal/sinks/<name>/<name>_test.go`:
   - Mapper: table-driven, including escaping and missing-field cases.
   - Flow: fake the vendor with `httptest.Server`; assert the exact call sequence,
     auth header, and payload; cover a vendor error and the no-optional-data path.
   - `New` without required config returns an error.

6. **Config & docs**: add the connector's variables to `.env.example` (comments on
   their own line), and a short section to the README (how to obtain credentials,
   required scopes).

7. **Done when**: `gofmt -l .` is empty, `go vet ./...` is clean,
   `go test -race ./...` passes, and `SINKS=<name>,console go run ./cmd/server`
   starts (or fails with a clear missing-config error).
