package etfmonitor

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ASXProductsURL is the ASX's downloadable ETP/investment-products CSV.
// BetasharesFundsURL is the issuer's own fund index, used as a fallback.
//
// Both are best-effort: URL rot here must never block a run (spec 21 §Drift),
// which is exactly why the checked-in universe stays the source of truth.
const (
	ASXProductsURL     = "https://www.asx.com.au/data/etp/etpFile.csv"
	BetasharesFundsURL = "https://www.betashares.com.au/fund/"
)

// tickerRe matches plausible ASX ETF codes (3-4 chars, may lead with a digit
// as the fixed-term bond funds do: 28BB, 30BB).
var tickerRe = regexp.MustCompile(`\b([A-Z0-9]{3,4})\b`)

func httpGet(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) tradex-etfmonitor/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// FetchASXProducts downloads the ASX ETP CSV and returns its ticker codes.
func FetchASXProducts(ctx context.Context, client *http.Client, url string) ([]string, error) {
	if url == "" {
		url = ASXProductsURL
	}
	body, err := httpGet(ctx, client, url, 8<<20)
	if err != nil {
		return nil, fmt.Errorf("asx products: %w", err)
	}
	r := csv.NewReader(strings.NewReader(string(body)))
	r.FieldsPerRecord = -1

	// The ASX file carries preamble lines before the real header; scan for the
	// first row containing a recognisable code column.
	codeCol := -1
	for codeCol < 0 {
		rec, err := r.Read()
		if err == io.EOF {
			return nil, fmt.Errorf("asx products: no code column found")
		}
		if err != nil {
			return nil, fmt.Errorf("asx products: %w", err)
		}
		for i, h := range rec {
			switch strings.ToLower(strings.TrimSpace(h)) {
			case "asx code", "asx_code", "code", "symbol", "ticker":
				codeCol = i
			}
		}
	}
	var out []string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("asx products: %w", err)
		}
		if codeCol < len(rec) {
			if c := strings.ToUpper(strings.TrimSpace(rec[codeCol])); c != "" {
				out = append(out, c)
			}
		}
	}
	if len(out) < 100 {
		return nil, fmt.Errorf("asx products: only %d rows — response looks wrong", len(out))
	}
	return dedupe(out), nil
}

// FetchBetasharesTickers scrapes the issuer's fund index for ticker codes.
// Fallback only: HTML scraping is fragile by nature, hence the row-count sanity
// check and the non-fatal contract.
func FetchBetasharesTickers(ctx context.Context, client *http.Client, url string) ([]string, error) {
	if url == "" {
		url = BetasharesFundsURL
	}
	body, err := httpGet(ctx, client, url, 8<<20)
	if err != nil {
		return nil, fmt.Errorf("betashares funds: %w", err)
	}
	// Codes appear in per-fund links and table cells; collect candidates and
	// rely on the caller diffing against a curated list.
	matches := tickerRe.FindAllStringSubmatch(string(body), -1)
	var out []string
	for _, m := range matches {
		out = append(out, m[1])
	}
	out = dedupe(out)
	if len(out) < 50 {
		return nil, fmt.Errorf("betashares funds: only %d candidate codes — response looks wrong", len(out))
	}
	return out, nil
}

// FetchLiveTickers tries the ASX CSV, then the Betashares index.
func FetchLiveTickers(ctx context.Context, client *http.Client) ([]string, string, error) {
	if out, err := FetchASXProducts(ctx, client, ""); err == nil {
		return out, "asx", nil
	} else {
		asxErr := err
		out, err := FetchBetasharesTickers(ctx, client, "")
		if err != nil {
			return nil, "", fmt.Errorf("asx: %v; betashares: %w", asxErr, err)
		}
		return out, "betashares", nil
	}
}

// DiffUniverse compares the checked-in universe against a live ticker list.
//
// added   = live tickers absent from our file (candidates to categorize)
// removed = our tickers absent from the live list (possible delist/rename,
//
//	e.g. ECAR -> DRIV)
//
// Only tickers the live source could plausibly cover are considered: when the
// fallback scraper is used its candidate set is noisy, so `added` is advisory
// and never auto-applied (spec 21 §Drift).
func DiffUniverse(checkedIn, live []string) (added, removed []string) {
	in := map[string]bool{}
	for _, s := range checkedIn {
		in[strings.ToUpper(s)] = true
	}
	on := map[string]bool{}
	for _, s := range live {
		s = strings.ToUpper(s)
		on[s] = true
		if !in[s] {
			added = append(added, s)
		}
	}
	for _, s := range checkedIn {
		if !on[strings.ToUpper(s)] {
			removed = append(removed, strings.ToUpper(s))
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
