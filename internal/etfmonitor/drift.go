package etfmonitor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// BetasharesFundsURL is the issuer's fund index. The fund table is rendered
// server-side with structured attributes:
//
//	data-name="Global Cybersecurity ETF" data-shortcode="HACK"
//
// which is why this is scraped rather than an API: the ASX's own downloadable
// product lists are gone (the old etpFile.csv 302s to a 404 page, and the
// markitdigital company directory carries operating companies only — none of
// the ~108 ETF tickers appear in it).
//
// The endpoint returns 403 to a bare Go/curl User-Agent, so browser-ish headers
// are required. That fragility is why a drift failure is REPORTED rather than
// silently swallowed (spec 21 §Drift).
const BetasharesFundsURL = "https://www.betashares.com.au/fund/"

// fundRowRe extracts (name, ticker) pairs from the fund table.
var fundRowRe = regexp.MustCompile(`data-name="([^"]+)"\s+data-shortcode="([A-Z0-9]{2,5})"`)

// minLiveFunds guards against a page redesign silently yielding a short list,
// which would otherwise read as "everything was delisted".
const minLiveFunds = 80

// LiveFund is one fund as the issuer currently lists it.
type LiveFund struct {
	Ticker string
	Name   string
}

// FetchBetasharesFunds downloads the issuer's fund index and returns the funds
// it lists. Best-effort: callers must treat errors as non-fatal.
func FetchBetasharesFunds(ctx context.Context, client *http.Client, url string) ([]LiveFund, error) {
	if url == "" {
		url = BetasharesFundsURL
	}
	if client == nil {
		client = http.DefaultClient
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// A plain client is rejected with 403; these headers are load-bearing.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "+
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-AU,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("betashares funds: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("betashares funds: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []LiveFund
	for _, m := range fundRowRe.FindAllStringSubmatch(string(body), -1) {
		ticker := strings.ToUpper(m[2])
		if seen[ticker] {
			continue
		}
		seen[ticker] = true
		out = append(out, LiveFund{Ticker: ticker, Name: strings.TrimSpace(m[1])})
	}
	if len(out) < minLiveFunds {
		return nil, fmt.Errorf("betashares funds: only %d funds parsed — page layout likely changed", len(out))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ticker < out[j].Ticker })
	return out, nil
}

// DiffUniverse compares the checked-in universe against the issuer's live list.
//
//	added   = live tickers absent from our file (new or renamed — categorize & add)
//	removed = our tickers the issuer no longer lists (delisted/renamed, e.g. ECAR->DRIV)
//
// Funds marked DriftExempt are skipped in the `removed` direction: a
// non-Betashares issuer (SEMI/Global X), an unlisted fund (BPCF) or a known
// page omission (IPAY) would otherwise raise the same false alarm every month,
// which is how a drift alert gets ignored.
func DiffUniverse(universe []Fund, live []LiveFund) (added, removed []string) {
	ours := map[string]Fund{}
	for _, f := range universe {
		ours[strings.ToUpper(f.Ticker)] = f
	}
	onPage := map[string]bool{}
	for _, l := range live {
		t := strings.ToUpper(l.Ticker)
		onPage[t] = true
		if _, ok := ours[t]; !ok {
			added = append(added, t)
		}
	}
	for _, f := range universe {
		t := strings.ToUpper(f.Ticker)
		if !onPage[t] && !f.DriftExempt {
			removed = append(removed, t)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
