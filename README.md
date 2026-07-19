# Tradex

Automated OANDA trading system in Go. A single always-on binary on a GCP VM
owns the entire trade hot path — market data → candles → session context →
strategy → risk → execution → trade management — and is the **only writer to
OANDA**. v1 trades the **EU LOVE** strategy (London Open Volatility Extension)
on `DE30_EUR` / `FR40_EUR`.

Design docs are the source of truth:

- [`docs/architecture.md`](docs/architecture.md) — system architecture, v1 decision log (§1.1)
- [`docs/specs/`](docs/specs/) — 14 normative component specs (data contracts → configuration)

## Repository layout

```
cmd/trader/            live engine entrypoint (graceful shutdown on SIGTERM)
cmd/backtester/        offline replay through the SAME strategy/risk code path
cmd/dashboard/         read-only Cloud Run ops UI (spec 14; never writes to OANDA)
internal/config/       YAML loader + ${ENV} secret resolution + fail-fast validation
internal/dashboard/    ops dashboard APIs, auth, caches, embedded UI
internal/oanda/        v20 REST + pricing-stream client (reconnect/backoff/heartbeat)
internal/marketdata/   stream consumer, price snapshot, staleness detection
internal/candles/      M5+H1 ring buffers, REST-confirmed closes, MarketEvent emission
internal/session/      EU session controller: range lock, Wilder ATR, VWAP, VolMA12
internal/strategy/     Strategy interface + registry + 1:1 router; eulove/ implementation
internal/risk/         gate chain (spec order) + sizing + margin/leverage gate
internal/execution/    OrderExecutor interface + OANDA impl (bracketed orders, idempotency)
internal/trademgmt/    2s timer loop: breakeven, time-cutoff, news flatten (level-triggered)
internal/controlplane/ HMAC webhook :8443 + ACTIVE/PAUSED/SYSTEM_LOCKED/DISABLED machine
internal/calendar/     economic-calendar cache (fail-safe) + trading-holiday file
internal/portfolio/    accounts → instrument groups → strategies; per-account P&L state
internal/scheduler/    ticker-based housekeeping (calendar refresh, reconcile) — NOT signals
internal/backtest/     simulation engine: fill model (spread+slippage), metrics, data cache
internal/logging/      slog JSON logging + trade audit publisher (stdout in v1)
internal/metrics/      counters/gauges behind an interface (in-memory now, Prometheus later)
pkg/types/             shared data contracts per docs/specs/01
pkg/utils/             IANA/DST-safe clock helpers, price rounding
config/                config.dev|demo|prod.yaml + holidays.yaml
deploy/                Dockerfile, systemd unit, VM deploy script
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
export CONTROL_HMAC_SECRET="$(openssl rand -hex 32)"
export DASHBOARD_TOKEN="$(openssl rand -hex 32)"  # bearer auth for ops UI

# 3. Run the live engine against fxpractice (paper)
go run ./cmd/trader --config config/config.dev.yaml

# 4. Run a backtest (downloads + caches OANDA candles as CSV)
go run ./cmd/backtester --config config/config.dev.yaml \
  --instrument DE30_EUR --from 2026-05-01 --to 2026-07-01

# 5. Ops dashboard (read-only; separate process — never on the trader hot path)
export DASHBOARD_TOKEN=dev
go run ./cmd/dashboard --config config/config.dashboard.mock.yaml
#    Open http://localhost:8080/?token=dev
#    Against fxpractice + local files: config/config.dev.yaml (needs OANDA_* + data/*)
```

The backtester prints the trade list, P&L / win rate / profit factor / max
drawdown, and writes an equity-curve CSV to `out/`. Cached candles land in
`data/candles/` so repeat runs work offline.

### Control plane

Signed JSON over HTTPS on `:8443` (HMAC-SHA256 of the raw body, hex in
`X-Signature`; unix `ts` within 60s; unique `nonce`):

```bash
body='{"command":"STATUS","args":{},"nonce":"'$(uuidgen)'","ts":'$(date +%s)'}'
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$CONTROL_HMAC_SECRET" -r | cut -d' ' -f1)
curl -sk -X POST "https://VM_IP:8443/command" -H "X-Signature: $sig" -d "$body"
```

Commands: `FLATTEN` (cancel all orders, close all trades → `DISABLED`),
`PAUSE` / `RESUME`, `RE_ARM` (only exit from `SYSTEM_LOCKED`; snapshots a new
baseline equity), `STATUS`.

### Ops dashboard (Cloud Run)

Read-only pull UI per [`docs/specs/14-dashboard.md`](docs/specs/14-dashboard.md):
open trades / account, engine health, economic calendar, per-day realized P&L.
Telegram remains the push channel. The dashboard **never** places orders and
**does not** expose control-plane commands (`FLATTEN` / `PAUSE` / … stay on the
VM HMAC webhook).

| Route | Auth | Purpose |
| --- | --- | --- |
| `GET /` | bearer / IAP | Single ops page (auto-refresh) |
| `GET /api/health` | open | Service liveness (no data) |
| `GET /api/overview` | yes | Accounts, open trades, health |
| `GET /api/calendar` | yes | High-impact events from `calendar-state.json` |
| `GET /api/pl?account=&window=7d\|30d\|all` | yes | Rollup totals |
| `GET /api/pl/daily?account=&from=&to=` | yes | Per-day series + 7d / all-time |

Local sources: `dashboard.calendar_file` / `ledger_file` (dev). Cloud: GCS
objects + BigQuery `trade_ledger`. Optional `status.json` for engine heartbeat;
without it the health panel degrades (amber) and does not invent state.
Deploy: `deploy/docker/Dockerfile.dashboard` → Cloud Run (`PORT` honored).

## Configuration

One YAML per environment (`config/config.{dev,demo,prod}.yaml`); the binary
takes `--config`. **Live vs paper is chosen by the OANDA host + credentials
loaded — never a boolean.** Secrets appear only as `${ENV_VAR}` references.
Validation is fail-fast at boot; the effective config is logged with secrets
redacted.

Multi-account model (`accounts:`): each entry maps one OANDA sub-account to an
instrument group, a strategy (registry key), and optional per-account risk
overrides. v1 activates only `eu-indices`; commented-out `us-indices` /
`fx-majors` examples show how future markets slot in. The strategy router
dispatches per instrument 1:1.

Key risk defaults (per account, `risk:`): 1% equity/trade, ATR brackets
(0.5×ATR SL / 1.5×ATR TP from strategy config), ≤10% equity margin, <5:1
leverage, −$150/day hard lock, 3-consecutive-loss halt, correlation guard
(`DE30_EUR`+`FR40_EUR` = one at a time), one trade per instrument.

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
   write `/etc/tradex/tradex.env` (mode 0640) with the three secrets, then
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
- **Async sidecars (deferred)**: the `logging.Publisher` interface is where the
  Pub/Sub publisher slots in (BigQuery ledger, Telegram notifier, calendar
  poller per specs 10–12).

## Spec deviation notes

Working code was preferred where the specs met OANDA reality; deviations:

1. **Fractional units** — `OrderRequest.Units`/`OpenTrade.Units` are `float64`,
   not the spec's `int64` (spec 01/07). OANDA index CFDs trade in 0.1-unit
   steps (`minimumTradeSize: 0.1`); with integer units a $5k account cannot
   express any viable DE30 size (1% risk on a 0.5×ATR stop ≈ 0.8 units). Units
   are rounded to OANDA's `tradeUnitsPrecision`.
2. **PointValue = 1.0 (quote currency)** — sizing values one price point per
   unit at 1.0 of the *quote* currency (EUR for `DE30_EUR`). Cross-currency
   conversion into the account currency is a TODO; on a EUR-denominated account
   it is exact.
3. **Trade management runs in `PAUSED`/`SYSTEM_LOCKED`** — spec 08's pseudocode
   gates the loop on `ACTIVE`, but spec 09 requires paused/locked positions to
   keep being managed to their exit. The loop only stops in `DISABLED`
   (post-FLATTEN, nothing left to manage). Entries are blocked by risk in all
   non-ACTIVE states either way.
4. **Executor host/account config moved** — spec 13 put `host`/`account_id`
   under `executor:`; the multi-account model moves them to `oanda:` (shared
   host per environment) and `accounts[]` (id per sub-account).
5. **Calendar provider is file-based in v1** — the GCS-object read path exists
   as a stub behind the `Provider` interface and fails safe (missing/stale
   state ⇒ "event imminent" ⇒ entries blocked, stops to breakeven). The
   Cloud Run poller sidecar is out of scope, per the brief. The **dashboard**
   process does implement GCS object fetch for `calendar-state.json` /
   `status.json` (separate from the trader VM read path).
6. **Backtester is in scope** (the specs deferred it) — it replays through the
   same strategy/risk/management code, with two backtest-only behaviors: a
   simulated daily `RE_ARM` at each new session day (otherwise one locked day
   would blank the rest of the dataset) and SL-before-TP when one candle spans
   both brackets (conservative fill model).
7. **`ClientOrderID` time component uses the signal timestamp** formatted
   `yyyymmdd-hhmm` in its own zone (candle-close time), which keys retries to
   the intended entry candle exactly.
8. **Boot state is `ACTIVE`** — spec 09's "boot into `SYSTEM_LOCKED` if the
   daily loss was already breached" needs the ledger sidecar (deferred); until
   then the equity reconcile + breaker re-check trips the lock on the first
   settled close. Listed under TODO.

## TODO (deferred by design)

- Pub/Sub publisher + BigQuery `trade_ledger` sidecar (spec 11) behind
  `logging.Publisher`; Telegram notifier + Cloud Monitoring metrics (spec 12)
  behind `metrics.Registry`.
- Calendar poller sidecar (Finnhub → GCS) + GCS provider implementation on the
  **trader** VM (spec 10). Dashboard already reads GCS when configured.
- Trader `status.json` publisher to GCS (async/best-effort; not on the order
  hot path) so the dashboard health panel can show green/red from live state.
- Secret Manager resolver behind `config.SecretResolver` (env vars in v1).
- Daily-loss recomputation from OANDA transaction history at boot (see
  deviation 8).
- US Sweep / Asia MeanRev strategies (interfaces and LIMIT-order plumbing are
  in place: `CancelOrder`, `LimitPrice`, GTD).
- 2026 holiday file is a best-effort placeholder — validate against the
  official Eurex/Euronext calendars before go-live.
- Dashboard control-plane command UI (explicitly out of scope for v1).
