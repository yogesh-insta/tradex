// Package momentum holds the market-agnostic signal math shared by the
// rotation lanes (internal/nserotator, internal/asxrotator).
//
// Only pure functions live here: month-end resampling, momentum returns, EMA
// regime, data-quality screens, ranking, and the entry/exit hysteresis in
// BuildTarget. Anything market-specific — Yahoo symbol suffixes, calendars,
// currency, portfolio and message formatting — stays in the lane packages.
//
// Mirrors kite/backtest/momentum_dual.py:
//   - month-end resample of daily closes
//   - momentum = monthEnd[last] / monthEnd[last-lookback] - 1
//   - regime   = last daily close of the index > EMA(regimeEMADays)
//
// BuildTarget is the reason this package exists: it is subtle, it has already
// been corrected once in production, and a second copy would mean the next fix
// has to land twice. Lane packages wrap these functions rather than reimplement
// them.
package momentum

import (
	"sort"
	"time"
)

// Candle is one daily close (adjusted when the provider supplies adjclose).
type Candle struct {
	Date  time.Time
	Close float64
}

// Series is a symbol's daily close history, ascending by date.
type Series struct {
	Symbol      string
	CompanyName string
	Candles     []Candle
}

// LastClose returns the most recent close and whether the series has any data.
func (s Series) LastClose() (float64, bool) {
	if len(s.Candles) == 0 {
		return 0, false
	}
	return s.Candles[len(s.Candles)-1].Close, true
}

// Score is one symbol's momentum reading — the minimal shape ranking needs.
// Lane packages carry their own richer, JSON-tagged ranked types (currency
// units differ per market) and convert at the boundary.
type Score struct {
	Symbol   string
	Momentum float64
}

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

// ShouldHoldEquity reports whether a run builds a target portfolio at all.
// With regimeFilter off the index reading is advisory: the book stays invested
// through downtrends. With it on, a below-EMA index forces 100% cash and the
// order diff sells everything.
func ShouldHoldEquity(regimeInvested, regimeFilter bool) bool {
	return regimeInvested || !regimeFilter
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

// BelowMinPrice reports whether the series' latest close sits under minPrice.
// An empty series counts as below: no price is not a tradeable price.
//
// This is a distinct screen from BadJumpUp/LargeDownJump, which look for a
// single violent move. A provider's back-adjustment for a demerger can scale a
// whole history smoothly toward zero with no jump at all — Yahoo reports
// TAH.AX at A$0.0000 for 103 consecutive months — and momentum computed off a
// near-zero base then ranks first every month on a phantom four-figure return.
// Only an absolute floor catches that. minPrice <= 0 disables the screen.
func BelowMinPrice(s Series, minPrice float64) bool {
	if minPrice <= 0 {
		return false
	}
	last, ok := s.LastClose()
	if !ok {
		return true
	}
	return last < minPrice
}

// Rank sorts eligible symbols by momentum descending; deterministic
// alphabetical tie-break.
func Rank(scores map[string]float64) []Score {
	out := make([]Score, 0, len(scores))
	for sym, m := range scores {
		out = append(out, Score{Symbol: sym, Momentum: m})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Momentum != out[j].Momentum {
			return out[i].Momentum > out[j].Momentum
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// RankIndex maps symbol -> 1-based position in a ranked list.
func RankIndex(ranked []Score) map[string]int {
	idx := make(map[string]int, len(ranked))
	for i, r := range ranked {
		idx[r.Symbol] = i + 1
	}
	return idx
}

// BuildTarget applies entry/exit hysteresis (kite/backtest/momentum_dual.py):
//
//	ENTRY — the top topK of ranked (fast, e.g. 6m)
//	EXIT  — a holding is dropped only when it is outside the top exitN on BOTH
//	        ranked and rankedSlow (e.g. 12m)
//
// held is the current book in portfolio order. Survivors keep their existing
// order and their slots; remaining slots are filled from the fast list in rank
// order. The result never exceeds topK. exitN == topK reproduces plain top-K
// rotation.
//
// A holding absent from both lists — policy-excluded, stale, delisted, below
// the price floor, or never scored — is not in the keep set and is therefore
// sold.
//
// More survivors than topK is a seeded-book state the backtest never reaches
// (starting empty, |held| <= topK is preserved every month), so it only shows
// up live: a portfolio.json carrying more names than the strategy sizes for.
// The overflow is cut by weakest rank, never by position in the file — cutting
// by file order sold a rank-5 name to keep a rank-149 one on the 2026-08 run.
func BuildTarget(ranked, rankedSlow []Score, held []string, topK, exitN int) []string {
	if topK <= 0 {
		return nil
	}
	if exitN < topK {
		exitN = topK
	}
	keep := make(map[string]bool, 2*exitN)
	for i, r := range ranked {
		if i >= exitN {
			break
		}
		keep[r.Symbol] = true
	}
	for i, r := range rankedSlow {
		if i >= exitN {
			break
		}
		keep[r.Symbol] = true
	}

	survivors := make([]string, 0, len(held))
	seen := make(map[string]bool, len(held))
	for _, sym := range held {
		if keep[sym] && !seen[sym] {
			survivors = append(survivors, sym)
			seen[sym] = true
		}
	}
	if len(survivors) > topK {
		survivors = cutWeakest(survivors, ranked, rankedSlow, topK)
	}

	target := make([]string, 0, topK)
	inTarget := make(map[string]bool, topK)
	for _, s := range survivors {
		target = append(target, s)
		inTarget[s] = true
	}
	for i, r := range ranked {
		if i >= topK || len(target) == topK {
			break
		}
		if !inTarget[r.Symbol] {
			target = append(target, r.Symbol)
			inTarget[r.Symbol] = true
		}
	}
	return target
}

// cutWeakest keeps the topK strongest of survivors and drops the rest. Strength
// is a symbol's best (lowest) position across the two lists, so a name held by
// either lookback is judged on whichever ranks it higher — the same asymmetry
// the keep set uses. Survivors retain their input order; only membership is
// decided here. Ties break on symbol for determinism.
func cutWeakest(survivors []string, ranked, rankedSlow []Score, topK int) []string {
	fast, slow := RankIndex(ranked), RankIndex(rankedSlow)
	best := func(sym string) int {
		r := 1 << 30
		if i, ok := fast[sym]; ok && i < r {
			r = i
		}
		if i, ok := slow[sym]; ok && i < r {
			r = i
		}
		return r
	}
	byRank := append([]string(nil), survivors...)
	sort.Slice(byRank, func(i, j int) bool {
		ri, rj := best(byRank[i]), best(byRank[j])
		if ri != rj {
			return ri < rj
		}
		return byRank[i] < byRank[j]
	})
	kept := make(map[string]bool, topK)
	for _, s := range byRank[:topK] {
		kept[s] = true
	}
	out := survivors[:0:0]
	for _, s := range survivors {
		if kept[s] {
			out = append(out, s)
		}
	}
	return out
}
