# Spec: Market Data (stream ingest)

## Purpose

Own the OANDA pricing WebSocket for the EU instruments: consume ticks, keep the latest
price per instrument in RAM, and detect/halt on staleness. This is the real-time trigger
source; authoritative OHLC is the candle builder's job (`03-candle-builder.md`).

## Inputs

- OANDA **pricing stream** `GET /v3/accounts/{id}/pricing/stream?instruments=DE40_EUR,FR40_EUR`
  (streamed PRICE + HEARTBEAT messages).
- Config: instrument list, staleness thresholds, reconnect backoff (`13-configuration.md`).

## Outputs

- **Price snapshot** — `map[instrument]Tick` in shared RAM, read by the candle builder,
  trade-management loop, and risk sizing. Guarded by a mutex / atomic value.
- **Tick channel** — buffered channel of `Tick` to the candle builder.
- **Health signals** — `stream_up`, `last_tick_at[instrument]` for observability.

## Behavior

1. **Connect** on boot (only for instruments whose market can be open today per the
   holiday calendar). One stream connection carries all EU instruments.
2. **Parse** PRICE messages into `Tick` (compute `Mid`, `Spread`); update the snapshot
   and forward to the tick channel. Ignore non-tradeable/`status != "tradeable"` prices
   for triggering but still record them for staleness.
3. **Heartbeats:** OANDA sends heartbeats ~every 5s. Track `last_msg_at`. If no message
   (tick or heartbeat) for `stream.heartbeat_timeout` (default 15s), treat the stream as
   down → reconnect.
4. **Reconnect** with exponential backoff (`stream.backoff_base` … `stream.backoff_max`,
   full jitter). On reconnect, re-subscribe and log a gap marker.
5. **Staleness halt:** if no *ticks* arrive for `stream.stale_halt` (default 60s) **while
   the EU session is open**, raise a staleness alarm and set a `market_data_stale` flag.
   Risk **blocks new entries** while stale; the trade-management loop keeps running on
   the last snapshot (broker brackets still protect open trades).
6. **Clock discipline:** stamp ticks with OANDA's event time (UTC), not local time.

## Config keys

```yaml
stream:
  instruments: ["DE40_EUR", "FR40_EUR"]
  heartbeat_timeout: 15s
  stale_halt: 60s
  backoff_base: 1s
  backoff_max: 60s
  tick_channel_buffer: 4096
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Stream disconnect | Backoff reconnect; log gap; snapshot goes stale after `stale_halt` |
| No heartbeat, socket half-open | `heartbeat_timeout` forces reconnect |
| Malformed/partial JSON line | Skip line, count `parse_errors`, do not crash |
| Prolonged outage during session | `market_data_stale` → risk blocks entries; page via observability |
| Clock skew on VM | Use OANDA event time; alert if VM NTP drift > 1s |

## Acceptance criteria

- After a forced socket drop, the stream reconnects and the snapshot resumes updating
  within `backoff_max`.
- With ticks suppressed for > `stale_halt` during session hours, `market_data_stale`
  becomes true and risk rejects new entries with reason `stale_market_data`.
- The latest price for each instrument is readable without blocking the ingest goroutine.

## Out of scope

- Candle construction (see `03-candle-builder.md`).
- Persisting ticks to GCS (deferred in v1).
