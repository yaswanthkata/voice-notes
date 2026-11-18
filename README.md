# voice-notes

Turn a voice memo into a CRM update. Upload or record audio; Gemini transcribes it
and extracts a structured note in a single call: summary, contact, typed next steps
(email, call, meeting, send document, to-do) with due dates and priority, insights
"between the lines" (opportunities, risks, competitors, product feedback, complaints),
sentiment and tags; the note is then delivered in parallel to any
number of destinations: **HubSpot**, **Notion**, **Google Sheets**, a signed
**webhook**, or the console.

```
audio ──▶ POST /v1/notes ──▶ Gemini (transcribe + extract, JSON schema)
                                  │
                                  ▼
                  ┌──────── Fanout (parallel) ────────┐
                  ▼          ▼          ▼             ▼
               HubSpot    Notion   Google Sheets   webhook / console
```

## Quick start

Requires Go 1.27+ and a [Gemini API key](https://aistudio.google.com/apikey).

```bash
cp .env.example .env          # set GEMINI_API_KEY
make run                      # or: go run ./cmd/server
```

Open <http://localhost:8080> to record or upload a memo, or use curl:

```bash
curl -F audio=@samples/sales-call-en.m4a localhost:8080/v1/notes
```

`samples/` has an English and a German memo. With the default `SINKS=console`
no CRM account is needed: the note is printed to the server's stdout. That's a
development sink; it prints note content, so don't enable it in production.

### Test page

The page at `/` is a dependency-free client for the API (no build step; the strict
CSP allows only its own files):

- record in the browser (converted to 16 kHz mono WAV) or drop/upload a file;
- **Send to**: one toggle per configured destination, so each memo can go to any
  subset of them (or none, to only analyse);
- the result: title, sentiment, summary, next steps with type, priority and due
  date, insights, contact, the full transcript, and each delivery with a link to
  the created record (Notion page, Google Sheet, HubSpot contact).

All model output is rendered with `textContent`, never as HTML.

## API

`POST /v1/notes`, multipart form with an `audio` file (WAV, MP3, M4A, AAC, OGG,
FLAC or WebM, up to `MAX_UPLOAD_MB`). The format is detected from the file's
bytes, not the client's Content-Type.

An optional `sinks` field chooses the destinations for this memo, e.g.
`-F sinks=notion,sheets`: any subset of the configured `SINKS`, or `none` to only
analyse. Without it, the note goes to every configured destination. Unknown names
are rejected with `400 unknown_destination` before the (paid) analysis runs.

| Status | Meaning |
|---|---|
| `201` | Analysed and delivered everywhere |
| `207` | Analysed; some destinations failed (see `deliveries`) |
| `502` | Analysed; all destinations failed. The note is still returned, so the analysis isn't lost |
| `4xx` | Invalid upload (`audio_missing`, `audio_too_large`, `unsupported_format`, …), `unknown_destination` (400), `unauthorized` (401) or `no_speech` (422) |
| `429` / `503` / `504` | Model quota, overload or timeout; retryable, with `Retry-After` |

```jsonc
// 201
{
  "request_id": "4KBQIXDZKVLXN3ACUAOLYH5FGJ",
  "note": {
    "transcript": "…", "language": "en",
    "title": "Meeting with Sarah Miller from Northwind Logistics",
    "summary": "…",
    "contact": { "name": "Sarah Miller", "company": "Northwind Logistics", "email": "sarah.miller@northwind.com" },
    "next_steps": [
      { "title": "Send pricing proposal for 40 seats", "due_date": "2026-11-20", "type": "document", "priority": "medium" }
    ],
    "insights": [{ "type": "competitor", "text": "Northwind is also evaluating Contoso." }],
    "sentiment": "positive", "tags": ["pricing"]
  },
  "deliveries": [{ "sink": "hubspot", "ok": true, "id": "123", "url": "https://app.hubspot.com/…" }]
}

// every error (4xx, 429, 5xx)
{ "request_id": "…", "error": "Friendly sentence, safe to show users.", "code": "model_busy", "retryable": true }
```

`207` and `502` are not errors in that sense: the body is the full result above,
plus `code: "delivery_failed"` and an `error` message, with each failure in `deliveries`.

Also: `GET /healthz` or `GET /health` (liveness; Cloud Run reserves `/healthz`) and `GET /meta` (what the test page needs).

## Configuration

All configuration is environment variables (`.env` is loaded for local runs; real
environment variables win). See [.env.example](.env.example) for every option.

| Variable | Default | |
|---|---|---|
| `GEMINI_API_KEY` | (required) | |
| `PORT` | `8080` | |
| `GEMINI_MODEL` | `gemini-3.5-flash-lite` | Fastest model with equal extraction quality on our samples (3–6 s per memo) |
| `SINKS` | `console` | Comma-separated: `console`, `hubspot`, `notion`, `sheets`, `webhook`; or `none` to analyse without delivering or storing anything |
| `API_KEY` | (empty) | If set, `/v1` requires `Authorization: Bearer <key>` or `X-API-Key` |
| `RATE_LIMIT_PER_MINUTE` | `10` | Per client IP |
| `MAX_CONCURRENT_JOBS` | `4` | More concurrent analyses are rejected with 503 + `Retry-After` |
| `TRUSTED_PROXIES` | (empty) | Load balancer IPs/CIDRs, so the rate limit sees real client IPs |
| `MAX_UPLOAD_MB` | `20` | Gemini's inline audio limit is about 20 MB |
| `ANALYZE_TIMEOUT_SECONDS` | `90` | Time budget for the AI analysis; upload and delivery have their own limits |

### Destinations

**HubSpot:** In HubSpot, go to Development → Keys → Service keys and create a key
with scopes `crm.objects.contacts.read` and `crm.objects.contacts.write`. Set
`HUBSPOT_TOKEN`, and optionally `HUBSPOT_PORTAL_ID` for deep links (EU accounts:
`HUBSPOT_APP_URL=https://app-eu1.hubspot.com`). The connector
upserts the contact by email, adds the note to the contact's timeline and creates
one task per next step (EMAIL, CALL or TODO, with priority). Insights are part of
the note. Without a valid email it doesn't guess; the note is created
without a contact.

**Notion:** In Notion, open Developer tools → Developer connections → New
connection, choose **API token**, and copy the token. Give it access to a page
(page menu → Connections → your connection), then set `NOTION_TOKEN` and
`NOTION_PARENT_PAGE_ID` (the page URL works). Each note becomes a sub-page with a
status line (sentiment, language and tag pills), a summary callout, next steps as
to-dos with coloured type and priority pills, insights as coloured callouts, the
contact, and the transcript in a collapsible toggle. Everything is created in one
request, so a failure can't leave a half-written page. To let others read the
notes, publish the parent page (Share → Publish).

**Google Sheets:** set `SHEETS_SPREADSHEET_ID` (the sheet URL works). Each note
becomes one row; a header row is written to an empty sheet. There is no secret:
the connector uses Google Application Default Credentials. On Cloud Run that is
the service's own service account, so share the sheet with its email as Editor;
locally, share it with your Google account and run:

```bash
gcloud auth application-default login \
  --scopes=https://www.googleapis.com/auth/spreadsheets,https://www.googleapis.com/auth/cloud-platform
```

Values are written with `valueInputOption=RAW`, so model output can never run as a
spreadsheet formula. A failed append is not retried, so a row is never duplicated.

**Webhook:** set `WEBHOOK_URL` (https). It receives a versioned `note.created`
event. With `WEBHOOK_SECRET` set, requests carry
`X-Signature-256: sha256=HMAC(secret, timestamp + "." + body)` and `X-Timestamp`.
This reaches Zapier, Make, n8n or any in-house service without new code.

## Design

```
cmd/server/       composition root: the only place concrete types are wired
internal/domain/  Note, Contact, Task, Delivery, domain errors; depends on nothing
internal/api/     HTTP: router, middleware, handler, error contract
internal/llm/     Gemini adapter (prompt.go = prompt + schema, gemini.go = transport)
internal/sinks/   connector interface, registry and parallel fan-out
internal/sinks/<name>/  one package per destination; real CRM mappings are split into
                        mapper.go (pure, unit-tested) and <name>.go (I/O)
internal/httpx/   shared HTTP client: timeouts, safe retries, typed errors
web/              dependency-free test page, embedded in the binary
```

Key decisions:

- **One model call.** Gemini takes audio natively, so transcription and extraction
  happen together. That's lower latency and one less failure point than separate
  speech-to-text and LLM steps. The output is constrained by a JSON schema; without
  it, output was not reliably valid JSON.
- **Dependencies point inward.** The API depends on two small interfaces
  (`Analyzer`, `Dispatcher`), not on Gemini or any CRM. The model or a connector
  can be swapped without touching the handler, and tests use fakes.
- **Connectors are plug-ins.** Each one registers itself in `init()` (like
  `database/sql` drivers), reads its own `PREFIX_*` variables, and is enabled with
  one import line in `main.go`. Adding Salesforce changes no core code.
- **No duplicate CRM records.** HTTP retries only happen where they're safe: 429
  is always retried (the request was not processed), while 5xx and network errors
  are retried only for idempotent methods, never for POST. Contacts are matched by
  normalised email; listing a sink twice is rejected at startup.
- **LLM for judgement, code for rules.** The model decides what is a task, its type and
  spoken urgency; deterministic rules (due today or tomorrow means high priority,
  enum fallbacks, date validation) are applied in code, where they are testable.
- **Analysis follows the client; delivery doesn't.** If the client disconnects
  during analysis, the model call is cancelled. Once the note exists, delivery
  runs on a detached context with its own timeout, so a dropped connection can't
  leave a CRM half-written.
- **Failures stay isolated.** Destinations run in parallel; one failing (or even
  panicking) doesn't affect the others, and the response reports each outcome.
- **Security baseline.** Optional API key compared in constant time, per-IP rate
  limit, concurrency cap with load shedding, upload size cap, content sniffing,
  strict CSP and security headers. Model output is treated as untrusted: it's
  HTML-escaped for HubSpot, sent as plain rich text to Notion, written as raw
  values (never formulas) to Google Sheets, and rendered with `textContent` in the
  browser. Connectors only call fixed vendor hosts, never user-supplied URLs.
- **No secrets where none are needed.** Google Sheets authenticates as the
  service's own identity (Application Default Credentials), so there is no key to
  store or rotate; the Cloud Run service runs as a service account with no roles.
- **Privacy.** Audio is never stored. Logs are structured JSON with a
  `request_id` and never contain transcripts, note content or request headers
  (including after a panic). HTTP error strings omit URLs and response bodies,
  which can contain emails. The `console` sink is the deliberate exception, for
  local development; `SINKS=none` analyses without delivering or storing anything.

## Development

```bash
make test     # go test -race ./...: no network or API keys needed
make check    # gofmt + go vet + tests (what CI should run)
make build    # static binary in ./bin
make docker   # distroless, non-root image; pass secrets at runtime with --env-file
```

External APIs (Gemini, HubSpot, Notion, Google Sheets, webhooks) are faked with
`httptest.Server`, so tests are fast and deterministic. Prompt changes are
checked differently: LLM output varies between runs, so compare several memo
types (English debrief, German debrief, pure reminder) over a few runs before
and after editing `internal/llm/prompt.go`.

## Deploy (Google Cloud Run)

[deploy/cloudrun.sh](deploy/cloudrun.sh) deploys to Google Cloud Run and prints an
HTTPS URL (browsers only allow microphone access on secure origins). Use a
dedicated Gemini key and revoke it afterwards; it stays server-side, so share
only the URL and `API_KEY`.

The script builds the Dockerfile with Cloud Build
(no local Docker needed) and deploys it. It scales to zero, so a review week
normally stays within the free tier; a billing account is still required.

```bash
gcloud auth login && gcloud config set project <your-project>
export GEMINI_API_KEY=... API_KEY=...            # API_KEY: any random string, shared with testers
export DEPLOY_SINKS=notion,sheets                # optional; default "none" (analysis only)
export NOTION_TOKEN=... NOTION_PARENT_PAGE_ID=... SHEETS_SPREADSHEET_ID=...
make deploy-gcp
make destroy-gcp                                 # when you're done
```

- Destinations come from `DEPLOY_SINKS`, not `SINKS`, so a local `.env` loaded into
  the shell can't leak into a deployment. `console` is refused: it would put
  transcripts into Cloud Logging. Connector settings (`NOTION_*`, `SHEETS_*`, …)
  are passed through when exported.
- The service runs as its own service account with no roles. For Google Sheets,
  share the spreadsheet with that account (the script prints its email).
- The request timeout is 300 s, above the server's own worst case, so the platform
  never cuts a slow analysis short. `.gcloudignore` keeps `.env` and other local
  files out of the upload; secrets reach Cloud Run through a temporary file that is
  deleted right after.
- On a brand-new project, Cloud Build may first need `roles/run.builder` on the
  default compute service account.

## Production next steps

Deliberately out of scope for this exercise:

- **Asynchronous pipeline.** Return `202` right after upload and process via a
  job queue with per-step retries, then notify the client. Long recordings and
  CRM outages would then delay a note instead of failing the request.
- **Human review** of the extracted note before it is written to the CRM.
- **Per-user connections:** today the operator configures destinations and users
  pick among them per memo. In production each user would connect their own CRM,
  Notion or Google account via OAuth, with tokens encrypted per tenant and
  per-tenant field mapping.
- **Shared rate limiting** (gateway or Redis) when running more than one instance.
- **Observability:** OpenTelemetry traces and metrics (latency per step,
  delivery failures per connector).
- **A prompt evaluation set** run in CI, and the prompt version stored with each note.
