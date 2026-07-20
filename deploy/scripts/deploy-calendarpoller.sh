#!/usr/bin/env bash
# Deploy Cloud Run calendarpoller + two-hourly Cloud Scheduler → GCS.
# Usage (from repo root, with gcloud auth and env vars set):
#   ./deploy/scripts/deploy-calendarpoller.sh
set -euo pipefail

PROJECT="${PROJECT:-fxtrade-prod-12345}"
REGION="${REGION:-us-east1}"
BUCKET="${BUCKET:-tradex-demo-state}"
OBJECT="${OBJECT:-calendar-state.json}"
GCS_URI="gs://${BUCKET}/${OBJECT}"
SERVICE="${SERVICE:-tradex-calendarpoller}"
SCHEDULER_JOB="${SCHEDULER_JOB:-tradex-calendarpoller-hourly}"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT}/tradex/calendarpoller:demo"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

need() { [[ -n "${!1:-}" ]] || { echo "missing env $1" >&2; exit 1; }; }
need FINNHUB_API_KEY
need GEMINI_API_KEY
need TELEGRAM_BOT_TOKEN
need TELEGRAM_CHAT_ID

echo "==> project=${PROJECT} region=${REGION} gcs=${GCS_URI}"

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

# Seed object only when missing so deploys never clobber a fresher live calendar.
if ! gcloud storage ls "${GCS_URI}" --project="${PROJECT}" >/dev/null 2>&1; then
  if [[ -f "${REPO_ROOT}/data/calendar-state.json" ]]; then
    gcloud storage cp "${REPO_ROOT}/data/calendar-state.json" "${GCS_URI}" --project="${PROJECT}"
    echo "==> seeded ${GCS_URI}"
  fi
else
  echo "==> keeping existing ${GCS_URI}"
fi

echo "==> building image ${IMAGE}"
cat > /tmp/tradex-cal-cloudbuild.yaml <<EOF
steps:
  - name: gcr.io/cloud-builders/docker
    args: ['build', '-t', '${IMAGE}', '-f', 'deploy/docker/Dockerfile.calendarpoller', '.']
images:
  - '${IMAGE}'
EOF
gcloud builds submit --project="${PROJECT}" --config=/tmp/tradex-cal-cloudbuild.yaml "${REPO_ROOT}"

# Env file for Cloud Run (no echo of values)
python3 - <<'PY'
import os
from pathlib import Path
keys = ['FINNHUB_API_KEY','GEMINI_API_KEY','TELEGRAM_BOT_TOKEN','TELEGRAM_CHAT_ID']
lines = []
for k in keys:
    v = os.environ[k].replace('\\','\\\\').replace('"','\\"')
    lines.append(f'{k}: "{v}"')
Path('/tmp/tradex-cal-env.yaml').write_text('\n'.join(lines)+'\n')
Path('/tmp/tradex-cal-env.yaml').chmod(0o600)
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
  --timeout=240 \
  --env-vars-file=/tmp/tradex-cal-env.yaml \
  --command=/app/calendarpoller \
  --args=--config=config/config.calendarpoller.cloudrun.yaml

rm -f /tmp/tradex-cal-env.yaml

URL="$(gcloud run services describe "${SERVICE}" --project="${PROJECT}" --region="${REGION}" --format='value(status.url)')"
echo "==> service_url=${URL}"

# Invoker SA for Scheduler → Cloud Run
INVOKER_SA="tradex-cal-scheduler@${PROJECT}.iam.gserviceaccount.com"
if ! gcloud iam service-accounts describe "${INVOKER_SA}" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud iam service-accounts create tradex-cal-scheduler \
    --project="${PROJECT}" --display-name='Tradex calendar Scheduler invoker'
fi
gcloud run services add-iam-policy-binding "${SERVICE}" \
  --project="${PROJECT}" --region="${REGION}" \
  --member="serviceAccount:${INVOKER_SA}" \
  --role="roles/run.invoker" >/dev/null

# Cloud Run runtime SA needs objectAdmin on the bucket
RUNTIME_SA="$(gcloud run services describe "${SERVICE}" --project="${PROJECT}" --region="${REGION}" --format='value(spec.template.spec.serviceAccountName)')"
if [[ -z "${RUNTIME_SA}" || "${RUNTIME_SA}" == "null" ]]; then
  RUNTIME_SA="$(gcloud projects describe "${PROJECT}" --format='value(projectNumber)')-compute@developer.gserviceaccount.com"
fi
gcloud storage buckets add-iam-policy-binding "gs://${BUCKET}" \
  --member="serviceAccount:${RUNTIME_SA}" \
  --role="roles/storage.objectAdmin" \
  --project="${PROJECT}" >/dev/null || true

# Two-hourly scheduler (even hours, UTC)
if gcloud scheduler jobs describe "${SCHEDULER_JOB}" --location="${REGION}" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud scheduler jobs update http "${SCHEDULER_JOB}" \
    --location="${REGION}" --project="${PROJECT}" \
    --schedule='0 */2 * * *' \
    --time-zone='UTC' \
    --uri="${URL}/run" \
    --http-method=POST \
    --attempt-deadline=240s \
    --oidc-service-account-email="${INVOKER_SA}" \
    --oidc-token-audience="${URL}"
else
  gcloud scheduler jobs create http "${SCHEDULER_JOB}" \
    --location="${REGION}" --project="${PROJECT}" \
    --schedule='0 */2 * * *' \
    --time-zone='UTC' \
    --uri="${URL}/run" \
    --http-method=POST \
    --attempt-deadline=240s \
    --oidc-service-account-email="${INVOKER_SA}" \
    --oidc-token-audience="${URL}"
fi

echo "==> triggering one run now"
gcloud scheduler jobs run "${SCHEDULER_JOB}" --location="${REGION}" --project="${PROJECT}"
sleep 15
gcloud storage ls -l "${GCS_URI}" --project="${PROJECT}" || true
echo "==> calendarpoller deploy complete"
echo "    GCS=${GCS_URI}"
echo "    URL=${URL}"
echo "    schedule=0 */2 * * * UTC"
