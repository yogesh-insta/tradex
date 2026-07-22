package nserotator

import (
	"sort"
	"time"
)

// Pure signal math. Mirrors kite/backtest/momentum.py:
//   - month-end resample of daily closes
//   - momentum = monthEnd[last] / monthEnd[last-lookback] - 1
//   - regime   = last daily close of ^NSEI > EMA(regimeEMADays) of daily closes
// Table-driven tests cross-check fixtures against the Python backtest.

// MonthEnds returns the last close of each calendar month, ascending.
// The (possibly partial) current month contributes its latest close — at run
// time (last trading day of the month) that IS the month-end close.
func MonthEnds(s Series) []float64 {
	var out []float64
	var curKey int // year*100+month
	for _, c := range s.Candles {
		key := c.Date.Year()*100 + int(c.Date.Month())
		if key != curKey {
			out = append(out, c.Close)
			curKey = key
		} else {
			out[len(out)-1] = c.Close
		}
	}
	return out
}

// MomentumReturn computes the lookback-month momentum from month-end closes.
// ok=false when history is insufficient.
func MomentumReturn(monthEnds []float64, lookbackMonths int) (float64, bool) {
	n := len(monthEnds)
	if n < lookbackMonths+1 {
		return 0, false
	}
	base := monthEnds[n-1-lookbackMonths]
	if base <= 0 {
		return 0, false
	}
	return monthEnds[n-1]/base - 1, true
}

// EMA returns the exponential moving average (span n, pandas adjust=False
// convention: alpha = 2/(n+1), seeded with the first value) of the last
// element. Returns ok=false with insufficient data (< n points).
func EMA(closes []float64, n int) (float64, bool) {
	if len(closes) < n || n <= 0 {
		return 0, false
	}
	alpha := 2.0 / float64(n+1)
	ema := closes[0]
	for _, c := range closes[1:] {
		ema = alpha*c + (1-alpha)*ema
	}
	return ema, true
}

// RegimeInvested reports whether the index's last close is above its EMA.
func RegimeInvested(index Series, emaDays int) (invested bool, lastClose, ema float64, ok bool) {
	closes := make([]float64, len(index.Candles))
	for i, c := range index.Candles {
		closes[i] = c.Close
	}
	ema, ok = EMA(closes, emaDays)
	if !ok || len(closes) == 0 {
		return false, 0, 0, false
	}
	lastClose = closes[len(closes)-1]
	return lastClose > ema, lastClose, ema, true
}

// Stale reports whether the series' latest candle is older than maxAge
// relative to now (catches renames/delistings/suspensions).
func Stale(s Series, now time.Time, maxAge time.Duration) bool {
	if len(s.Candles) == 0 {
		return true
	}
	return now.Sub(s.Candles[len(s.Candles)-1].Date) > maxAge
}

// BadJumpUp reports whether any single-day UP move in the last windowDays
// exceeds maxMove. Upward spikes are usually bad feed data; exclude from momentum.
func BadJumpUp(s Series, windowDays int, maxMove float64) bool {
	return largeJump(s, windowDays, maxMove, true)
}

// LargeDownJump reports whether any single-day DOWN move exceeds maxMove.
// Downward gaps are often splits/demergers (e.g. VEDL ex-date); warn only.
func LargeDownJump(s Series, windowDays int, maxMove float64) bool {
	return largeJump(s, windowDays, maxMove, false)
}

func largeJump(s Series, windowDays int, maxMove float64, upward bool) bool {
	n := len(s.Candles)
	start := n - windowDays
	if start < 1 {
		start = 1
	}
	for i := start; i < n; i++ {
		prev := s.Candles[i-1].Close
		cur := s.Candles[i].Close
		if prev <= 0 || cur <= 0 {
			continue
		}
		move := cur/prev - 1
		if upward {
			if move > maxMove {
				return true
			}
		} else if move < -maxMove {
			return true
		}
	}
	return false
}

// BadJumpWindowDays is how many daily candles the bad-jump screen should cover
// for a momentum lookback of lookbackMonths. ~22 trading days/month plus buffer
// so the screen reaches at least as far back as the momentum base month-end.
func BadJumpWindowDays(lookbackMonths int) int {
	if lookbackMonths <= 0 {
		return 90
	}
	return lookbackMonths*22 + 10
}

// Ranked is one symbol's momentum score.
type Ranked struct {
	Symbol      string  `json:"symbol"`
	CompanyName string  `json:"company_name,omitempty"`
	LastClose   float64 `json:"last_close,omitempty"`
	Momentum    float64 `json:"momentum"`
	MarketCap   float64 `json:"market_cap_inr,omitempty"` // Yahoo summary; INR for .NS
}

// Rank sorts eligible symbols by momentum descending; deterministic
// alphabetical tie-break (spec 20).
func Rank(scores map[string]float64) []Ranked {
	out := make([]Ranked, 0, len(scores))
	for sym, m := range scores {
		out = append(out, Ranked{Symbol: sym, Momentum: m})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Momentum != out[j].Momentum {
			return out[i].Momentum > out[j].Momentum
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}
