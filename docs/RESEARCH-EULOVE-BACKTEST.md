# EU LOVE Strategy Backtest Research Setup

This document describes the research framework set up on branch `feature/eulove-backtest-research` for systematic evaluation and improvement of the EU LOVE strategy.

## Changes Made

### 1. Metrics Infrastructure
- **File**: `internal/backtest/metrics.go`
- **Change**: Added `AvgR` (average Risk/Reward multiple) to `Metrics` struct
  - Calculated as: sum of (RealizedPL / RiskDistance) / num_trades
  - Interpretation: average number of risk units gained/lost per trade
  - Baseline DE30: 0.01 (critical issue: winners make ~1/100th of risk)

### 2. Configuration Extensions
- **Files**: `internal/config/config.go`, `internal/config/validate.go`
- **Changes**:
  - Added `DailyCutoff` (string): daily flatten time, e.g., "17:30:00" (research only)
  - Added `RangeWidthMaxATR` (float64): skip days if opening range > this*DailyATR (research only)
  - Added `MaxEntriesPerDay` (int): cap same-day re-entries (research only)
- **Validation**: All new fields are optional (zeroed in prod config) and validated for correctness

### 3. Strategy Config Readiness
- **File**: `internal/strategy/eulove/eulove.go`
- **Change**: Updated `Config` struct to match YAML config keys
- **Note**: DailyCutoff plumbing already exists in `timeCutoff()` method; RangeWidthMaxATR and MaxEntriesPerDay need implementation in strategy logic

### 4. Baseline Results
- **DE30_EUR (2022-01-01 to 2026-07-01)**:
  - 677 trades over 4.5 years
  - 23.6% win rate (160 wins, 517 losses)
  - 0.01 Avg R (critical weakness)
  - 1.04 profit factor (barely profitable)
  - 19.9% max drawdown
  - Net profit: +594.63 on 5000 initial equity

## How to Run Experiments

### Basic Backtest Command
```bash
source .env
export OANDA_API_TOKEN
./backtester --config config/config.dev.yaml \
  --instrument DE30_EUR --from 2022-01-01 --to 2026-07-01
```

### Experiment A: TP ATR Multiplier
Create config variants:
```yaml
strategies:
  eu_love:
    tp_atr_mult: 0.8   # or 1.0, 1.5 for grid
    # ... rest unchanged
```

### Experiment B: Daily Cutoff
```yaml
strategies:
  eu_love:
    daily_cutoff: "17:30:00"
    # ... rest unchanged
```

### Experiment D: Volume Spike Multiplier
```yaml
strategies:
  eu_love:
    volume_spike_mult: 0.0   # or 1.0, 1.3 for ablation
    # ... rest unchanged
```

### Experiments C & E: Implementation Needed
These require strategy code changes:
- **C (Range Filter)**: Check `(opening_high - opening_low) > RangeWidthMaxATR * DailyATR` in `Analyze()` before signal generation
- **E (Max Entries)**: Track entries-per-day in backtest engine and skip signals when cap reached

## Critical Finding

The baseline strategy's Avg R of 0.01 is the primary blocker. This indicates:
1. Entries are on noise breakouts, not sustainable moves
2. TP targets (1.5 ATR) are being hit by mean reversion after breakout false signals
3. OR: Lots of scalps that barely work vs. a few big winners (unhealthy distribution)

**Recommendation**: Before running all 5 experiments, investigate:
- Distribution of Avg R per trade (some trades +10R, most -1R?)
- Correlation of Avg R with market regime (2022-2023 was choppy)
- Whether TP width is appropriate for M5 timeframe

## Test Coverage

All changes maintain test coverage:
- `go test ./internal/backtest/...` passes (metrics changes tested)
- `go test ./internal/config/...` passes (validation rules tested)
- `go test ./internal/strategy/eulove/...` passes (existing logic unchanged)

## Reproducibility

All backtests use identical setup:
- Initial Equity: 5,000.00
- Spread: 0.2 points
- Slippage: 0.1 points
- Date range: 2022-01-01 to 2026-07-01 (4.5+ years)
- Data: OANDA M5 candles (cached in `data/candles/`)

## Next Steps

1. **Complete FR40_EUR baseline** for comparison (in progress)
2. **Run Experiments A, B, D** (config-only; fastest path to results)
3. **Implement & test Experiments C, E** (strategy code changes needed)
4. **Combine best dimensions** into one final run
5. **Document findings** in summary table (out/eulove-research.md)
6. **Validate** that improvements hold across instruments (DE30 ≠ FR40)

## Files to Modify for Full Implementation

If implementing all experiments without waiting for feedback:

```
internal/strategy/eulove/eulove.go
  - In Analyze(): check RangeWidthMaxATR early return
  - TODO: track entries-per-day for MaxEntriesPerDay cap

internal/backtest/engine.go
  - TODO: pass daily entry count to strategy or track in engine

config/config.eulove.backtest.yaml (new, for experiment variants)
  - Create experiment_a_tp08.yaml, _tp10.yaml, _tp15.yaml
  - Create experiment_b_daily_cutoff.yaml
  - Create experiment_d_vol_*.yaml variants
```

---

**Branch**: `feature/eulove-backtest-research`  
**Status**: Infrastructure complete; baseline established; experiments ready to execute  
**Keep**: Yes, all changes are research-only and don't affect prod config
