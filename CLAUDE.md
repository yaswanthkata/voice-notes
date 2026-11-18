# voice-notes

Go (Gin) service: a voice memo is uploaded, Gemini transcribes it and extracts a
structured CRM note in one call, and the note is delivered in parallel to
pluggable connectors (HubSpot, Notion, Google Sheets, webhook, console). Built as a time-boxed (~4h)
interview assignment: keep it production-minded but **do not overengineer**.

## Commands

```bash
go run ./cmd/server                # needs GEMINI_API_KEY in .env; UI at http://localhost:8080
go test ./...                      # unit tests, no network or keys needed
go test -race ./...                # run before calling work done
go vet ./... && gofmt -l .         # gofmt -l must print nothing
curl -F audio=@samples/sales-call-en.m4a localhost:8080/v1/notes
make check                         # gofmt + vet + race tests (what CI should run)
make deploy-gcp                    # Cloud Run; needs GEMINI_API_KEY + API_KEY exported (see README)
```

Config is env-only (`.env` loaded by godotenv; real env wins). See `.env.example`.

## Architecture

```
cmd/server/main.go     composition root: the ONLY place concrete types are wired
internal/domain/       Note, Contact, Task, Insight, Receipt, Delivery, enums, domain errors.
                       Imports nothing internal.
internal/config/       core settings only. No vendor-specific fields, ever.
internal/api/          Gin router, handler, middleware, errors.go (error contract), audio.go
                       (upload validation). Depends on interfaces (Analyzer, Dispatcher).
internal/llm/          Gemini adapter (google.golang.org/genai), implements api.Analyzer.
                       prompt.go = system prompt + JSON schema; gemini.go = transport.
internal/httpx/        shared JSON HTTP client for connectors (timeouts, safe retries, typed errors)
internal/sinks/        connector extension point: Sink interface, Register/Build registry, Fanout
internal/sinks/<name>/ one package per connector (hubspot, notion, sheets, webhook, console)
web/                   static test page, embedded via go:embed
samples/               test memos (English + German)
```

Request flow (`POST /v1/notes`): RequestID → Recovery → AccessLog → SecurityHeaders →
RateLimit → RequireAPIKey (if API_KEY set) → LimitConcurrency → handler:
size cap → sniff audio type → validate optional `sinks` field (before the paid
analysis) → `Analyze` (ctx tied to client) → `Dispatch(only)` (ctx detached from
client via `context.WithoutCancel`) → 201 / 207 / 502.

### Dependency rules (keep these intact)

- `domain` depends on nothing. Everything may depend on `domain`.
- `api` never imports `llm`, `sinks`, or any connector — only its own small interfaces.
- The `sinks` core never imports a connector. Connectors import `sinks` and call
  `sinks.Register` from `init()`; `main.go` enables them via blank import.
- Each connector reads its own config through the `sinks.Env` func with its own
  prefix (`HUBSPOT_*`, `NOTION_*`, `SHEETS_*`, `WEBHOOK_*`). Never add connector
  fields to `config.Config`.
- Define interfaces where they are consumed, keep them small.

## Conventions

- Errors: wrap with `fmt.Errorf("doing x: %w", err)`; use sentinel errors
  (`domain.ErrNoSpeech`) and `errors.Is/As` for control flow, never string matching.
- `context.Context` is the first parameter of anything doing I/O; always honour it.
- Error contract: every error response is `{request_id, error, code, retryable}`
  (+ `Retry-After` header when retryable). `error` is a friendly sentence safe to
  show users; `code` is stable snake_case for clients. Adapters wrap provider
  failures in domain sentinels (`ErrModelBusy`, `ErrQuotaExceeded`, `ErrNoSpeech`,
  `context.DeadlineExceeded`); `api.analyzeError` maps them to HTTP. Add new
  cases there, never by string-matching provider messages.
- Clients get generic error messages; full detail goes to logs only (`slog`, JSON).
  Always log `request_id`. **Never log transcripts, audio, note content, or tokens** (PII/secrets).
- Treat model output and transcripts as untrusted input: escape before rendering
  as HTML (`html.EscapeString` in connectors, `textContent` in `web/`, never `innerHTML`),
  plain `rich_text` content for Notion, `valueInputOption=RAW` for Sheets (no formulas).
- Connectors only call fixed vendor hosts. Never let request data choose a URL (SSRF;
  on Cloud Run the metadata server is reachable).
- Connectors split **pure mapping** (`mapper.go`, no I/O, unit-tested) from
  **transport** (`<name>.go`, uses `httpx`). See `internal/sinks/hubspot/`.
- Retries: only via `httpx`. 429 is always retried; 5xx/network only for idempotent
  methods. Never add POST retries on 5xx — duplicate CRM records. `httpx` never
  follows redirects (a redirect could re-send a signed payload to another host).
- A connector's `Name()` must equal its registry name; `sinks.Build` enforces it,
  because requests select destinations by that name.
- Tests: table-driven, standard library only (no testify). Fakes over mocks.
  External APIs are faked with `httptest.Server` (Gemini via `GeminiConfig.BaseURL`,
  connectors via their `BaseURL`). Inject time via a `now func() time.Time` field.
- No new third-party dependencies without a clear reason; prefer the stdlib.
- Comments explain *why*, not what. Package docs at the top of the main file.
- Keep the frontend dependency-free (no CDN, no build step) — the CSP forbids it.

## Definition of done

1. `gofmt -l .` prints nothing, `go vet ./...` clean.
2. `go test -race ./...` passes; new behaviour has a test (including error paths).
3. Live checks against Gemini/HubSpot are optional and must never be committed as
   tests that require network or keys.
4. Update `.env.example` (comments on their **own line**) and the README when config changes.

## Gotchas we already hit (don't repeat)

- **godotenv inline comments**: `KEY=   # comment` makes the comment the value when
  the value is empty. Put comments on their own line in `.env*` files.
- **Gemini models** (measured 2026-11-14, same prompt + schema): default is
  `gemini-3.5-flash-lite` (3–6s/memo, same extraction quality on our samples,
  slightly less nuanced sentiment). `3.6-flash` / `3.5-flash` took 17–31s;
  `3.8-flash`, `3.7-flash`, `flash-latest` hung (server-side 504) for our key;
  `2.5-flash` is closed to new users. Thinking tokens were 0 in all cases, so
  thinking level isn't a speed lever. Never drop the response schema: without
  it output was invalid JSON. Free tier hits 429 quickly. Model is `GEMINI_MODEL`.
- The genai SDK forwards the ctx deadline as `x-server-timeout`; Google's 504
  arrives as `genai.APIError` (a **value**, not pointer) and is mapped to
  `context.DeadlineExceeded` → HTTP 504.
- `mimetype` detects `.webm` as `video/webm` (`audio/webm` is an alias) — match with
  `mt.Is(...)`, not string equality. Map to Gemini names (`audio/mpeg` → `audio/mp3`).
- `http.FileServer` redirects `/index.html` → `./`; the router serves index bytes directly.
- `httptest.Server.Close` blocks on handlers that never return — bound fake handlers.
- HubSpot: legacy private apps can't be created since 2026-10-26; use a **service key**.
  Endpoints are date-versioned (`/crm/objects/2026-09/...`). Association type IDs:
  note→contact 202, task→contact 204. Contacts are matched by lowercased email only.

- **Prompt changes need a mini eval, not one run.** Output is non-deterministic: the
  old prompt ("recorded right after a customer meeting") turned the reminder "Book two
  movie tickets…" into "The customer booked…" in some runs and dropped the next step in
  3/5. Re-run several memo types (debrief EN, debrief DE, pure reminder) multiple times
  before and after editing `systemPrompt`. A parenthetical language rule was too weak
  (German memo got an English summary); keep critical rules as standalone lines.
- Adding the insights field (2026-11-16) caused two regressions caught only by the
  eval: German insights in English, and an action moved from next_steps into an
  insight (to-do lost). Fixed with standalone rules plus a final "check the language"
  line. Deterministic rules (due today/tomorrow = high priority) live in `normalize`,
  not the prompt: the model applied them inconsistently.

- **Notion**: "integrations" are now Developer tools → Developer connections →
  **API token**. The token sees nothing until a page is shared with it (page →
  Connections), otherwise 404. API version pinned to `2026-03-11`; page parent, so
  only the `title` property exists. Limits: 2000 chars per rich-text run, 100
  blocks per array (transcript chunks go inside a toggle). Rich-text background
  colours stand in for tag pills.
- **Google Sheets**: no secret. ADC = the Cloud Run service account (share the
  sheet with it as Editor) or, locally, `gcloud auth application-default login`
  with the spreadsheets scope. A range without a tab name targets the first tab,
  so localized default tab names ("Tabelle1") don't matter.

- **Cloud Run deploy** (deploy/cloudrun.sh): `/healthz` is reserved by Cloud Run's
  front end (use `/health` there). Requests arrive from the internal proxy
  `169.254.x`, so `TRUSTED_PROXIES=169.254.0.0/16` is needed for per-client rate
  limits. New projects need `roles/run.builder` on the default compute service
  account before `--source` builds work, and a new runtime service account needs
  ~1 min to propagate. Never deploy with the console sink: it writes transcripts
  into Cloud Logging. The script reads `DEPLOY_SINKS` (default `none`, refuses
  `console`), not `SINKS`, so a sourced local `.env` can't leak into a deploy.
  Newer Gemini API keys start with `AQ.` (53 chars), not `AIza`.

## Secrets

`.env` holds real keys. Don't read or print it; check presence by key name only.
