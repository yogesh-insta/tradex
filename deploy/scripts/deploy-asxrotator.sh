#!/usr/bin/env bash
# Deploy Cloud Run asxrotator + weekday-17:00-Sydney Cloud Scheduler (spec 22).
# Usage (from repo root, gcloud authed, TELEGRAM_* exported):
#   ./deploy/scripts/deploy-asxrotator.sh
set -euo pipefail

PROJECT="${PROJECT:-fxtrade-prod-12345}"
REGION="${REGION:-us-east1}"
BUCKET="${BUCKET:-tradex-demo-state}"
SERVICE="${SERVICE:-tradex-asxrotator}"
SCHEDULER_JOB="${SCHEDULER_JOB:-tradex-asxrotator-daily}"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT}/tradex/asxrotator:prod"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
GCS_PREFIX="gs://${BUCKET}/asxrotator"

need() { [[ -n "${!1:-}" ]] || { echo "missing env $1" >&2; exit 1; }; }
need TELEGRAM_BOT_TOKEN
need TELEGRAM_CHAT_ID

echo "==> project=${PROJECT} region=${REGION} state=${GCS_PREFIX}"

gcloud config set project "${PROJECT}" >/dev/null
gcloud services enable \
  run.googleapis.com artifactregistry.googleapis.com cloudbuild.googleapis.com \
  cloudscheduler.googleapis.com storage.googleapis.com \
  --project="${PROJECT}"

if ! gcloud artifacts repositories describe tradex --location="${REGION}" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud artifacts repositories create tradex \
    --repository-format=docker --location="${REGION}" --project="${PROJECT}" \
    --description='Tradex container images'
fi

if ! gcloud storage buckets describe "gs://${BUCKET}" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud storage buckets create "gs://${BUCKET}" --project="${PROJECT}" --location="${REGION}"
  echo "==> created bucket gs://${BUCKET}"
fi

# portfolio.json is NOT seeded from a template, unlike the NSE lane.
#
# The rotator sizes ten BUY orders as total_capital_aud / top_k, so a
# placeholder capital produces a real Telegram message full of orders sized
# against money that does not exist. There is no safe default here, so the
# deploy stops and asks rather than inventing one.
if ! gcloud storage ls "${GCS_PREFIX}/portfolio.json" --project="${PROJECT}" >/dev/null 2>&1; then
  if [[ "${ALLOW_MISSING_PORTFOLIO:-}" == "1" ]]; then
    # Infrastructure-first deploy: stand the service up now, supply state later.
    # The lane is inert until portfolio.json exists — every run fails fast on
    # "portfolio: ... not found" and notifies, which is the correct behaviour
    # and a standing reminder. It never invents a capital figure.
    cat >&2 <<EOF

WARNING: ${GCS_PREFIX}/portfolio.json does not exist.
ALLOW_MISSING_PORTFOLIO=1 — deploying anyway. The service will be LIVE but
INERT: every run errors until you upload the file. See the end of this script
for the one command that finishes the job.

EOF
  else
  cat >&2 <<EOF

ERROR: ${GCS_PREFIX}/portfolio.json does not exist.

Create it with YOUR real AUD capital and current ASX holdings, then re-run.
Orders are sized capital/top_k, so this file decides what the first run
advises. Example (empty book, A\$100,000):

  cat > /tmp/asx-portfolio.json <<'JSON'
  {
    "as_of": "$(date +%Y-%m-%d)",
    "total_capital_aud": 100000,
    "holdings": [],
    "notes": "initial"
  }
JSON
  gcloud storage cp /tmp/asx-portfolio.json ${GCS_PREFIX}/portfolio.json --project=${PROJECT}

Holdings entries look like:
  {"symbol": "BHP", "qty": 120, "avg_price": 41.55}

Re-run with ALLOW_MISSING_PORTFOLIO=1 to deploy the service anyway and add
the file afterwards; it will be live but inert until you do.

EOF
  exit 1
  fi
else
  echo "==> using existing ${GCS_PREFIX}/portfolio.json"
fi

echo "==> building image ${IMAGE}"
cat > /tmp/tradex-asx-cloudbuild.yaml <<EOF
steps:
  - name: gcr.io/cloud-builders/docker
    args: ['build', '-t', '${IMAGE}', '-f', 'deploy/docker/Dockerfile.asxrotator', '.']
images:
  - '${IMAGE}'
EOF
gcloud builds submit --project="${PROJECT}" --config=/tmp/tradex-asx-cloudbuild.yaml "${REPO_ROOT}"

# Env file for Cloud Run (no echo of values)
python3 - <<'PY'
import os
from pathlib import Path
keys = ['TELEGRAM_BOT_TOKEN','TELEGRAM_CHAT_ID']
lines = []
for k in keys:
    v = os.environ[k].replace('\\','\\\\').replace('"','\\"')
    lines.append(f'{k}: "{v}"')
Path('/tmp/tradex-asx-env.yaml').write_text('\n'.join(lines)+'\n')
Path('/tmp/tradex-asx-env.yaml').chmod(0o600)
print('env_keys', keys)
PY

echo "==> deploying Cloud Run ${SERVICE}"
gcloud run deploy "${SERVICE}" \
  --project="${PROJECT}" \
  --region="${REGION}" \
  --image="${IMAGE}" \
  --platform=managed \
  --no-allow-unauthenticated \
  --port=8080 \
  --memory=512Mi \
  --cpu=1 \
  --min-instances=0 \
  --max-instances=1 \
  --timeout=540 \
  --env-vars-file=/tmp/tradex-asx-env.yaml \
  --command=/app/asxrotator \
  --args=--config=config/config.asxrotator.cloudrun.yaml

rm -f /tmp/tradex-asx-env.yaml

URL="$(gcloud run services describe "${SERVICE}" --project="${PROJECT}" --region="${REGION}" --format='value(status.url)')"
echo "==> service_url=${URL}"

# Invoker SA for Scheduler → Cloud Run (same pattern as the NSE lane)
INVOKER_SA="tradex-asx-scheduler@${PROJECT}.iam.gserviceaccount.com"
if ! gcloud iam service-accounts describe "${INVOKER_SA}" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud iam service-accounts create tradex-asx-scheduler \
    --project="${PROJECT}" --display-name='Tradex ASX rotator Scheduler invoker'
fi
gcloud run services add-iam-policy-binding "${SERVICE}" \
  --project="${PROJECT}" --region="${REGION}" \
  --member="serviceAccount:${INVOKER_SA}" \
  --role="roles/run.invoker" >/dev/null

# Runtime SA needs objectAdmin on the bucket (portfolio read + rec/heartbeat write)
RUNTIME_SA="$(gcloud run services describe "${SERVICE}" --project="${PROJECT}" --region="${REGION}" --format='value(spec.template.spec.serviceAccountName)')"
if [[ -z "${RUNTIME_SA}" || "${RUNTIME_SA}" == "null" ]]; then
  RUNTIME_SA="$(gcloud projects describe "${PROJECT}" --format='value(projectNumber)')-compute@developer.gserviceaccount.com"
fi
gcloud storage buckets add-iam-policy-binding "gs://${BUCKET}" \
  --member="serviceAccount:${RUNTIME_SA}" \
  --role="roles/storage.objectAdmin" \
  --project="${PROJECT}" >/dev/null || true

# Weekdays 17:00 Australia/Sydney — an hour after the 16:00 ASX close, so the
# day's closing prices are settled on Yahoo. The in-process gate (XASX calendar)
# picks the actual last trading day; other days exit fast with {"skipped":true}.
# Scheduler handles the DST shift; the binary carries tzdata for the same reason.
SCHEDULE='0 17 * * 1-5'
SCHEDULER_TZ='Australia/Sydney'
if gcloud scheduler jobs describe "${SCHEDULER_JOB}" --location="${REGION}" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud scheduler jobs update http "${SCHEDULER_JOB}" \
    --location="${REGION}" --project="${PROJECT}" \
    --schedule="${SCHEDULE}" \
    --time-zone="${SCHEDULER_TZ}" \
    --uri="${URL}/run" \
    --http-method=POST \
    --attempt-deadline=540s \
    --oidc-service-account-email="${INVOKER_SA}" \
    --oidc-token-audience="${URL}"
else
  gcloud scheduler jobs create http "${SCHEDULER_JOB}" \
    --location="${REGION}" --project="${PROJECT}" \
    --schedule="${SCHEDULE}" \
    --time-zone="${SCHEDULER_TZ}" \
    --uri="${URL}/run" \
    --http-method=POST \
    --attempt-deadline=540s \
    --oidc-service-account-email="${INVOKER_SA}" \
    --oidc-token-audience="${URL}"
fi

echo "==> asxrotator deploy complete"
echo "    URL=${URL}"
echo "    schedule=${SCHEDULE} ${SCHEDULER_TZ} (17:00 Sydney weekdays; gate picks month-end)"
echo "    state=${GCS_PREFIX}/  (portfolio.json is YOURS to maintain)"
echo ""
echo "Manual test run (skips date gate, sends a REAL Telegram message):"
echo "  curl -X POST -H \"Authorization: Bearer \$(gcloud auth print-identity-token)\" \"${URL}/run?force=1\""
