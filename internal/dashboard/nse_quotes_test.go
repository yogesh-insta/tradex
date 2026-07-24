package dashboard

import (
	"context"
	"testing"
	"time"
)

func TestEnrichPortfolioQuotes(t *testing.T) {
	portfolio := map[string]any{
		"holdings": []any{
			map[string]any{"symbol": "FOO", "qty": float64(10), "avg_price": 100.0},
			map[string]any{"symbol": "BAR", "qty": float64(5), "avg_price": 200.0},
			map[string]any{"symbol": "NA", "qty": float64(1), "avg_price": 50.0},
		},
	}
	prices := map[string]float64{"FOO": 110, "BAR": 180}
	enrichPortfolioQuotes(portfolio, prices)

	foo := portfolio["holdings"].([]any)[0].(map[string]any)
	if foo["last_price"] != 110.0 {
		t.Fatalf("FOO last_price: %v", foo["last_price"])
	}
	if foo["pct_vs_avg"] != 0.1 {
		t.Fatalf("FOO pct: %v", foo["pct_vs_avg"])
	}
	if foo["unrealized_inr"] != 100.0 {
		t.Fatalf("FOO unrealized: %v", foo["unrealized_inr"])
	}

	summary, ok := portfolio["quote_summary"].(map[string]any)
	if !ok {
		t.Fatal("missing quote_summary")
	}
	if summary["priced_holdings"] != 2 {
		t.Fatalf("priced_holdings: %v", summary["priced_holdings"])
	}
	// cost 10*100 + 5*200 = 2000; value 10*110 + 5*180 = 2000; unrealized 0
	if summary["unrealized_inr"] != 0.0 {
		t.Fatalf("unrealized_inr: %v", summary["unrealized_inr"])
	}
}

func TestLatestNSEReportEnrichesPortfolioQuotes(t *testing.T) {
	const prefix = "gs://tradex-demo-state/nserotator"
	rec := []byte(`{"month":"2026-07","orders":[],"top_ranked":[]}`)
	pf := []byte(`{"as_of":"2026-07-20","total_capital_inr":100000,"holdings":[{"symbol":"BAR","qty":10,"avg_price":50}]}`)
	objects := StaticObjectFetcher{
		prefix + "/recommendation-2026-07.json": rec,
		prefix + "/portfolio.json":              pf,
	}
	svc, err := NewService(testCfg(), Deps{
		Objects:   objects,
		NSEQuoter: StaticNSEQuoter{"BAR": 55},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC) }

	out, err := svc.LatestNSEReport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pfOut, ok := out["portfolio"].(map[string]any)
	if !ok {
		t.Fatalf("portfolio: %#v", out["portfolio"])
	}
	holdings := pfOut["holdings"].([]any)
	h := holdings[0].(map[string]any)
	if h["last_price"] != 55.0 {
		t.Fatalf("last_price: %v", h["last_price"])
	}
	if h["pct_vs_avg"] != 0.1 {
		t.Fatalf("pct_vs_avg: %v", h["pct_vs_avg"])
	}
}
