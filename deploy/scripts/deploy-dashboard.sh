#!/usr/bin/env bash
# Deploy Cloud Run dashboard + read-only ops UI (spec 14).
# Usage (from repo root, gcloud authed, DASHBOARD_TOKEN exported):
#   ./deploy/scripts/deploy-dashboard.sh
set -euo pipefail

PROJECT="${PROJECT:-fxtrade-prod-12345}"
REGION="${REGION:-us-east1}"
SERVICE="${SERVICE:-tradex-dashboard}"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT}/tradex/dashboard:prod"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

need() { [[ -n "${!1:-}" ]] || { echo "missing env $1" >&2; exit 1; }; }
need DASHBOARD_TOKEN

echo "==> project=${PROJECT} region=${REGION}"

gcloud config set project "${PROJECT}" >/dev/null
gcloud services enable \
  run.googleapis.com artifactregistry.googleapis.com cloudbuild.googleapis.com \
  storage.googleapis.com \
  --project="${PROJECT}"

if ! gcloud artifacts repositories describe tradex --location="${REGION}" --project="${PROJECT}" >/dev/null 2>&1; then
  gcloud artifacts repositories create tradex \
    --repository-format=docker --location="${REGION}" --project="${PROJECT}" \
    --description='Tradex container images'
fi

echo "==> building image ${IMAGE}"
cat > /tmp/tradex-dashboard-cloudbuild.yaml <<EOF
steps:
  - name: gcr.io/cloud-builders/docker
    args: ['build', '-t', '${IMAGE}', '-f', 'deploy/docker/Dockerfile.dashboard', '.']
images:
  - '${IMAGE}'
EOF
gcloud builds submit --project="${PROJECT}" --config=/tmp/tradex-dashboard-cloudbuild.yaml "${REPO_ROOT}"

# Env file for Cloud Run
python3 - <<'PY'
import os
from pathlib import Path
keys = ['DASHBOARD_TOKEN']
lines = []
for k in keys:
    v = os.environ[k].replace('\\','\\\\').replace('"','\\"')
    lines.append(f'{k}: "{v}"')
Path('/tmp/tradex-dashboard-env.yaml').write_text('\n'.join(lines)+'\n')
Path('/tmp/tradex-dashboard-env.yaml').chmod(0o600)
print('env_keys', keys)
PY

echo "==> deploying Cloud Run ${SERVICE}"
gcloud run deploy "${SERVICE}" \
  --project="${PROJECT}" \
  --region="${REGION}" \
  --image="${IMAGE}" \
  --platform=managed \
  --allow-unauthenticated \
  --port=8080 \
  --memory=256Mi \
  --cpu=1 \
  --min-instances=0 \
  --max-instances=2 \
  --timeout=120 \
  --env-vars-file=/tmp/tradex-dashboard-env.yaml \
  --command=/app/dashboard \
  --args=--config=config/config.dashboard.cloudrun.yaml

rm -f /tmp/tradex-dashboard-env.yaml

URL="$(gcloud run services describe "${SERVICE}" --project="${PROJECT}" --region="${REGION}" --format='value(status.url)')"
echo "==> service_url=${URL}"

echo "==> dashboard deploy complete"
echo "    URL=${URL}"
echo "    Open with ?token=<DASHBOARD_TOKEN from .env> (token is never printed here)"
echo "    Note: ETF/NSE data fetch from gs://tradex-demo-state/{etfmonitor,nserotator}/"
