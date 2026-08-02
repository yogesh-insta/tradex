#!/usr/bin/env bash
# Deploy Cloud Run nserotator + weekday-18:00-IST Cloud Scheduler (spec 20).
# Usage (from repo root, gcloud authed, TELEGRAM_* exported):
#   ./deploy/scripts/deploy-nserotator.sh
set -euo pipefail

PROJECT="${PROJECT:-fxtrade-prod-12345}"
REGION="${REGION:-us-east1}"
BUCKET="${BUCKET:-tradex-demo-state}"
SERVICE="${SERVICE:-tradex-nserotator}"
SCHEDULER_JOB="${SCHEDULER_JOB:-tradex-nserotator-daily}"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT}/tradex/nserotator:prod"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
GCS_PREFIX="gs://${BUCKET}/nserotator"

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

# Seed portfolio.json only when missing — NEVER clobber live user state.
if ! gcloud storage ls "${GCS_PREFIX}/portfolio.json" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud storage cp "${REPO_ROOT}/data/nserotator-portfolio.json" "${GCS_PREFIX}/portfolio.json" --project="${PROJECT}"
  echo "==> seeded ${GCS_PREFIX}/portfolio.json"
else
  echo "==> keeping existing ${GCS_PREFIX}/portfolio.json"
fi

echo "==> building image ${IMAGE}"
cat > /tmp/tradex-nse-cloudbuild.yaml <<EOF
steps:
  - name: gcr.io/cloud-builders/docker
    args: ['build', '-t', '${IMAGE}', '-f', 'deploy/docker/Dockerfile.nserotator', '.']
images:
  - '${IMAGE}'
EOF
gcloud builds submit --project="${PROJECT}" --config=/tmp/tradex-nse-cloudbuild.yaml "${REPO_ROOT}"

# Env file for Cloud Run (no echo of values)
python3 - <<'PY'
import os
from pathlib import Path
keys = ['TELEGRAM_BOT_TOKEN','TELEGRAM_CHAT_ID']
lines = []
for k in keys:
    v = os.environ[k].replace('\\','\\\\').replace('"','\\"')
    lines.append(f'{k}: "{v}"')
Path('/tmp/tradex-nse-env.yaml').write_text('\n'.join(lines)+'\n')
Path('/tmp/tradex-nse-env.yaml').chmod(0o600)
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
  --env-vars-file=/tmp/tradex-nse-env.yaml \
  --command=/app/nserotator \
  --args=--config=config/config.nserotator.cloudrun.yaml

rm -f /tmp/tradex-nse-env.yaml

URL="$(gcloud run services describe "${SERVICE}" --project="${PROJECT}" --region="${REGION}" --format='value(status.url)')"
echo "==> service_url=${URL}"

# Invoker SA for Scheduler → Cloud Run (reuse pattern from calendarpoller)
INVOKER_SA="tradex-nse-scheduler@${PROJECT}.iam.gserviceaccount.com"
if ! gcloud iam service-accounts describe "${INVOKER_SA}" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud iam service-accounts create tradex-nse-scheduler \
    --project="${PROJECT}" --display-name='Tradex NSE rotator Scheduler invoker'
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

# Weekdays 15:30 Australia/Sydney. The in-process gate (IST/NSE calendar) picks
# the actual last trading day; other days exit fast with {"skipped":true}.
SCHEDULE='30 15 * * 1-5'
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

echo "==> nserotator deploy complete"
echo "    URL=${URL}"
echo "    schedule=${SCHEDULE} ${SCHEDULER_TZ} (15:30 Sydney weekdays; gate picks month-end)"
echo "    state=${GCS_PREFIX}/  (portfolio.json is YOURS to maintain)"
echo ""
echo "Manual test run (skips date gate, sends a REAL Telegram message):"
echo "  curl -X POST -H \"Authorization: Bearer \$(gcloud auth print-identity-token)\" \"${URL}/run?force=1\""
