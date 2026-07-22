# Spec: ASX ETF Momentum Monitor (monthly, advisory-only)

Status: ACCEPTED — implemented, reviewed and merged via PR #8.
The *mechanism* is reviewed and tested; the *parameters* are not validated
(see §Open questions 1). Treat output as a shortlist, not a system.

## Purpose

A **monthly, advisory-only** ETF monitor for the ASX, separate from the OANDA
hot path and independent of the NSE rotator (spec 20). Once a month it ranks
the Australian ETF universe by recency-tilted momentum, gates on trend, and
delivers to Telegram:

1. **Exit alerts** for funds the user holds that have dropped below their
   200-day trend — the disciplined sell rule, and the main reason the job exists.
2. A **top 10** of funds currently trending up, with risk (vol, max drawdown)
   and an inverse-volatility sizing suggestion.

**It never places orders.** No broker connectivity, no credentials for money
movement. The user executes manually.

The logic is a faithful port of `kite/betashares/screen.py`, which was developed
and validated against live data first. The Go signal math is pinned to that
implementation by table-driven tests (same discipline spec 20 applies to
`kite/backtest/momentum.py`).

## Non-goals (v1)

- No order placement, no broker connectivity.
- No automatic holdings reconciliation — the user maintains `holdings.json`.
- No intraday data, no always-on process.
- No auto-adding of newly listed funds to the universe (drift is reported, never applied).
- No backtest. The trend gate and recency tilt are judgement calls documented
  here, not optimised parameters. Treat the output as a shortlist, not a system.

## Placement in the repo

Follows the nserotator pattern (spec 20): stateless scheduled Cloud Run job,
durable state in GCS, Telegram delivery, trader untouched.

```
cmd/etfmonitor/main.go        one-shot + Cloud Run HTTP /run, --force
internal/etfmonitor/
  config.go                   config + grouped universe load/validate
  yahoo.go                    daily closes (.AX), 3y range, retry/backoff
  signal.go                   PURE math: returns, SMA trend, vol, maxDD,
                              split guard, recency score, inverse-vol weights
  holdings.go                 GCS/local StateStore, holdings + report + heartbeat
  drift.go                    ASX/Betashares list fetch + diff (best-effort)
  report.go                   Telegram text
  run.go                      orchestration
config/config.etfmonitor.cloudrun.yaml, .dev.yaml
config/universe-asx-etf.yaml
deploy/docker/Dockerfile.etfmonitor
deploy/scripts/deploy-etfmonitor.sh
```

Reuses `internal/calendar` (TelegramClient, `ParseGSURI`) and `internal/logging`.

## Universe (normative)

`config/universe-asx-etf.yaml`, ported from `screen.py`'s FUNDS dict, in five groups:

| Group | Ranked? | Rationale |
| --- | --- | --- |
| `standard` | **yes — the only group eligible for the top 10** | Equity, thematic, commodity, smart-beta, global, diversified, equity-income. |
| `geared` | separate section | 2-3x leveraged long. In any rising market these top a momentum table on leverage alone; that is not a signal. Labelled "GEARED 2-3x — high risk, size tiny". |
| `inverse` | **never ranked** | Bear/short funds. Ranking them among long momentum is meaningless. |
| `fx` | separate section | Currency bets, not growth exposure. |
| `excluded` | never fetched | Cash and bonds. Momentum on a cash ETF is noise. Carried in the file only so the drift check does not report them as "new" every month. |

Non-Betashares issuers are tagged (`issuer:`) and surfaced as "buyable via
broker" — e.g. **SEMI is a Global X fund**, not Betashares, despite appearing in
the original screener.

Refreshed via config PR. The drift check (below) warns when it goes stale.

## Algorithm (normative)

Config defaults: `momentum_lookbacks_td: [63, 126, 252]`,
`recency_weights: [0.5, 0.3, 0.2]`, `trend_sma_days: 200`, `top_n: 10`.

1. **Fetch** ~3 years of daily closes per fund from the Yahoo chart API
   (`<TICKER>.AX`), preferring adjusted closes. Bounded concurrency, retry with
   backoff. Held funds outside the universe are fetched too.

2. **Staleness guard (mandatory).** Reject any fund whose latest close is more
   than 10 days old. A delisted fund keeps returning years of history, so its
   trailing returns still compute and it will rank on prices that no longer
   exist. Observed live: **IPAY last traded 2025-02-14 and ranked #7** on
   17-month-old data before this guard existed. Staleness also counts toward the
   >20% data-outage threshold — one stale fund is a delisting, but a stale
   universe means the feed is serving dead data.

3. **Bad-data guard (mandatory).** Reject any series containing a single-session
   move greater than **50%**. No ASX ETF — not even a 3x geared one — moves that
   far in a session; when it appears it is always an unadjusted share
   consolidation. Observed live: **BBOZ 2024-05-30 +10053%**, **BBUS 2025-12-01
   +911%**, the latter fabricating a **+611% 12-month return and 526% annualised
   vol** and ranking **#1 of 108** in the Python screener before the guard existed.

   Rejection (not repair) is deliberate: a break corrupts the trailing returns,
   the volatility *and* the max drawdown, and keeps corrupting vol/maxDD for
   years after it has fallen out of every return window.

4. **Metrics** per fund, matching `screen.py` exactly:
   - `ret(N) = close[last] / close[last-N] - 1` for each lookback
   - `SMA(200)` — requires a full 200 sessions
   - `vol = stdev(daily returns) * sqrt(252)`, **sample** stdev (ddof=1, pandas' default)
   - `maxDD = min(close / cummax(close) - 1)`

5. **Trend gate (hard filter).** A standard fund is eligible only if
   `close > SMA(trend_sma_days)`. Funds below trend are **disqualified** and
   listed separately under "Below trend — not eligible". Never surface a fund
   the exit rule would immediately sell. A fund with fewer than
   `trend_sma_days` sessions has no trend read and is not eligible.

6. **Recency-tilted score** for eligible funds:

   ```
   score = 0.5*ret_3m + 0.3*ret_6m + 0.2*ret_12m
   ```

   Weights favour recent momentum so decelerators (big 12m, weak or negative 3m)
   rank below fresh accelerators. Funds with less history use only the lookbacks
   they have and **renormalise the weights to sum to 1** (3m-only → 1.0;
   3m+6m → 0.625/0.375). A valid 3m return is **required** — without recent
   momentum there is nothing to tilt toward.

   > Note: for *standard* funds the 3m-only branch is unreachable — it needs
   > 64-126 sessions, and the trend gate rejects anything under 200. Only the
   > 3m+6m case (200-252 sessions) fires in practice. Young standard funds are
   > not gently down-weighted; they are excluded outright and surfaced on the
   > **Watchlist** instead.

7. **Rank** descending, alphabetical tie-break. Take `top_n`.

8. **Per-fund output**: ticker, name, 3m/6m/12m, trend (UP by construction),
   vol, maxDD, classification, suggested weight.
   - *classification*: `accelerating` when the 3m return annualised exceeds the
     12m return, else `trending`. When 12m history is absent the longest
     available window substitutes; with only a 3m read the fund is `new`.
   - *suggested weight*: inverse volatility across the top 10,
     `w_i = (1/vol_i) / Σ(1/vol)`, labelled **"satellite sizing suggestion,
     5-10% of portfolio, not core"**.

9. **Geared + FX** ranked in their own section. They skip the trend gate but
   still require the SAME minimum history, so groups stay consistent. **Inverse** funds are tracked and
   listed but never ranked.

## On the Yahoo client being thinner than nserotator's

`internal/nserotator/yahoo.go` carries cookie-jar and crumb-bootstrap machinery
that this package omits. That is not lost resilience — it is machinery for an
endpoint this lane never calls.

nserotator's `FetchDaily` hits the same `/v8/finance/chart/` endpoint with the
same plain client and a User-Agent header, no crumb involved. The crumb and
session cookies exist solely for `/v10/finance/quoteSummary`, which supplies
**market cap** for the NSE rotator's Telegram message. This lane reports no
market cap, so it makes no such call.

If Yahoo tightened access to the chart API, both lanes would break identically.
Do NOT copy the crumb code here "for resilience" — it would add a cookie jar, a
bootstrap round-trip and a shared-mutex cache that no request path uses.

Extracting a shared client is deliberately deferred: the two lanes use different
endpoints, and spec 20 states nserotator is "deliberately self-contained".
Coupling two advisory lanes to save a ~150-line HTTP wrapper is a poor trade
until a third consumer appears.

## Watchlist (funds too new to rank)

A fund needs a full `trend_sma_days` of history before it can rank — roughly
**200 sessions ≈ 9.5 months from listing**, a hard cliff, not a taper. That is
deliberate: the 200-day line IS the exit rule, so a fund with no trend read has
no sell signal, and recommending an entry without an exit would be incoherent.

But excluded must not mean invisible. Standard funds that clear the score but
lack a trend read are listed under **"Watchlist — too new to rank"** with their
3m return and session progress (`120/200`), closest first.

The "months to go" figure is arithmetic on the session shortfall (~21 sessions
per month). **No listing date is tracked anywhere** — a data gap would make the
estimate optimistic. It is a rough guide, not an eligibility date.

## Held-fund exit alerts (key feature)

`gs://<bucket>/etfmonitor/holdings.json` — user-maintained, hand-edited, never
read from a broker:

```json
{
  "as_of": "2026-07-31",
  "holdings": [
    {"ticker": "HACK", "qty": 400, "avg_price": 11.20},
    {"ticker": "NDQ", "qty": 150, "avg_price": 42.80}
  ],
  "notes": "free text"
}
```

`qty` appears in the exit alert (`EXIT HACK (400 units) — …`) so the message
says how much to sell, not merely what.

`avg_price` is recorded for the user's own bookkeeping and is **deliberately
unused by any logic**. The exit rule is trend-based; letting entry price
influence a sell decision is precisely the anchoring bias the 200-day rule
exists to remove.

- Any held fund **below its 200-day** → `EXIT: <ticker> below 200-day —
  momentum broken`, at the **top** of the message.
- Held fund with stale (>10 days) or missing data → loud warning (possible
  rename/corporate action).
- Held fund whose series contains a split break → warning, not a false exit:
  the trend cannot be judged from corrupted data.
- A missing `holdings.json` is **not** an error — a user with no positions still
  gets the monthly top 10.
- `as_of` older than 60 days → STALE-HOLDINGS warning.

## Universe drift check

Best-effort each run: scrape the Betashares fund index and diff it against the
checked-in universe.

**Source.** `https://www.betashares.com.au/fund/`. The fund table is rendered
server-side with structured attributes:

```
data-name="Global Cybersecurity ETF" data-shortcode="HACK"
```

It is scraped rather than pulled from an API because **the ASX's own product
lists are gone**: the old `etpFile.csv` 302s to a 404 page, and the
markitdigital company directory carries operating companies only (1835 rows,
none of the ~105 ETF tickers). The endpoint also returns **403 to a bare Go
User-Agent**, so browser-like headers are load-bearing.

- Live tickers absent from our file → `NEW FUNDS (categorize & add): …`.
  **Never auto-added and never auto-ranked.** A new fund cannot rank for ~9.5
  months anyway, so this is housekeeping, not a blocker. Grouping is a human
  decision: a geared fund mis-filed as `standard` would put a 2-3x leveraged
  product into the top 10.
- Our tickers absent from the live list → `Possibly delisted/renamed: …`
  (this is how **ECAR → DRIV** would have been caught).
- Funds marked `drift_exempt: true` are skipped in the "removed" direction —
  a different issuer (SEMI/Global X) or an unlisted vehicle (BPCF) would
  otherwise raise the same false alarm every month, which is how a drift alert
  gets trained into background noise.
- **No drift is reported explicitly** ("Universe verified against N live
  funds — no drift"), because silence is indistinguishable from a check that
  never ran.

**Failure is non-fatal but NEVER silent.** The run continues, and the message
carries `DRIFT CHECK DID NOT RUN: <error>`. A quietly-dead drift check is worse
than none: it leaves the operator believing the universe is being watched while
nothing is. The scrape depends on a third-party page layout and bot protection,
so it *will* break eventually.

## Outputs

1. **Telegram**, plain text (no parse mode — fund names contain `&` and `+`, and
   no markdown metacharacter appears anywhere in the message). Order: exit
   alerts → warnings → top 10 → below-trend → geared/FX → inverse → rejected →
   drift notes → advisory footer.
2. `gs://<bucket>/etfmonitor/report-YYYY-MM.json` — durable record including the
   full message text for manual retrieval.
3. `gs://<bucket>/etfmonitor/heartbeat.json` — dead-man check.
4. Structured slog JSON to Cloud Logging.

## Failure modes (fail-safe = say nothing wrong, loudly)

| Failure | Behavior |
| --- | --- |
| >20% of the **standard** universe unusable (unfetchable or stale) | No recommendations. Telegram "RUN FAILED — data". Exit 1. |
| `holdings.json` unparseable | No recommendations. Telegram "RUN FAILED — holdings state". Exit 1. |
| `holdings.json` missing | **Not an error.** Report proceeds with no exit alerts. |
| `holdings.json` `as_of` older than 60 days | Proceed with a STALE-HOLDINGS warning. |
| Fund's latest close older than 10 days | Rejected as stale/delisted, named in the report. Counts toward the 20% data-outage bar. |
| Series contains an unadjusted split | Fund rejected from ranking, listed with its break date. |
| Fund has <200 sessions | Not eligible for the top 10 (no trend read); surfaced on the Watchlist. |
| No fund passes the trend gate | Valid outcome, reported as such: "nothing is trending, stay in cash". |
| Drift fetch failed | Logged AND reported in the Telegram message. Non-fatal. |
| Telegram send fails | Retry ×3; report JSON is still written to GCS; exit 1. |
| Run context expired | Failure alert is sent on a **detached** context so the alert survives the very deadline that caused the failure. |
| This month already delivered | Exit 0 without sending (`skipped: true`). `--force` overrides. |
| Ranking ties | Deterministic alphabetical tie-break. |

## Scheduling

Monthly on the 1st, Cloud Scheduler `0 7 1 * *` UTC (~evening AEST). No
last-trading-day gate is needed — unlike the NSE rotator this job issues no
orders, so the exact session it runs on does not matter.

**Monthly idempotency gate.** Before doing any work the run reads
`heartbeat.json`; if its `month` equals the current month, the run exits 0
without delivering. Because the NSE rotator's date gate is absent here, nothing
else would stop a Cloud Scheduler retry or a stray manual `/run` from sending a
duplicate report and overwriting `report-YYYY-MM.json`.

The heartbeat is written **only after a successful delivery**, so a retry
following a failure still runs — which is what a retry is for. If the heartbeat
cannot be read at all, the run proceeds: a possible duplicate is preferable to a
silently missed month.

`--force` / `?force=1` bypasses this gate to re-send. That is its only purpose;
it does NOT relax the data-outage bar, the trend gate, or any bad-data guard —
a manual run must not be able to produce a report the scheduled one would refuse
to produce.

## Testing (normative)

Signal math is pinned to `screen.py` by fixtures in
`internal/etfmonitor/testdata/screen_py_fixtures.json`, generated by calling the
Python `metrics()` directly rather than by reimplementing it. Regenerate when
the reference changes:

```bash
cd /path/to/kite/betashares
python3 /path/to/tradex/internal/etfmonitor/testdata/gen_fixtures.py
```

The generator is checked in at `internal/etfmonitor/testdata/gen_fixtures.py`.
Note that `kite/` is **not a git repository** — if it is lost, the checked-in
fixtures are the only surviving record of the reference behaviour, which is
precisely why they live here rather than being computed at test time.

Mandatory cases:

- **Pinned math** — returns, SMA trend, vol (ddof=1) and maxDD match Python to 1e-9.
- **Split guard** — an unadjusted-split series is REJECTED, not ranked #1. The
  test also asserts the corrupted series *would* have out-scored a clean one,
  so the guard's value is demonstrated rather than assumed.
- **Trend gate** — a fund with a large trailing return that sits below its
  200-day does not appear in the top list, and does appear under "below trend".
- **Staleness** — a delisted fund is rejected while live funds still rank; a
  wholly stale universe fails the run loudly.
- **Watchlist** — a 150-session fund lands on the watchlist with its progress
  rather than ranking or vanishing.
- **Drift** — a failed drift check is reported in the message; added/removed are
  diffed correctly and `drift_exempt` funds do not warn.
- **Recency tilt** — a decelerator ranks below a fresh accelerator at equal mean
  return, and below one with a *lower* mean when its 3m is negative.
- **Weight renormalisation** — 3m-only → 1.0; 3m+6m → 0.625/0.375; no 3m → unscored.
- **Inverse-vol weights** — sum to 1, calmer fund weighted higher, zero-vol not infinite.
- **Exit alerts** — held broken fund alerts, held trending fund does not, exits
  precede the top list.
- **Report formatting** — required fields present, no markdown metacharacters.
- **Fetch-failure threshold** — run fails and the failure still reaches Telegram.
- **Monthly idempotency** — a second run in the same month is skipped and sends
  nothing; `--force` re-sends; a retry after a FAILED run is not skipped.

`go build ./...`, `go vet ./...` and `go test ./...` must stay green.

## Open questions / deliberate gaps

1. **The parameters are not backtested.** 0.5/0.3/0.2 and the 200-day gate are
   reasoned defaults ported from a screener, not optimised values. If this lane
   is ever trusted with real sizing, it needs the same offline validation spec 20
   had before implementation.
2. **The trend gate is pro-cyclical.** It will keep you out of bottoms and put
   you in after moves have started. That is the intended trade — drawdown
   control over entry price — but it should be stated, not discovered.
3. **The drift source is a scrape of a third-party page.** It works today
   (verified: 105 funds parsed, zero unexplained diffs against the universe),
   but a redesign or a tightened bot-check will break it. Failure is reported
   rather than swallowed, so you will know.
4. **Concentration is unmanaged.** The top 10 can legitimately be eight flavours
   of US tech. Inverse-vol weighting does not fix correlation. A future version
   should cap exposure per underlying theme.
