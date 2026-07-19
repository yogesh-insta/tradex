# Spec: FX Validation & Offline Backtest

## Purpose

Define the **validation path** for the FX TRLD lane: offline replay harness, cost model,
statistical **soft targets**, paper soak, and promotion guidance.

**Implementation status:** `cmd/backtester` today replays **EU LOVE only** (Berlin
session + `eulove`). The FX harness below is the **design contract** — not yet wired
to `fxtrld` / `FXSession` / `fx_risk`. Until it ships, paper soak on `fxpractice` is
the practical validation path; treat offline soft-target reports as a TODO.

**Decision B (locked):** soft targets — build and paper-trade; backtest metrics **guide**
promotion. They are **not** a hard live blocker. Missing a target means document and
revise `16`/`17` before sizing up capital — never loosen `18` kill-switch limits to
“pass” a report.

Stronger evidence than EU (which shipped paper-only without a required offline
harness), but intentionally softer than a mandatory gate.

Design: [`../fx-usdjpy-architecture.md`](../fx-usdjpy-architecture.md) §6.
Strategy under test: [`17-strategy-fx-trld.md`](./17-strategy-fx-trld.md) +
[`16-fx-session-controller.md`](./16-fx-session-controller.md) +
[`18-fx-risk-profile.md`](./18-fx-risk-profile.md).

## Inputs

- Historical OANDA (or equivalent) **D1** and **M5** candles for `USD_JPY`.
- Holiday / FX session calendar aligned with production config.
- Optional event list for stress slices (FOMC, BOJ, CPI, NFP).
- Production-equivalent config (`fx_session`, `strategies.fx_trld`, `fx_risk`) — no
  hidden overrides in the harness except cost-model knobs.

## Outputs

- Backtest report: trades, expectancy, win rate, profit factor, max DD, exposure calendar.
- Soft-target pass/miss annotations (informational for promotion review).
- Paper soak checklist result.
- Explicit **promotion decision** record (who/when/config hash) when moving to live.

## Behavior

### Pipeline

```text
Historical M5 + D1
    → rebuild SessionState (same rules as 16)
    → on each M5 close in window: fx_trld.Analyze() (17)
    → apply FX risk gates that are deterministic offline (18 subset)
    → simulate fills with cost model
    → apply mgmt: BE at +1R, Friday flatten, soft-cutoff weak flatten
    → aggregate metrics → soft-target report
```

Offline risk subset **must** include: one-trade/day, range/ATR preconditions (via
strategy), spread model, news blackout when event file provided, Friday no-entry /
flatten. Live-only gates (stale stream, account equity API) are N/A offline; paper
covers those.

### Cost model (defaults)

```yaml
fx_backtest:
  spread_pips: 1.0          # applied round-turn or per side — document in report
  slippage_pips: 0.2        # adverse on entry (and exit if modeled)
  pip_size: 0.01            # USD_JPY
  starting_equity: 5000
  sample:
    train_start: "2020-01-01"
    train_end:   "2024-06-30"
    test_start:  "2024-07-01"
    test_end:    "2025-12-31"
```

Report must state whether spread is charged once or twice and match the implementation.

### Required analyses

1. **In-sample (train)** — tune only within pre-declared config bounds; no tick-level
   curve fitting of dozens of free parameters.
2. **Out-of-sample (test)** — primary soft-target focus; train metrics are informational.
3. **Event stress** — separate slice for ±1 session day around FOMC/BOJ (and CPI/NFP if
   tagged); report expectancy and max adverse excursion.
4. **Weekend flat invariant** — zero simulated holds across FX weekend close.

### Soft targets (OOS — guide, not hard blockers)

| Target | Guidance |
| --- | --- |
| Expectancy | Mean R per trade &gt; 0 after costs |
| Trade count | ≥ `min_oos_trades` (default 80) so the sample is not noise |
| Max drawdown | ≤ `max_oos_dd_frac` of starting equity (default 0.15) |
| Daily loss stress | No simulated day breaches `daily_loss_limit` without the breaker
  locking further entries that day |
| Profit factor | ≥ `min_profit_factor` (default 1.1) |
| Weekend | No open position across Friday hard flatten → Sunday open |

If soft targets are missed → **revise matrix/windows** (`16`/`17`) and re-run; do **not**
loosen `18` kill-switch limits to improve the report. Paper FX may still run while
evidence is gathered.

### Paper soak (recommended after offline report)

- Run on OANDA `fxpractice` with the FX account, same config hash as the OOS report.
- Duration ≥ `paper_min_weeks` (default 4) as a soft target.
- Live spreads: reject rate for `spread_too_wide` logged; if &gt; `max_spread_reject_frac`
  of signals, revisit `max_spread_pips` before live.
- No critical control-plane / reconcile incidents.

### Live promotion (review)

- Small FX live account only after reviewing offline **and** paper evidence.
- Record: config hash, report paths, soft-target outcomes, approver, timestamp.
- First live phase: unchanged strategy params; only risk `daily_loss_limit` may be
  tightened, never loosened vs paper.

## Config keys

```yaml
fx_backtest:
  spread_pips: 1.0
  slippage_pips: 0.2
  pip_size: 0.01
  starting_equity: 5000
  min_oos_trades: 80
  max_oos_dd_frac: 0.15
  min_profit_factor: 1.1
  paper_min_weeks: 4
  max_spread_reject_frac: 0.40
  sample:
    train_start: "2020-01-01"
    train_end: "2024-06-30"
    test_start: "2024-07-01"
    test_end: "2025-12-31"
```

## Failure modes

| Failure | Handling |
| --- | --- |
| Missing M5 history gaps | Fail harness run; do not impute breakouts across gaps |
| Config drift vs prod | Flag in promotion record if hash ≠ paper/live intent |
| Soft target miss | Document; revise `16`/`17`; do not treat as automated hard block |
| Overfit suspicion (train ≫ OOS) | Soft flag: OOS expectancy should hold; document |

## Acceptance criteria

- Harness invokes the **same** `Analyze()` and session distiller logic as production
  packages (or a verified port with golden-vector parity tests).
- Golden vectors: fixed `(candles → SessionState → Signal)` fixtures match unit tests in
  `16`/`17`.
- OOS report is reproducible given the same data snapshot + config.
- Promotion to live records soft-target outcomes (pass/miss) even when proceeding.

## Out of scope

- Tick lake / full L2 simulation.
- ML parameter search.
- EU LOVE backtest (optional later; not required for FX promotion).
- Hard CI/deploy gates that refuse paper enablement when soft targets miss.
