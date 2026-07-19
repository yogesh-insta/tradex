# Spec: Ops Dashboard (Cloud Run, read-only)

## Purpose

Provide an **internet-accessible, read-only operations view** of Tradex so an operator
can inspect open risk, engine health, the economic calendar, and realized P&L without
relying only on Telegram alerts. The dashboard is a **pull** UI; Telegram remains the
**push** channel (`12-observability-and-alerts.md`).

This component is **off the money path**. It must never block, couple into, or share
process fate with the VM trader.

## Inputs

- **OANDA REST (read-only):** account summary + `openTrades` (and related account fields)
  for live positions / equity / margin. Uses the same host/token model as config, but
  this service **must not** place, modify, or cancel orders.
- **GCS `calendar-state.json`:** durable economic-calendar state written by the calendar
  poller (`10-economic-calendar.md`). Same object the VM reads.
- **GCS `status.json` (optional but preferred):** compact engine heartbeat / health
  snapshot published asynchronously by the trader (or a thin publisher). If missing in
  early builds, health may be partially derived from OANDA + ledger freshness and the
  gap must be documented.
- **BigQuery `trade_ledger`:** closed-trade history for realized P&L aggregates
  (`11-trade-ledger-persistence.md`).
- **Auth secret / IAP:** allowlisted identity or shared bearer for HTTPS access.
- Config: accounts, GCS URIs, BQ dataset/table, cache TTLs, auth mode
  (`13-configuration.md` style).

## Outputs

- HTTPS UI (`GET /`) — single ops page with four sections (below).
- JSON APIs consumed by the UI (and curl/debug):
  - `GET /api/health` — dashboard service liveness (not trader liveness).
  - `GET /api/overview` — accounts, open trades, engine health (aggregated).
  - `GET /api/calendar` — upcoming events from GCS calendar state.
  - `GET /api/pl?account=&window=7d|30d|all` — rollup totals per account.
  - `GET /api/pl/daily?account=&from=&to=` — **per-day** realized P&L series.
- Deployable **Cloud Run** service (scale-to-zero). No always-on VM for the UI.

## Panels (normative)

### 1. Open trades & account

Per configured account:

- Table: instrument, direction, units, entry, current SL/TP, unrealized P&L, open time,
  account name.
- Summary: NAV/equity, realized P&L today (if available), margin used/available,
  control-plane `SystemState` when present in `status.json`.

### 2. Engine / bot health

Per engine/account (v1: EU account; later multi-account):

- State: `ACTIVE` | `PAUSED` | `SYSTEM_LOCKED` | `DISABLED` (from `status.json` when
  available).
- Stream: up / stale; last tick age.
- Liveness: last heartbeat age; last reconcile ok/fail.
- Calendar freshness: `as_of` age from calendar state (or status mirror).
- Visual: green / amber / red thresholds from config.

### 3. Economic calendar

- List upcoming **high-impact** events from `calendar-state.json`.
- Show region, title, impact, UTC time (and Europe/Berlin wall time for EU).
- If object missing/stale, show fail-safe banner (same semantics as VM: treat as unknown /
  imminent — display warning; do not invent events).

### 4. P&L summary (incl. per-day)

Per account:

- **Daily series:** realized P&L **per calendar day** for at least the last **30 days**
  (table required; simple bar/sparkline optional).
- **7-day total:** sum of the last 7 daily rows.
- **All-time total:** sum of all realized closed trades in the ledger for that account.
- Optional per day: trade count, wins / losses.

**Realized P&L day bucketing:** group by `DATE(close_time)` in **UTC** unless config
sets another reporting timezone. Prefer `close_time` (not `entry_time`) so daily P&L
matches when the trade realized. Document the timezone in the UI.

## Behavior

1. **Hosting:** Cloud Run service serves UI + API. Cold starts are acceptable.
2. **Read-only:** no order placement; no control-plane commands in v1 UI
   (`FLATTEN` / `PAUSE` / `RESUME` / `RE_ARM` stay on the VM HMAC webhook —
   `09-control-plane.md`).
3. **Caching:** cache OANDA overview, calendar object, and BQ aggregates in process
   memory for `cache_ttl` (default 60s overview/calendar, 60–300s P&L). Never full-scan
   BigQuery on every browser refresh.
4. **Auth:** reject unauthenticated requests to `/api/*` and `/` (except optionally
   `/api/health` if used by an uptime check without data leakage).
5. **Multi-account:** honor the config `accounts` model; aggregate overview; filter P&L
   by `account`.
6. **Failure isolation:** OANDA/GCS/BQ errors return partial payloads + error flags in
   JSON/UI. Dashboard outage must not affect the trader.
7. **Auto-refresh:** UI polls every 15–30s (config).

### BigQuery shaping (informative)

```sql
-- daily realized PL
SELECT
  account,
  DATE(close_time, "UTC") AS day,
  SUM(realized_pl) AS realized_pl,
  COUNT(*) AS trade_count
FROM `project.dataset.trade_ledger`
WHERE close_time IS NOT NULL
  AND account = @account
  AND DATE(close_time, "UTC") BETWEEN @from AND @to
GROUP BY account, day
ORDER BY day DESC;

-- all-time
SELECT account, SUM(realized_pl) AS realized_pl, COUNT(*) AS trade_count
FROM `project.dataset.trade_ledger`
WHERE close_time IS NOT NULL AND account = @account
GROUP BY account;
```

Use parameterized queries; restrict selected columns; rely on partition/cluster if present.

### Optional `status.json` schema (when trader publishes)

```json
{
  "as_of": "2026-07-19T02:00:00Z",
  "accounts": [
    {
      "name": "eu-indices",
      "state": "ACTIVE",
      "stream_up": true,
      "last_tick_age_ms": 1200,
      "last_heartbeat_at": "2026-07-19T02:00:00Z",
      "last_reconcile_ok": true,
      "market_data_stale": false
    }
  ]
}
```

Publishing must be async / best-effort and **never** on the order hot path.

## Config keys

```yaml
dashboard:
  listen_addr: ":8080"
  auth:
    mode: "bearer"          # bearer | iap
    token_ref: "projects/…/secrets/dashboard-token"
  oanda:
    host: "api-fxpractice.oanda.com"   # read-only usage
    token: "${OANDA_API_TOKEN}"
  accounts:                 # or reuse top-level accounts from env config
    - name: "eu-indices"
      oanda_account_id: "${OANDA_ACCOUNT_ID_EU}"
  gcs:
    calendar_object: "gs://tradex-<env>-state/calendar-state.json"
    status_object: "gs://tradex-<env>-state/status.json"   # optional
  bigquery:
    project: "tradex-<env>"
    dataset: "tradex"
    table: "trade_ledger"
  cache_ttl:
    overview: 60s
    calendar: 60s
    pl: 180s
  ui:
    refresh_interval: 20s
    pl_daily_lookback_days: 30
    reporting_tz: "UTC"     # day bucket for per-day PL
  health:
    heartbeat_stale: 120s
    tick_stale: 60s
```

## Failure modes

| Failure | Handling |
| --- | --- |
| OANDA unreachable | Overview shows last cache or error; other panels still serve |
| GCS calendar missing/stale | Calendar panel warns; no fake events |
| GCS status missing | Health panel degraded; derive what is safe from OANDA/BQ freshness |
| BigQuery error / quota | PL panel error state; do not retry-storm |
| Auth misconfigured | Fail closed (deny all data routes) |
| Cloud Run cold start | UI may spin briefly; acceptable |

## Acceptance criteria

- Authenticated browser (off-VPN) sees all four panels.
- Unauthenticated requests to data routes get 401/403.
- Open-trades view matches OANDA for configured accounts (or explicit mock mode for local
  demos).
- Calendar panel parses the same `calendar-state.json` schema as the VM.
- P&L section shows **per-day** rows plus **7d** and **all-time** totals per account.
- Sum of the last 7 daily rows equals the 7d total for the same account (within rounding).
- Day buckets use configured `reporting_tz` (default UTC) on `close_time`.
- Aggregates are cached; repeated refreshes within TTL do not re-query BQ.
- Dashboard failure cannot affect VM trading.
- Fits Always Free / free-tier assumptions (Cloud Run scale-to-zero; tiny BQ bytes).

## Out of scope

- Trading actions / control-plane command UI (v1).
- Tick charts / tick lake.
- Mobile-native app.
- Multi-user RBAC beyond allowlist / bearer / IAP.
- Running the dashboard on the trader e2-micro.
- Implementing the trader `status.json` publisher (separate change; dashboard must degrade
  gracefully without it).

## Deployment notes (informative)

- Preferred path: `cmd/dashboard` → container → Cloud Run in `tradex-dev` first.
- Secrets: OANDA token, dashboard auth token, optional BQ SA via Cloud Run runtime SA.
- Keep UI dense/ops-oriented; auto-refresh; empty states when no trades/events.
