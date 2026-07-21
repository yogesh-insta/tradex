package nserotator

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// NSEConstituentsURL is the official Nifty 200 constituent CSV.
const NSEConstituentsURL = "https://nsearchives.nseindia.com/content/indices/ind_nifty200list.csv"

// FetchConstituents best-effort downloads the official list. Callers must
// treat errors as non-fatal (spec 20 §Alerts 3): the checked-in universe
// remains the source of truth.
func FetchConstituents(ctx context.Context, httpClient *http.Client, url string) ([]string, error) {
	if url == "" {
		url = NSEConstituentsURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
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
	r := csv.NewReader(io.LimitReader(resp.Body, 4<<20))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	symCol := -1
	for i, h := range header {
		if strings.EqualFold(strings.TrimSpace(h), "Symbol") {
			symCol = i
			break
		}
	}
	if symCol < 0 {
		return nil, fmt.Errorf("nse constituents: no Symbol column")
	}
	var out []string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if symCol < len(rec) {
			if s := strings.TrimSpace(rec[symCol]); s != "" {
				out = append(out, s)
			}
		}
	}
	if len(out) < 150 {
		return nil, fmt.Errorf("nse constituents: only %d rows — response looks wrong", len(out))
	}
	return out, nil
}

// DiffUniverse compares the checked-in universe with the official list.
func DiffUniverse(checkedIn, official []string) (added, removed []string) {
	in := map[string]bool{}
	for _, s := range checkedIn {
		in[s] = true
	}
	off := map[string]bool{}
	for _, s := range official {
		off[s] = true
		if !in[s] {
			added = append(added, s) // in official, missing from our file
		}
	}
	for _, s := range checkedIn {
		if !off[s] {
			removed = append(removed, s) // in our file, dropped from official
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
