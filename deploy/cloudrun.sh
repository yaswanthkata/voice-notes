#!/usr/bin/env bash
# Deploys voice-notes to Google Cloud Run (HTTPS URL included, scales to zero).
#
# Usage:
#   GEMINI_API_KEY=... API_KEY=... deploy/cloudrun.sh     # create or update
#   deploy/cloudrun.sh destroy                             # delete the service
#
# Requires: gcloud CLI, logged in (gcloud auth login) with a project selected
# (gcloud config set project <id>) that has billing enabled. Docker is not
# needed locally: Cloud Build builds the Dockerfile.
#
# Optional env: REGION (default europe-west1), SERVICE (default voice-notes),
# DEPLOY_SINKS (default none: analysis only) plus any connector variables
# (NOTION_TOKEN, ...). DEPLOY_SINKS is separate from SINKS on purpose, so a
# local .env (SINKS=console) loaded into the shell can't leak into a deploy.
#
# Notes:
# - The request timeout is raised to 300 s, above the server's own worst case
#   (upload + 90 s analysis + delivery), so the platform never cuts a request.
# - max-instances 1 caps cost and keeps the in-memory rate limiter consistent.
# - Sinks default to "none": the console sink would print notes (transcripts
#   are personal data) into Cloud Logging, so it is refused here.
# - The service runs as its own service account with no IAM roles: Gemini uses
#   the API key, and the Sheets connector only reaches spreadsheets that were
#   explicitly shared with this account.
# - Secrets are passed as environment variables through a private temp file:
#   never in the repo, the image or the shell history. Project members with
#   Cloud Run access can read them, so use a dedicated, revocable Gemini key.
set -euo pipefail

REGION="${REGION:-europe-west1}"
SERVICE="${SERVICE:-voice-notes}"

if [[ "${1:-}" == "destroy" ]]; then
  gcloud run services delete "$SERVICE" --region "$REGION" --quiet
  echo "Deleted $SERVICE. The built image stays in Artifact Registry (cloud-run-source-deploy); delete it there if you like."
  exit 0
fi

: "${GEMINI_API_KEY:?set GEMINI_API_KEY (use a dedicated key you can revoke)}"
: "${API_KEY:?set API_KEY so strangers cannot use your Gemini quota}"

# Normalise once (lowercase, no spaces) so every check below sees what the app
# will see: config.go lowercases and trims SINKS too.
DEPLOY_SINKS="$(printf '%s' "${DEPLOY_SINKS:-none}" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')"
export DEPLOY_SINKS
if [[ ",$DEPLOY_SINKS," == *",console,"* ]]; then
  echo "DEPLOY_SINKS must not include console: it would write transcripts into Cloud Logging." >&2
  exit 1
fi

apis=(run.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com iam.googleapis.com)
[[ ",$DEPLOY_SINKS," == *",sheets,"* ]] && apis+=(sheets.googleapis.com)
gcloud services enable "${apis[@]}"

project="$(gcloud config get-value project 2>/dev/null)"
runtime_sa="${SERVICE}-run@${project}.iam.gserviceaccount.com"
if ! gcloud iam service-accounts describe "$runtime_sa" >/dev/null 2>&1; then
  gcloud iam service-accounts create "${SERVICE}-run" --display-name "voice-notes runtime (no roles)"
  # IAM is eventually consistent: deploying with a brand-new account right
  # away fails with "actAs denied ... or it may not exist".
  echo "Waiting 60 s for the new service account to propagate..."
  sleep 60
fi

env_file="$(mktemp)"
trap 'rm -f "$env_file"' EXIT
chmod 600 "$env_file"
python3 - "$env_file" <<'PY'
import json, os, sys
env = {
    "GEMINI_API_KEY": os.environ["GEMINI_API_KEY"],
    "API_KEY": os.environ["API_KEY"],
    "SINKS": os.environ["DEPLOY_SINKS"],  # already normalised and checked above
    "RATE_LIMIT_PER_MINUTE": os.environ.get("RATE_LIMIT_PER_MINUTE", "30"),
    # Requests reach the container from Cloud Run's internal proxy (169.254.x);
    # trusting only that range lets the rate limiter see real client IPs from
    # X-Forwarded-For without letting clients spoof theirs.
    "TRUSTED_PROXIES": os.environ.get("TRUSTED_PROXIES", "169.254.0.0/16"),
}
for k in ("GEMINI_MODEL", "NOTION_TOKEN", "NOTION_PARENT_PAGE_ID", "SHEETS_SPREADSHEET_ID",
          "SHEETS_TAB", "WEBHOOK_URL", "WEBHOOK_SECRET"):
    if os.environ.get(k):
        env[k] = os.environ[k]
# JSON is valid YAML, which is what --env-vars-file expects; it also quotes safely.
json.dump(env, open(sys.argv[1], "w"))
PY

# --allow-unauthenticated only skips Google sign-in; the app's own API_KEY
# still protects /v1.
gcloud run deploy "$SERVICE" \
  --source "$(dirname "$0")/.." \
  --region "$REGION" \
  --allow-unauthenticated \
  --timeout 300 \
  --memory 512Mi \
  --cpu 1 \
  --min-instances 0 \
  --max-instances 1 \
  --service-account "$runtime_sa" \
  --env-vars-file "$env_file"

# Cloud Run serves two URLs; this is the stable, readable one (name-number.region).
number="$(gcloud projects describe "$project" --format 'value(projectNumber)')"
echo "Live at https://${SERVICE}-${number}.${REGION}.run.app"
if [[ ",$DEPLOY_SINKS," == *",sheets,"* ]]; then
  echo "Google Sheets: share the spreadsheet with $runtime_sa as Editor (no key needed)."
fi
echo "Logs: gcloud run services logs read $SERVICE --region $REGION --limit 50"
