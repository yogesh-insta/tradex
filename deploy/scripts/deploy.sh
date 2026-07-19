#!/usr/bin/env bash
# Pull/build/restart script for the GCP VM.
# Usage: deploy.sh [demo|prod]   (default: demo)
# Assumes: repo cloned at /opt/tradex, Go toolchain installed, systemd unit
# installed as tradex.service, secrets in /etc/tradex/tradex.env.
set -euo pipefail

ENVIRONMENT="${1:-demo}"
APP_DIR="/opt/tradex"
BIN_DIR="${APP_DIR}/bin"

case "${ENVIRONMENT}" in
  demo|prod) ;;
  *) echo "usage: $0 [demo|prod]" >&2; exit 1 ;;
esac

echo "==> deploying tradex (${ENVIRONMENT})"
export PATH="/usr/local/go/bin:${PATH}"
cd "${APP_DIR}"

echo "==> pulling latest main"
# Repo may be owned by a different user than the deploy SSH user.
git config --global --add safe.directory "${APP_DIR}"
git fetch origin main
git checkout main
git reset --hard origin/main

echo "==> building"
mkdir -p "${BIN_DIR}"
go build -trimpath -ldflags="-s -w" -o "${BIN_DIR}/trader" ./cmd/trader
go build -trimpath -ldflags="-s -w" -o "${BIN_DIR}/backtester" ./cmd/backtester

echo "==> validating config (fail fast before restart)"
test -f "${APP_DIR}/config/config.${ENVIRONMENT}.yaml"

echo "==> pointing service at ${ENVIRONMENT} and restarting"
sudo systemctl set-environment TRADEX_ENV="${ENVIRONMENT}"
sudo systemctl restart tradex.service

sleep 3
sudo systemctl --no-pager --lines=20 status tradex.service
echo "==> deployed $(git rev-parse --short HEAD) to ${ENVIRONMENT}"
