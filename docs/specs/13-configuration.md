# Spec: Configuration & Secrets

## Purpose

All tunables live in a **per-environment config file**; all credentials live in **Secret
Manager**. No magic numbers in code, no secrets in files. Dev and prod are **separate GCP
projects**; live vs paper is chosen by the OANDA host/credentials loaded — not a boolean.

## Layout

```
config/
  dev.yaml        # tradex-dev project, api-fxpractice host
  prod.yaml       # tradex-prod project, host per account (paper until go-live)
  holidays.yaml   # checked-in trading holidays / half-days
```

The binary takes `--env dev|prod` (or `TRADEX_ENV`) and loads the matching file. Secrets
are referenced by resource name and resolved at boot from Secret Manager via the attached
service account (no key files).

## Consolidated config (per environment)

```yaml
env: "dev"
project: "tradex-dev"

stream:            # 02-market-data-stream.md
  instruments: ["DE30_EUR", "FR40_EUR"]
  heartbeat_timeout: 15s
  stale_halt: 60s
  backoff_base: 1s
  backoff_max: 60s
  tick_channel_buffer: 4096

candles:           # 03-candle-builder.md
  price: "M"
  timeframes: { DE30_EUR: ["H1","M5"], FR40_EUR: ["H1","M5"] }
  window_max: 50
  rest_confirm_delay: 2s
  rest_confirm_timeout: 5s

eu_session:        # 04-eu-session-controller.md
  tz: "Europe/Berlin"
  range_start: "08:00:00"
  range_end: "09:00:00"
  trade_window_start: "09:05:00"
  atr_period_days: 14
  vol_ma_candles: 12
  instruments: ["DE30_EUR", "FR40_EUR"]

eu_love:           # 05-strategy-eu-love.md
  volume_spike_mult: 1.0
  sl_atr_mult: 0.5
  tp_atr_mult: 1.5
  breakeven_at_r: 1.0
  entry_window_end: "11:00:00"

# FX lane (15–19) — enabled when FX account + USD_JPY are configured
fx_session:        # 16-fx-session-controller.md
  tz: "Asia/Tokyo"
  range_start: "09:00:00"
  range_end: "11:00:00"
  trade_window_start: "16:00:00"
  soft_cutoff: "21:00:00"
  prep_time: "07:30:00"
  atr_period_days: 14
  vol_ma_candles: 12
  instruments: ["USD_JPY"]

fx_trld:           # 17-strategy-fx-trld.md
  volume_spike_mult: 1.0
  sl_atr_mult: 0.5
  tp_atr_mult: 1.5
  breakeven_at_r: 1.0
  min_atr_frac: 0.15
  max_atr_frac: 1.25
  max_spread_pips: 1.5
  trade_window_start: "16:00:00"
  entry_window_end: "19:00:00"
  trail_after_r: 0

fx_risk:           # 18-fx-risk-profile.md
  account_id: "${OANDA_FX_ACCOUNT_ID}"
  risk_per_trade: 0.01
  daily_loss_limit: 150
  consecutive_loss_halt: 3
  max_concurrent: 1
  max_margin_frac: 0.10
  max_leverage: 5.0
  news_block_before: 60m
  max_spread_pips: 1.5
  one_trade_per_day: true
  reopen_quiet_minutes: 30
  friday_no_entry: "12:00:00"      # America/New_York
  friday_hard_flatten: "16:00:00"  # America/New_York
  soft_cutoff_flatten_r: 0.5
  calendar_regions: ["US", "JP"]

fx_backtest:       # 19-fx-validation-backtest.md (harness; not required by trader boot)
  spread_pips: 1.0
  slippage_pips: 0.2
  pip_size: 0.01
  starting_equity: 5000
  min_oos_trades: 80
  max_oos_dd_frac: 0.15
  min_profit_factor: 1.1
  paper_min_weeks: 4

risk:              # 06-risk-management.md (EU account profile)
  risk_per_trade: 0.01
  daily_loss_limit: 150
  consecutive_loss_halt: 3
  max_concurrent: 1
  max_margin_frac: 0.10
  max_leverage: 5.0
  news_block_before: 30m
  correlation_groups: [["DE30_EUR","FR40_EUR"]]

executor:          # 07-order-executor.md
  account_id: "${OANDA_ACCOUNT_ID}"    # EU; from Secret Manager
  # FX orders use fx_risk.account_id; multi-account binding is per OrderRequest.Account
  host: "api-fxpractice.oanda.com"
  time_in_force: "FOK"
  request_timeout: 5s
  max_retries: 3
  retry_backoff_base: 250ms

mgmt:              # 08-trade-management-loop.md
  tick_interval: 2s
  reconcile_interval: 20s
  news_block_before: 30m
  eu_friday_cutoff: "17:30:00"
  eu_daily_cutoff: ""
  fx_friday_cutoff_tz: "America/New_York"
  fx_friday_cutoff: "16:00:00"
  fx_soft_cutoff_tz: "Asia/Tokyo"
  fx_soft_cutoff: "21:00:00"

control_plane:     # 09-control-plane.md
  listen: ":8443"
  auth: { secret_ref: "projects/…/secrets/control-hmac", max_skew: 60s, nonce_ttl: 300s }
  allow_cidrs: ["<admin-ip-range>"]

calendar:          # 10-economic-calendar.md
  economic:
    provider: "finnhub"              # primary; Finnhub free may 403 → fallback required
    fallback_provider: "gemini"      # Live Search via google.golang.org/genai
    gemini_model: "gemini-2.5-flash-lite"
    poll_interval: 20m
    lookahead_days: 7
    gcs_object: "gs://tradex-dev-state/calendar-state.json"
    local_file: "data/calendar-state.json"  # durable on-VM / ops override
    vm_refresh: 5m
    staleness_max: 90m
    high_impact_only: true
    regions: ["EU", "US", "JP"]
    telegram_review: true
    auto_write_on_telegram_ok: true
  holidays: { file: "config/holidays.yaml", markets: ["XETR","XPAR"] }

persistence:       # 11-trade-ledger-persistence.md
  topics: { trades: "trade-events" }
  bigquery: { dataset: "tradex", table: "trade_ledger" }
  buffer_size: 8192
  flush_timeout: 5s

observability:     # 12-observability-and-alerts.md
  heartbeat_interval: 30s
  liveness_timeout: 120s
  reconcile_interval: 20s
  drawdown_warn: 120
  alert_topic: "alerts"
  telegram:
    bot_token: "${TELEGRAM_BOT_TOKEN}"
    chat_id: "${TELEGRAM_CHAT_ID}"

dashboard:         # 14-dashboard.md (Cloud Run; not loaded by the trader binary)
  listen_addr: ":8080"
  auth: { mode: "bearer", token_ref: "projects/…/secrets/dashboard-token" }
  gcs:
    calendar_object: "gs://tradex-<env>-state/calendar-state.json"
    status_object: "gs://tradex-<env>-state/status.json"
  bigquery: { project: "tradex-<env>", dataset: "tradex", table: "trade_ledger" }
  cache_ttl: { overview: 60s, calendar: 60s, pl: 180s }
  ui: { refresh_interval: 20s, pl_daily_lookback_days: 30, reporting_tz: "UTC" }
```

## Secrets (Secret Manager, per project)

| Secret | Used by |
| --- | --- |
| `oanda-token` | executor, market-data, candle-builder, dashboard (read-only) |
| `oanda-account-id` (EU) | executor, risk, dashboard (read-only) |
| `oanda-fx-account-id` (FX) | executor, FX risk, dashboard when FX lane enabled |
| `control-hmac` | control-plane |
| `telegram-token`, `telegram-chat` | observability/notifier **and** calendar poller review (`TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`) |
| `finnhub-key` | calendar poller primary (`FINNHUB_API_KEY`) |
| `gemini-api-key` | calendar poller Gemini fallback (`GEMINI_API_KEY`) |
| `dashboard-token` | dashboard auth (bearer mode) |

## Validation (at boot)

- Fail fast if: a required secret is missing, a required instrument is absent from OANDA,
  `range_end <= range_start`, any ATR/mult ≤ 0, `daily_loss_limit <= 0`, or the timezone
  is unknown.
- Log the **effective config** (secrets redacted) once at startup for auditability.

## Acceptance criteria

- Same binary runs dev/prod purely by `--env`; no code differences (`architecture.md` §8).
- Changing a threshold (e.g. `sl_atr_mult`) requires only a config edit + restart.
- No secret value is ever written to logs or the config files.

## Out of scope

- Per-component behavior (see each component spec).
