package etfmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"

	"github.com/yogesh-insta/tradex/internal/calendar"
)

// Holding is one self-tracked ETF position. Hand-edited by the user; never read
// from a broker (spec 21 §Non-goals).
type Holding struct {
	Ticker   string  `json:"ticker"`
	Qty      float64 `json:"qty"`
	AvgPrice float64 `json:"avg_price"`
}

// Holdings is the user-maintained state file.
type Holdings struct {
	AsOf     string    `json:"as_of"` // YYYY-MM-DD
	Holdings []Holding `json:"holdings"`
	Notes    string    `json:"notes,omitempty"`
}

// AsOfTime parses AsOf; zero time when unparseable.
func (h Holdings) AsOfTime() time.Time {
	t, err := time.Parse("2006-01-02", h.AsOf)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Tickers returns the held tickers, uppercased.
func (h Holdings) Tickers() []string {
	out := make([]string, 0, len(h.Holdings))
	for _, x := range h.Holdings {
		if t := strings.ToUpper(strings.TrimSpace(x.Ticker)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ExitAlert is a held fund whose trend has broken — the disciplined sell rule
// and the main reason this job exists (spec 21 §Exit alerts).
type ExitAlert struct {
	Ticker string  `json:"ticker"`
	Name   string  `json:"name"`
	Reason string  `json:"reason"`
	Ret3M  Ret     `json:"-"`
	Vol    float64 `json:"vol,omitempty"`
}

// Report is the durable record of one run.
type Report struct {
	RunAt       string      `json:"run_at"` // RFC3339
	Month       string      `json:"month"`  // YYYY-MM
	Exits       []ExitAlert `json:"exit_alerts"`
	Top         []Scored    `json:"top"`
	BelowTrend  []Scored    `json:"below_trend"`
	Geared      []Scored    `json:"geared_fx"`
	Inverse     []Scored    `json:"inverse"`
	Rejected    []string    `json:"rejected,omitempty"` // bad data / too little history
	DriftNotes  []string    `json:"drift_notes,omitempty"`
	Warnings    []string    `json:"warnings,omitempty"`
	MessageText string      `json:"message_text"` // full Telegram text (manual fallback)
}

// Heartbeat records the last successful run for the dead-man check.
type Heartbeat struct {
	LastSuccess string `json:"last_success"` // RFC3339
	Month       string `json:"month"`
}

// StateStore reads/writes holdings + report + heartbeat, either on GCS
// (gs://bucket/prefix) or a local directory (dev). Same shape as the NSE
// rotator's store.
type StateStore struct {
	GCSPrefix string // gs://bucket/etfmonitor (no trailing slash)
	LocalDir  string
	Client    *storage.Client
}

func (st *StateStore) uri(name string) string {
	return strings.TrimRight(st.GCSPrefix, "/") + "/" + name
}

// ReadHoldings loads holdings.json. A missing file is NOT an error: a user with
// no positions yet should still get the monthly top-10 report.
func (st *StateStore) ReadHoldings(ctx context.Context) (Holdings, error) {
	raw, err := st.read(ctx, "holdings.json")
	if err != nil {
		if isNotExist(err) {
			return Holdings{}, nil
		}
		return Holdings{}, fmt.Errorf("holdings: %w", err)
	}
	var h Holdings
	if err := json.Unmarshal(raw, &h); err != nil {
		return Holdings{}, fmt.Errorf("holdings parse: %w", err)
	}
	for i := range h.Holdings {
		h.Holdings[i].Ticker = strings.ToUpper(strings.TrimSpace(h.Holdings[i].Ticker))
	}
	return h, nil
}

func isNotExist(err error) bool {
	return os.IsNotExist(err) || err == storage.ErrObjectNotExist
}

// WriteReport persists the durable monthly record.
func (st *StateStore) WriteReport(ctx context.Context, r Report) error {
	return st.write(ctx, "report-"+r.Month+".json", r)
}

// WriteHeartbeat records a successful run.
func (st *StateStore) WriteHeartbeat(ctx context.Context, hb Heartbeat) error {
	return st.write(ctx, "heartbeat.json", hb)
}

func (st *StateStore) read(ctx context.Context, name string) ([]byte, error) {
	if st.GCSPrefix != "" {
		bucket, object, err := calendar.ParseGSURI(st.uri(name))
		if err != nil {
			return nil, err
		}
		r, err := st.Client.Bucket(bucket).Object(object).NewReader(ctx)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, 8<<20))
	}
	return os.ReadFile(filepath.Join(st.LocalDir, name))
}

func (st *StateStore) write(ctx context.Context, name string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if st.GCSPrefix != "" {
		bucket, object, err := calendar.ParseGSURI(st.uri(name))
		if err != nil {
			return err
		}
		w := st.Client.Bucket(bucket).Object(object).NewWriter(ctx)
		w.ContentType = "application/json"
		w.CacheControl = "no-cache"
		if _, err := w.Write(raw); err != nil {
			_ = w.Close()
			return fmt.Errorf("gcs write %s: %w", name, err)
		}
		return w.Close()
	}
	if err := os.MkdirAll(st.LocalDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(st.LocalDir, name), raw, 0o644)
}
