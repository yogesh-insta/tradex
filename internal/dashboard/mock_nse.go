package dashboard

import (
	"encoding/json"
	"math"
	"time"

	"github.com/yogesh-insta/tradex/internal/nserotator"
)

// mockNSEState builds a realistic rotator recommendation + portfolio for mock
// mode, so the NSE section (including the entry/exit hysteresis bands) can be
// demoed locally without GCS. Keys match the URIs Service.LatestNSEReport reads.
//
// The fixture is built to exercise the exit rule, not just the happy path:
// holdings sit inside the buy list, inside the hold-only buffer, and outside
// the fast list entirely (kept alive by the 12m list only).
func mockNSEState(now time.Time) map[string][]byte {
	const prefix = "gs://tradex-demo-state/nserotator"
	rec := mockRecommendation(now)
	recJSON, err := json.Marshal(rec)
	if err != nil {
		return nil
	}
	pfJSON, err := json.Marshal(mockPortfolio(now))
	if err != nil {
		return nil
	}
	eqJSON, err := json.Marshal(mockEquity("nse", "INR", now, 580000, 545000, 1.0018))
	if err != nil {
		return nil
	}
	return map[string][]byte{
		prefix + "/recommendation-" + now.Format("2006-01") + ".json": recJSON,
		prefix + "/portfolio.json":                                    pfJSON,
		prefix + "/equity-history.json":                               eqJSON,
	}
}

func mockEquity(lane, ccy string, now time.Time, cost, start float64, daily float64) EquityHistory {
	points := make([]EquityPoint, 0, 40)
	val := start
	for i := 45; i >= 0; i-- {
		d := now.AddDate(0, 0, -i)
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		val *= daily
		points = append(points, EquityPoint{
			Date: d.Format("2006-01-02"), Cost: cost, Value: math.Round(val),
			NHoldings: 10, Priced: 10, Source: "reconstructed",
		})
	}
	return EquityHistory{
		Lane: lane, Currency: ccy, UpdatedAt: now.Format(time.RFC3339),
		Note:   "mock equity history",
		Points: points,
		Events: []EquityEvent{
			{Date: now.AddDate(0, 0, -30).Format("2006-01-02"), Kind: "book", Label: "Mock book opened"},
			{Date: now.AddDate(0, 0, -3).Format("2006-01-02"), Kind: "algo", Label: "SELL TATACHEM, BUY HAL"},
		},
	}
}

// mockUniverse is ordered by 6m momentum (fast rank = index+1).
var mockUniverse = []struct {
	sym, name        string
	mom, momSlow     float64
	rankSlow         int
	price, mcapCrore float64
}{
	{"BEL", "Bharat Electronics", 0.94, 1.31, 2, 412, 301000},
	{"HAL", "Hindustan Aeronautics", 0.88, 1.04, 6, 5230, 349000},
	{"TRENT", "Trent", 0.81, 1.42, 1, 6890, 245000},
	{"DIXON", "Dixon Technologies", 0.76, 0.97, 8, 15420, 92000},
	{"PERSISTENT", "Persistent Systems", 0.71, 0.88, 11, 6140, 94000},
	{"MAZDOCK", "Mazagon Dock Shipbuilders", 0.68, 1.19, 3, 4310, 87000},
	{"COFORGE", "Coforge", 0.64, 0.79, 14, 8920, 59000},
	{"LTIM", "LTIMindtree", 0.61, 0.54, 21, 6280, 186000},
	{"BHARTIARTL", "Bharti Airtel", 0.58, 0.71, 17, 1840, 1042000},
	{"POLYCAB", "Polycab India", 0.55, 0.83, 12, 7410, 111000},
	{"CGPOWER", "CG Power & Industrial", 0.52, 1.08, 5, 812, 124000},
	{"KAYNES", "Kaynes Technology", 0.49, 0.92, 9, 5680, 36000},
	{"SUZLON", "Suzlon Energy", 0.46, 1.14, 4, 71, 97000},
	{"RVNL", "Rail Vikas Nigam", 0.43, 0.62, 19, 412, 86000},
	{"IRFC", "Indian Railway Finance", 0.41, 0.58, 20, 148, 193000},
	{"JSWENERGY", "JSW Energy", 0.38, 0.66, 18, 632, 110000},
	{"PRESTIGE", "Prestige Estates", 0.35, 0.44, 26, 1720, 74000},
	{"LODHA", "Macrotech Developers", 0.33, 0.51, 23, 1284, 128000},
	{"MOTHERSON", "Samvardhana Motherson", 0.31, 0.47, 25, 182, 128000},
	{"ZOMATO", "Eternal (Zomato)", 0.29, 1.02, 7, 268, 236000},
	{"PAYTM", "One97 Communications", 0.27, 0.90, 10, 812, 51000},
	{"IDEA", "Vodafone Idea", 0.24, 0.81, 13, 14, 98000},
	{"NATIONALUM", "National Aluminium", 0.22, 0.77, 15, 214, 39000},
	{"HINDZINC", "Hindustan Zinc", 0.20, 0.73, 16, 512, 216000},
	{"VEDL", "Vedanta", 0.18, 0.56, 22, 462, 180000},
	{"TATAPOWER", "Tata Power", 0.16, 0.49, 24, 428, 136000},
	{"BSE", "BSE Ltd", 0.14, 0.41, 28, 4820, 65000},
	{"MCX", "Multi Commodity Exchange", 0.12, 0.19, 41, 6210, 31000},
	{"ANGELONE", "Angel One", 0.10, 0.38, 29, 2640, 23000},
	{"KPITTECH", "KPIT Technologies", 0.08, 0.43, 27, 1380, 37000},
}

func mockRecommendation(now time.Time) nserotator.Recommendation {
	const topK, exitN = 10, 30
	ranked := make([]nserotator.Ranked, 0, len(mockUniverse))
	for _, u := range mockUniverse {
		ranked = append(ranked, nserotator.Ranked{
			Symbol: u.sym, CompanyName: u.name, LastClose: u.price,
			Momentum: u.mom, MomentumSlow: u.momSlow, RankSlow: u.rankSlow,
			MarketCap: u.mcapCrore * 1e7,
		})
	}
	// Holds: ranks 1, 3, 5, 7, 10 (buy list), 14 and 22 (hold-only buffer),
	// 28 (buffer, weak on 12m), and GRASIM — off the 6m list entirely, alive
	// only because it is 12m #12.
	holdsInfo := []nserotator.HoldInfo{
		{Symbol: "BEL", Rank: 1, RankSlow: 2},
		{Symbol: "TRENT", Rank: 3, RankSlow: 1},
		{Symbol: "PERSISTENT", Rank: 5, RankSlow: 11},
		{Symbol: "COFORGE", Rank: 7, RankSlow: 14},
		{Symbol: "POLYCAB", Rank: 10, RankSlow: 12},
		{Symbol: "RVNL", Rank: 14, RankSlow: 19},
		{Symbol: "IDEA", Rank: 22, RankSlow: 13},
		{Symbol: "MCX", Rank: 28, RankSlow: 41},
		{Symbol: "GRASIM", Rank: 0, RankSlow: 12},
	}
	holds := make([]string, 0, len(holdsInfo))
	for _, h := range holdsInfo {
		holds = append(holds, h.Symbol)
	}
	regimeFilterOff := false
	return nserotator.Recommendation{
		RunAt:          now.Format(time.RFC3339),
		Month:          now.Format("2006-01"),
		RegimeInvested: true,
		RegimeFilter:   &regimeFilterOff, // matches the shipped config
		NiftyClose:     26480,
		NiftyEMA:       24910,
		Orders: []nserotator.Order{
			// TATACHEM fell out of the top 30 on both lists — the only sell.
			{Side: "SELL", Symbol: "TATACHEM", CompanyName: "Tata Chemicals",
				Qty: 62, LastClose: 964, ApproxValue: 59768, MarketCap: 24500 * 1e7},
			{Side: "BUY", Symbol: "HAL", CompanyName: "Hindustan Aeronautics",
				Qty: 11, LastClose: 5230, ApproxValue: 57530, MarketCap: 349000 * 1e7},
		},
		Holds:     holds,
		HoldsInfo: holdsInfo,
		TopRanked: ranked,
		Params: nserotator.RunParamsRecord{
			LookbackMonths: 6, ExitLookbackMonths: 12, TopK: topK, ExitRankN: exitN,
		},
		Excluded: []string{
			"ADANIENSOL (excluded by policy)", "ADANIENT (excluded by policy)",
			"ADANIGREEN (excluded by policy)", "ADANIPORTS (excluded by policy)",
			"ADANIPOWER (excluded by policy)",
		},
		Warnings:    []string{"mock data — not a real recommendation"},
		MessageText: "mock",
	}
}

func mockPortfolio(now time.Time) nserotator.Portfolio {
	return nserotator.Portfolio{
		AsOf:            now.AddDate(0, 0, -3).Format("2006-01-02"),
		TotalCapitalINR: 600000,
		Holdings: []nserotator.Holding{
			{Symbol: "BEL", Qty: 145, AvgPrice: 341},
			{Symbol: "TRENT", Qty: 9, AvgPrice: 6120},
			{Symbol: "PERSISTENT", Qty: 10, AvgPrice: 5480},
			{Symbol: "COFORGE", Qty: 7, AvgPrice: 8110},
			{Symbol: "POLYCAB", Qty: 8, AvgPrice: 6890},
			{Symbol: "RVNL", Qty: 146, AvgPrice: 398},
			{Symbol: "IDEA", Qty: 4200, AvgPrice: 12},
			{Symbol: "MCX", Qty: 9, AvgPrice: 5940},
			{Symbol: "GRASIM", Qty: 21, AvgPrice: 2740},
			{Symbol: "TATACHEM", Qty: 62, AvgPrice: 1010},
		},
		Notes: "mock portfolio for local UI demos",
	}
}
