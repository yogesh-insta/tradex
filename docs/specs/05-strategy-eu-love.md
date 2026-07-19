# Spec: Strategy — EU LOVE (London Open Volatility Extension)

## Purpose

Pure decision function: given a closed **M5** `MarketEvent` and the EU `SessionState`,
decide whether a confirmed breakout of the 08:00–09:00 range has occurred and, if so,
emit a `Signal`. No side effects, no I/O, no order placement.

Source strategy: `../backup/Europe market strategy.md` (§3–§4), adapted to the
in-VM architecture (no Cloud Run) and the resolved decisions in `architecture.md` §10.

## Inputs

- `MarketEvent` (M5 close) — from the candle builder via the router.
- `SessionState` — from `04-eu-session-controller.md` (`RangeLocked` must be true).
- Config: buffers, volume-spike multiplier, ATR multiples.

## Output

- `*Signal` (nil = no setup). Populated fields: `Direction`, `OrderType="MARKET"`,
  `EntryPrice` (reference = candle close), `StopLoss`, `TakeProfit`, `Policy`, `Reason`.

## Behavior

Only evaluate when: `RangeLocked == true`, `now >= trade_window_start` (09:05 CET),
`DailyATR > 0`, and within the entry window (see below).

**Long entry matrix** (all must hold on the just-closed M5 candle):
1. `Last.Close > OpeningHigh` — candle **closes completely above** the range high
   (close strictly above; wick-only breaks do not count).
2. `Last.Close > VWAP` — execution side of the daily VWAP.
3. `Last.Volume > volume_spike_mult × VolMA12` — volume expansion vs the preceding
   12 candles (`volume_spike_mult` default 1.0 = "higher than average").

**Short entry matrix** — mirror: `Close < OpeningLow`, `Close < VWAP`, volume expansion.

**On a satisfied matrix**, emit a `Signal`:
- `EntryPrice` = `Last.Close` (reference; executor uses a MARKET order).
- `StopLoss` = entry ∓ `sl_atr_mult × DailyATR` (below for long, above for short;
  `sl_atr_mult` default 0.5).
- `TakeProfit` = entry ± `tp_atr_mult × DailyATR` (`tp_atr_mult` default 1.5 → 1:3 R:R).
- `Policy.BreakevenAtR` = 1.0 (move SL→entry at +1R; see `08-trade-management-loop.md`).
- `Policy.TimeCutoff` = the applicable flatten time (Friday 17:30 CET; see risk/mgmt).
- `Reason` = which conditions fired (audit).

Otherwise return **nil**.

### Entry window & re-entry

- **Entry window:** M5 closes from `trade_window_start` (09:05) until
  `entry_window_end` (default 11:00 CET, i.e. before the common 11:00 macro slot). No new
  `Signal`s after that; management still runs.
- **Re-entry:** same-day re-entry after a stop-out **is allowed** (the legacy "one
  execution per day" rule is intentionally dropped — `architecture.md` §10). Whipsaw is
  bounded by the correlation guard + per-account controls in `06-risk-management.md`, not here.
- The strategy stays **stateless across calls** beyond what `SessionState` provides;
  it does not track "already traded today" — concurrency/dedupe is risk's job.

## Config keys

```yaml
eu_love:
  volume_spike_mult: 1.0
  sl_atr_mult: 0.5
  tp_atr_mult: 1.5
  breakeven_at_r: 1.0
  entry_window_end: "11:00:00"   # Europe/Berlin
```

## Failure modes

| Condition | Result |
| --- | --- |
| `RangeLocked == false` | Return nil (no evaluation) |
| `DailyATR <= 0` or NaN | Return nil; controller/risk already blocks; log `atr_unavailable` |
| Wick breaks but close inside | No signal (close-based rule) |
| Both long & short conditions in one candle | Impossible by construction (close can't be both above high and below low) |
| Outside entry window | Return nil |

## Acceptance criteria

- Deterministic: identical `(MarketEvent, SessionState)` always yields the identical
  `Signal`/nil (no clocks, no randomness, no globals).
- A close 1 tick above `OpeningHigh` with volume just under the threshold → nil; nudging
  volume over the threshold → LONG signal with SL/TP at the configured ATR multiples.
- SL/TP respect direction sign; R:R equals `tp_atr_mult / sl_atr_mult` (3.0 by default).
- Unit tests cover long, short, wick-only, low-volume, and below-VWAP rejections.

## Out of scope

- Sizing, margin, correlation (risk). Order placement (executor). Exits (trade-mgmt).
- US resting-limit, FX TRLD, and Asia index strategies (separate specs; same `Strategy` iface).
