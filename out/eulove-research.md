# EU LOVE Strategy Backtest Research

## Baseline Results (DE30_EUR, 2022-01-01 to 2026-07-01)

| Metric | Value |
|--------|-------|
| Total Trades | 677 |
| Wins | 160 |
| Losses | 517 |
| Win Rate | 23.6% |
| Avg R (Risk/Reward) | 0.01 |
| Gross Profit | 15,552.42 |
| Gross Loss | 14,957.80 |
| Profit Factor | 1.04 |
| Net P&L | 594.63 |
| Max Drawdown | 1,040.80 (19.9%) |
| Initial Equity | 5,000.00 |
| Final Equity | 5,594.63 |

### Baseline Interpretation

The current EU LOVE strategy is marginally profitable with extremely concerning metrics:

1. **23.6% Win Rate**: Only 1 in 4 trades are winners. This is well below the ~40% typically needed for profitable systems without strong reward/risk ratios.

2. **0.01 Avg R**: The average winning trade only makes 0.01R (where R = risk per trade). This means on average, winners make almost nothing relative to losers. For comparison, a healthy strategy should have avg R of 0.5R or higher.

3. **1.04 Profit Factor**: Barely above 1.0 (where 1.0 = break-even in terms of gross profit/loss magnitude). The strategy is surviving only because of spread/slippage luck or because winners happen to be slightly larger than losers.

4. **High Drawdown (19.9%)**: The max drawdown of 19.9% relative to peak equity is substantial and represents significant underwater periods.

**Conclusion**: The baseline strategy is fundamentally broken. Before optimization, we need to understand why Avg R is so low. This suggests:
- Entry signals are generating excessive whipsaws
- Take profit targets are too tight (1.5 ATR may be being hit by noise)
- Stop losses may be well-placed but entry conditions are poor
- Volume filter may be allowing poor-quality breakouts

---

## Planned Experiments

### Experiment A: TP ATR Multiplier Grid
**Hypothesis**: Current 1.5 ATR TP target is either too tight (getting stopped out by noise) or too loose (missing profitable runner trades).

**Test Values**: 
- 0.8 ATR (tighter targets, faster winners but more noise sensitivity)
- 1.0 ATR (baseline midpoint)
- 1.5 ATR (current setting)

**Metrics to Track**: Win Rate change, Avg R change, Profit Factor, trade count

---

### Experiment B: Daily Time Cutoff
**Hypothesis**: End-of-day trades are more likely to whipsaw overnight. Adding a 17:30 CET daily flat (in addition to Friday 17:30) may reduce churn.

**Implementation**: Set `daily_cutoff: "17:30:00"` in config

**Metrics**: Trade reduction, win rate impact, Avg R change

---

### Experiment C: Opening Range Width Filter
**Hypothesis**: Days with very wide opening ranges (high volatility) may be noise-prone. Filtering them out could reduce breakout false signals.

**Test Values**:
- Skip days where (opening_high - opening_low) > 0.4 × DailyATR
- Skip days where (opening_high - opening_low) > 0.5 × DailyATR

**Implementation**: Check range width in Strategy.Analyze before entry signal generation

**Metrics**: Trade count reduction, win rate, Avg R, drawdown reduction

---

### Experiment D: Volume Spike Multiplier Ablation
**Hypothesis**: The volume spike condition is either too permissive (letting noise in) or too strict (filtering out momentum). Testing extremes will show impact.

**Test Values**:
- 0.0 (disable volume filter entirely, always enter on other conditions)
- 1.0 (current: volume > 1.0 × VolMA12)
- 1.3 (stricter: volume > 1.3 × VolMA12)

**Metrics**: Trade count, win rate, Avg R, profit factor

---

### Experiment E: Max Entries Per Day
**Hypothesis**: Multiple re-entries per day may be capturing whipsaws. Capping at 2 entries/day (one long or short exit + one re-entry attempt) could reduce losses.

**Test Value**: Set `max_entries_per_day: 2` (default 0 = unlimited)

**Tracking**: Count entries per day, measure reduced churn, win rate impact

---

## Combined Best-of-Breed Run

Once individual experiments are complete, run a final backtest combining the single best value from each dimension:
- TP ATR: [winner]
- Daily Cutoff: on/off based on results
- Range Width: [optimal threshold if effective]
- Volume Mult: [optimal value]
- Max Entries: [optimal cap]

---

## Research Notes

### Known Issues to Investigate
1. **Why is Avg R so low (0.01)?** This is the critical blocker. Potential causes:
   - Entry conditions are triggering on noise/wicks but not sustainable moves
   - TP is being hit by mean reversion after noise breakouts
   - SL is well-calibrated but entries are poor
   
2. **Volume Filter Effectiveness**: The VolMA12 baseline and 1.0x multiplier may not be optimal for DE30 index futures behavior.

3. **Regime Dependency**: EU LOVE is designed for breakout momentum. Performance during range-bound or mean-reversion regimes (2022-2023 was choppy) will be poor.

### Out of Scope (Future Research)
- Instrument comparison: FR40_EUR will be tested for consistency
- Timeframe experiments (M5 is fixed; H1 experiments would be separate research)
- Correlation with macro regimes (VIX, trend direction, economic calendar)
- Survivor bias in backtests (OANDA M5 data availability limits reach-back)

### Reproducibility
All experiments use the same config structure:
- Initial Equity: 5,000
- Spread: 0.2 points, Slippage: 0.1 points
- Date Range: 2022-01-01 to 2026-07-01 (same as baseline)
- Data Source: OANDA M5 candles (cached locally after first fetch)

---

## Recommendations After Research Completion

Before merging any changes to `main`:
1. Run FR40_EUR baseline and all experiments for consistency check
2. Identify which experiments move Avg R or win rate meaningfully (>5% relative change)
3. Flag experiments where improvement is within noise (fewer than ~100 trades difference or ±0.1 Profit Factor)
4. Combine best dimensions into one final config and validate over fresh out-of-sample period if possible
5. Document the "why" behind any parameter change (not just that it improves backtest)

No production config changes until Avg R is fixed. A system with 23.6% win rate and 0.01 R is not viable for live trading regardless of backtest profit.
