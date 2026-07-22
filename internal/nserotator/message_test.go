package nserotator

import (
	"strings"
	"testing"
)

func TestFormatMessageOneLinePerSuggestionWithMarketCap(t *testing.T) {
	msg := FormatMessage(Recommendation{
		Month:          "2026-07",
		RegimeInvested: true,
		NiftyClose:     24238,
		NiftyEMA:       24396,
		Orders: []Order{
			{Side: "BUY", Symbol: "TITAN", CompanyName: "Titan Company Limited", Qty: 10, LastClose: 4721, ApproxValue: 47210, MarketCap: 2.45e12},
			{Side: "SELL", Symbol: "OLDCO", CompanyName: "Old Company Limited", Qty: 5, LastClose: 10000, ApproxValue: 50000, MarketCap: 1.2e11},
		},
		TopRanked: []Ranked{
			{Symbol: "TITAN", CompanyName: "Titan Company Limited", LastClose: 4721, Momentum: 0.45, MarketCap: 2.45e12},
			{Symbol: "BAJFINANCE", CompanyName: "Bajaj Finance Limited", LastClose: 1060, Momentum: 0.38, MarketCap: 8.12e12},
		},
	}, 6, 200)

	if strings.Contains(msg, " | ") {
		t.Fatalf("expected one line per suggestion, got pipe-separated block:\n%s", msg)
	}
	if !strings.Contains(msg, "TITAN — Titan Company Limited · ₹4,721 · +45% · ₹2.45L Cr") {
		t.Fatalf("missing formatted top-ranked line:\n%s", msg)
	}
	if !strings.Contains(msg, "BAJFINANCE — Bajaj Finance Limited · ₹1,060 · +38% · ₹8.12L Cr") {
		t.Fatalf("missing second ranked line:\n%s", msg)
	}
	if !strings.Contains(msg, "BUY  TITAN — Titan Company Limited") || !strings.Contains(msg, "₹2.45L Cr") {
		t.Fatalf("missing buy line with market cap:\n%s", msg)
	}
	if !strings.Contains(msg, "SELL OLDCO — Old Company Limited") || !strings.Contains(msg, "₹12.0K Cr") {
		t.Fatalf("missing sell line with market cap:\n%s", msg)
	}
}

func TestFormatMarketCapINR(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "MCap n/a"},
		{5e9, "₹500 Cr"},
		{1.2e11, "₹12.0K Cr"},
		{2.45e12, "₹2.45L Cr"},
		{8.12e12, "₹8.12L Cr"},
	}
	for _, tc := range cases {
		if got := formatMarketCapINR(tc.in); got != tc.want {
			t.Errorf("formatMarketCapINR(%v) = %q want %q", tc.in, got, tc.want)
		}
	}
}
