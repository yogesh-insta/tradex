# Spec: FX USD/JPY Lane — Overview

## Purpose

Charter for the **FX TRLD** market lane: scope, instruments, account isolation, and
success metrics. Design rationale lives in
[`../fx-usdjpy-architecture.md`](../fx-usdjpy-architecture.md). Component behaviour is
normative in specs `16`–`19`.

This lane is **additive**. EU LOVE (`05`) and the EU account are unchanged.

## Scope

| Area | FX lane decision |
| --- | --- |
| Strategy | **FX TRLD** — Tokyo Range → London Drive (registry / `Signal.Strategy`: `fx_trld`) |
| Instrument | `USD_JPY` only (exact OANDA name verified at boot via instruments API) |
| Account | Dedicated OANDA account (`fx-usdjpy`), isolated margin + daily P&L from EU |
| Entry | MARKET after M5 close fully outside Tokyo range (see `17`) |
| Session clock | `Asia/Tokyo` (see `16`) |
| Validation | Offline backtest + paper soak are **soft targets** (`19`, decision B) |
| Deferred | Additional JPY crosses, US Sweep, Asia index mean-reversion |

## Inputs

- System architecture: [`../architecture.md`](../architecture.md),
  [`../fx-usdjpy-architecture.md`](../fx-usdjpy-architecture.md).
- Shared contracts: `01-data-contracts.md`.
- Shared hot-path components: market data, candles, risk iface, executor, mgmt, control
  plane, calendar, ledger, dashboard.

## Outputs

- Runnable FX lane behind the same strategy router + unified risk, bound to the FX
  account.
- Specs `16`–`19` as the build contract.
- Live promotion **guided** by `19` soft targets (Decision B) — not a hard blocker.

## Success metrics

| Metric | Soft target (guide) |
| --- | --- |
| Backtest expectancy (after spread/slippage model) | &gt; 0 |
| Backtest max drawdown | Within configured daily/consecutive kill-switch budget (stress) |
| Paper trading | Positive expectancy over configured `paper_min_weeks` with live spreads |
| Operational | One trade max per Tokyo session day; weekend flat; news blackouts observed |

## Component specs (this lane)

| # | Spec | Responsibility |
| --- | --- | --- |
| 16 | [`16-fx-session-controller.md`](./16-fx-session-controller.md) | Tokyo range lock, 14d ATR, VWAP, VolMA |
| 17 | [`17-strategy-fx-trld.md`](./17-strategy-fx-trld.md) | Entry matrix → `Signal` |
| 18 | [`18-fx-risk-profile.md`](./18-fx-risk-profile.md) | FX sizing, one-trade/day, weekend/news gates |
| 19 | [`19-fx-validation-backtest.md`](./19-fx-validation-backtest.md) | Soft-target harness design + promotion guidance |

## Failure modes

| Failure | Handling |
| --- | --- |
| Soft targets missed | Document; revise `16`/`17` before sizing up live; do not loosen `18` |
| FX account missing / wrong host | Boot fails closed for FX lane; EU unaffected |
| Calendar region misconfigured | Fail-safe news blackout for FX (`18`) |

## Acceptance criteria

- Router maps `USD_JPY` → FX session controller → `fx_trld` only.
- EU instruments never evaluate TRLD; `USD_JPY` never evaluates EU LOVE.
- Documentation cross-links architecture § FX lane and specs `15`–`19`.

## Out of scope

- Changing EU LOVE matrices, EU risk numbers, or EU session clocks.
