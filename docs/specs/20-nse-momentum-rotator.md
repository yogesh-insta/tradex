# Spec: NSE Momentum Rotator (monthly, advisory-only)

Status: DRAFT — pending review.

## Purpose

A **monthly, advisory-only** equity rotation lane for NSE (India), separate from
the OANDA hot path. Once a month it ranks a fixed universe of NSE large caps by
12-month momentum, applies a market-regime filter, diffs the target portfolio
against user-maintained holdings, and delivers exact BUY/SELL orders to Telegram.
**It never places orders.** The user executes manually in Zerodha Kite.

Strategy was validated offline (2010–2026 daily data, see `kite/backtest/`):
12-month lookback, top 5 equal-weight, regime filter → ~23% CAGR gross,
max DD −18%, vs Nifty 50 buy-and-hold 9.7%. Expectation setting: with
survivorship bias, costs, and taxes, realistic outcome is low-to-mid-teens
CAGR with materially smaller drawdowns than buy-and-hold; edge in the most
recent 8 years was thin (17.6% vs 17.3% benchmark) — the drawdown reduction
is the main prize.

## Non-goals (v1)

- No order placement, no Kite/broker connectivity, no credentials for money movement.
- No intraday data, no websocket, no always-on process.
- No automatic holdings reconciliation (user maintains the portfolio file).
- No dashboard panel (possible later; the GCS output is designed for it).

## Placement in the repo

Follows the calendar-poller pattern (spec 10): stateless scheduled Cloud Run
job, durable state in GCS, Telegram delivery, trader untouched.

```
cmd/nserotator/            entrypoint (one-shot run, then exit)
internal/nserotator/
  universe.go              universe load + validation (from config)
  yahoo.go                 Yahoo Finance chart-API client (daily closes)
  signal.go                momentum ranking + regime filter (pure functions)
  diff.go                  target vs holdings -> orders
  telegram.go              message formatting + send (reuse calendar telegram client)
  gcs.go                   portfolio.json read, recommendation.json write
config/config.nserotator.cloudrun.yaml
deploy/docker/Dockerfile.nserotator
deploy/scripts/deploy-nserotator.sh
```

Reuses: `internal/config` (YAML + `${ENV}` secrets + fail-fast validation),
`internal/calendar/telegram.go` client (extract to `internal/notify` if cleaner),
GCS helpers, slog JSON logging.

## Scheduling

- **Cloud Scheduler** fires the Cloud Run job **every trading-candidate day at
  18:00 IST (12:30 UTC)**, cron `30 12 * * 1-5`.
- First step of the run: **last-trading-day gate** — compute whether *today* is
  the last NSE trading day of the calendar month using the checked-in NSE
  holiday file (`config/holidays-nse.yaml`, same pattern as `config/holidays.yaml`).
  If not, log and exit 0. This avoids fragile "last weekday" cron math and
  handles exchange holidays correctly.
- Manual run: `gcloud run jobs execute nserotator --args=--force` (`--force`
  skips the date gate; for testing and for re-sends).

## Algorithm (normative)

Parameters (config, defaults shown): `lookback_months: 12`, `top_k: 5`,
`regime_ema_days: 200`, `cost_note_pct: 0.12`.

1. **Fetch** ~5 years of daily closes for: Nifty 50 index (`^NSEI`) and every
   universe symbol (`<SYMBOL>.NS`) from the Yahoo Finance chart API
   (`https://query1.finance.yahoo.com/v8/finance/chart/{symbol}?range=5y&interval=1d`).
   No API key. Retries with backoff; ≥3 consecutive failures for a symbol →
   symbol is excluded from ranking and flagged in the Telegram message.
2. **Regime**: invested iff last close of `^NSEI` > EMA(200) of its daily closes.
   If not invested → target portfolio = 100% cash (SELL everything held).
3. **Rank**: for each universe symbol with ≥ `lookback_months` of history,
   momentum = `close_today / close_12m_ago − 1`. Sort descending. Target =
   top `top_k`, equal weight of `portfolio.total_capital_inr`.
4. **Diff** against holdings from `portfolio.json`:
   - SELL: held symbol not in target (or regime = cash). Quantity: full holding.
   - BUY: target symbol not held. Quantity: `floor(capital / top_k / last_close)`.
   - HOLD: in both; no rebalancing of existing position sizes in v1 (reduces
     churn and tax events; full-weight rebalance only when a slot turns over).
5. **Deliver + persist** (see below). The job is stateless; everything it needs
   is fetched or read from GCS at run time.

All signal math must be pure functions with table-driven unit tests mirroring
`kite/backtest/momentum.py` outputs on fixture data.

## Inputs

- Yahoo Finance chart API (no auth) — daily closes.
- `gs://<bucket>/nserotator/portfolio.json` — **user-maintained**, hand-edited
  after executing (or skipping) orders. Schema:

```json
{
  "as_of": "2026-07-31",
  "total_capital_inr": 500000,
  "holdings": [
    {"symbol": "TITAN", "qty": 120, "avg_price": 3450.00},
    {"symbol": "BAJFINANCE", "qty": 55, "avg_price": 7200.00}
  ],
  "notes": "free text"
}
```

- `config/holidays-nse.yaml` — checked-in NSE holiday list, validated yearly
  against the official NSE calendar (same ritual as Eurex/Euronext file).
- Universe: ~60 NSE large-cap symbols, checked into config (initial list =
  the backtest universe). Reviewed manually once a year; changes are config
  PRs, not code.

## Outputs

1. **Telegram message** (existing bot, dedicated chat or the calendar chat —
   config key `telegram.chat_id`):

```
NSE ROTATOR — 2026-07-31 (regime: INVESTED, Nifty 24,812 > EMA200 23,904)

ORDERS (execute at next open):
  SELL INFY        qty 80    (~₹1,29,000)
  BUY  BAJFINANCE  qty 13    (~₹99,000)

HOLD: TITAN, ICICIBANK, MARUTI, SUNPHARMA

Top 10 momentum: BAJFINANCE 41% | TITAN 38% | ... 
Excluded (data failure): none
Reminder: update portfolio.json after executing.
```

2. **`gs://<bucket>/nserotator/recommendation-YYYY-MM.json`** — durable record
   of every run (inputs digest, ranking, regime, orders). Enables later
   dashboard panel and a paper audit trail of advice vs. execution.
3. Structured slog JSON to Cloud Logging.

## Failure modes (fail-safe = say nothing wrong, loudly)

| Failure | Behavior |
| --- | --- |
| Yahoo unreachable / >20% of universe failed | No orders. Telegram: "RUN FAILED — data" with detail. Exit 1 (Cloud Run alerting). |
| `^NSEI` fetch failed | No orders (regime unknown). Telegram failure message. Exit 1. |
| `portfolio.json` missing/unparseable | No orders. Telegram: "RUN FAILED — portfolio state". Exit 1. |
| `portfolio.json` `as_of` older than 45 days | Proceed but prefix message with a STALE-PORTFOLIO warning (user likely forgot to update). |
| Telegram send fails | Retry ×3; recommendation JSON is still written to GCS; exit 1 so the failure is visible in Cloud Run. |
| Ranking ties / equal momentum | Deterministic tie-break: alphabetical symbol order. |

Never write a recommendation file and exit 0 unless the Telegram send succeeded
or the file contains the full message content for manual retrieval.

## Config (config.nserotator.cloudrun.yaml)

```yaml
nserotator:
  lookback_months: 12
  top_k: 5
  regime_ema_days: 200
  universe: [RELIANCE, TCS, INFY, ...]        # ~60 symbols, .NS implied
  gcs_bucket: ${NSEROTATOR_BUCKET}
  telegram:
    bot_token: ${TELEGRAM_BOT_TOKEN}
    chat_id: ${TELEGRAM_NSE_CHAT_ID}
  holidays_file: config/holidays-nse.yaml
  yahoo_timeout_s: 30
```

Secrets only as `${ENV}` references; fail-fast validation at boot (repo standard).

## Deploy

- `Dockerfile.nserotator` (distroless, same base pattern as calendar poller).
- Cloud Run **Job** (not service — no HTTP surface), region `asia-south1`
  (Mumbai) for locality; any region works since it's advisory.
- Cloud Scheduler `30 12 * * 1-5` UTC → executes the job.
- Service account: `roles/storage.objectAdmin` on the `nserotator/` prefix only.
  No OANDA, no Kite, no other permissions.
- CI: standard vet/lint/test in existing `ci.yml` (new packages picked up
  automatically); deploy via `deploy/scripts/deploy-nserotator.sh`.

## Ops ritual (user)

1. Last trading day ~18:05 IST: receive Telegram message.
2. Next morning: place the listed orders manually in Kite at/near open.
3. After fills: edit `gs://…/portfolio.json` (qty, avg_price, `as_of`).
   A helper `gsutil cp` one-liner is documented in `data/README-nserotator.md`.
4. Yearly (December): refresh universe list + `holidays-nse.yaml`.

## Open questions for review

1. Universe maintenance: keep the static 60-name list (simplest, drift risk) or
   fetch current Nifty-100 constituents at run time (accurate, new dependency)?
2. Should HOLD positions be rebalanced back to equal weight when they drift
   >X% (more turnover/tax vs. tighter tracking of the backtest)?
3. Dedicated Telegram chat vs. reusing the calendar review chat?
4. Is ₹-denominated `total_capital_inr` manually bumped by you as profits
   accrue, or should the job mark-to-market holdings + cash automatically?
