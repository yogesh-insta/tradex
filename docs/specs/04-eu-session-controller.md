# Spec: EU Session Controller

## Purpose

Produce the read-only `SessionState` the EU LOVE strategy needs, on the EU session clock:
the **08:00–09:00 CET opening range**, the **14-day daily ATR**, a **running session
VWAP**, and the **12-candle volume MA**. It is a *distiller* — it computes context and
never emits signals or touches orders.

## Inputs

- H1 + M5 candles from `03-candle-builder.md`.
- Daily candles from OANDA REST for the 14-day ATR (pre-session, ~07:30 CET).
- Clock + trading-holiday calendar (`10-economic-calendar.md`) in `Europe/Berlin` (DST-aware).
- Config: window times, ATR length, instruments.

## Outputs

- `SessionState` per EU instrument (`DE30_EUR`, `FR40_EUR`), refreshed continuously
  during the session, read-only to `Analyze()`.

## Behavior

Daily lifecycle (per instrument, `Europe/Berlin`):

1. **Pre-session (07:30 CET):** pull daily candles; compute **14-day ATR** (Wilder). Set
   `RangeLocked=false`. Skip the whole day if the holiday calendar marks it closed/half.
2. **Range window (08:00:00–08:59:59):** accumulate `OpeningHigh`/`OpeningLow` from
   candles; start the **running VWAP** (`Σ(typical×vol)/Σvol`, typical=(H+L+C)/3) from
   08:00; maintain `VolMA12` over the trailing 12 M5 candles.
3. **Lock (09:00:00):** freeze `OpeningHigh`/`OpeningLow`, set `RangeLocked=true`. VWAP
   and VolMA12 keep updating through the session (they are "running").
4. **Serve** `SessionState` to the strategy for the trade window (from 09:05 CET).
5. **Reset** at end of session for the next day.

### Recovery on boot (mid-session restart)

- If the VM starts **after 09:00**, rebuild `OpeningHigh/Low` from REST H1/M5 candles for
  the 08:00–09:00 window (do not wait for a live window), set `RangeLocked=true`,
  recompute ATR from daily candles, and rebuild VWAP/VolMA12 from session candles so far.
- If the VM starts **before 08:00**, run the normal lifecycle.

## Config keys

```yaml
eu_session:
  tz: "Europe/Berlin"
  range_start: "08:00:00"
  range_end:   "09:00:00"
  trade_window_start: "09:05:00"   # first M5 close eligible for entry
  atr_period_days: 14
  vol_ma_candles: 12
  instruments: ["DE30_EUR", "FR40_EUR"]
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Missing daily candles for ATR | Block entries for that instrument (ATR unknown = cannot size); alert |
| Boot during range window | Accumulate from live + REST backfill; lock at 09:00 as normal |
| Holiday/half-day | Controller stays idle; no `SessionState` served |
| DST transition | Times resolved via IANA tz; range window still the local 08:00–09:00 |
| Gap/unconfirmed candle | Prefer confirmed REST candles for range/ATR; recompute on reconcile |

## Acceptance criteria

- At 09:00:00 CET, `RangeLocked=true` and `OpeningHigh/Low` equal the H1 08:00 candle's
  high/low (and match REST).
- ATR matches an independent 14-day Wilder ATR calc on the same daily candles.
- A restart at 10:00 CET reconstructs the identical locked range and a VWAP within
  rounding tolerance of the pre-restart value.
- The controller never returns a `Signal` or calls the executor.

## Out of scope

- Entry logic (strategy), sizing (risk), US/Asia controllers (future).
