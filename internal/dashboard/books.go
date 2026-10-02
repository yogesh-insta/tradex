package dashboard

// nseBookHistory is a sample curve used until a live snapshot exists.
// Real holdings stay in Cloud Storage, not in git.
func nseBookHistory() []bookSlice {
	return []bookSlice{
		{
			AsOf:  "2026-01-01",
			Label: "Example book",
			Holdings: []bookHolding{
				{Symbol: "RELIANCE", Qty: 10, Avg: 1000},
				{Symbol: "TCS", Qty: 5, Avg: 4000},
			},
		},
	}
}
