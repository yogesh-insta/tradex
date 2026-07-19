# Spec: Strategy — FX TRLD (Tokyo Range → London Drive)

## Purpose

Pure decision function: given a closed **M5** `MarketEvent` and the FX `SessionState`,
decide whether a confirmed breakout of the Tokyo range has occurred during the London
drive window, and if so emit a `Signal`. No side effects, no I/O, no order placement.

Source design: [`../fx-usdjpy-architecture.md`](../fx-usdjpy-architecture.md) §2–§4.
Session context: [`16-fx-session-controller.md`](./16-fx-session-controller.md).

## Inputs

- `MarketEvent` (M5 close) — from the candle builder via the router (`USD_JPY` only).
- `SessionState` — from `16-fx-session-controller.md` (`RangeLocked` must be true).
- Config: volume spike, ATR multiples, range-width fractions, entry window end, spread
  gate (spread read from latest tick on the event’s price path / snapshot mid+spread
  fields when available — see Behavior).
- Calendar / one-trade-day / system state are **not** evaluated here — risk (`18`) owns
  those gates. The strategy may still encode soft time window via config times compared
  to `MarketEvent.Now`.

## Output

- `*Signal` (nil = no setup). Populated fields: `Direction`, `OrderType="MARKET"`,
  `EntryPrice` (reference = candle close), `StopLoss`, `TakeProfit`, `Policy`,
  `Strategy="fx_trld"`, `Reason`.

## Behavior

Only evaluate when all of the following hold:

- `RangeLocked == true`
- `DailyATR > 0`
- `MarketEvent.Timeframe == M5`
- `now` (instrument tz `Asia/Tokyo`) ∈ `[trade_window_start, entry_window_end)`
- Tokyo range width `W = OpeningHigh - OpeningLow` satisfies
  `min_atr_frac × DailyATR ≤ W ≤ max_atr_frac × DailyATR`

If any precondition fails → **nil**.

### Long entry matrix (all must hold on the just-closed M5 candle)

1. `Last.Close > OpeningHigh` — candle **closes completely above** the Tokyo range high
   (close strictly above; wick-only breaks do not count).
2. `Last.Close > VWAP` — execution side of the session VWAP.
3. `Last.Volume > volume_spike_mult × VolMA12` — volume expansion vs the preceding
   12 M5 candles (`volume_spike_mult` default 1.0).
4. **Spread gate (strategy-local):** if `MarketEvent` / price snapshot exposes spread and
   `spread_pips > max_spread_pips`, return nil (`Reason` would be unused — just nil).
   If spread is unavailable, defer to risk (`18`) which rejects `spread_too_wide`.

### Short entry matrix

Mirror: `Close < OpeningLow`, `Close < VWAP`, volume expansion, same spread/range gates.

### On a satisfied matrix, emit a `Signal`

- `EntryPrice` = `Last.Close` (reference; executor uses MARKET).
- `StopLoss` = entry ∓ `sl_atr_mult × DailyATR` (below for long, above for short;
  `sl_atr_mult` default 0.5).
- `TakeProfit` = entry ± `tp_atr_mult × DailyATR` (`tp_atr_mult` default 1.5 → 1:3 R:R).
- `Policy.BreakevenAtR` = `breakeven_at_r` (default 1.0).
- `Policy.TimeCutoff` = **Friday NY hard flatten only** (from mgmt/risk config; see
  `18`). Non-Friday signals leave `TimeCutoff` zero.
- `Policy.Trail` = `""` by default; if `trail_after_r > 0` is enabled post-backtest,
  set broker trail id per executor conventions — **v1 default off**.
- `Reason` = audit string of which conditions fired (range side, VWAP, vol, width band).

Otherwise return **nil**.

### Entry window & re-entry

- **Entry window:** M5 closes from `trade_window_start` (default 16:05 `Asia/Tokyo`)
  until `entry_window_end` (default 19:00). No new signals after that; management still
  runs on open trades.
- **Re-entry:** same-day re-entry after a stop-out is **not** allowed for FX TRLD.
  Enforcement is in risk (`18`: one trade per instrument per session day), not in this
  pure function — the strategy stays **stateless** across calls beyond `SessionState`.

### Soft cutoff (management, not `TimeCutoff`)

Weak-R flatten at soft cutoff (`Asia/Tokyo` 21:00 default, profitR &lt;
`soft_cutoff_flatten_r`) is owned entirely by trade-management (`18` / mgmt
`fx_soft_cutoff*`). The strategy must **not** seed `Policy.TimeCutoff` with the soft
cutoff — that field is unconditional flatten.

## Config keys

```yaml
fx_trld:
  volume_spike_mult: 1.0
  sl_atr_mult: 0.5
  tp_atr_mult: 1.5
  breakeven_at_r: 1.0
  min_atr_frac: 0.15          # skip tiny Tokyo ranges
  max_atr_frac: 1.25          # skip already-blown Asia ranges
  max_spread_pips: 1.5        # USD_JPY; pip = 0.01 JPY
  trade_window_start: "16:05:00"  # Asia/Tokyo (must match session; past OANDA AU maint)
  entry_window_end: "19:00:00"    # Asia/Tokyo
  trail_after_r: 0                # 0 = off; enable only after backtest
```

## Failure modes

| Condition | Result |
| --- | --- |
| `RangeLocked == false` | Return nil |
| `DailyATR <= 0` or NaN | Return nil |
| Range width outside `[min,max] × ATR` | Return nil |
| Wick breaks but close inside | No signal |
| Below/above VWAP wrong side | No signal |
| Low volume vs VolMA12 | No signal |
| Spread &gt; max (when known) | Return nil |
| Outside entry window | Return nil |
| Both long & short on one candle | Impossible by construction |

## Acceptance criteria

- Deterministic: identical `(MarketEvent, SessionState, config)` ⇒ identical `Signal`/nil.
- Close 1 pip above `OpeningHigh` with volume just under threshold → nil; nudging volume
  over threshold → LONG with SL/TP at configured ATR multiples.
- R:R equals `tp_atr_mult / sl_atr_mult` (3.0 by default).
- Unit tests: long, short, wick-only, low-volume, wrong-VWAP, narrow range, wide range,
  outside window.
- Never sizes units, never calls OANDA, never reads account state.

## Out of scope

- Sizing, one-trade/day, news blackout, weekend flatten (risk / mgmt — `18`, `08`).
- Session range construction (`16`).
- Backtest harness (`19`).
