//go:build integration

package nserotator

import (
	"context"
	"testing"
)

func TestFetchQuoteDetailsLiveMarketCap(t *testing.T) {
	c := &YahooClient{Retries: 2, Timeout: 0}
	syms := []string{"TITAN", "BAJFINANCE", "ADANIENSOL"}
	q := c.FetchQuoteDetails(context.Background(), syms)
	for _, sym := range syms {
		d := q[sym]
		t.Logf("%s name=%q price=%.2f cap=%.0f", sym, d.CompanyName, d.Price, d.MarketCap)
		if d.MarketCap <= 0 {
			t.Fatalf("%s: missing market cap", sym)
		}
	}
}
