package asxrotator

import (
	"time"

	"github.com/yogesh-insta/tradex/internal/momentum"
	"github.com/yogesh-insta/tradex/internal/yahoo"
)

// Signal math lives in internal/momentum, shared with the NSE lane, so the
// entry/exit hysteresis has exactly one implementation. This file is the
// ASX-facing surface: aliases, the lane's own JSON-tagged Ranked type
// (market_cap_aud), and the min-price gate that has no NSE equivalent.

// DefaultMinPriceAUD is the price floor applied when config omits one.
// A$1.00 clears the back-adjustment wreckage (see Config.MinPriceAUD) and
// screens out sub-dollar names whose spreads make the strategy untradeable.
const DefaultMinPriceAUD = 1.00

// YahooSuffix is the market suffix for ASX symbols.
const YahooSuffix = ".AX"

type (
	Candle      = momentum.Candle
	Series      = momentum.Series
	YahooClient = yahoo.YahooClient
	QuoteDetail = yahoo.QuoteDetail
)

// MonthEnds returns the last close of each calendar month, ascending.
func MonthEnds(s Series) []float64 { return momentum.MonthEnds(s) }

// MomentumReturn computes the lookback-month momentum from month-end closes.
func MomentumReturn(monthEnds []float64, lookbackMonths int) (float64, bool) {
	return momentum.MomentumReturn(monthEnds, lookbackMonths)
}

// RegimeInvested reports whether ^AXJO's last close is above its EMA.
func RegimeInvested(index Series, emaDays int) (invested bool, lastClose, ema float64, ok bool) {
	return momentum.RegimeInvested(index, emaDays)
}

// ShouldHoldEquity reports whether a run builds a target portfolio at all.
// See docs/specs/22 § Regime filter for why the shipped config turns it off.
func ShouldHoldEquity(regimeInvested, regimeFilter bool) bool {
	return momentum.ShouldHoldEquity(regimeInvested, regimeFilter)
}

// Stale reports whether the series' latest candle is older than maxAge.
func Stale(s Series, now time.Time, maxAge time.Duration) bool {
	return momentum.Stale(s, now, maxAge)
}

// BadJumpUp reports a single-day UP move above maxMove in the last windowDays.
func BadJumpUp(s Series, windowDays int, maxMove float64) bool {
	return momentum.BadJumpUp(s, windowDays, maxMove)
}

// LargeDownJump reports a single-day DOWN move beyond maxMove.
func LargeDownJump(s Series, windowDays int, maxMove float64) bool {
	return momentum.LargeDownJump(s, windowDays, maxMove)
}

// BadJumpWindowDays sizes the jump screen for a lookback in months.
func BadJumpWindowDays(lookbackMonths int) int {
	return momentum.BadJumpWindowDays(lookbackMonths)
}

// BelowMinPrice reports whether the series' latest close is under minPrice —
// the ASX lane's mandatory data-quality gate. See Config.MinPriceAUD for why
// the jump screens are not sufficient on this market. minPrice <= 0 disables
// the screen; an empty series counts as below.
func BelowMinPrice(s Series, minPrice float64) bool {
	return momentum.BelowMinPrice(s, minPrice)
}

// Ranked is one symbol's momentum score as persisted in the recommendation.
// Lane-local because MarketCap is AUD here and INR in the NSE lane; sharing
// the type would mislabel one of them in stored JSON.
type Ranked struct {
	Symbol      string  `json:"symbol"`
	CompanyName string  `json:"company_name,omitempty"`
	LastClose   float64 `json:"last_close,omitempty"`
	Momentum    float64 `json:"momentum"`
	MarketCap   float64 `json:"market_cap_aud,omitempty"` // Yahoo summary; AUD for .AX
	// MomentumSlow/RankSlow place the same symbol on the exit lookback list.
	// RankSlow is 1-based; 0 means unranked there (insufficient history).
	MomentumSlow float64 `json:"momentum_slow,omitempty"`
	RankSlow     int     `json:"rank_slow,omitempty"`
}

// Rank sorts eligible symbols by momentum descending; alphabetical tie-break.
func Rank(scores map[string]float64) []Ranked {
	sorted := momentum.Rank(scores)
	out := make([]Ranked, 0, len(sorted))
	for _, s := range sorted {
		out = append(out, Ranked{Symbol: s.Symbol, Momentum: s.Momentum})
	}
	return out
}

// RankIndex maps symbol -> 1-based position in a ranked list.
func RankIndex(ranked []Ranked) map[string]int {
	return momentum.RankIndex(scoresOf(ranked))
}

// BuildTarget applies entry/exit hysteresis; see momentum.BuildTarget.
func BuildTarget(ranked, rankedSlow []Ranked, holdings []Holding, topK, exitN int) []string {
	held := make([]string, 0, len(holdings))
	for _, h := range holdings {
		held = append(held, h.Symbol)
	}
	return momentum.BuildTarget(scoresOf(ranked), scoresOf(rankedSlow), held, topK, exitN)
}

func scoresOf(ranked []Ranked) []momentum.Score {
	if ranked == nil {
		return nil
	}
	out := make([]momentum.Score, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, momentum.Score{Symbol: r.Symbol, Momentum: r.Momentum})
	}
	return out
}
