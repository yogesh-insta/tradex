# 22 — ASX 200 Momentum Rotator

Monthly, advisory-only momentum rotation over the S&P/ASX 200. Third rotation
lane alongside spec 20 (NSE) and spec 21 (ASX ETF monitor). Never places orders:
it ranks, diffs against a user-maintained portfolio, and reports via Telegram.

- Package: `internal/asxrotator` · Command: `cmd/asxrotator` · Route: `/asx`
- Config: `config/config.asxrotator.{dev,cloudrun}.yaml`
- State: `gs://tradex-demo-state/asxrotator/{portfolio,heartbeat,recommendation-YYYY-MM}.json`

## THE STRATEGY IN FULL

Identical rules to the NSE rotator:

- **Entry** — buy the top 10 by 6-month trailing return.
- **Exit** — sell only when a holding is outside the top 30 on **both** the
  6-month and 12-month lists (hysteresis; `exit_rank_n = 3 × top_k`).
- **Hold** — anything still on either list stays, even below the entry rank.
- **Weighting** — equal weight, `capital / top_k` per slot. Holds are never
  resized.
- **Cadence** — monthly, on the last ASX trading day.
- **Regime** — `^AXJO` vs its 200-day EMA. Computed and reported, but the gate
  ships **off**.

The signal math is shared with the NSE lane in `internal/momentum`, so the
hysteresis has exactly one implementation. The Yahoo client is shared in
`internal/yahoo`, market-selected by `Suffix` (`.AX` here).

## Backtest

`kite/asx_momentum/asx_nse_aligned.py` imports the NSE engine
(`kite/backtest/momentum_dual.py`) rather than reimplementing it, so the
comparison is like-for-like. 197 of the current ASX 200, Aug 2011 – Jul 2026,
prices ≥ A$1.00, 0.12% round-trip:

| K  | exit_rank_n | CAGR  | Max DD | Sharpe |
|----|-------------|-------|--------|--------|
| 10 | 30          | 25.0% | -30.1% | 1.11   |
| 5  | 15          | 22.1% | -32.8% | 0.85   |
| 3  | 9           | 18.1% | -44.2% | 0.66   |
| 1  | 3           |  7.9% | -67.1% | 0.42   |
| —  | ASX 200 index (STW, real)  | 8.6%  | -25.3% | 0.70 |
| —  | equal-weight the 197 names | 14.5% | -25.6% | 1.15 |

K=10 ships. The K gradient is far steeper than on NSE: K=1 underperforms the
index outright with a -67% drawdown. Concentration does not work on this
universe. Costs barely matter — turnover is ~9.6%/month thanks to the exit
hysteresis, so 25.0% at 0.12% becomes 24.8% at an ASX-realistic 0.30%.

## Survivorship — read before quoting 25% to anyone

The backtest universe is the **current** (2026-08) ASX 200 membership pulled
back to 2011. Names that were in the index and later collapsed or were acquired
are absent. Measured directly:

- equal-weight buy-and-hold of these 197 survivors: **14.5%/yr**
- the real, investable ASX 200 total return (STW), same window: **8.6%/yr**
- ⇒ roughly **+6%/yr of the headline is universe selection**, before any strategy

So the defensible expectation for the shipped config is **high teens, not 25%**,
and even that assumes the strategy's edge over its own biased benchmark
(+10.5%/yr) is real rather than itself inflated — momentum selects hardest for
exactly the names survivorship protects.

Correcting this needs point-in-time constituents plus prices for delisted
names; Norgate Data is the practical ASX source. Until then this section is the
correction, and the 25% figure must not travel without it.

For scale, the same defect on a 20-year window measured **+13.5%/yr**, and the
raw uncleaned run read 41–51% CAGR. Longer windows are worse, not better, here.

## Data quality — the price floor (`min_price_aud`)

**This gate is mandatory and has no NSE equivalent.**

Yahoo back-adjusts for demergers by scaling the entire prior history down.
`TAH.AX` (Tabcorp — Echo 2011, Lottery Corp 2022) reads **A$0.0000 for 103
consecutive months**, 2005-01 to 2013-07. Five other names are affected: `LTR`,
`MEZ`, `PDI`, `PLS`, `NST`.

Momentum computed off a near-zero base reads ~**+1800%** and ranks first every
month. Critically, `BadJumpUp` / `LargeDownJump` **do not catch this**: the
corrupted series is smooth, merely scaled — there is no violent single-day move
to detect. Only an absolute price floor works.

Behaviour:

- Applied **before** scoring, to **both** momentum lists. A sub-floor name must
  not be able to keep itself alive via the slow list — that is precisely how the
  artifact would persist in a live book.
- A **held** name that falls below the floor is therefore SOLD, not held.
- Every drop is logged at INFO and reported in `below_min_price` on the
  recommendation, in the Telegram message, and on the dashboard. A silent filter
  over a 200-name universe is how a data-quality gate stops being noticed.
- Default and shipped value: **A$1.00**. It doubles as a penny-stock/liquidity
  screen, which is why it is config rather than a constant.
- A negative value is a config **error**, not clamped: it almost certainly means
  someone meant to disable the screen and got the sign wrong, and disabling it
  is the failure this guards.

Unguarded, the 15-year backtest reads 41–51% CAGR — entirely artifact — against
25.0% with the floor on.

The NSE lane needs none of this. The same test on Nifty 200 data found zero
sub-rupee prices and zero monthly returns above +300%.

Regression test: `TestBackAdjustmentArtifactIsScreenedOnlyByPriceFloor`. It
asserts both that the fixture reproduces the phantom momentum *and* that the
jump screens miss it — so if a future change makes the jump screen catch this
shape, the test fails and tells you the floor is no longer load-bearing.

## Regime filter — ships OFF

Same conclusion as spec 20, **different evidence**. Do not copy the NSE
reasoning across. Clean 15-year window, shipped config:

|                                 | CAGR  | Max DD    | Sharpe | Turnover |
|---------------------------------|-------|-----------|--------|----------|
| regime OFF                      | 25.0% | -30.1%    | 1.11   | 9.6%/mo  |
| regime ON (`^AXJO` > 200d EMA)  | 14.0% | **-34.4%**| 0.80   | 22.2%/mo |

On NSE the filter at least bought drawdown protection for the CAGR it cost. On
ASX over this window it costs 11 points of CAGR **and makes drawdown worse**,
while more than doubling turnover — it is not drawdown insurance here.

Caveat recorded honestly: this 15-year window contains no GFC-scale bear market.
Over 20 years the filter *did* reduce drawdown (-34.4% vs -54.3%). That is an
argument for keeping the flag, not for enabling it.

With the filter off the index reading is advisory only, and the message says so
("BELOW EMA — staying invested") rather than printing "exit all positions"
above a list of BUY orders.

## Tax posture

Not addressed by spec 20, and material here. **Every return figure above is
pre-tax.**

- Australia's **50% CGT discount** requires holding longer than 12 months.
- This strategy turns over ~9.6% of the book per month; average holding life is
  well under 12 months, so **most gains will not qualify**.
- **Franking credits** are largely forfeited too (45-day holding period rule).

The Telegram message carries a one-line reminder. This is advisory-only
software and not tax advice, but omitting the point would be misleading given
the turnover profile.

## Universe and drift

`config/universe-asx200.yaml` — 197 tickers, bare uppercase, Yahoo symbol is
`<ticker>.AX`. Three index members (`IFL`, `NSR`, `XYX`) returned no Yahoo data
on 2026-08-04 and are omitted with a note in the file.

**Refresh ritual: QUARTERLY** (March, June, September, December) — the ASX 200
rebalances four times a year, unlike Nifty 200's semi-annual review.

S&P publishes membership behind a login, so unlike NSE there is **no official
free endpoint**. `constituents_url` should point at an ASX 200 index ETF
holdings CSV (STW, IOZ or A200) as a proxy. Left empty with `drift_check: true`,
the rotator emits an explicit "UNIVERSE DRIFT NOT CHECKED" warning on every run
— deliberately noisy, because a drift check that silently never fires reads as
reassurance it has not earned.

## Calendar and currency

| | NSE lane | ASX lane |
|---|---|---|
| Yahoo suffix | `.NS` | `.AX` |
| Regime index | `^NSEI` | `^AXJO` |
| Holidays | `config/holidays-nse.yaml` | `config/holidays-asx.yaml` |
| Market key | `XNSE` | `XASX` |
| Currency | INR (`total_capital_inr`) | AUD (`total_capital_aud`) |
| Timezone | IST, fixed offset | `Australia/Sydney` — **observes DST** |

The AUD capital field is deliberately not named like the INR one: a shared name
across two currencies is how a portfolio file ends up parsed by the wrong lane
and sized in the wrong money.

`holidays-asx.yaml` lists **national** ASX closures only. State-specific days
(Melbourne Cup, most Labour Days, state King's Birthdays) do not close the
exchange; adding one would skip a real trading day and fire the month-end run
early.

## Dashboard

Route `/asx`, API `GET /api/asx`. The NSE and ASX lanes share one JS renderer
(`renderRotator(rec, M)`), with `M` carrying currency formatters, field names
and labels — the strategies are identical, so the rendering should be too.
NSE defines no `money` formatter and falls back to its existing `price`, so its
page renders byte-identically to before this lane existed.

ASX-specific additions: AUD prices keep cents (ASX share prices are routinely
under A$10), aggregates use thousands separators, and a "Below price floor"
section lists screened names.

Portfolio holdings are **not** enriched with live quotes on this lane: the
dashboard's quoter is NSE-wired, and the recommendation already carries
`last_close`. Better a visibly static mark than one silently priced off the
wrong exchange. An ASX quoter is a separate change.

## Non-goals

- No order placement. Advisory-only, same as the other lanes.
- No change to NSE behaviour, config or numbers.
- No cross-lane portfolio view or currency conversion.
- No attempt to correct survivorship bias in the backtest — recorded, not hidden.
