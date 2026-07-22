# Spec: NSE Momentum Rotator (monthly, advisory-only)

Status: DRAFT — pending review.

## Purpose

A **monthly, advisory-only** equity rotation lane for NSE (India), separate from
the OANDA hot path. Once a month it ranks a fixed universe of NSE large caps by
6-month trailing momentum, applies a market-regime filter, diffs the target portfolio
against user-maintained holdings, and delivers exact BUY/SELL orders to Telegram.
**It never places orders.** The user executes manually in Zerodha Kite.

Strategy was validated offline (2010–2026 daily data, see `kite/backtest/`):
6-month lookback (default; 49.89% CAGR vs 47.66% for 12-month in backtests),
top 8 equal-weight, regime filter. Earlier 12-month validation showed ~23% CAGR
gross, max DD −18%, vs Nifty 50 buy-and-hold 9.7%. Expectation setting: with
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

Parameters (config, defaults shown): `lookback_months: 6`, `top_k: 8`,
`regime_ema_days: 200`, `cost_note_pct: 0.12`.

> `top_k: 8` (not 5): on the Nifty 200 universe the 200-run validation showed
> top-8 cuts max drawdown to ~-25% (vs ~-32% for top-5) with comparable returns
> — midcaps need the extra diversification.

1. **Fetch** ~5 years of daily closes for: Nifty 50 index (`^NSEI`) and every
   universe symbol (`<SYMBOL>.NS`) from the Yahoo Finance chart API
   (`https://query1.finance.yahoo.com/v8/finance/chart/{symbol}?range=5y&interval=1d`).
   No API key. Retries with backoff; ≥3 consecutive failures for a symbol →
   symbol is excluded from ranking and flagged in the Telegram message.
2. **Regime**: invested iff last close of `^NSEI` > EMA(200) of its daily closes.
   If not invested → target portfolio = 100% cash (SELL everything held).
3. **Rank**: for each universe symbol with ≥ `lookback_months` of history,
   momentum = `close_today / close_{lookback_months}_ago − 1`. Sort descending. Target =
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
- Universe: **Nifty 200**, checked in at `config/universe-nse200.yaml`
  (source: official NSE constituent CSV, as-of date in the file header).
  Refreshed yearly via config PR. Recently listed names (e.g. GROWW, SWIGGY)
  participate only once they have `lookback_months` of history.

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
  lookback_months: 6
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

## Resolved decisions (review 2026-07-20)

1. **Universe: Nifty 200, static checked-in list** (`config/universe-nse200.yaml`),
   refreshed yearly. Runtime constituent fetching rejected (NSE scraping fragility).
2. **No rebalancing of HOLD positions** — fewer orders, fewer taxable events;
   full weight applied only when a slot turns over.
3. **Telegram: reuse the calendar review chat** (`TELEGRAM_CHAT_ID`); no new env var.
4. **Capital is manual** — user maintains `total_capital_inr` in `portfolio.json`;
   no mark-to-market logic in the job.

## Pre-implementation gate — PASSED (2026-07-20)

Re-run on the Nifty 200 universe (2010–2026, both halves tested): strategy beat
its equal-weight benchmark by ~20%/yr in each half; top-8 chosen over top-5 for
drawdown control (−25% vs −32%). **Absolute CAGR figures (~40%) are inflated by
severe survivorship bias** (today's constituents include stocks that grew into
the index); written expectation remains **15–20% CAGR** with DDs in the −20…−30%
range. The bias affects the backtest only — trading today's list forward has no
lookahead.

## Alerts & monitoring (normative)

1. **Failure alerts** — every failure row in the table above sends a Telegram
   "RUN FAILED — <reason>" message AND exits non-zero. A Cloud Monitoring alert
   policy on Cloud Run job execution failures (email to owner) is part of the
   deploy, covering the case where even Telegram is down.
2. **Dead-man check** — a successful run always sends a Telegram message, even
   when there are zero orders ("no changes this month"). Silence on the last
   trading day therefore always means something is broken. Additionally, the
   run writes `nserotator/heartbeat.json` (`last_success` timestamp) to GCS;
   a Cloud Monitoring check alerts if no success in 35 days.
3. **Universe drift detection** — each run best-effort fetches the official NSE
   constituent CSV (`ind_nifty200list.csv`) and diffs it against
   `config/universe-nse200.yaml`. On drift: prepend a Telegram warning listing
   added/removed symbols ("universe file needs update — config PR"). Fetch
   failure is logged but never blocks the run (checked-in list remains truth).
4. **Per-symbol data staleness** — any universe symbol whose latest close is
   older than 7 trading days is excluded from ranking and listed in the message
   (catches renames/delistings like TATAMOTORS → TMCV/TMPV). If a **held**
   symbol goes stale, that's a prominent warning — likely a corporate action
   needing manual attention.
5. **Bad-data guard** — a symbol with a >50% single-day move is excluded from
   ranking and flagged (split/bonus mis-adjustment protection; Yahoo usually
   adjusts, but renames and ISIN changes have burned this exact repo before).
6. **Holiday-file staleness** — every December run warns if `holidays-nse.yaml`
   lacks entries for the coming year (same yearly ritual as the EU calendar).
7. **Portfolio staleness** — `as_of` older than 45 days → STALE-PORTFOLIO
   warning prefix (defined in failure modes above).
