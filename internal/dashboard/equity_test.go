package dashboard

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/momentum"
	"github.com/yogesh-insta/tradex/internal/yahoo"
)

func TestReconstructPiecewiseBooks(t *testing.T) {
	books := []bookSlice{
		{AsOf: "2026-07-01", Holdings: []bookHolding{{Symbol: "AAA", Qty: 10, Avg: 100}}},
		{AsOf: "2026-07-10", Holdings: []bookHolding{{Symbol: "AAA", Qty: 10, Avg: 100}, {Symbol: "BBB", Qty: 5, Avg: 200}}},
	}
	series := map[string][]momentum.Candle{
		"AAA": {
			{Date: date("2026-07-01"), Close: 100},
			{Date: date("2026-07-02"), Close: 110},
			{Date: date("2026-07-10"), Close: 120},
			{Date: date("2026-07-11"), Close: 125},
		},
		"BBB": {
			{Date: date("2026-07-10"), Close: 200},
			{Date: date("2026-07-11"), Close: 180},
		},
	}
	pts := reconstruct(books, series, "reconstructed")
	if len(pts) != 4 {
		t.Fatalf("points: %d %#v", len(pts), pts)
	}
	// Jul 1-2: AAA only. Cost 1000, value 10*100 / 10*110.
	if pts[0].Cost != 1000 || pts[0].Value != 1000 || pts[0].NHoldings != 1 {
		t.Fatalf("jul1: %+v", pts[0])
	}
	if pts[1].Value != 1100 {
		t.Fatalf("jul2 value: %+v", pts[1])
	}
	// Jul 10+: both names. Cost 1000+1000=2000, value 10*120+5*200=2200.
	if pts[2].Cost != 2000 || pts[2].Value != 2200 || pts[2].NHoldings != 2 {
		t.Fatalf("jul10: %+v", pts[2])
	}
	if pts[3].Value != 2150 { // 10*125 + 5*180
		t.Fatalf("jul11 value: %+v", pts[3])
	}
}

func TestFollowThrough(t *testing.T) {
	orders := []any{
		map[string]any{"side": "SELL", "symbol": "GONE", "qty": float64(10)},
		map[string]any{"side": "SELL", "symbol": "KEPT", "qty": float64(5)},
		map[string]any{"side": "BUY", "symbol": "NEW", "qty": float64(3)},
		map[string]any{"side": "BUY", "symbol": "HELD", "qty": float64(1)},
	}
	got := followThrough(orders, map[string]bool{"KEPT": true, "HELD": true})
	want := []string{"executed", "pending", "pending", "executed"}
	if len(got) != 4 {
		t.Fatalf("len %d", len(got))
	}
	for i, w := range want {
		if got[i].Status != w {
			t.Errorf("%s %s: status %s want %s", got[i].Side, got[i].Symbol, got[i].Status, w)
		}
	}
}

func TestMergeLiveWins(t *testing.T) {
	base := EquityHistory{Points: []EquityPoint{
		{Date: "2026-08-01", Cost: 1, Value: 1, Source: "reconstructed"},
	}}
	extra := EquityHistory{Points: []EquityPoint{
		{Date: "2026-08-01", Cost: 2, Value: 3, Source: "live"},
		{Date: "2026-08-02", Cost: 2, Value: 4, Source: "live"},
	}}
	got := mergeHistory(base, extra)
	if len(got.Points) != 2 {
		t.Fatalf("len %d", len(got.Points))
	}
	if got.Points[0].Value != 3 || got.Points[0].Source != "live" {
		t.Fatalf("live should win: %+v", got.Points[0])
	}
}

func TestLatestNSEReportPersistsEquity(t *testing.T) {
	const prefix = "gs://tradex-demo-state/nserotator"
	rec := []byte(`{"month":"2026-08","run_at":"2026-08-02T09:54:19Z","orders":[{"side":"SELL","symbol":"FOO","qty":1}]}`)
	pf := []byte(`{"as_of":"2026-08-18","total_capital_inr":100000,"holdings":[{"symbol":"BAR","qty":10,"avg_price":50}]}`)
	objects := StaticObjectFetcher{
		prefix + "/recommendation-2026-08.json": rec,
		prefix + "/portfolio.json":              pf,
	}
	series := StaticSeriesFetcher{
		"BAR": yahoo.Series{Symbol: "BAR", Candles: []momentum.Candle{
			{Date: date("2026-08-18"), Close: 55},
		}},
		"LAURUSLABS": yahoo.Series{Symbol: "LAURUSLABS", Candles: []momentum.Candle{
			{Date: date("2026-07-24"), Close: 1600},
			{Date: date("2026-08-18"), Close: 1814},
		}},
	}
	svc, err := NewService(testCfg(), Deps{
		Objects:   objects,
		NSEQuoter: StaticNSEQuoter{"BAR": 55},
		NSESeries: series,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) }

	out, err := svc.LatestNSEReport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	eq, ok := out["equity"].(EquityHistory)
	if !ok {
		t.Fatalf("equity: %#v", out["equity"])
	}
	if len(eq.Points) == 0 {
		t.Fatal("expected equity points")
	}
	if len(eq.FollowThrough) != 1 || eq.FollowThrough[0].Status != "executed" {
		t.Fatalf("follow-through: %+v", eq.FollowThrough)
	}
	raw, ok := objects[nseEquityURI]
	if !ok || len(raw) == 0 {
		t.Fatal("equity-history.json was not persisted")
	}
	var stored EquityHistory
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.FollowThrough != nil {
		t.Fatal("disk copy should omit follow-through")
	}
	if len(stored.Points) == 0 {
		t.Fatal("stored points empty")
	}
}

type StaticSeriesFetcher map[string]yahoo.Series

func (s StaticSeriesFetcher) FetchDaily(_ context.Context, symbol string, _ int) (yahoo.Series, error) {
	if ser, ok := s[symbol]; ok {
		return ser, nil
	}
	return yahoo.Series{}, nil
}

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}
