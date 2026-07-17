To develop specific, specialized strategies for each major index group, we must design individual algorithmic logic that targets the precise structural quirks, institutional participants, and liquidity profiles of those unique assets.
By building specialized strategy modules, you can exploit regional anomalies while using a single, unified risk module to protect your capital.
------------------------------
## 1. US Markets (US100 / SPX500 / US30)

* Market Characteristics: High-velocity institutional algorithmic flows, heavy tech-driven momentum, and frequent "liquidity sweeps" (fake-outs) right before explosive moves.
* The Strategy: The Opening Range Institutional Liquidity Sweep Engine [1, 2] 

                  [ 09:30 EST: Define 15-Min Opening Range ]
                                       │
                  ┌────────────────────┴────────────────────┐
                  ▼                                         ▼
     [Price Sweeps Above High]                 [Price Sweeps Below Low]
  (Traps retail buyers, drops back in)      (Traps retail sellers, pops back in)
                  │                                         │
                  ▼                                         ▼
       [SHORT to Opening Low]                    [LONG to Opening High]

## Algorithmic Logic

* Step 1: At 09:30 EST (New York open), the bot enters a passive tracking state. It records the exact High and Low of the first 15 minutes of trading. [3, 4] 
* Step 2: The bot monitors for an institutional "stop-hunt" or sweep.
* Short Trigger: The price spikes above the 15-minute high, creating an illusion of a breakout, but immediately reverses and prints a candle closing back inside the opening range.
   * Long Trigger: The price spikes below the 15-minute low, traps retail short-sellers, and immediately closes back inside the opening range.
* Step 3 Execution: Enter immediately upon the internal close.
* Stop Loss: Placed exactly 2 pips beyond the extreme tip of the fake-out wick.
   * Take Profit: Set at the opposite side of the 15-minute opening range. This provides an asymmetric, high-reward-to-risk ratio. [5] 

------------------------------
## 2. European Markets (DE40 / FR40 / UK100)

* Market Characteristics: Highly orderly, persistent, and clean trends during European business hours. These indexes are heavily weighted with traditional industrial, banking, and luxury corporations that lack the hyper-volatile retail swings of US tech stocks.
* The Strategy: The London Open Volatility Extension (LOVE) Algorithm

## Algorithmic Logic

* Step 1: Between 07:00 and 08:00 GMT (Frankfurt open / London pre-market), calculate the historical Average True Range (ATR) over the last 14 days on the 1-hour chart. [6] 
* Step 2: At exactly 08:00 GMT (London Open), monitor the 5-minute chart for a decisive, high-volume candle breakout that moves more than 0.25 × Daily ATR in a single direction. [7] 
* Step 3 Execution: Unlike the US market, European breakouts are highly reliable and rarely sweep.
* Entry: Enter in the direction of the 08:00 GMT momentum breakout.
   * Stop Loss: Set at the daily Volume-Weighted Average Price (VWAP) line.
   * Take Profit: Set at a distance equal to 1.5 × Daily ATR from the entry price. This strategy aims to capture the bulk of the daily structural expansion. [8, 9, 10] 

------------------------------
## 3. Asian & Pacific Markets (JP225 / AU200 / HK33)

* Market Characteristics: Highly sensitive to overnight US stock market closes and domestic central bank updates (e.g., Bank of Japan interventions). Outside of major news, these markets are heavily mean-reverting and prone to trading inside strict horizontal channels.
* The Strategy: The Overnight Gap Reversal & Bollinger Mean-Reversion Bot

  [ New York Closes Deeply Positive ] ──> [ JP225 Gaps Up at Tokyo Open ]
                                                      │
                                                      ▼
                                       [ Price Hits Upper Bollinger Band ]
                                                      │
                                                      ▼
                                           [ Execution: SHORT ]
                                    (Fills the overnight gap to mean)

## Algorithmic Logic

* Step 1: At the market open for Tokyo (09:00 JST), check for an asset Opening Gap relative to the previous day’s close. [11] 
* Step 2: Apply a 20-period Bollinger Band (2.5 Standard Deviations) on the 15-minute chart.
* Short Condition: If the index gaps up significantly and open prices are resting above the upper Bollinger Band, institutional sellers will typically fade the retail gap excitement.
   * Long Condition: If the index gaps down significantly and open prices drop below the lower Bollinger Band, buyers will step in to cover short positions. [12, 13, 14, 15] 
* Step 3 Execution: Enter a mean-reversion counter-trend trade as soon as the first 15-minute candle closes back inside the Bollinger Band boundary.
* Stop Loss: Placed at a fixed distance of 1% of the index value.
   * Take Profit: Placed exactly at the 20-period Moving Average (the baseline mean). [16, 17, 18] 

------------------------------
## 4. Code Architecture Design: Strategy Interface Pattern
To manage these distinct market behaviors cleanly in your Go engine without mixing code bases, implement a Strategy Interface. This decouples your operational algorithms from your transaction and risk systems. [19] 

                  ┌──────────────────────────────┐
                  │    type Strategy interface   │
                  │  • Analyze(ticks) Signal     │
                  └──────────────┬───────────────┘
                                 │
         ┌───────────────────────┼───────────────────────┐
         ▼                       ▼                       ▼
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│  US_Sweep.go    │     │  EU_Breakout.go │     │ Asia_MeanRev.go │
│ (Implements     │     │ (Implements     │     │ (Implements     │
│  Strategy)      │     │  Strategy)      │     │  Strategy)      │
└─────────────────┘     └─────────────────┘     └─────────────────┘

## Memory Handling & Data Lifecycle for the Go Engine

* Custom Struct Mappings: When your OANDA bot initializes, it creates a registry map: map[string]Strategy. The key is the asset name (e.g., "NAS100_USD" maps to your US_Sweep logic struct; "DE30_EUR" maps to your EU_Breakout struct).
* Isolated Data Slices: Each strategy struct maintains its own small, sliding-window array of historical candle data in memory (e.g., maximum 50 candles). This keeps RAM use extremely low, preventing garbage collection spikes on your free-tier GCP VM.
* Concurrent Analysis: As OANDA streams live price ticks through a central WebSocket goroutine, the engine looks up the symbol in your map and spins up a lightweight goroutine to execute that asset's unique analysis function instantly.

------------------------------
