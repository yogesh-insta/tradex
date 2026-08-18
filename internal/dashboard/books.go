package dashboard

// nseBookHistory is the piecewise holdings record used to reconstruct a daily
// equity curve until live snapshots exist. ALCODIS is omitted throughout
// (frozen / untradeable, tracked outside the strategy).
func nseBookHistory() []bookSlice {
	return []bookSlice{
		{
			AsOf:  "2026-07-24",
			Label: "Rotator book from Kite screenshots",
			Holdings: []bookHolding{
				{Symbol: "LAURUSLABS", Qty: 450, Avg: 1572.64},
				{Symbol: "BHEL", Qty: 1500, Avg: 418.30},
				{Symbol: "NATIONALUM", Qty: 1500, Avg: 343.17},
				{Symbol: "MCX", Qty: 200, Avg: 2789.00},
				{Symbol: "BHARATFORG", Qty: 300, Avg: 2193.33},
				{Symbol: "SHRIRAMFIN", Qty: 900, Avg: 1057.45},
				{Symbol: "CGPOWER", Qty: 470, Avg: 917.00},
				{Symbol: "PREMIERENE", Qty: 400, Avg: 1032.44},
				{Symbol: "POWERINDIA", Qty: 20, Avg: 32230.00},
				{Symbol: "CUMMINSIND", Qty: 105, Avg: 5575.00},
				{Symbol: "NYKAA", Qty: 1500, Avg: 320.00},
				{Symbol: "RADICO", Qty: 150, Avg: 4056.45},
				{Symbol: "ENRIN", Qty: 150, Avg: 3270.00},
			},
		},
		{
			AsOf:  "2026-08-02",
			Label: "Dropped ALCODIS from the strategy book",
			Holdings: []bookHolding{
				{Symbol: "LAURUSLABS", Qty: 450, Avg: 1572.64},
				{Symbol: "BHEL", Qty: 1500, Avg: 418.30},
				{Symbol: "NATIONALUM", Qty: 1538, Avg: 347.17},
				{Symbol: "MCX", Qty: 200, Avg: 2789.00},
				{Symbol: "BHARATFORG", Qty: 300, Avg: 2193.33},
				{Symbol: "SHRIRAMFIN", Qty: 900, Avg: 1057.45},
				{Symbol: "CGPOWER", Qty: 644, Avg: 908.36},
				{Symbol: "PREMIERENE", Qty: 400, Avg: 1032.44},
				{Symbol: "POWERINDIA", Qty: 20, Avg: 32369.13},
				{Symbol: "CUMMINSIND", Qty: 105, Avg: 5599.07},
				{Symbol: "NYKAA", Qty: 1500, Avg: 320.00},
				{Symbol: "RADICO", Qty: 150, Avg: 4056.45},
				{Symbol: "ENRIN", Qty: 150, Avg: 3270.00},
			},
		},
		{
			AsOf:  "2026-08-18",
			Label: "Combined Kite + Groww; CUMMINSIND / NYKAA / ENRIN gone",
			Holdings: []bookHolding{
				{Symbol: "LAURUSLABS", Qty: 450, Avg: 1572.64},
				{Symbol: "BHEL", Qty: 1900, Avg: 417.42},
				{Symbol: "NATIONALUM", Qty: 1978, Avg: 350.77},
				{Symbol: "MCX", Qty: 300, Avg: 2743.04},
				{Symbol: "BHARATFORG", Qty: 355, Avg: 2196.30},
				{Symbol: "SHRIRAMFIN", Qty: 900, Avg: 1057.45},
				{Symbol: "CGPOWER", Qty: 892, Avg: 896.69},
				{Symbol: "PREMIERENE", Qty: 750, Avg: 1039.94},
				{Symbol: "POWERINDIA", Qty: 24, Avg: 32384.20},
				{Symbol: "RADICO", Qty: 180, Avg: 4139.00},
			},
		},
	}
}
