# Spec: FX Risk Profile (USD/JPY lane)

## Purpose

Define how the **unified risk module** (`06-risk-management.md`) behaves for the FX
account and `USD_JPY` / `FX_TRLD` signals. Risk remains one in-process module; this spec
is the **per-account / per-market profile** and the FX-only gates that protect expectancy
(weekend, reopen quiet, one-trade/day, spread, BOJ pause).

EU risk numbers and correlation groups are unchanged.

## Inputs

- `Signal` with `Strategy == "FX_TRLD"` and `Instrument == "USD_JPY"`.
- FX account state: equity, margin, open trades, today’s realized P&L, consecutive-loss
  counter, session-day trade counter for `USD_JPY`.
- `SystemState` (per FX market/account binding).
- `market_data_stale`; calendar state with FX-relevant regions.
- Latest spread for `USD_JPY` (from market-data snapshot).
- Instrument metadata (pip/point value, margin rate, min size, precision).
- Config: `fx_risk` (+ shared `risk` defaults where not overridden).

## Output

- Sized `OrderRequest` bound to the **FX account**, or rejection with machine reason.

## Behavior — gate order (FX account)

Same fail-fast order as `06`, with FX deltas called out:

1. **System state:** reject unless FX lane `ACTIVE`.
2. **Kill/breaker:** today’s FX realized loss ≤ `-daily_loss_limit` → `SYSTEM_LOCKED`,
   reject `daily_loss_breaker`.
3. **Data health:** reject `stale_market_data`.
4. **News window:** reject `news_blackout` if a high-impact event tagged for FX regions
   (`US`, `JP`, or instrument `USD_JPY`) is within `news_block_before` (default 60m for
   FX — wider than EU 30m). Fail-safe if calendar stale/unknown.
5. **Concurrency:** reject `already_open` if an open `USD_JPY` trade exists.
6. **One trade / session day:** reject `already_traded_today` if a `USD_JPY` entry was
   already accepted (filled or working) for the current Tokyo session date
   (`Asia/Tokyo`). Counts stop-outs — no same-day re-entry.
7. **Correlation:** FX v1 has a single instrument — no multi-name correlation group.
   (Future JPY crosses would add a group here.)
8. **Max concurrent:** reject if open positions on FX account ≥ `max_concurrent`
   (default 1).
9. **Consecutive losses:** ≥ `consecutive_loss_halt` (3) → lock, reject
   `consecutive_loss_breaker`.
10. **Spread gate:** reject `spread_too_wide` if snapshot spread in pips
    &gt; `max_spread_pips`.
11. **Weekend / reopen quiet:** reject `fx_session_closed` outside tradable FX hours;
    reject `reopen_quiet` for `reopen_quiet_minutes` after Sunday FX open.
12. **Friday entry cut:** reject `friday_entry_cut` for new entries after
    `friday_no_entry` (`America/New_York`, default 12:00) — flatten policy is separate.
13. **Sizing** — same formula as `06` (1% equity, SL distance, margin/leverage scale-down).

### Sizing

Identical equation to `06-risk-management.md`. FX point/pip value comes from OANDA
instrument metadata for `USD_JPY`. Defaults:

- `risk_per_trade: 0.01`
- `max_margin_frac: 0.10`
- `max_leverage: 5.0` (scale down; broker leverage may be higher — we self-cap)

### Weekend flatten & news (with trade-management)

Risk blocks **entries**. Exits:

| Situation | Action |
| --- | --- |
| Friday `friday_hard_flatten` (`America/New_York` 16:00 default) | Unconditional flatten via mgmt `TimeCutoff` / account policy |
| Soft cutoff `Asia/Tokyo` 21:00 and profitR &lt; 0.5 | Flatten (mgmt FX rule) |
| High-impact in `news_block_before` and trade in profit | Move SL → entry (BE) |
| High-impact window and trade underwater | Flatten |
| Manual / vol regime `PAUSE` (BOJ intervention) | No new entries; existing managed per PAUSE rules in control plane |

### Account isolation

- FX daily P&L baseline, consecutive-loss counter, and kill switch are **per FX
  account**. EU breaker does not lock FX and vice versa (unless operator issues a
  global command).
- `OrderRequest.Account` must be the FX account id.

## Config keys

```yaml
fx_risk:
  account_id: "${OANDA_FX_ACCOUNT_ID}"
  risk_per_trade: 0.01
  daily_loss_limit: 150          # size to account; example for ~$5k
  consecutive_loss_halt: 3
  max_concurrent: 1
  max_margin_frac: 0.10
  max_leverage: 5.0
  news_block_before: 60m
  max_spread_pips: 1.5
  one_trade_per_day: true
  reopen_quiet_minutes: 30
  friday_no_entry: "12:00:00"    # America/New_York
  friday_hard_flatten: "16:00:00" # America/New_York
  soft_cutoff_flatten_r: 0.5     # flatten if below this R at soft cutoff
  calendar_regions: ["US", "JP"]

# trade-management additions (see also 08)
mgmt:
  fx_friday_cutoff_tz: "America/New_York"
  fx_friday_cutoff: "16:00:00"
  fx_soft_cutoff_tz: "Asia/Tokyo"
  fx_soft_cutoff: "21:00:00"
```

Calendar config should include high-impact Fed, BOJ, US CPI/NFP, JP CPI for regions
`US`/`JP` (amendment to `10` / `13` when wiring).

## Failure modes

| Failure | Handling |
| --- | --- |
| FX equity unreadable | Reject all FX entries (`account_state_unknown`); alert |
| Calendar stale | `news_blackout` fail-safe |
| EU and FX signals same process tick | Independent accounts; no shared correlation |
| Spread missing | Reject `spread_unknown` (fail-safe) when `require_spread: true` (default true) |

## Acceptance criteria

- After one accepted `USD_JPY` signal on a Tokyo date, a second signal the same day is
  rejected `already_traded_today` even if the first trade already closed.
- Spread above `max_spread_pips` → `spread_too_wide` with no order.
- Friday after `friday_no_entry` → no new FX entries; open trades flatten by
  `friday_hard_flatten`.
- Sizing on $5,000 equity / 1% / 0.5×ATR stop ≈ $50 risk within rounding.
- Risk never calls OANDA; only emits `OrderRequest` to the executor with FX `Account`.
- EU correlation group unaffected by `USD_JPY` opens.

## Out of scope

- Entry matrix (`17`), session ATR/range (`16`), backtest promotion (`19`).
- Placing orders (executor), re-arm mechanics (control plane).
