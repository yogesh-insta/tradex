# Spec: NSE Momentum Rotator (monthly, advisory-only)

Status: DRAFT — pending review.

## Purpose

A **monthly, advisory-only** equity rotation lane for NSE (India), separate from
the OANDA hot path. Once a month it ranks a fixed universe of NSE large caps by
6-month trailing momentum, diffs a target portfolio against user-maintained
holdings, and delivers exact BUY/SELL orders to Telegram.
**It never places orders.** The user executes manually in Zerodha Kite.

## THE STRATEGY IN FULL

> Read this section before changing anything in `internal/nserotator/`. Every
> rule below is implemented by one named function; the parameter names are the
> literal YAML keys in `config/config.nserotator.*.yaml`.

**Universe.** Nifty 200, checked in at `config/universe-nse200.yaml`. Names on
the `excluded_symbols` policy blocklist are struck from every list before
ranking. Names with stale data, a >50% single-day up-move (bad feed), or less
history than the lookback are dropped.

**Rank twice.** Every month-end, score each surviving symbol on two trailing
returns computed from month-end closes:

| list | key | window | job |
| --- | --- | --- | --- |
| fast | `lookback_months: 6` | 6 months | decides what to **buy** |
| slow | `exit_lookback_months: 12` | 12 months | decides what to **keep** |

**Entry.** Buy the top `top_k: 10` of the fast list. Equal weight; quantity is
`floor(total_capital_inr / top_k / last_close)`.

**Exit (hysteresis — the core idea).** A holding is sold **only when it sits
outside the top `exit_rank_n: 30` on BOTH lists.** A name that slips to 6m rank
14 but is still 12m rank 9 keeps its slot instead of being sold and bought back
weeks later. Survivors keep their slots; leftover slots are filled from the fast
list in rank order. Setting `exit_rank_n == top_k` restores plain top-K
rotation. Implemented by `BuildTarget` (`signal.go`).

> This rule is why the portfolio churns ~9%/month instead of ~40%. It is
> roughly return-neutral on its own; what it buys is lower turnover and
> shallower drawdowns.

**Regime filter — currently OFF.** See the dedicated section below. This is the
single largest lever in the strategy and the one most likely to be revisited.

**No rebalancing of existing positions.** Weights drift; only a slot turnover
triggers a trade. Reduces churn and taxable events.

### Regime filter

`regime_filter` (bool, **default true**, shipped as **false**) gates the entire
book to cash while `^NSEI` closes below its `regime_ema_days: 200` EMA. When
off, the EMA is still fetched and reported — it just no longer drives the
target. Implemented by `ShouldHoldEquity` (`signal.go`).

Backtest, Nifty 200, 2011-01 → 2026-06 (15.4y), 0.12% round-trip costs, at the
shipped `top_k: 10` / `exit_rank_n: 30` / blocklist applied:

| | CAGR | max DD | Sharpe | turnover/mo | growth |
| --- | --- | --- | --- | --- | --- |
| `regime_filter: true` | 36.1% | −23.2% | 1.61 | 19.2% | 119x |
| **`regime_filter: false` (shipped)** | **49.4%** | **−26.8%** | **1.69** | **8.5%** | **503x** |

> Re-measured 2026-08-02. The previous table claimed −13.3% max DD with the
> filter ON, which was wrong by ~10 points and made the filter look like
> drawdown insurance it never provided. The shipped choice is *more* clearly
> right on the corrected numbers, not less.

The filter costs ~13 points of CAGR. It also *halves the benefit of the exit
hysteresis*: a forced liquidation overrides every hold decision, so turnover
more than doubles (8.5% → 19.2%). Roughly 4x terminal wealth is being traded
for a max drawdown of −26.8% instead of −23.2% — only 3.6 points of drawdown
relief for 13 points of CAGR. A 15% per-name stop is a far better exchange rate
(−4 CAGR for −5.2 DD) if drawdown is the real concern; see § Early exits.

**Flip it back to `true` if** the drawdown is not survivable in practice — a
strategy abandoned at the bottom returns 0%, which beats neither variant. This
is a risk-appetite decision, not an optimisation; do not "tune" it on the same
16.6 years that chose it.

### Early exits

There is no mid-month exit. A name that breaks down on day 2 is carried to
month-end, and even then leaves only if it is outside the top `exit_rank_n` on
both lists. This is deliberate — every overlay tested makes the strategy worse
on a risk-adjusted basis. Measured 2026-08-02 on daily closes, rotation
unchanged, the exiting slot sitting in cash until the next rebalance:

| overlay | CAGR | max DD | Sharpe | turnover/mo | fires on |
| --- | --- | --- | --- | --- | --- |
| **none (shipped)** | **46.0%** | −26.5% | **1.64** | 8.5% | — |
| fixed stop 15% from entry | 42.0% | **−21.3%** | 1.58 | 24.0% | 9.4% of slots |
| fixed stop 20% | 42.7% | −26.3% | 1.58 | 15.0% | 4.0% |
| trailing stop 25% | 43.0% | −28.4% | 1.60 | 12.8% | 2.6% |
| trailing stop 15% | 36.8% | −22.9% | 1.43 | 34.8% | 15.0% |
| trailing stop 10% | 25.6% | −24.8% | 1.13 | 77.4% | 37.5% |
| exit below own EMA100 | 34.8% | −20.8% | 1.47 | 49.2% | 23.8% |
| exit below own EMA200 | 38.7% | −29.5% | 1.49 | 23.4% | 9.5% |

(Baseline differs from the table above because returns here are walked daily
rather than resampled to month-end; compare only within this table.)

**No overlay improves Sharpe.** Momentum's edge is tolerating drawdown in
individual names, so stops mostly sell winners during ordinary noise — a 10%
trailing stop fires on 37.5% of slots and churns 77% of the book per month.
Tightening the rank rule instead does not work either: `exit_rank_n: 10` gives
44.8% CAGR at a *worse* −30.4% drawdown.

The one defensible variant is a **fixed 15% stop below entry**: −4 points of
CAGR for 5.2 points less drawdown, the best exchange rate available and ~2.5x
more efficient than the regime filter. It needs no code — place GTT stops at
the broker; the rotator stays monthly. Note the sim exits at the close of the
day the level breaks, while real stops fill intraday and gap through, and that
tripling turnover has a short-term capital-gains cost no backtest here models.

### Honest expectations

The backtest has survivorship bias (today's Nifty 200 constituents, back-applied),
models fills at month-end closes, ignores taxes and lot rounding, and measures
drawdown on **month-end equity only** — intra-month pain is invisible. Live
runs now execute mid-session (see Scheduling), so signals come off a partial
candle. Treat the headline CAGR as an upper bound: realistic outcome is a large
haircut to these numbers, and the turnover reduction is the most transferable
result. The regime filter's cost is concentrated in a handful of whipsaw
re-entries, so its true expense has wide error bars.

### Where the numbers come from

`kite/backtest/momentum_dual.py` (`run_dual` = this strategy, `run_plain` =
the pre-2026 plain-rotation baseline). `internal/nserotator/signal.go` mirrors
its math function-for-function; `signal_test.go` cross-checks fixtures against
it. **If you change signal math, change both and re-run the backtest.**

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

- **Cloud Scheduler** fires the Cloud Run job **weekdays at 15:30
  Australia/Sydney**, cron `30 15 * * 1-5` in `Australia/Sydney`.
  > That resolves to 10:00 IST (AEDT) / 11:00 IST (AEST) — **during** the NSE
  > session (09:15–15:30 IST), not after the close. Momentum, the EMA200
  > reading, and `floor(capital/top_k/price)` quantities are therefore computed
  > from that day's partial candle. Accepted deliberately for delivery timing;
  > the backtest assumes month-end closes and does not model this.
- First step of the run: **last-trading-day gate** — compute whether *today* is
  the last NSE trading day of the calendar month using the checked-in NSE
  holiday file (`config/holidays-nse.yaml`, same pattern as `config/holidays.yaml`).
  If not, log and exit 0. This avoids fragile "last weekday" cron math and
  handles exchange holidays correctly.
- Manual run: `gcloud run jobs execute nserotator --args=--force` (`--force`
  skips the date gate; for testing and for re-sends).

## Algorithm (normative)

Parameters as shipped: `lookback_months: 6`, `exit_lookback_months: 12`,
`top_k: 10`, `exit_rank_n: 30`, `regime_ema_days: 200`, `regime_filter: false`,
`excluded_symbols: [ADANIENSOL, ADANIENT, ADANIGREEN, ADANIPORTS, ADANIPOWER]`.

> `top_k: 10` (not 5): on the Nifty 200 universe, larger books cut max drawdown
> materially with comparable returns — midcaps need the diversification.
> `exit_rank_n: 3 × top_k` won on CAGR, drawdown, Sharpe and turnover at every
> book size tested.

1. **Fetch** ~5 years of daily closes for: Nifty 50 index (`^NSEI`) and every
   universe symbol (`<SYMBOL>.NS`) from the Yahoo Finance chart API
   (`https://query1.finance.yahoo.com/v8/finance/chart/{symbol}?range=5y&interval=1d`).
   No API key. Retries with backoff; ≥3 consecutive failures for a symbol →
   symbol is excluded from ranking and flagged in the Telegram message.
2. **Regime**: invested iff last close of `^NSEI` > EMA(`regime_ema_days`) of
   its daily closes. Gates the book to 100% cash **only when `regime_filter` is
   true**; otherwise recorded and reported but not acted on (`ShouldHoldEquity`).
3. **Rank twice**: for each surviving symbol, momentum from month-end closes over
   `lookback_months` (fast) and `exit_lookback_months` (slow). Sort each descending.
   `excluded_symbols` are struck from **both** lists, so the blocklist cannot be
   defeated by the exit rule.
4. **Target** (`BuildTarget`): keep every holding inside the top `exit_rank_n`
   on either list; fill remaining slots from the top `top_k` of the fast list.
   Equal weight of `portfolio.total_capital_inr`; never more than `top_k` names.
5. **Diff** against holdings from `portfolio.json`:
   - SELL: held symbol not in target (or regime = cash). Quantity: full holding.
   - BUY: target symbol not held. Quantity: `floor(capital / top_k / last_close)`.
   - HOLD: in both; no rebalancing of existing position sizes in v1 (reduces
     churn and tax events; full-weight rebalance only when a slot turns over).
6. **Deliver + persist** (see below). The job is stateless; everything it needs
   is fetched or read from GCS at run time.

All signal math must be pure functions with table-driven unit tests mirroring
`kite/backtest/momentum_dual.py` outputs on fixture data.

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
- Cloud Scheduler `30 15 * * 1-5` in `Australia/Sydney` → executes the job.
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
its equal-weight benchmark by ~20%/yr in each half; a larger book was chosen
over top-5 for drawdown control (−25% vs −32%). Superseded in 2026-08 by
`top_k: 10` + exit hysteresis + `regime_filter: false` — see § THE STRATEGY IN
FULL for the current parameters. **Absolute CAGR figures (~40%) are inflated by
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
