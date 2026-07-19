# Spec: FX Session Controller (USD/JPY)

## Purpose

Produce the read-only `SessionState` the FX TRLD strategy needs, on the Tokyo session
clock: the **09:00–11:00 JST Tokyo range**, the **14-day daily ATR**, a **running
session VWAP**, and the **12-candle volume MA**. It is a *distiller* — it computes
context and never emits signals or touches orders.

Design: [`../fx-usdjpy-architecture.md`](../fx-usdjpy-architecture.md) §4.1.
Consumer: [`17-strategy-fx-trld.md`](./17-strategy-fx-trld.md).

## Inputs

- H1 + M5 candles from `03-candle-builder.md` for `USD_JPY`.
- Daily candles from OANDA REST for the 14-day ATR (pre-session, ~07:30 JST).
- Clock in `Asia/Tokyo` (DST-aware; Japan has no DST — still use IANA for consistency).
  **v1:** weekend skip only (`SkipWeekends`). Configured JP/US FX holiday-file entries
  are not yet wired into the FX controller (EU uses `holidays.yaml` for XETR/XPAR).
- Config: window times, ATR length, instruments.

## Outputs

- `SessionState` for `USD_JPY`, refreshed during the active session, read-only to
  `Analyze()`.

## Behavior

Daily lifecycle (instrument `USD_JPY`, `Asia/Tokyo`):

1. **Pre-session (07:30 JST):** pull daily candles; compute **14-day ATR** (Wilder). Set
   `RangeLocked=false`. Skip weekend days (`SkipWeekends`). Full JP/US FX holiday
   calendar is a future wiring item — do not assume `holidays.yaml` gates FX today.
2. **Range window (09:00:00–10:59:59):** accumulate `OpeningHigh`/`OpeningLow` from
   candles; start the **running VWAP** (`Σ(typical×vol)/Σvol`, typical=(H+L+C)/3) from
   `range_start`; maintain `VolMA12` over the trailing 12 M5 candles.
3. **Lock (11:00:00):** freeze `OpeningHigh`/`OpeningLow`, set `RangeLocked=true`. VWAP
   and VolMA12 keep updating through the session (they are "running").
4. **Serve** `SessionState` through the London drive trade window (from 16:00 JST) and
   until soft cutoff / session end.
5. **Reset** at end of session day for the next Tokyo day.

VWAP session anchor: from `range_start` (09:00) continuously — not reset at London open —
so London breaks are judged against the day’s accumulated Tokyo+post-Tokyo VWAP.

### Recovery on boot (mid-session restart)

- If the VM starts **after 11:00 JST**, rebuild `OpeningHigh/Low` from REST H1/M5 candles
  for the 09:00–11:00 window, set `RangeLocked=true`, recompute ATR from daily candles,
  and rebuild VWAP/VolMA12 from session candles so far.
- If the VM starts **during** the range window, accumulate from live + REST backfill;
  lock at 11:00 as normal.
- If the VM starts **before 09:00**, run the normal lifecycle.

### FX weekend / reopen

- Controller stays idle on weekends (typical: Friday NY close → Sunday NY open). Exact
  open/close beyond weekend skip is ops/future holiday wiring; do not invent ticks.
- After Sunday reopen, still wait for the next Tokyo range window; do not synthesize a
  partial Sunday night range as “Tokyo range.”

## Config keys

```yaml
fx_session:
  tz: "Asia/Tokyo"
  range_start: "09:00:00"
  range_end:   "11:00:00"        # lock instant; window is [start, end)
  trade_window_start: "16:00:00" # first M5 close eligible (strategy also gates)
  soft_cutoff: "21:00:00"        # no new entries after; mgmt may flatten weak trades
  prep_time: "07:30:00"
  atr_period_days: 14
  vol_ma_candles: 12
  instruments: ["USD_JPY"]
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Missing daily candles for ATR | Block entries for `USD_JPY` (ATR unknown); alert |
| Boot during range window | Live + REST backfill; lock at 11:00 |
| FX weekend / holiday | Controller idle; no usable `SessionState` for entries |
| Gap / unconfirmed candle | Prefer confirmed REST for range/ATR; recompute on reconcile |
| Empty range (High==Low) | Still lock; strategy range-width filter will reject |

## Acceptance criteria

- At 11:00:00 JST, `RangeLocked=true` and `OpeningHigh/Low` match the extremal high/low
  of confirmed candles in `[09:00, 11:00)` (and match REST rebuild).
- ATR matches an independent 14-day Wilder ATR on the same daily candles.
- A restart at 17:00 JST reconstructs the identical locked range and a VWAP within
  rounding tolerance of the pre-restart value.
- The controller never returns a `Signal` or calls the executor.
- EU session controller and FX session controller never share mutable state.

## Out of scope

- Entry logic (`17`), sizing (`18`), backtest harness (`19`).
- US Sweep / Asia index controllers.
