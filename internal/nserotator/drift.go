package nserotator

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// NSEConstituentsURL is the official Nifty 200 constituent CSV.
const NSEConstituentsURL = "https://nsearchives.nseindia.com/content/indices/ind_nifty200list.csv"

// FetchNSEUniverse downloads the official Nifty 200 CSV and returns symbol → company name.
func FetchNSEUniverse(ctx context.Context, httpClient *http.Client, csvURL string) (map[string]string, error) {
	if csvURL == "" {
		csvURL = NSEConstituentsURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, csvURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) tradex-nserotator/1.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nse constituents: HTTP %d", resp.StatusCode)
	}
	return parseNSEConstituentsCSV(io.LimitReader(resp.Body, 4<<20))
}

func parseNSEConstituentsCSV(r io.Reader) (map[string]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, err
	}
	nameCol, symCol := -1, -1
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "company name":
			nameCol = i
		case "symbol":
			symCol = i
		}
	}
	if symCol < 0 || nameCol < 0 {
		return nil, fmt.Errorf("nse constituents: missing Company Name or Symbol column")
	}
	out := make(map[string]string)
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if symCol < len(rec) && nameCol < len(rec) {
			sym := strings.TrimSpace(rec[symCol])
			name := strings.TrimSpace(rec[nameCol])
			if sym != "" && name != "" {
				out[sym] = name
			}
		}
	}
	if len(out) < 150 {
		return nil, fmt.Errorf("nse constituents: only %d rows — response looks wrong", len(out))
	}
	return out, nil
}

// FetchConstituents best-effort downloads the official list. Callers must
// treat errors as non-fatal (spec 20 §Alerts 3): the checked-in universe
// remains the source of truth.
func FetchConstituents(ctx context.Context, httpClient *http.Client, url string) ([]string, error) {
	names, err := FetchNSEUniverse(ctx, httpClient, url)
	if err != nil {
		return nil, err
	}
	return nseSymbolList(names), nil
}

// FetchCompanyNames downloads the official Nifty 200 CSV and returns symbol → name.
func FetchCompanyNames(ctx context.Context, httpClient *http.Client, url string) (map[string]string, error) {
	return FetchNSEUniverse(ctx, httpClient, url)
}

func nseSymbolList(names map[string]string) []string {
	out := make([]string, 0, len(names))
	for sym := range names {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}

func DiffUniverse(checkedIn, official []string) (added, removed []string) {
	in := map[string]bool{}
	for _, s := range checkedIn {
		in[s] = true
	}
	off := map[string]bool{}
	for _, s := range official {
		off[s] = true
		if !in[s] {
			added = append(added, s)
		}
	}
	for _, s := range checkedIn {
		if !off[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// httpStatusError carries an HTTP status for auth-retry decisions.
type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

func isAuthHTTP(err error) bool {
	var he *httpStatusError
	return errors.As(err, &he) && (he.StatusCode == http.StatusUnauthorized || he.StatusCode == http.StatusForbidden)
}
