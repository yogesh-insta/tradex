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
# Service/runtime may own data/*; reclaim so git can update the working tree.
sudo chown -R "$(id -un):$(id -gn)" "${APP_DIR}"
git fetch origin main
git checkout main
git reset --hard origin/main

echo "==> building"
mkdir -p "${BIN_DIR}"
go build -trimpath -ldflags="-s -w" -o "${BIN_DIR}/trader" ./cmd/trader
go build -trimpath -ldflags="-s -w" -o "${BIN_DIR}/backtester" ./cmd/backtester

echo "==> installing systemd failure alert"
sudo install -m 0644 deploy/systemd/tradex.service /etc/systemd/system/tradex.service
sudo install -m 0644 deploy/systemd/tradex-failure-alert@.service /etc/systemd/system/tradex-failure-alert@.service
sudo install -m 0644 deploy/systemd/tradex-heartbeat-check.service /etc/systemd/system/tradex-heartbeat-check.service
sudo install -m 0644 deploy/systemd/tradex-heartbeat-check.timer /etc/systemd/system/tradex-heartbeat-check.timer
sudo chmod 0750 deploy/systemd/tradex-failure-alert.py
sudo chmod 0750 deploy/systemd/tradex-heartbeat-check.py
sudo systemctl daemon-reload
sudo systemctl enable --now tradex-heartbeat-check.timer
# The service owns runtime heartbeat state; git/build files remain deploy-user owned.
sudo chown -R tradex:tradex "${APP_DIR}/data"

echo "==> validating config (fail fast before restart)"
test -f "${APP_DIR}/config/config.${ENVIRONMENT}.yaml"

echo "==> pointing service at ${ENVIRONMENT} and restarting"
sudo systemctl set-environment TRADEX_ENV="${ENVIRONMENT}"
sudo systemctl restart tradex.service

sleep 3
if ! sudo systemctl --no-pager --lines=20 status tradex.service; then
  echo "==> tradex.service unhealthy; recent logs:" >&2
  sudo journalctl -u tradex.service -n 40 --no-pager >&2 || true
  exit 1
fi
echo "==> deployed $(git rev-parse --short HEAD) to ${ENVIRONMENT}"
