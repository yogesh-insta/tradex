#!/usr/bin/env bash
# One-time (or re-run) setup for Tradex on a shared Ubuntu e2-micro (e.g. fxtrade-vm).
# Run on the VM as root/sudo from a checkout of this repo, or after scp of the tree.
#
#   sudo ./deploy/gcp/install.sh [--binary /path/to/trader] [--env-file /path/to/tradex.env]
#
# Does not start trading until /etc/tradex/tradex.env has real secrets and the binary exists.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
INSTALL_ROOT="${TRADEX_HOME:-/opt/tradex}"
BINARY_SRC=""
ENV_SRC=""
ENABLE=true

usage() {
  cat <<'EOF'
Usage: sudo ./deploy/gcp/install.sh [options]

Options:
  --binary PATH     Linux amd64 trader binary to install at /opt/tradex/bin/trader
  --env-file PATH   Secrets file to install as /etc/tradex/tradex.env (0640)
  --no-enable       Install unit but do not enable/start tradex.service
  -h, --help        Show help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) BINARY_SRC="$2"; shift 2 ;;
    --env-file) ENV_SRC="$2"; shift 2 ;;
    --no-enable) ENABLE=false; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage; exit 1 ;;
  esac
done

if [[ "$(id -u)" -ne 0 ]]; then
  echo "error: run with sudo" >&2
  exit 1
fi

if ! id tradex &>/dev/null; then
  useradd --system --home-dir "$INSTALL_ROOT" --shell /usr/sbin/nologin tradex
fi

mkdir -p "$INSTALL_ROOT"/{bin,config,data,out,deploy/gcp,deploy/systemd,deploy/scripts}
mkdir -p /etc/tradex

# Sync configs + deploy assets from the checkout used to invoke this script.
install -d "$INSTALL_ROOT/config"
cp -f "$ROOT"/config/config.*.yaml "$INSTALL_ROOT/config/" 2>/dev/null || true
cp -f "$ROOT/config/holidays.yaml" "$INSTALL_ROOT/config/" 2>/dev/null || true
cp -f "$ROOT/deploy/systemd/tradex.service" /etc/systemd/system/tradex.service
cp -f "$ROOT/deploy/scripts/deploy.sh" "$INSTALL_ROOT/deploy/scripts/" 2>/dev/null || true
chmod 755 "$INSTALL_ROOT/deploy/scripts/deploy.sh" 2>/dev/null || true
# Optional seed files (calendar fail-safe / local ledger samples)
if [[ -d "$ROOT/data" ]]; then
  cp -f "$ROOT/data/"*.json "$INSTALL_ROOT/data/" 2>/dev/null || true
fi
# If installing from a full git checkout, keep source for future on-VM builds.
if [[ -d "$ROOT/cmd" && "$ROOT" != "$INSTALL_ROOT" ]]; then
  rsync -a --delete \
    --exclude '.git' --exclude '.env' --exclude 'bin' --exclude 'out' --exclude 'data/candles' \
    "$ROOT"/ "$INSTALL_ROOT"/ 2>/dev/null || true
fi

if [[ -n "$BINARY_SRC" ]]; then
  install -m 755 "$BINARY_SRC" "$INSTALL_ROOT/bin/trader"
elif [[ ! -x "$INSTALL_ROOT/bin/trader" ]]; then
  echo "note: no binary at $INSTALL_ROOT/bin/trader yet — pass --binary or build on the VM:"
  echo "  cd $INSTALL_ROOT && go build -trimpath -ldflags='-s -w' -o bin/trader ./cmd/trader"
fi

if [[ -n "$ENV_SRC" ]]; then
  install -m 640 -o root -g tradex "$ENV_SRC" /etc/tradex/tradex.env
elif [[ ! -f /etc/tradex/tradex.env ]]; then
  install -m 640 -o root -g tradex "$ROOT/deploy/gcp/tradex.env.example" /etc/tradex/tradex.env
  echo "note: wrote placeholder /etc/tradex/tradex.env — fill secrets before starting"
fi

chown -R tradex:tradex "$INSTALL_ROOT"
# Keep bin executable by tradex
if [[ -x "$INSTALL_ROOT/bin/trader" ]]; then
  chown tradex:tradex "$INSTALL_ROOT/bin/trader"
  chmod 755 "$INSTALL_ROOT/bin/trader"
fi

systemctl daemon-reload

if $ENABLE; then
  if [[ ! -x "$INSTALL_ROOT/bin/trader" ]]; then
    echo "error: cannot enable — missing $INSTALL_ROOT/bin/trader" >&2
    exit 1
  fi
  if ! grep -q '^OANDA_API_TOKEN=.\+' /etc/tradex/tradex.env 2>/dev/null; then
    echo "error: /etc/tradex/tradex.env missing OANDA_API_TOKEN — refusing to start" >&2
    exit 1
  fi
  systemctl enable tradex.service
  systemctl restart tradex.service
  sleep 2
  systemctl --no-pager --lines=25 status tradex.service || true
else
  echo "installed unit; enable later with: systemctl enable --now tradex"
fi

echo "==> Tradex installed under $INSTALL_ROOT (env=/etc/tradex/tradex.env)"
