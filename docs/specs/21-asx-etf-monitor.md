# Spec: ASX ETF Momentum Monitor (monthly, advisory-only)

Status: DRAFT — pending review.

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

2. **Bad-data guard (mandatory).** Reject any series containing a single-session
   move greater than **50%**. No ASX ETF — not even a 3x geared one — moves that
   far in a session; when it appears it is always an unadjusted share
   consolidation. Observed live: **BBOZ 2024-05-30 +10053%**, **BBUS 2025-12-01
   +911%**, the latter fabricating a **+611% 12-month return and 526% annualised
   vol** and ranking **#1 of 108** in the Python screener before the guard existed.

   Rejection (not repair) is deliberate: a break corrupts the trailing returns,
   the volatility *and* the max drawdown, and keeps corrupting vol/maxDD for
   years after it has fallen out of every return window.

3. **Metrics** per fund, matching `screen.py` exactly:
   - `ret(N) = close[last] / close[last-N] - 1` for each lookback
   - `SMA(200)` — requires a full 200 sessions
   - `vol = stdev(daily returns) * sqrt(252)`, **sample** stdev (ddof=1, pandas' default)
   - `maxDD = min(close / cummax(close) - 1)`

4. **Trend gate (hard filter).** A standard fund is eligible only if
   `close > SMA(trend_sma_days)`. Funds below trend are **disqualified** and
   listed separately under "Below trend — not eligible". Never surface a fund
   the exit rule would immediately sell. A fund with fewer than
   `trend_sma_days` sessions has no trend read and is not eligible.

5. **Recency-tilted score** for eligible funds:

   ```
   score = 0.5*ret_3m + 0.3*ret_6m + 0.2*ret_12m
   ```

   Weights favour recent momentum so decelerators (big 12m, weak or negative 3m)
   rank below fresh accelerators. Funds with less history use only the lookbacks
   they have and **renormalise the weights to sum to 1** (3m-only → 1.0;
   3m+6m → 0.625/0.375). A valid 3m return is **required** — without recent
   momentum there is nothing to tilt toward.

6. **Rank** descending, alphabetical tie-break. Take `top_n`.

7. **Per-fund output**: ticker, name, 3m/6m/12m, trend (UP by construction),
   vol, maxDD, classification, suggested weight.
   - *classification*: `accelerating` when the 3m return annualised exceeds the
     12m return, else `trending`. When 12m history is absent the longest
     available window substitutes; with only a 3m read the fund is `new`.
   - *suggested weight*: inverse volatility across the top 10,
     `w_i = (1/vol_i) / Σ(1/vol)`, labelled **"satellite sizing suggestion,
     5-10% of portfolio, not core"**.

8. **Geared + FX** ranked in their own section. **Inverse** funds are tracked and
   listed but never ranked.

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

Best-effort each run: fetch the current ASX ETP list (primary: the ASX products
CSV; fallback: `betashares.com.au/fund/`) and diff against the checked-in universe.

- Tickers live but not in our file → `NEW FUNDS (categorize & add): …`.
  **Never auto-added and never auto-ranked** — a new fund has no 200-day history
  and cannot rank for ~a year anyway, so this is housekeeping, not a blocker.
- Tickers in our file but not live → `Possibly delisted/renamed: …`
  (this is how **ECAR → DRIV** would have been caught).

Fetch failure is logged and **non-fatal**; the checked-in universe stays the
source of truth. The fallback scraper's candidate set is noisy by nature, which
is exactly why `added` is advisory only.

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
| >20% of the **standard** universe failed to fetch | No recommendations. Telegram "RUN FAILED — data". Exit 1. |
| `holdings.json` unparseable | No recommendations. Telegram "RUN FAILED — holdings state". Exit 1. |
| `holdings.json` missing | **Not an error.** Report proceeds with no exit alerts. |
| `holdings.json` `as_of` older than 60 days | Proceed with a STALE-HOLDINGS warning. |
| Series contains an unadjusted split | Fund rejected from ranking, listed with its break date. |
| Fund has <200 sessions | Not eligible for the top 10 (no trend read); listed as not scored. |
| No fund passes the trend gate | Valid outcome, reported as such: "nothing is trending, stay in cash". |
| Drift fetch failed | Logged, non-fatal. |
| Telegram send fails | Retry ×3; report JSON is still written to GCS; exit 1. |
| Run context expired | Failure alert is sent on a **detached** context so the alert survives the very deadline that caused the failure. |
| Ranking ties | Deterministic alphabetical tie-break. |

## Scheduling

Monthly on the 1st, Cloud Scheduler `0 7 1 * *` UTC (~evening AEST). No
last-trading-day gate is needed — unlike the NSE rotator this job issues no
orders, so the exact session it runs on does not matter. `--force` / `?force=1`
bypasses freshness guards for manual runs.

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
- **Recency tilt** — a decelerator ranks below a fresh accelerator at equal mean
  return, and below one with a *lower* mean when its 3m is negative.
- **Weight renormalisation** — 3m-only → 1.0; 3m+6m → 0.625/0.375; no 3m → unscored.
- **Inverse-vol weights** — sum to 1, calmer fund weighted higher, zero-vol not infinite.
- **Exit alerts** — held broken fund alerts, held trending fund does not, exits
  precede the top list.
- **Report formatting** — required fields present, no markdown metacharacters.
- **Fetch-failure threshold** — run fails and the failure still reaches Telegram.

`go build ./...`, `go vet ./...` and `go test ./...` must stay green.

## Open questions / deliberate gaps

1. **The parameters are not backtested.** 0.5/0.3/0.2 and the 200-day gate are
   reasoned defaults ported from a screener, not optimised values. If this lane
   is ever trusted with real sizing, it needs the same offline validation spec 20
   had before implementation.
2. **The trend gate is pro-cyclical.** It will keep you out of bottoms and put
   you in after moves have started. That is the intended trade — drawdown
   control over entry price — but it should be stated, not discovered.
3. **The ASX products CSV URL is unverified** and may rot. That is survivable by
   design (non-fatal, Betashares fallback), but the first live run should confirm
   which source actually answers.
4. **Concentration is unmanaged.** The top 10 can legitimately be eight flavours
   of US tech. Inverse-vol weighting does not fix correlation. A future version
   should cap exposure per underlying theme.
