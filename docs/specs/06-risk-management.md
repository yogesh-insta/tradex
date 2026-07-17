# Spec: Unified Risk Module

## Purpose

The single source of truth for "can we take this trade, and at what size?" Consumes a
`Signal`, applies per-trade sizing and per-account gates, and either emits a sized
`OrderRequest` or rejects (with a reason). Also owns the daily loss breaker and kill
switch. Evaluated **per account** (v1: one EU account).

## Inputs

- `Signal` from the strategy engine.
- Account state: equity, available margin, open trades (from executor/reconcile),
  today's realized P&L, consecutive-loss counter.
- `SystemState` from the control plane (`ACTIVE`/`PAUSED`/`SYSTEM_LOCKED`/`DISABLED`).
- `market_data_stale` flag (`02-market-data-stream.md`); calendar state (`10-economic-calendar.md`).
- Instrument metadata (point value, margin rate, min size, precision) from executor.
- Config: risk numbers, correlation groups.

## Output

- `OrderRequest` (sized, idempotent) on approval, or a **rejection** with a machine
  reason (`logged`, surfaced to observability).

## Behavior — gate order (fail fast; first failure rejects)

1. **System state:** reject unless `ACTIVE` (`PAUSED`/`SYSTEM_LOCKED`/`DISABLED` → reject
   `system_not_active`).
2. **Kill/breaker:** if today's realized loss ≤ `-daily_loss_limit` ($150) → force
   `SYSTEM_LOCKED` and reject `daily_loss_breaker`. (Re-arm only via control plane.)
3. **Data health:** reject `stale_market_data` if the snapshot is stale.
4. **News window:** reject `news_blackout` if a high-impact event for this instrument's
   region is within `news_block_before` (30 min) — fail-safe if calendar is stale/unknown.
5. **Concurrency (per index):** reject `already_open` if an open trade exists for the
   instrument.
6. **Correlation guard:** reject `correlated_open` if any instrument in the same
   correlation group is open (v1 group: `{DE40_EUR, FR40_EUR}` → only one at a time).
7. **Max concurrent:** reject `max_concurrent` if open positions ≥ `max_concurrent`.
8. **Consecutive losses:** if the consecutive-loss counter ≥ `consecutive_loss_halt` (3)
   → force `SYSTEM_LOCKED`, reject `consecutive_loss_breaker`.
9. **Sizing** (only if all gates pass) — see below. Reject `size_too_small` if computed
   units < instrument min size; reject `margin_gate` if it can't be scaled to fit.

### Sizing

```
riskCapital = equity * risk_per_trade            # 1% => $50 on $5,000
stopPoints  = |Signal.EntryPrice - Signal.StopLoss| / pointSize
units       = floor( riskCapital / (stopPoints * pointValue) )
```

- **Margin/leverage gate:** required margin = `units * price * marginRate`. If it exceeds
  `max_margin_frac` of equity (10% = $500), **scale units down** until it fits AND
  effective leverage < `max_leverage` (5:1). If even min size violates the gate → reject.
- Round units to instrument precision; sign by direction.
- Emit `OrderRequest` with `StopLoss`, `TakeProfit`, `Policy`, `ClientOrderID`, `Account`.

### P&L / counters

- On trade close (from reconcile/ledger), update realized P&L (daily) and the
  consecutive-loss counter (reset on any win). These feed gates 2 and 8.
- Daily P&L baseline is the **equity snapshot** set at session start or at `RE_ARM`.

## Config keys

```yaml
risk:
  risk_per_trade: 0.01          # 1% of equity
  daily_loss_limit: 150         # USD; hard lock when realized loss hits this
  consecutive_loss_halt: 3
  max_concurrent: 1             # effective cap given correlation guard (EU)
  max_margin_frac: 0.10         # required margin <= 10% equity
  max_leverage: 5.0
  news_block_before: 30m
  correlation_groups:
    - ["DE40_EUR", "FR40_EUR"]
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Equity/margin unreadable | Reject all new entries (`account_state_unknown`); alert |
| Calendar stale/unknown | Treat as event imminent → `news_blackout` (fail-safe) |
| Two signals same tick (both EU) | Serialized; second sees the first's open → `correlated_open` |
| Breaker trips mid-eval | Lock immediately; managed positions keep broker stops |

## Acceptance criteria

- With one EU position open, a signal on the other EU instrument is rejected
  `correlated_open`.
- Sizing: on $5,000 equity, `risk_per_trade=0.01`, a stop distance of `0.5×ATR` yields
  units such that a stop-out loses ≈ $50 (within rounding/point-value).
- A cumulative realized loss reaching −$150 flips state to `SYSTEM_LOCKED` and rejects
  further entries until `RE_ARM`.
- 3 consecutive losing trades → `consecutive_loss_breaker` lock.
- Risk never calls OANDA directly; it only emits `OrderRequest` to the executor.

## Out of scope

- Placing/modifying orders (executor). Deciding entries (strategy). Re-arm mechanics
  (control plane).
