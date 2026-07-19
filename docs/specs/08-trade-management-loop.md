# Spec: Trade-Management Loop (exits)

## Purpose

A single, market-agnostic **timer-driven** loop that applies the dynamic exit behaviours
brokers can't express on their own — **breakeven**, **time-cutoff flatten**, and
**news flatten** — on top of the always-on OANDA brackets (the primary, crash-proof
exit). Behaviour is data-driven per trade via `ManagementPolicy`; the loop itself is
common to all markets (see `architecture.md` §5).

## Inputs

- **Timer** — `time.Ticker`, `mgmt.tick_interval` (default 2s). Sole driver.
- **Price snapshot** — RAM read from `02-market-data-stream.md` (per instrument mid).
- **Open trades** — periodic reconcile via `executor.OpenTrades()`.
- **Calendar state** — RAM cache from `10-economic-calendar.md` (next high-impact event per region).
- **ManagementPolicy** — attached to each `OpenTrade` at entry.

## Output

- `ModifyStop` / `Close` calls **through the executor** (single writer). Trade events to
  the ledger via the publisher.

## Behavior — one pass per tick

```text
on each timer tick (if SystemState == ACTIVE):
  for each openTrade (from last reconcile):
      price   = snapshot[trade.instrument]
      profitR = signedProfit(price, trade.entry, trade.direction) / trade.riskDistance

      # 1. time-cutoff (unconditional flatten)
      if policy.TimeCutoff != zero and now >= policy.TimeCutoff:
          executor.Close(trade.id); continue

      # 2. news flatten (stops -> breakeven before high-impact events)
      if calendar.minsToHighImpact(trade.region) <= news_block_before:
          if trade.currentSL != trade.entry:
              executor.ModifyStop(trade.id, trade.entry)

      # 3. breakeven (once profit >= policy.BreakevenAtR)
      if profitR >= policy.BreakevenAtR:
          if trade.currentSL != trade.entry:
              executor.ModifyStop(trade.id, trade.entry)

  periodically (mgmt.reconcile_interval): openTrades = executor.OpenTrades()
```

**Correctness properties:**
- **Level-triggered, not edge-triggered** — checks state ("is SL already at entry?"),
  so a missed pass / restart / stale price never skips the action; the next pass applies
  it. Re-issuing an already-applied modify is a no-op.
- **Reconciled** — a trade closed by its bracket vanishes from `OpenTrades()` and is
  dropped silently.
- **Ticks not required** — price is a cached read; the loop keeps working through brief
  stream hiccups (brackets protect regardless).

### EU-specific policy values (v1)

- `BreakevenAtR = 1.0` (move SL→entry at +1R).
- `TimeCutoff`: **Friday 17:30 CET** hard flatten (Friday liquidity cliff). Non-Friday
  days: no hard time-cutoff for EU in v1 (positions ride to TP/SL/news); configurable.
- News flatten: move SL→entry `news_block_before` (30 min) ahead of a high-impact EU
  event; entries are already blocked by risk in that window.

### FX-specific extras (see also `18`)

Applied in `trademgmt` when the FX account loop has FX config enabled:

- **Friday NY hard flatten** (`fx_friday_cutoff`, default 16:00 `America/New_York`) —
  unconditional close (also seeded as `Policy.TimeCutoff` on Friday signals).
- **Soft cutoff weak flatten** (`fx_soft_cutoff` default 21:00 `Asia/Tokyo`): flatten
  only if `profitR < soft_cutoff_flatten_r` (default 0.5). **Not** via
  `Policy.TimeCutoff` (that field is unconditional).
- **News:** underwater → market flatten; in profit → move SL to entry (unlike EU
  always-BE).

### Trailing (optional)

- Pure trailing is offloaded to OANDA (`trailingStopLossOnFill`) when a policy sets
  `Trail`. EU/FX v1 sets `Trail=""` (no trailing). The loop never emulates trailing.

## Config keys

```yaml
mgmt:
  tick_interval: 2s
  reconcile_interval: 20s
  news_block_before: 30m
  eu_friday_cutoff: "17:30:00"   # Europe/Berlin
  eu_daily_cutoff: ""            # empty = none (v1)
  fx_friday_cutoff_tz: "America/New_York"
  fx_friday_cutoff: "16:00:00"
  fx_soft_cutoff_tz: "Asia/Tokyo"
  fx_soft_cutoff: "21:00:00"
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Snapshot stale | Use last price; brackets still protect; breakeven precision degrades gracefully |
| Executor modify/close fails | Retry next pass (idempotent); alert if persistent |
| Reconcile fails | Keep previous open-trades view; retry; alert if persistent |
| Calendar stale/unknown | Fail-safe: treat event imminent → move stops to breakeven |
| VM restart mid-trade | State recovery rebuilds open trades; loop resumes; level-trigger re-applies |

## Acceptance criteria

- With a trade at +1R and SL below entry, the next pass moves SL exactly to entry; a
  second pass issues no further modify.
- At Friday 17:30 CET, all open EU trades are market-closed within one
  `reconcile_interval`.
- Simulating a 30-min-to-CPI window moves open EU stops to breakeven and blocks entries.
- Disabling the timer stops all dynamic management but brackets still exit trades.

## Out of scope

- Entry decisions/sizing. Trailing emulation. US/Asia-specific policies (future values).
