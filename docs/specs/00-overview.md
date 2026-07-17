# Tradex Specifications — Overview (v1)

This folder holds one spec per component. Specs are **normative** (what to build);
[`../architecture.md`](../architecture.md) is the design rationale. Where they disagree,
architecture wins and the spec should be corrected.

## v1 scope

- **Strategy:** EU LOVE (London Open Volatility Extension) only.
- **Instruments:** `DE40_EUR`, `FR40_EUR` (exact OANDA symbols verified via
  `GET /v3/accounts/{id}/instruments` at boot — see `07-order-executor.md`).
- **Account:** one OANDA account for the EU market, funded ~$5,000 USD, `fxpractice`
  (paper) host for validation.
- **Deferred:** US Sweep, Asia mean-reversion, tick data lake, offline backtest harness.

## Component specs

Files are numbered in read/dependency order (shared types first, then the hot path from
market data to execution, then the supporting services and config).

| # | Spec | Responsibility |
| --- | --- | --- |
| 01 | [`01-data-contracts.md`](./01-data-contracts.md) | Canonical Go structs shared across all components |
| 02 | [`02-market-data-stream.md`](./02-market-data-stream.md) | OANDA price stream ingest, reconnect, staleness halt, price snapshot |
| 03 | [`03-candle-builder.md`](./03-candle-builder.md) | Multi-timeframe candle buffers (1h + 5m), authoritative OHLC via REST |
| 04 | [`04-eu-session-controller.md`](./04-eu-session-controller.md) | EU session clock, 08:00–09:00 range lock, 14-day ATR, running VWAP |
| 05 | [`05-strategy-eu-love.md`](./05-strategy-eu-love.md) | EU LOVE `Analyze()` entry matrix → `Signal` |
| 06 | [`06-risk-management.md`](./06-risk-management.md) | Sizing, per-account gates, correlation guard, kill switch |
| 07 | [`07-order-executor.md`](./07-order-executor.md) | Single OANDA writer: bracketed orders, idempotency, instrument metadata |
| 08 | [`08-trade-management-loop.md`](./08-trade-management-loop.md) | Timer loop: breakeven, time-cutoff, news flatten, reconcile |
| 09 | [`09-control-plane.md`](./09-control-plane.md) | HMAC command webhook, state machine, `FLATTEN`/`PAUSE`/`RESUME`/`RE_ARM` |
| 10 | [`10-economic-calendar.md`](./10-economic-calendar.md) | Economic-calendar poller + durable GCS state + trading-holiday config |
| 11 | [`11-trade-ledger-persistence.md`](./11-trade-ledger-persistence.md) | Async publisher → Pub/Sub → BigQuery trade ledger |
| 12 | [`12-observability-and-alerts.md`](./12-observability-and-alerts.md) | Alerts, liveness, reconciliation, drawdown monitoring |
| 13 | [`13-configuration.md`](./13-configuration.md) | Per-environment config file + Secret Manager layout |

## Conventions used in every spec

Each spec has: **Purpose · Inputs · Outputs · Behavior · Config keys · Failure modes ·
Acceptance criteria · Out of scope**.

- **Times** are IANA-zoned (`Europe/Berlin` for EU) and DST-aware. "CET" in prose means
  the Frankfurt wall-clock (`Europe/Berlin`), which is CET/CEST per season.
- **Money path is synchronous, in-process on the VM.** No component on the entry/exit
  path makes a cross-service call (see `architecture.md` §2).
- **Idempotency everywhere:** orders carry `clientExtensions.id`; ledger upserts key on
  `trade_id`; command handlers are safe to replay.
- **Config-driven:** no magic numbers in code. Every threshold below maps to a key in
  `13-configuration.md`.
