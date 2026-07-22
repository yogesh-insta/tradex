package etfmonitor

import (
	"math"
	"sort"
	"time"
)

// Pure signal math. Mirrors kite/betashares/screen.py:
//   - returns   = close[last] / close[last-N] - 1   (N in trading days)
//   - trend     = close[last] > SMA(trendSMADays)
//   - vol       = stdev(daily pct change) * sqrt(252), SAMPLE stdev (pandas
//                 .std() defaults to ddof=1 — matching this matters)
//   - maxDD     = min(close / cummax(close) - 1)
//   - split guard rejects a series containing an unadjusted split
//
// Table-driven tests pin these to the Python screener's outputs.

// TradingDaysPerYear is pandas' annualization convention in screen.py.
const TradingDaysPerYear = 252

// DataBreakThreshold is the single-session move above which a series is treated
// as corrupted by an unadjusted split rather than as a real return. No ASX ETF
// — not even a 3x geared one — moves this far in a session; when it appears it
// is always a share consolidation the price feed failed to divide out.
// Observed live: BBOZ 2024-05-30 +10053%, BBUS 2025-12-01 +911% (the latter
// fabricated a +611% 12-month return and 526% vol, ranking #1 of 108).
const DataBreakThreshold = 0.5

// Candle is one daily close (adjusted when the feed provides adjclose).
type Candle struct {
	Date  time.Time
	Close float64
}

// Series is one fund's daily close history, ascending by date.
type Series struct {
	Ticker  string
	Candles []Candle
}

// Ret is a trailing return that may not be computable from the available history.
type Ret struct {
	Value float64
	OK    bool
}

// Metrics is the full read on one fund.
type Metrics struct {
	Returns  []Ret // aligned with the configured lookbacks
	SMA      float64
	TrendUp  bool
	HasTrend bool // false when history is shorter than the SMA window
	Vol      float64
	MaxDD    float64
	Days     int
}

// TrailingReturn is close[n-1]/close[n-lookbackTD] - 1, matching screen.py's
// `last / px.iloc[-days] - 1`. ok=false when history is too short.
func TrailingReturn(closes []float64, lookbackTD int) Ret {
	n := len(closes)
	if lookbackTD <= 0 || n <= lookbackTD {
		return Ret{}
	}
	base := closes[n-lookbackTD]
	if base <= 0 {
		return Ret{}
	}
	return Ret{Value: closes[n-1]/base - 1, OK: true}
}

// SMA is the mean of the last n closes. ok=false with insufficient data.
func SMA(closes []float64, n int) (float64, bool) {
	if n <= 0 || len(closes) < n {
		return 0, false
	}
	var sum float64
	for _, c := range closes[len(closes)-n:] {
		sum += c
	}
	return sum / float64(n), true
}

// AnnualizedVol is the sample standard deviation (ddof=1, pandas default) of
// daily simple returns, scaled by sqrt(252).
func AnnualizedVol(closes []float64) float64 {
	rets := dailyReturns(closes)
	if len(rets) < 2 {
		return 0
	}
	var mean float64
	for _, r := range rets {
		mean += r
	}
	mean /= float64(len(rets))
	var ss float64
	for _, r := range rets {
		ss += (r - mean) * (r - mean)
	}
	// ddof=1: divide by n-1, not n.
	return math.Sqrt(ss/float64(len(rets)-1)) * math.Sqrt(TradingDaysPerYear)
}

// MaxDrawdown is the worst peak-to-trough decline over the series (negative).
func MaxDrawdown(closes []float64) float64 {
	if len(closes) == 0 {
		return 0
	}
	peak := closes[0]
	worst := 0.0
	for _, c := range closes {
		if c > peak {
			peak = c
		}
		if peak > 0 {
			if dd := c/peak - 1; dd < worst {
				worst = dd
			}
		}
	}
	return worst
}

func dailyReturns(closes []float64) []float64 {
	if len(closes) < 2 {
		return nil
	}
	out := make([]float64, 0, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		if closes[i-1] <= 0 {
			continue
		}
		out = append(out, closes[i]/closes[i-1]-1)
	}
	return out
}

// DataBreak reports the date of the last single-session move exceeding
// threshold — the signature of an unadjusted split. A series with a break is
// REJECTED from ranking (spec 21 §Bad-data guard): its returns, vol and max
// drawdown are all corrupted, and vol/maxDD stay corrupted for years after the
// break falls out of every return window.
func DataBreak(s Series, threshold float64) (time.Time, bool) {
	var at time.Time
	var found bool
	for i := 1; i < len(s.Candles); i++ {
		prev, cur := s.Candles[i-1].Close, s.Candles[i].Close
		if prev <= 0 || cur <= 0 {
			continue
		}
		if math.Abs(cur/prev-1) > threshold {
			at, found = s.Candles[i].Date, true
		}
	}
	return at, found
}

// Compute derives every metric for one series.
func Compute(s Series, lookbacksTD []int, trendSMADays int) Metrics {
	closes := make([]float64, len(s.Candles))
	for i, c := range s.Candles {
		closes[i] = c.Close
	}
	m := Metrics{Days: len(closes), Vol: AnnualizedVol(closes), MaxDD: MaxDrawdown(closes)}
	for _, lb := range lookbacksTD {
		m.Returns = append(m.Returns, TrailingReturn(closes, lb))
	}
	if sma, ok := SMA(closes, trendSMADays); ok {
		m.SMA, m.HasTrend = sma, true
		m.TrendUp = closes[len(closes)-1] > sma
	}
	return m
}

// Score is the recency-tilted momentum blend (spec 21 §Selection).
//
//	score = 0.5*ret_3m + 0.3*ret_6m + 0.2*ret_12m
//
// Funds with less than the full history use only the lookbacks they have and
// RENORMALIZE the weights to sum to 1 (3m-only -> 1.0; 3m+6m -> 0.625/0.375),
// so a young fund is not penalised for missing windows. The shortest lookback
// is required — without a 3m return there is no recent momentum to tilt toward
// and the fund is not scored.
func Score(returns []Ret, weights []float64) (float64, bool) {
	if len(returns) == 0 || len(returns) != len(weights) {
		return 0, false
	}
	if !returns[0].OK {
		return 0, false // no recent momentum, no score
	}
	var sum, wsum float64
	for i, r := range returns {
		if !r.OK {
			continue
		}
		sum += weights[i] * r.Value
		wsum += weights[i]
	}
	if wsum <= 0 {
		return 0, false
	}
	return sum / wsum, true
}

// Classification labels momentum shape: "accelerating" when the most recent
// window annualizes above the longest one (fresh strength), else "trending".
// Spec 21 states the 3m-vs-12m comparison; when 12m history is absent the
// longest available window substitutes, and a fund with only a 3m read is "new".
func Classification(returns []Ret, lookbacksTD []int) string {
	if len(returns) == 0 || !returns[0].OK {
		return "new"
	}
	short := annualize(returns[0].Value, lookbacksTD[0])
	for i := len(returns) - 1; i >= 1; i-- {
		if !returns[i].OK {
			continue
		}
		long := annualize(returns[i].Value, lookbacksTD[i])
		if short > long {
			return "accelerating"
		}
		return "trending"
	}
	return "new"
}

func annualize(ret float64, lookbackTD int) float64 {
	if lookbackTD <= 0 {
		return ret
	}
	periods := float64(TradingDaysPerYear) / float64(lookbackTD)
	return math.Pow(1+ret, periods) - 1
}

// Scored is one ranked fund. Ret3M/6M/12M are conveniences for the report,
// derived from Returns (which is aligned with the configured lookbacks).
type Scored struct {
	Ticker         string  `json:"ticker"`
	Name           string  `json:"name"`
	Issuer         string  `json:"issuer,omitempty"` // set for non-Betashares funds
	Score          float64 `json:"score"`
	Ret3M          Ret     `json:"ret_3m"`
	Ret6M          Ret     `json:"ret_6m"`
	Ret12M         Ret     `json:"ret_12m"`
	Returns        []Ret   `json:"-"`
	TrendUp        bool    `json:"trend_up"`
	Vol            float64 `json:"vol"`
	MaxDD          float64 `json:"max_drawdown"`
	Classification string  `json:"classification"`
	Weight         float64 `json:"suggested_weight,omitempty"`
	// Watchlist bookkeeping: how much history the fund has vs how much the
	// trend gate needs. Zero for funds that already cleared the gate.
	Sessions       int `json:"sessions,omitempty"`
	SessionsNeeded int `json:"sessions_needed,omitempty"`

	group string // universe group; set by the run, read by the report
}

// SetGroup records which universe group this fund came from.
func (s *Scored) SetGroup(g string) { s.group = g }

// fillConvenienceReturns populates Ret3M/6M/12M from Returns positionally.
func (s *Scored) fillConvenienceReturns() {
	if len(s.Returns) > 0 {
		s.Ret3M = s.Returns[0]
	}
	if len(s.Returns) > 1 {
		s.Ret6M = s.Returns[1]
	}
	if len(s.Returns) > 2 {
		s.Ret12M = s.Returns[2]
	}
}

// RankByScore sorts descending by score with a deterministic alphabetical
// tie-break (same convention as the NSE rotator).
func RankByScore(in []Scored) []Scored {
	out := append([]Scored(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Ticker < out[j].Ticker
	})
	return out
}

// InverseVolWeights assigns w_i = (1/vol_i) / sum(1/vol), so calmer funds get
// more of the satellite sleeve. Funds with non-positive vol are given zero
// weight rather than infinite. Returns weights aligned with the input.
func InverseVolWeights(in []Scored) []float64 {
	inv := make([]float64, len(in))
	var total float64
	for i, s := range in {
		if s.Vol > 0 {
			inv[i] = 1 / s.Vol
			total += inv[i]
		}
	}
	out := make([]float64, len(in))
	if total <= 0 {
		return out
	}
	for i := range inv {
		out[i] = inv[i] / total
	}
	return out
}
