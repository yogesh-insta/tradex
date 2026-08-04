package asxrotator

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

// Universe drift for the ASX lane.
//
// The NSE lane scrapes the official NSE constituent CSV. S&P publishes ASX 200
// membership behind a login, so there is no equivalent free official endpoint.
// The practical substitute is the holdings CSV of an ASX 200 index ETF (STW,
// IOZ, A200), which tracks the index and is published daily.
//
// Because that source is operator-chosen rather than official, ConstituentsURL
// is config and may be empty. When it is empty and drift_check is on, Run
// raises a warning every month — deliberately noisy. A drift check that
// silently never fires is worse than none: it reads as reassurance.
//
// Cadence note: the ASX 200 rebalances QUARTERLY (Mar/Jun/Sep/Dec), unlike
// Nifty 200's semi-annual review, so the config refresh ritual is quarterly.

var asxTicker = regexp.MustCompile(`^[A-Z0-9]{3}$`)

// FetchConstituents downloads and parses an index-holdings CSV into a sorted
// ticker list. Format-tolerant: it scans every row for the first field that
// looks like an ASX ticker, which survives the header and metadata preambles
// the ETF issuers put at the top of these files.
func FetchConstituents(ctx context.Context, client *http.Client, url string) ([]string, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("no constituents_url configured")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) tradex-rotator/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return parseConstituentsCSV(body)
}

func parseConstituentsCSV(body []byte) ([]string, error) {
	r := csv.NewReader(strings.NewReader(string(body)))
	r.FieldsPerRecord = -1 // issuer files have ragged preamble rows
	r.LazyQuotes = true

	seen := map[string]bool{}
	var out []string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // skip malformed rows rather than abandoning the file
		}
		for _, f := range rec {
			t := strings.ToUpper(strings.TrimSpace(f))
			t = strings.TrimSuffix(t, ".AX")
			if !asxTicker.MatchString(t) || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
			break // one ticker per row
		}
	}
	if len(out) < 50 {
		return nil, fmt.Errorf("parsed only %d tickers — not an ASX 200 holdings file?", len(out))
	}
	sort.Strings(out)
	return out, nil
}

// DiffUniverse reports symbols the official list adds and drops relative to
// the configured universe.
func DiffUniverse(configured, official []string) (added, removed []string) {
	cfg := make(map[string]bool, len(configured))
	for _, s := range configured {
		cfg[strings.ToUpper(strings.TrimSpace(s))] = true
	}
	off := make(map[string]bool, len(official))
	for _, s := range official {
		off[strings.ToUpper(strings.TrimSpace(s))] = true
	}
	for s := range off {
		if !cfg[s] {
			added = append(added, s)
		}
	}
	for s := range cfg {
		if !off[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
