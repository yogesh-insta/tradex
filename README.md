# Tradex

Automated OANDA trading system in Go. A single always-on binary on a GCP VM
owns the entire trade hot path — market data → candles → session context →
strategy → risk → execution → trade management — and is the **only writer to
OANDA**.

Shipped lanes:

- **EU LOVE** (`eu_love`) on `DE30_EUR` / `FR40_EUR`
- **FX TRLD** (`fx_trld`) on `USD_JPY` (dedicated account; enable via config)

Design docs are the source of truth:

- [`docs/architecture.md`](docs/architecture.md) — system architecture, decision log (§1.1)
- [`docs/fx-usdjpy-architecture.md`](docs/fx-usdjpy-architecture.md) — FX lane rationale
- [`docs/specs/`](docs/specs/) — normative component specs (`00`–`19`)

## Repository layout

```
cmd/trader/            live engine entrypoint (graceful shutdown on SIGTERM)
cmd/backtester/        offline replay through the SAME strategy/risk code path
cmd/calendarpoller/    Finnhub → Gemini → Telegram → durable calendar-state.json
cmd/dashboard/         read-only Cloud Run ops UI (spec 14; never writes to OANDA)
internal/config/       YAML loader + ${ENV} secret resolution + fail-fast validation
internal/dashboard/    ops dashboard APIs, auth, caches, embedded UI
internal/oanda/        v20 REST + pricing-stream client (reconnect/backoff/heartbeat)
internal/marketdata/   stream consumer, price snapshot, staleness detection
internal/candles/      M5+H1 ring buffers, REST-confirmed closes, MarketEvent emission
internal/session/      session controllers + hub (EU + FX clocks / range / ATR / VWAP)
internal/strategy/     Strategy interface + registry + 1:1 router; eulove/ + fxtrld/
internal/risk/         gate chain (spec order) + sizing + FX gates + margin/leverage
internal/execution/    OrderExecutor interface + OANDA impl (bracketed orders, idempotency)
internal/trademgmt/    2s timer loop: breakeven, time-cutoff, news flatten (level-triggered)
internal/controlplane/ HMAC webhook :8443 + global pause/flatten state + account breaker locks
internal/calendar/     economic-calendar cache + poller pipeline + trading-holiday file
internal/portfolio/    accounts → instrument groups → strategies; per-account P&L state
internal/scheduler/    ticker-based housekeeping (calendar refresh, reconcile) — NOT signals
internal/backtest/     simulation engine: fill model (spread+slippage), metrics, data cache
internal/logging/      slog JSON logging + trade audit publisher (stdout in v1)
internal/metrics/      counters/gauges behind an interface (in-memory now, Prometheus later)
pkg/types/             shared data contracts per docs/specs/01
pkg/utils/             IANA/DST-safe clock helpers, price rounding
config/                config.dev|demo|prod.yaml + dashboard*.yaml + holidays.yaml
data/                  calendar-state.json + ops README (calendar ritual)
deploy/                Dockerfile(s), systemd unit, VM deploy script
.github/workflows/     ci.yml (vet+lint+test+build), deploy.yml (manual, demo|prod)
```

## Quickstart

Requirements: Go 1.25+.

```bash
# 1. Build + test
go build ./... && go test ./...

# 2. Secrets (v1: env vars; Secret Manager later behind the same interface)
export OANDA_API_TOKEN="..."           # from OANDA fxpractice account
export OANDA_ACCOUNT_ID_EU="101-..."   # EU sub-account id
export OANDA_ACCOUNT_ID_FX="101-..."   # FX USD/JPY sub-account id (when FX active)
export CONTROL_HMAC_SECRET="$(openssl rand -hex 32)"
export DASHBOARD_TOKEN="$(openssl rand -hex 32)"  # bearer auth for ops UI
# Calendar poller (optional locally):
export FINNHUB_API_KEY="..."
export GEMINI_API_KEY="..."
export TELEGRAM_BOT_TOKEN="..."
export TELEGRAM_CHAT_ID="..."

# 3. Run the live engine against fxpractice (paper)
#    config.dev.yaml has eu-indices + fx-usdjpy active by default
go run ./cmd/trader --config config/config.dev.yaml

# 4. Run an EU backtest (downloads + caches OANDA candles as CSV)
#    FX offline harness is specified in docs/specs/19 but not wired yet
go run ./cmd/backtester --config config/config.dev.yaml \
  --instrument DE30_EUR --from 2026-05-01 --to 2026-07-01

# 5. Economic calendar poller (one shot: Finnhub → Gemini → Telegram → file)
go run ./cmd/calendarpoller -config config/config.dev.yaml

# 6. Ops dashboard (read-only; separate process — never on the trader hot path)
export DASHBOARD_TOKEN=dev
go run ./cmd/dashboard --config config/config.dashboard.mock.yaml
#    Open http://localhost:8080/?token=dev
#    Against fxpractice + local files: config/config.dashboard.cloudrun.yaml
#    (needs OANDA_* + data/*)
```

The backtester prints the trade list, P&L / win rate / profit factor / max
drawdown, and writes an equity-curve CSV to `out/`. Cached candles land in
`data/candles/` so repeat runs work offline.

### FX enablement

| Env | `fx-usdjpy` default | Notes |
| --- | --- | --- |
| `config.dev.yaml` / `config.demo.yaml` | `active: true` | Needs `OANDA_ACCOUNT_ID_FX` |
| `config.prod.yaml` | `active: false` | Flip deliberately before live FX |

Registry / signal strategy key is always `fx_trld` (not `FX_TRLD`). Soft validation
targets (offline backtest + paper soak) **guide** promotion (Decision B) — see
[`docs/specs/19-fx-validation-backtest.md`](docs/specs/19-fx-validation-backtest.md).
`cmd/backtester` is EU-only today; FX paper soak on `fxpractice` is the practical
check until the FX harness ships.

### Control plane

Signed JSON over HTTPS on `:8443` (HMAC-SHA256 of the raw body, hex in
`X-Signature`; unix `ts` within 60s; unique `nonce`):

```bash
body='{"command":"STATUS","args":{},"nonce":"'$(uuidgen)'","ts":'$(date +%s)'}'
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$CONTROL_HMAC_SECRET" -r | cut -d' ' -f1)
curl -sk -X POST "https://VM_IP:8443/command" -H "X-Signature: $sig" -d "$body"
```

Commands: `FLATTEN` (cancel all orders, close all trades → `DISABLED`),
`PAUSE` / `RESUME` (process-wide), `RE_ARM` (clears breaker-locked accounts and
snapshots their new baseline equity), `STATUS` (includes each account's lock state).

### Ops dashboard (Cloud Run)

Read-only pull UI per [`docs/specs/14-dashboard.md`](docs/specs/14-dashboard.md):
open trades / account (EU **and** FX), engine health, economic calendar, per-day
realized P&L. Calendar review Telegram is the push channel for calendar ops;
**trade/session Telegram alerts are deferred** (spec 12). The dashboard **never**
places orders and **does not** expose control-plane commands (`FLATTEN` / `PAUSE` /
… stay on the VM HMAC webhook).

| Route | Auth | Purpose |
| --- | --- | --- |
| `GET /` | bearer / IAP | Single ops page (auto-refresh) |
| `GET /api/health` | open | Service liveness (no data) |
| `GET /api/overview` | yes | Accounts (EU + FX), open trades, health |
| `GET /api/calendar` | yes | High-impact events from `calendar-state.json` |
| `GET /api/pl?account=&window=7d\|30d\|all` | yes | Rollup totals |
| `GET /api/pl/daily?account=&from=&to=` | yes | Per-day series + 7d / all-time |

Local sources: `dashboard.calendar_file` / `ledger_file` (dev). Cloud: GCS
objects + BigQuery `trade_ledger` when configured. Optional `status.json` for
engine heartbeat; without it the health panel degrades (amber) and does not invent
state. Deploy: `deploy/docker/Dockerfile.dashboard` → Cloud Run (`PORT` honored);
image CMD uses `config/config.dashboard.cloudrun.yaml`.

**Prod access** — open with the bearer token from `.env` (must match the live
Cloud Run env `DASHBOARD_TOKEN`, which is set from `.env` at deploy — not
necessarily Secret Manager):

```bash
# Open (token from .env)
source .env
open "https://tradex-dashboard-eqzqihi64a-ue.a.run.app/?token=${DASHBOARD_TOKEN}"
```

Rotate and push to Cloud Run:

```bash
NEW=$(openssl rand -hex 32)
# also write NEW into .env as DASHBOARD_TOKEN=...
gcloud run services update tradex-dashboard \
  --project=fxtrade-prod-12345 \
  --region=us-east1 \
  --update-env-vars="DASHBOARD_TOKEN=${NEW}"
```

### Calendar poller

Normative: [`docs/specs/10-economic-calendar.md`](docs/specs/10-economic-calendar.md).
Ops ritual: [`data/README-calendar.md`](data/README-calendar.md).

Pipeline: **Finnhub primary → Gemini Live Search** (`gemini-2.5-flash-lite`) →
**Telegram review** → write durable `data/calendar-state.json` (and/or GCS). The
trader VM only reads the durable file (fail-safe if missing/stale).

```bash
go run ./cmd/calendarpoller -config config/config.dev.yaml
```

## Configuration

One YAML per environment (`config/config.{dev,demo,prod}.yaml`); the binary
takes `--config`. **Live vs paper is chosen by the OANDA host + credentials
loaded — never a boolean.** Secrets appear only as `${ENV_VAR}` references.
Validation is fail-fast at boot; the effective config is logged with secrets
redacted.

Multi-account model (`accounts:`): each entry maps one OANDA sub-account to an
instrument group, a strategy (registry key), and optional per-account risk
overrides. Dev/demo activate `eu-indices` and `fx-usdjpy`; prod keeps FX inactive
until enabled. The strategy router dispatches per instrument 1:1 (`eu_love` /
`fx_trld`).

Key risk defaults (per account): 1% equity/trade, ATR brackets (0.5×ATR SL /
1.5×ATR TP from strategy config), ≤10% equity margin, <5:1 leverage, −$150/day
hard lock, 3-consecutive-loss halt. EU: correlation guard (`DE30_EUR`+`FR40_EUR`
= one at a time). FX: one accepted entry per Tokyo session day, weekend/reopen
gates, 60m news blackout (`fx_risk`).

## Deployment (GCP VM)

1. **Project + VM**: separate GCP projects for dev/prod. Create an e2-micro:
   `gcloud compute instances create tradex-vm --machine-type=e2-micro
   --zone=europe-west3-a --image-family=debian-12 --image-project=debian-cloud`
2. **Service account**: attach a least-privilege SA (Secret Manager accessor
   when you move secrets off env files; Pub/Sub publisher later). No key files.
3. **Firewall**: allow `:8443` only from admin source ranges:
   `gcloud compute firewall-rules create tradex-control
   --allow=tcp:8443 --source-ranges=<ADMIN_IP>/32 --target-tags=tradex`
4. **Install**: clone to `/opt/tradex`, install Go, create the `tradex` user,
   write `/etc/tradex/tradex.env` (mode 0640) with secrets (include
   `OANDA_ACCOUNT_ID_FX` / `GEMINI_API_KEY` when using those paths), then
   `sudo cp deploy/systemd/tradex.service /etc/systemd/system/ &&
   sudo systemctl enable --now tradex`.
5. **Deploys**: `deploy/scripts/deploy.sh demo|prod` (pull → build → restart),
   or the `Deploy` GitHub workflow (manual dispatch with an environment input;
   SSH secrets live in GitHub Environments so prod can require reviewers).

Environment strategy: **local (dev config, fxpractice) → demo (VM, fxpractice,
paper validation) → prod (separate project, fxtrade)** — same binary, same
code path, different config file + credentials.

## Scaling plan

- **More markets**: add a strategy package, register it in the registry, add an
  account entry with its instrument group — the router, risk engine (per
  account), and management loop already generalize.
- **Multi-VM**: one VM per account/category keeps the single-writer invariant
  per OANDA account while isolating blast radius; the control plane runs per VM.
- **Queue-based execution (later)**: the `OrderExecutor` interface is the seam —
  a queue-backed implementation can serialize writers behind Pub/Sub without
  touching strategy/risk code.
- **Async sidecars**: calendar poller is implemented (`cmd/calendarpoller`).
  Pub/Sub → BigQuery ledger and trade/session Telegram notifier remain deferred
  behind `logging.Publisher` / `metrics.Registry` (specs 11–12).

## Spec deviation notes

Working code was preferred where the specs met OANDA reality; deviations:

1. **Fractional units** — `OrderRequest.Units`/`OpenTrade.Units` are `float64`,
   not the original int64 (spec 01/07). OANDA index CFDs trade in 0.1-unit
   steps (`minimumTradeSize: 0.1`); with integer units a $5k account cannot
   express any viable DE30 size (1% risk on a 0.5×ATR stop ≈ 0.8 units). Units
   are rounded to OANDA's `tradeUnitsPrecision`.
2. **PointValue is boot-time priced** — sizing values one OANDA pip per unit in
   the account currency. When the instrument's quote/P&L currency differs from
   the account currency, startup obtains a current OANDA midpoint for the direct
   conversion pair (for example, `0.01 / USD_JPY` for `USD_JPY` in a USD account).
   The value is fixed until restart, so it can drift as the conversion rate moves.
3. **Trade management runs in `PAUSED`/`SYSTEM_LOCKED`/`DISABLED`** — spec 08's
   pseudocode gates the loop on `ACTIVE`, but spec 09 requires paused/locked
   positions to keep being managed to their exit. `DISABLED` (post-FLATTEN) also
   keeps managing so a partial flatten retries closes until flat. Entries are
   blocked by risk in all non-ACTIVE states either way.
4. **Executor host/account config moved** — early drafts put `host`/`account_id`
   under `executor:`; the multi-account model uses `oanda:` (shared host) and
   `accounts[]` (id per sub-account).
5. **Trader calendar read path is file-based** — the VM reads durable
   `calendar-state.json` (fail-safe). The **poller** that produces that file
   (`cmd/calendarpoller`: Finnhub → Gemini → Telegram) is implemented; Cloud
   Scheduler → Cloud Run scheduling may still be ops wiring. Dashboard also
   reads the same schema (local file or GCS).
6. **Backtester is EU LOVE only** — replays through the same strategy/risk/
   management code for EU sessions, with two backtest-only behaviors: a simulated
   daily `RE_ARM` at each new session day (otherwise one locked day would blank
   the rest of the dataset) and SL-before-TP when one candle spans both brackets
   (conservative fill model). FX soft-target harness keys in spec 19 are design
   only until `fxtrld` is wired into `cmd/backtester`.
7. **`ClientOrderID` time component uses the signal timestamp** formatted
   `yyyymmdd-hhmm` in its own zone (candle-close time), which keys retries to
   the intended entry candle exactly.
8. **Boot restores daily risk state** — before entries start, the trader reads
   today's OANDA `ORDER_FILL` transactions per active account, reconstructs
   realized P&L and the consecutive-loss streak, and locks only the breached
   account when either configured breaker was already breached.

## TODO (deferred by design)

- Pub/Sub publisher + BigQuery `trade_ledger` sidecar (spec 11) behind
  `logging.Publisher`; trade/session **Telegram notifier** + Cloud Monitoring
  metrics (spec 12) behind `metrics.Registry`. Calendar review Telegram is live.
- Cloud Scheduler → calendar poller on a schedule (binary exists; wiring is ops).
- Trader `status.json` publisher to GCS (async/best-effort; not on the order
  hot path) so the dashboard health panel can show green/red from live state.
- Secret Manager resolver behind `config.SecretResolver` (env vars in v1).
- US Sweep / Asia MeanRev strategies (interfaces and LIMIT-order plumbing are
  in place: `CancelOrder`, `LimitPrice`, GTD).
- 2026 holiday file is a best-effort placeholder — validate against the
  official Eurex/Euronext calendars before go-live.
- Dashboard control-plane command UI (explicitly out of scope for v1).
