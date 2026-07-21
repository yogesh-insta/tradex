package nserotator

import "testing"

func TestBuildOrdersRotation(t *testing.T) {
	holdings := []Holding{
		{Symbol: "TITAN", Qty: 10, AvgPrice: 3000},
		{Symbol: "INFY", Qty: 50, AvgPrice: 1500},
	}
	target := []string{"TITAN", "BAJFINANCE"}
	prices := map[string]float64{"TITAN": 3500, "INFY": 1400, "BAJFINANCE": 7000}

	res := BuildOrders(holdings, target, prices, 100000, 2)

	if len(res.Sells) != 1 || res.Sells[0].Symbol != "INFY" || res.Sells[0].Qty != 50 {
		t.Fatalf("want full SELL INFY 50, got %+v", res.Sells)
	}
	if len(res.Holds) != 1 || res.Holds[0] != "TITAN" {
		t.Fatalf("want HOLD TITAN, got %+v", res.Holds)
	}
	// per slot 50000 / 7000 = 7.14 → 7
	if len(res.Buys) != 1 || res.Buys[0].Symbol != "BAJFINANCE" || res.Buys[0].Qty != 7 {
		t.Fatalf("want BUY BAJFINANCE 7, got %+v", res.Buys)
	}
}

func TestBuildOrdersCashRegimeSellsEverything(t *testing.T) {
	holdings := []Holding{
		{Symbol: "A", Qty: 5, AvgPrice: 10},
		{Symbol: "B", Qty: 7, AvgPrice: 20},
	}
	res := BuildOrders(holdings, nil, map[string]float64{"A": 12}, 100000, 8)
	if len(res.Sells) != 2 || len(res.Buys) != 0 || len(res.Holds) != 0 {
		t.Fatalf("cash regime must sell all: %+v", res)
	}
	// price unknown for B → approx 0 but order still present
	for _, s := range res.Sells {
		if s.Symbol == "B" && s.ApproxValue != 0 {
			t.Errorf("unknown price should give 0 approx, got %v", s.ApproxValue)
		}
	}
}

func TestBuildOrdersSkipsUnaffordableShare(t *testing.T) {
	res := BuildOrders(nil, []string{"MRF"}, map[string]float64{"MRF": 150000}, 400000, 8)
	// slot = 50000 < one MRF share → no buy
	if len(res.Buys) != 0 {
		t.Fatalf("unaffordable share must be skipped, got %+v", res.Buys)
	}
}

func TestInrFormatting(t *testing.T) {
	cases := map[float64]string{
		999:      "999",
		1000:     "1,000",
		123456:   "1,23,456",
		1234567:  "12,34,567",
		12345678: "1,23,45,678",
	}
	for in, want := range cases {
		if got := inr(in); got != want {
			t.Errorf("inr(%v)=%q want %q", in, got, want)
		}
	}
}
