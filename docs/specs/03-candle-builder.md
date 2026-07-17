# Spec: Candle Builder (multi-timeframe)

## Purpose

Provide **authoritative OHLCV candles** in the two timeframes the EU path needs — **H1**
(for the 08:00–09:00 range and the daily/ATR context) and **M5** (for breakout execution
triggers) — in separate in-memory buffers. Streamed ticks give real-time shape; OANDA's
REST candle endpoint gives the authoritative close so a dropped stream cannot corrupt a
signal.

## Inputs

- Tick channel + price snapshot from `02-market-data-stream.md`.
- OANDA REST candles: `GET /v3/instruments/{instrument}/candles?granularity={M5|H1}&price=M`.
- Config: timeframes per instrument, window sizes, confirmation policy.

## Outputs

- **Per-(instrument, timeframe) ring buffers** of `Candle` (newest last, capped at
  `window.max` = 50).
- **`MarketEvent`** emitted to the strategy router **when an M5 candle closes** (v1
  decision trigger), carrying the closed candle + the M5 window + latest price.
- H1 candles are consumed by the session controller (range + ATR), not by the router.

## Behavior

1. **Buffer assembly (real-time):** fold ticks into the currently forming candle for each
   timeframe (align boundaries to the instrument's exchange clock; M5 aligned to :00/:05,
   H1 aligned to the top of the hour). Mark `Complete=false` while forming.
2. **Authoritative confirmation:** shortly after a candle boundary, fetch the just-closed
   candle from REST and **replace** the locally-assembled candle with the REST OHLCV,
   setting `Complete=true`. This is the version handed downstream.
   - If REST is briefly unavailable, emit the locally-built candle marked
     `Complete=true` but flagged `unconfirmed`, and reconcile when REST recovers.
3. **Emit trigger:** on each **M5** close, build a `MarketEvent` and hand it to the
   router (`05-strategy-eu-love.md` runs via the engine).
4. **Startup backfill:** on boot, GET the last `window.max` candles for each timeframe so
   the session controller and strategy have history immediately (see recovery in
   `04-eu-session-controller.md`).
5. **Timeframe isolation:** never derive M5 from H1 or vice-versa; each is sourced
   independently to avoid drift (architecture §4.2).

## Config keys

```yaml
candles:
  price: "M"            # mid
  timeframes:
    DE40_EUR: ["H1", "M5"]
    FR40_EUR: ["H1", "M5"]
  window_max: 50
  rest_confirm_delay: 2s   # wait after boundary before pulling authoritative close
  rest_confirm_timeout: 5s
```

## Failure modes

| Failure | Handling |
| --- | --- |
| REST confirm times out | Emit locally-built candle `unconfirmed`; reconcile later; count `unconfirmed_candles` |
| Stream gap mid-candle | Candle may be partial; REST confirm corrects it on close |
| Boundary/DST edge (23h/25h day) | Align to exchange clock via IANA tz; unit-tested around DST switch |
| Duplicate REST candle on reconcile | Idempotent replace keyed by `(instrument, timeframe, start)` |

## Acceptance criteria

- For a normal session, every emitted M5 `MarketEvent.Last` matches OANDA's REST M5
  candle for that boundary (OHLCV equal within price precision).
- The H1 candle for 08:00–09:00 exactly spans the range window used by the session
  controller.
- Killing the stream for 30s then restoring it still yields correct confirmed candles
  (REST backfills the gap).

## Out of scope

- Deciding trades (strategy) or computing session indicators (session controller).
