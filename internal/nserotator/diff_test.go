package nserotator

import "testing"

func TestBuildOrdersRotation(t *testing.T) {
	holdings := []Holding{
		{Symbol: "TITAN", Qty: 10, AvgPrice: 3000},
		{Symbol: "INFY", Qty: 50, AvgPrice: 1500},
	}
	target := []string{"TITAN", "BAJFINANCE"}
	prices := map[string]float64{"TITAN": 3500, "INFY": 1400, "BAJFINANCE": 7000}

	res := BuildOrders(holdings, target, prices, 100000, 2, nil)

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
	res := BuildOrders(holdings, nil, map[string]float64{"A": 12}, 100000, 8, nil)
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
	res := BuildOrders(nil, []string{"MRF"}, map[string]float64{"MRF": 150000}, 400000, 8, nil)
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

// A frozen holding is untradeable: it must never appear as a SELL, even though
// it is absent from the target, and never as a BUY. The blocklist cannot do
// this — it yields a monthly SELL for a position that cannot be exited.
func TestBuildOrdersNeverTradesFrozenHoldings(t *testing.T) {
	holdings := holdingsOf("A", "STUCK", "B")
	frozen := map[string]bool{"STUCK": true}
	prices := map[string]float64{"A": 100, "B": 100, "C": 100, "STUCK": 50}

	res := BuildOrders(holdings, []string{"A", "C"}, prices, 100000, 2, frozen)

	for _, o := range append(append([]Order{}, res.Sells...), res.Buys...) {
		if o.Symbol == "STUCK" {
			t.Errorf("frozen symbol produced a %s order: %+v", o.Side, o)
		}
	}
	for _, h := range res.Holds {
		if h == "STUCK" {
			t.Error("frozen symbol must not be reported as a HOLD")
		}
	}
	if len(res.Frozen) != 1 || res.Frozen[0] != "STUCK" {
		t.Errorf("Frozen = %v, want [STUCK]", res.Frozen)
	}
	// B is not frozen and not in target, so it must still be sold.
	if len(res.Sells) != 1 || res.Sells[0].Symbol != "B" {
		t.Errorf("Sells = %+v, want just B", res.Sells)
	}
}

// Frozen names in the target (only reachable by calling BuildOrders directly)
// must not be bought.
func TestBuildOrdersSkipsFrozenBuys(t *testing.T) {
	res := BuildOrders(nil, []string{"STUCK", "A"}, map[string]float64{"STUCK": 10, "A": 10},
		100000, 2, map[string]bool{"STUCK": true})
	if len(res.Buys) != 1 || res.Buys[0].Symbol != "A" {
		t.Errorf("Buys = %+v, want just A", res.Buys)
	}
}
