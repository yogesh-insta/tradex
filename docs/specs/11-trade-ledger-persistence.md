# Spec: Persistence (async publisher → Pub/Sub → BigQuery)

## Purpose

Durably record what the system did — **the trade ledger** — without ever blocking the
hot path. All persistence is fire-and-forget from the VM; consumers are Cloud Run
sidecars. Tick-level capture is **deferred** in v1.

## Inputs

- Trade events from the executor and trade-management loop: `opened`, `modified`,
  `closed`, `rejected` (+ risk decisions).
- Config: topic names, batch/flush policy, dataset/table ids.

## Outputs

- Pub/Sub messages (at-least-once).
- BigQuery `trade_ledger` rows (idempotent upsert by `trade_id`).

## Behavior

1. **Async publisher (VM):** non-blocking; enqueue events to an in-process buffered
   channel, publish to Pub/Sub from a background goroutine. A slow/broken publisher
   **must never** delay `Open`/`ModifyStop`/`Close`.
2. **Delivery semantics:** at-least-once. Every event carries a stable key
   (`trade_id` + event seq) so the consumer dedupes.
3. **BigQuery logger (Cloud Run):** subscribes to the trades topic; **upsert by
   `trade_id`** (MERGE) so replays/retries don't duplicate. Latency-insensitive.
4. **Flush on shutdown:** on `SIGTERM`, flush the buffer (bounded wait) before exit.
5. **Backpressure:** if the buffer fills (Pub/Sub outage), drop *oldest telemetry* with a
   counter + alert rather than blocking trading; the OANDA account + ledger reconcile can
   backfill the truth later.

## Schema (BigQuery `trade_ledger`)

| Column | Type | Notes |
| --- | --- | --- |
| `trade_id` | STRING | OANDA trade id (primary key for MERGE) |
| `client_order_id` | STRING | idempotency key from executor |
| `account` | STRING | per-market account |
| `instrument` | STRING | `DE30_EUR` / `FR40_EUR` |
| `strategy` | STRING | `EU_LOVE` |
| `direction` | STRING | LONG/SHORT |
| `units` | INT64 | signed |
| `entry_price` | FLOAT64 | |
| `stop_loss` / `take_profit` | FLOAT64 | initial bracket |
| `exit_price` | FLOAT64 | on close |
| `realized_pl` | FLOAT64 | account currency |
| `open_time` / `close_time` | TIMESTAMP | UTC |
| `exit_reason` | STRING | `tp` / `sl` / `breakeven` / `time_cutoff` / `news` / `flatten` |
| `reason` | STRING | entry audit (which conditions fired) |
| `event_seq` | INT64 | ordering within a trade |
| `ingested_at` | TIMESTAMP | consumer write time |

## Config keys

```yaml
persistence:
  project: "tradex-<env>"
  topics:
    trades: "trade-events"
  bigquery:
    dataset: "tradex"
    table: "trade_ledger"
  buffer_size: 8192
  flush_timeout: 5s
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Pub/Sub publish fails | Retry in background; buffer; drop-oldest with alert if saturated |
| BigQuery insert fails | Consumer retries; dead-letter after N attempts |
| Duplicate delivery | MERGE on `trade_id` (+`event_seq`) → idempotent |
| VM crash before flush | Trade truth still recoverable from OANDA; ledger backfilled on reconcile |

## Acceptance criteria

- Publishing never adds measurable latency to `Open`/`Close` (publish is off-thread).
- Replaying the same trade events yields one logical row per trade (no dupes).
- A Pub/Sub outage does not stop trading; telemetry resumes when it recovers.
- `realized_pl` in the ledger reconciles with OANDA transaction history.

## Out of scope

- Tick lake (GCS→BQ) — deferred. KPI/analytics SQL — see `12-observability-and-alerts.md`.
