# Spec: Configuration & Secrets

## Purpose

All tunables live in a **per-environment config file**; credentials are `${ENV_VAR}`
references (Secret Manager later behind the same interface). No magic numbers in code,
no secret values in committed files. Dev and prod are **separate GCP projects**; live vs
paper is chosen by the OANDA host/credentials loaded — not a boolean.

## Layout

```
config/
  config.dev.yaml                 # local / fxpractice
  config.demo.yaml                # VM paper
  config.prod.yaml                # live host (FX account inactive by default)
  config.dashboard.cloudrun.yaml  # Cloud Run ops UI (EU + FX accounts)
  config.dashboard.mock.yaml      # local dashboard demo (no OANDA)
  holidays.yaml                   # checked-in trading holidays / half-days
```

The binary takes `--config path/to/file.yaml`. Secrets are resolved at boot from the
environment (and later Secret Manager via the attached service account).

## Consolidated config (per environment)

Matches `config/config.dev.yaml` shape (abridged):

```yaml
env: "dev"
project: "tradex-dev"

oanda:
  host: "api-fxpractice.oanda.com"
  stream_host: "stream-fxpractice.oanda.com"
  token: "${OANDA_API_TOKEN}"

accounts:
  - name: "eu-indices"
    oanda_account_id: "${OANDA_ACCOUNT_ID_EU}"
    instruments: ["DE30_EUR", "FR40_EUR"]
    strategy: "eu_love"
    active: true
  - name: "fx-usdjpy"
    oanda_account_id: "${OANDA_ACCOUNT_ID_FX}"
    instruments: ["USD_JPY"]
    strategy: "fx_trld"
    active: true          # prod defaults false until deliberately enabled

stream:            # 02 — instruments derived from active accounts
  heartbeat_timeout: 15s
  stale_halt: 60s
  backoff_base: 1s
  backoff_max: 60s
  tick_channel_buffer: 4096

candles:           # 03
  price: "M"
  timeframes:
    DE30_EUR: ["H1", "M5"]
    FR40_EUR: ["H1", "M5"]
    USD_JPY: ["H1", "M5"]
  window_max: 50
  rest_confirm_delay: 2s
  rest_confirm_timeout: 5s

eu_session:        # 04
  tz: "Europe/Berlin"
  range_start: "08:00:00"
  range_end: "09:00:00"
  trade_window_start: "09:05:00"
  atr_period_days: 14
  vol_ma_candles: 12
  instruments: ["DE30_EUR", "FR40_EUR"]

fx_session:        # 16
  tz: "Asia/Tokyo"
  range_start: "09:00:00"
  range_end: "11:00:00"
  trade_window_start: "16:00:00"
  soft_cutoff: "21:00:00"
  prep_time: "07:30:00"
  atr_period_days: 14
  vol_ma_candles: 12
  instruments: ["USD_JPY"]

strategies:
  eu_love:         # 05
    volume_spike_mult: 1.0
    sl_atr_mult: 0.5
    tp_atr_mult: 1.5
    breakeven_at_r: 1.0
    entry_window_end: "11:00:00"
  fx_trld:         # 17
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

fx_risk:           # 18 — bound via accounts[] strategy fx_trld
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
  pip_size: 0.01

# fx_backtest:     # 19 — design keys; NOT loaded by config.go / cmd/backtester yet
#   spread_pips: 1.0
#   … (see 19-fx-validation-backtest.md)

backtest:          # EU backtester knobs (cmd/backtester)
  spread_points: 1.0

risk:              # 06 — EU account profile
  risk_per_trade: 0.01
  daily_loss_limit: 150
  consecutive_loss_halt: 3
  max_concurrent: 1
  max_margin_frac: 0.10
  max_leverage: 5.0
  news_block_before: 30m
  correlation_groups: [["DE30_EUR", "FR40_EUR"]]

executor:          # 07 — host/account live under oanda: + accounts[]
  time_in_force: "FOK"
  request_timeout: 5s
  max_retries: 3
  retry_backoff_base: 250ms

mgmt:              # 08
  tick_interval: 2s
  reconcile_interval: 20s
  news_block_before: 30m
  eu_friday_cutoff: "17:30:00"
  fx_friday_cutoff_tz: "America/New_York"
  fx_friday_cutoff: "16:00:00"
  fx_soft_cutoff: "21:00:00"

control_plane:     # 09
  listen: ":8443"
  auth: { secret: "${CONTROL_HMAC_SECRET}", max_skew: 60s, nonce_ttl: 300s }

calendar:          # 10
  economic:
    provider: "file"                 # trader VM read path (fail-safe)
    fallback_provider: "gemini"      # used by cmd/calendarpoller
    gemini_model: "gemini-2.5-flash-lite"
    poll_interval: 20m
    lookahead_days: 7
    state_file: "data/calendar-state.json"
    local_file: "data/calendar-state.json"
    vm_refresh: 5m
    staleness_max: 90m
    high_impact_only: true
    regions: ["EU", "US", "JP"]
    telegram_review: true
    auto_write_on_telegram_ok: true
  holidays: { file: "config/holidays.yaml", markets: ["XETR", "XPAR"] }

persistence:       # 11
  topics: { trades: "trade-events" }
  buffer_size: 8192
  flush_timeout: 5s

observability:     # 12 — trade Telegram notifier deferred; calendar review uses telegram:
  heartbeat_interval: 30s
  liveness_timeout: 120s
  reconcile_interval: 20s
  drawdown_warn: 120
  alert_topic: "alerts"
  telegram:
    bot_token: "${TELEGRAM_BOT_TOKEN}"
    chat_id: "${TELEGRAM_CHAT_ID}"

dashboard:         # 14 — Cloud Run / cmd/dashboard; not on trader hot path
  listen_addr: ":8080"
  auth: { mode: "bearer", token: "${DASHBOARD_TOKEN}" }
  calendar_file: "data/calendar-state.json"
  ledger_file: "data/trade-ledger.json"
  cache_ttl: { overview: 60s, calendar: 60s, pl: 180s }
  ui: { refresh_interval: 20s, pl_daily_lookback_days: 30, reporting_tz: "UTC" }
```

## Secrets (env today; Secret Manager later)

| Env / secret | Used by |
| --- | --- |
| `OANDA_API_TOKEN` | trader, market-data, candle-builder, dashboard (read-only) |
| `OANDA_ACCOUNT_ID_EU` | EU account, dashboard |
| `OANDA_ACCOUNT_ID_FX` | FX account, dashboard when FX enabled |
| `CONTROL_HMAC_SECRET` | control-plane |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | calendar poller review (trade notifier deferred) |
| `FINNHUB_API_KEY` | `cmd/calendarpoller` primary |
| `GEMINI_API_KEY` | `cmd/calendarpoller` Gemini fallback |
| `DASHBOARD_TOKEN` | dashboard bearer auth |

## Validation (at boot)

- Fail fast if: a required secret for an **active** account is missing, a required
  instrument is absent from OANDA, `range_end <= range_start`, any ATR/mult ≤ 0,
  `daily_loss_limit <= 0`, or the timezone is unknown.
- When `fx_trld` is active, `fx_session` / `strategies.fx_trld` / `fx_risk` validate too.
- Log the **effective config** (secrets redacted) once at startup for auditability.

## Acceptance criteria

- Same binary runs dev/demo/prod by `--config`; no code differences (`architecture.md` §8).
- Changing a threshold (e.g. `sl_atr_mult`) requires only a config edit + restart.
- No secret value is ever written to logs or the config files.
- FX enablement is `accounts[].active` + `OANDA_ACCOUNT_ID_FX` only.

## Out of scope

- Per-component behavior (see each component spec).
