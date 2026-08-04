package nserotator

import "github.com/yogesh-insta/tradex/internal/yahoo"

// The Yahoo client moved to internal/yahoo so the NSE and ASX lanes share one
// implementation; the market is selected by YahooClient.Suffix (".NS" here).
// These aliases keep every existing call site and construction literal in this
// package — and in cmd/nserotator and internal/dashboard — working unchanged.
type (
	YahooClient = yahoo.YahooClient
	QuoteDetail = yahoo.QuoteDetail
)

// YahooSuffix is the market suffix for NSE symbols. Construct the client with
// Suffix: nserotator.YahooSuffix — the field has no default, so a missing one
// fails loudly rather than silently fetching another exchange.
const YahooSuffix = ".NS"
