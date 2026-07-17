To replicate the institutional-grade, regime-adaptive architecture for the US index market—focusing heavily on US100 (Nasdaq 100) and SPX500 (S&P 500)—the system must shift away from the steady, trending model used for European indices. [1, 2, 3] 
The US market is heavily dominated by algorithmic liquidity providers, high-frequency derivatives hedging (like 0DTE options), and highly volatile institutional positioning around the market open. To trade these indices successfully, your architecture must deploy an Opening Range Liquidity Sweep Engine that exploits retail breakout traps to secure high-probability, low-slippage execution.
------------------------------
## 1. Market Nuances & Structural Arbitrage (The US Edge)
The US cash market open at 09:30 EST (New York Open) triggers a massive surge of institutional order routing. Because these indices are highly visible and heavily traded by retail participants, major liquidity providers routinely engineer fake breakouts (liquidity sweeps) to trap premature momentum buyers or short-sellers, absorbing their stop-losses before moving the index in the true direction.

       [09:30 EST] New York Opening Bell ──> Sudden Liquidity Shock / Erratic Price Discovery
                                      │
                                      ▼
       [09:30–09:45 EST] Initial Balance Formation ──> Establish Local High & Low Boundaries
                                      │
                                      ▼
       [09:45–10:30 EST] The Liquidity Sweep Window ──> Stop-Hunting / Retail Breakout Traps

The strategy focuses entirely on this 09:45 to 10:30 EST window. Instead of joining a breakout, the bot waits for the index to briefly breach the early morning range boundaries, monitors for an immediate institutional reversal, and executes a counter-trend position to capture the true daily expansion.
------------------------------
## 2. End-to-End System Flow Architecture
By adhering to your established plug-and-play architecture, the US Strategy module drops cleanly into your existing hybrid system:

[OANDA WebSockets] ──> (Real-Time Pricing) ──> [Always-On Go VM Engine]
                                                        │
                                         (09:45 EST Trigger Condition)
                                                        │
                                                        ▼
[BigQuery Logs] <── (Record Telemetry) <── [Cloud Run Strategy Engine]
                                                        │
                                              (Signal Matrix Verified)
                                                        │
                                                        ▼
[OANDA REST API] <── (Execute Order) <── [VM Transaction Gateway]


   1. Passive Monitoring: The Go application on the e2-micro VM tracks real-time price ticks via OANDA's WebSockets, keeping a rolling 15-minute high/low anchor in its local, low-overhead memory.
   2. Serverless Invocation: If a tick breaks the 15-minute range boundary after 09:45 EST, the VM routes a JSON payload containing the last 30 candles and current account equity parameters to Cloud Run.
   3. Algorithmic Verification: Cloud Run spins up, runs the sweep validation matrix, calculates the exact fractional position sizing against your $5,000 capital, and returns a transaction directive.
   4. Execution & Auditing: The VM processes the trade via OANDA's REST endpoints using a unique client ID to prevent double-execution, while simultaneously writing structural trade metadata to BigQuery.

------------------------------
## 3. The US Opening Range Institutional Liquidity Sweep Strategy
This algorithm identifies true institutional reversals by verifying that a breach of the opening 15-minute range is an exhausted fake-out rather than a valid structural trend breakout.

                    [09:30–09:45 EST: Record Initial High/Low Range]
                                         │
                                         ▼
                 [09:45–10:30 EST: Price Spikes Beyond Range Boundary]
                                         │
                                         ▼
            ┌────────────────────────────┴────────────────────────────┐
            ▼                                                         ▼
  [Price Spikes ABOVE Range High]                           [Price Spikes BELOW Range Low]
                +                                                         +
  [5-Min Candle Closes BACK INSIDE Range]                   [5-Min Candle Closes BACK INSIDE Range]
                +                                                         +
  [RSI(14) > 70 Overbought Extreme]                         [RSI(14) < 30 Oversold Extreme]
                │                                                         │
                ▼                                                         ▼
       [Execution: SELL SHORT]                                   [Execution: BUY LONG]

## Step 1: Defining the Initial Balance

* Between 09:30:00 EST and 09:44:59 EST, the VM records every price tick.
* At exactly 09:45:00 EST, it locks the Initial High and Initial Low of that 15-minute window.

## Step 2: The Liquidity Sweep Trigger

* The system monitors the index on a 5-minute candlestick chart. [4] 
* Short Condition Structure: The price must move above the Initial High, creating a standard technical breakout signal. However, if the 5-minute candle fails to maintain momentum and closes back inside the 15-minute range boundary, a sweep is flagged.
* Long Condition Structure: The price drops below the Initial Low, trapping retail short-sellers, but the 5-minute candle immediately reverses and closes back inside the range.

## Step 3: Core Algorithmic Filters
To prevent getting run over by a true structural macro trend (such as on an unexpected Federal Reserve announcement), the Cloud Run engine runs two validation matrices:

* The Reversion Oscillator Check: For a Short Entry, a 14-period RSI on the 5-minute chart must print an overbought extreme (>70) during the outer spike, confirming buying exhaustion. For a Long Entry, the RSI must print an oversold extreme (<30).
* The Volume Verification: The volume on the reversal candle (the candle that closed back inside the range) must show an expansion greater than the 20-period volume moving average. This confirms that institutional algorithms are actively absorbing the retail breakout liquidity.

------------------------------
## 4. Mathematical Risk Management & Position Sizing
Operating with a strict $5,000 capital base means your algorithm must focus heavily on defensive exposure mitigation. US tech indices carry high point-values and require precision risk calculations.
## The Sizing Calculation

* Risk Ceiling: Hard-cap the trade risk at 1% of your total portfolio equity ($50 total exposure).
* Stop-Loss Placement: Place the initial stop-loss exactly 2 pips beyond the high/low apex point of the fake-out spike wick. This point represents absolute structural invalidation; if the market breaks past it again, a true trend is forming.
* Unit Sizing Equation:
$$\text{Units} = \frac{\text{Risk Capital (\$50)}}{\text{Stop-Loss Distance in Points} \times \text{Index Point Value}}$$ 
* Leverage Safe-Guard: If the stop-loss wick is extremely wide due to high opening volatility, the calculated unit size might require excessive margin. If the required OANDA margin exceeds 15% of your available balance ($750), the system automatically aborts the setup to keep effective leverage tight. [5] 

## Target Management

* Take-Profit Levels: The primary target is placed precisely at the opposite boundary of the 15-minute initial range. Because the range itself forms the risk-to-reward boundary, this setup typically yields an asymmetric 1:2.5 to 1:4 Risk-to-Reward Ratio.
* The Volatility Breakeven Shift: As soon as the position achieves a 1:1 risk-to-reward profit ratio, the VM engine fires an API command to move the OANDA stop-loss order directly to the execution entry price, guaranteeing a risk-free trade.

------------------------------
## 5. Blind Spots, Anomalies, and Safety Guardrails
Because the US market operates with intense velocity, you must program strict exception handling directly into your strategy variables.

* Macro Data Exclusions (The 08:30 / 10:00 EST Block): Critical economic catalysts—such as Consumer Price Index (CPI), Non-Farm Payrolls (NFP), or Institute for Supply Management (ISM) data—are released either at 08:30 EST or 10:00 EST. If an ISM report drops at 10:00 EST, price action will become violently erratic. Program the bot to completely bypass any sweep setups occurring within 15 minutes of a high-impact US macroeconomic release.
* The Trend-Day Filter: If the US market gaps open by more than 1.5% relative to the previous day's close, it indicates massive structural imbalances (e.g., global systemic news). On these rare "Trend Days," the market will likely break out and never look back, rendering mean-reversion sweep strategies unprofitable. If the daily opening gap exceeds a 1.2% threshold, the system automatically deactivates the Sweep module for that session.
* Hard Time-Cutoff: The edge of the opening liquidity sweep decays rapidly as European markets approach their local lunch hour and New York volume stabilizes. Ensure your Go system enforces a strict time-decay exit: if a trade is open but has not achieved its profit target by 11:30 EST, the bot triggers an unconditional market order to exit the position, protecting your capital from afternoon consolidation chop.
