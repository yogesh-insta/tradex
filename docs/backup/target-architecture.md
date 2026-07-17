# Target Architecture (Revised)

Revised design incorporating the decisions from the architecture review:

1. **The VM owns the entire trade hot path.** Cloud Run + Pub/Sub are async-only sidecars.
2. **Separate GCP projects** for dev and prod; live vs paper is chosen by the OANDA host.
3. **One unified risk module** in-process (per-trade + portfolio + kill switch).
4. [https://excalidraw.com/#json=7Yf6-OlR3emQ_pSR5uxg3,CMf0pyqkzMx4vAhDxZ7e-w](https://excalidraw.com/#json=7Yf6-OlR3emQ_pSR5uxg3,CMf0pyqkzMx4vAhDxZ7e-w)

---

## 1. High-level shape

```
                              (async, non-critical, at-least-once OK)
                        ┌──────────► Pub/Sub ──► Cloud Run sidecars:
                        │                         • BigQuery logger (trades, ticks metadata)
                        │                         • Analytics / KPI jobs
                        │                         • Telegram notifier
                        │                         • Economic + holiday calendar poller
                        │
OANDA stream ─────► [ VM: always-on Go engine ] ─── GCS (tick .jsonl.gz, 15-min batches) ──► BigQuery
 (prices, WS)           │
                        │  Hot path (all in-process, single writer):
                        │   1. WS ingest + reconnect/backoff + staleness halt
                        │   2. Authoritative candles (stream trigger + REST OHLC)
                        │   3. Strategy registry  map[string]Strategy  (US / EU / Asia)
                        │   4. Unified risk module (per-trade + portfolio + kill switch)
                        │   5. Executor: open-with-bracket, client-order-id idempotency
                        │
                        └─────► OANDA REST  (fxpractice | fxtrade, chosen by env/creds)
```

**Hot path = synchronous, in-process, on the VM.** No cross-service call is on the execution path.
**Everything async** (logging, analytics, notifications, calendar) goes VM -> Pub/Sub -> Cloud Run.

---



## 2. Components



### 2.1 VM engine (always-on, e2-micro, Go)

Single binary; the only writer to OANDA. Modules (each its own package, config-driven):

- **Market data**: OANDA pricing WebSocket; heartbeat monitoring; reconnect with exponential backoff;
gap detection. Real-time ticks drive triggers; authoritative OHLC comes from the OANDA candle
endpoint so a dropped stream cannot corrupt a signal.
- **Session controllers** (in RAM, rebuildable): US tracks 09:30–09:45 ET opening range; EU tracks
08:00–09:00 CET range + 14-day ATR; Asia tracks overnight gap + Bollinger state. All window times
derived from IANA zones with DST + trading-holiday calendar applied.
- **Strategy registry**: `map[string]Strategy`, key = instrument (e.g. `NAS100_USD` -> US sweep).
Each strategy keeps its own sliding window (<=50 candles) and implements `Analyze(ctx) Signal`.
- **Unified risk module** (single source of truth):
  - Per-trade: 1% equity risk, stop distance per strategy, margin/leverage gatekeeper.
  - Portfolio: max daily loss halt, max concurrent positions, max trades/day,
  consecutive-loss circuit breaker, correlation guard (e.g. US100/SPX500).
  - Global kill switch (halt all new entries; managed positions keep broker-side stops).
- **Executor** (broker-abstracted): `OrderExecutor` interface, OANDA implementation first.
Every entry opens **atomically with bracket SL/TP**. Idempotency via `clientExtensions.id`.
Instrument-metadata layer (point value, margin rate, min size, precision, symbol mapping)
so a broker switch does not break sizing.
- **State recovery** (on boot): rebuild open trades from `GET /openTrades`; rebuild opening ranges
by pulling historical candles; derive the daily one-trade-per-index guard from OANDA/BQ history.
- **Async publisher**: fire-and-forget events to Pub/Sub for logging/analytics/notify; buffers ticks
and drains to GCS every 15 min; flushes on SIGTERM. (Hard-crash tick loss is best-effort;
add a local-disk WAL if lossless training data is required.)



### 2.2 Cloud Run sidecars (async only, scale-to-zero OK)

Latency-insensitive; cold starts are harmless here.

- **BigQuery logger**: consumes trade/tick-metadata events; idempotent upsert by `trade_id`.
- **Analytics/KPI**: scheduled SQL for Sharpe, profit factor, max drawdown, equity curve.
- **Telegram notifier**: consumes alert events; sends to Telegram.
- **Calendar poller**: pulls economic + trading-holiday calendars on a schedule, writes a compact
state the VM reads; VM **fails safe** if the state is stale/unknown.



### 2.3 Data lake

GCS (tick `.jsonl.gz`, 30-day lifecycle) -> BigQuery (`historical_ticks`, `trade_ledger`) for KPIs
and future ML. Model class for signals should be gradient-boosted trees / classical ML / RL, not an LLM.

---



## 3. Environments (separate projects)


| Aspect           | Dev project                           | Prod project                   |
| ---------------- | ------------------------------------- | ------------------------------ |
| GCP project      | `tradex-dev`                          | `tradex-prod`                  |
| OANDA host       | `api-fxpractice.oanda.com` (paper)    | `api-fxtrade.oanda.com` (live) |
| Service accounts | dev-scoped, least privilege           | prod-scoped, least privilege   |
| Secrets          | Secret Manager (dev)                  | Secret Manager (prod)          |
| VM               | e2-micro (may be scheduled/off-hours) | e2-micro always-on             |
| Blast radius     | isolated                              | isolated                       |


Live trading is gated by **which OANDA host/credentials are loaded**, not a boolean flag. All config
lives in a separate per-environment config file (per coding guidelines); no code differences between envs.

---



## 4. Cross-cutting concerns

- **Idempotency**: `clientExtensions.id` on every order; BQ upserts keyed by `trade_id`; Pub/Sub
consumers assume at-least-once and dedupe.
- **Time**: all session logic via IANA zones; DST-safe; trading-holiday + half-day aware.
- **Observability**: WS staleness alarm (halt + page if no ticks during market hours),
reconnect metrics, execution-failure alerts, periodic RAM-vs-OANDA reconciliation, daily P&L vs
drawdown limits -> Telegram + Cloud Monitoring.
- **Security**: Secret Manager + attached SA (no key files); least-privilege per component.
- **Backtest parity**: the strategy library and risk module are importable by an offline backtest
harness running over BigQuery history — identical code path to live.

---



## 5. What changed vs. the original diagram


| Original                                             | Revised                                                       |
| ---------------------------------------------------- | ------------------------------------------------------------- |
| Strategy math + decision in Cloud Run (sync)         | Entire hot path in-process on the VM                          |
| "All GCP integrations via Pub/Sub" (incl. execution) | Pub/Sub async-only; no sync cross-service call on hot path    |
| OANDA written from VM and Cloud Run                  | Single writer (VM) with client-order-id idempotency           |
| Two Risk Managers                                    | One unified risk module (per-trade + portfolio + kill switch) |
| Dev/prod in one project via a flag                   | Separate projects; live gated by OANDA host                   |
| Recovery = open trades only                          | Recovery also rebuilds opening ranges + daily-trade guard     |
| News filter unspecified                              | Named provider, cached, fail-safe; + trading-holiday calendar |
| Alerting = placeholder                               | Defined liveness/staleness/reconciliation/drawdown signals    |


