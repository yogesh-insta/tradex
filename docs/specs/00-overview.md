# Tradex Specifications — Overview (v1 + FX lane)

This folder holds one spec per component. Specs are **normative** (what to build);
[`../architecture.md`](../architecture.md) is the system design rationale. FX lane
rationale: [`../fx-usdjpy-architecture.md`](../fx-usdjpy-architecture.md). Where they
disagree, architecture wins and the spec should be corrected.

## v1 scope (EU — shipped path)

- **Strategy:** EU LOVE (London Open Volatility Extension).
- **Instruments:** `DE30_EUR`, `FR40_EUR` (exact OANDA symbols verified via
  `GET /v3/accounts/{id}/instruments` at boot — see `07-order-executor.md`).
  AU practice uses `DE30_EUR` (DAX / Germany 40 CFD); conceptual docs may still
  say “Germany 40”.
- **Account:** one OANDA account for the EU market, funded ~$5,000 USD, `fxpractice`
  (paper) host for validation.
- **Deferred from EU v1:** US Sweep, Asia index mean-reversion, tick data lake.
  Offline backtest was not required for EU v1.

## FX lane scope (next — specs landed)

- **Strategy:** FX TRLD (Tokyo Range → London Drive) — see `15`–`19`.
- **Instrument:** `USD_JPY` on a **dedicated** OANDA account.
- **Validation:** mandatory offline backtest gates + paper soak before live (`19`).
- **Design:** [`../fx-usdjpy-architecture.md`](../fx-usdjpy-architecture.md).

## Component specs

Files are numbered in read/dependency order (shared types first, then the hot path from
market data to execution, then the supporting services and config, then the FX lane).

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
| 10 | [`10-economic-calendar.md`](./10-economic-calendar.md) | Economic-calendar poller (Finnhub → Gemini → Telegram) + durable GCS/file state + trading-holiday config |
| 11 | [`11-trade-ledger-persistence.md`](./11-trade-ledger-persistence.md) | Async publisher → Pub/Sub → BigQuery trade ledger |
| 12 | [`12-observability-and-alerts.md`](./12-observability-and-alerts.md) | Alerts, liveness, reconciliation, drawdown monitoring |
| 13 | [`13-configuration.md`](./13-configuration.md) | Per-environment config file + Secret Manager layout |
| 14 | [`14-dashboard.md`](./14-dashboard.md) | Read-only Cloud Run ops dashboard (open trades, health, calendar, per-day PL) |
| 15 | [`15-fx-usdjpy-overview.md`](./15-fx-usdjpy-overview.md) | FX lane charter: scope, account, success metrics |
| 16 | [`16-fx-session-controller.md`](./16-fx-session-controller.md) | Tokyo range lock, 14-day ATR, VWAP, VolMA |
| 17 | [`17-strategy-fx-trld.md`](./17-strategy-fx-trld.md) | FX TRLD `Analyze()` entry matrix → `Signal` |
| 18 | [`18-fx-risk-profile.md`](./18-fx-risk-profile.md) | FX account gates: one-trade/day, weekend, spread, news |
| 19 | [`19-fx-validation-backtest.md`](./19-fx-validation-backtest.md) | Offline harness + paper/live promotion gates |

## Conventions used in every spec

Each spec has: **Purpose · Inputs · Outputs · Behavior · Config keys · Failure modes ·
Acceptance criteria · Out of scope**.

- **Times** are IANA-zoned (`Europe/Berlin` for EU, `Asia/Tokyo` for FX session) and
  DST-aware. "CET" in prose means the Frankfurt wall-clock (`Europe/Berlin`), which is
  CET/CEST per season.
- **Money path is synchronous, in-process on the VM.** No component on the entry/exit
  path makes a cross-service call (see `architecture.md` §2).
- **Idempotency everywhere:** orders carry `clientExtensions.id`; ledger upserts key on
  `trade_id`; command handlers are safe to replay.
- **Config-driven:** no magic numbers in code. Every threshold below maps to a key in
  `13-configuration.md`.
