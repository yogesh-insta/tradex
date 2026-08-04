package asxrotator

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

// Holding is one manually-maintained position.
type Holding struct {
	Symbol   string  `json:"symbol"`
	Qty      int64   `json:"qty"`
	AvgPrice float64 `json:"avg_price"`
}

// Portfolio is the user-maintained state file (spec 22). The user edits it
// after executing (or skipping) recommended orders.
type Portfolio struct {
	AsOf string `json:"as_of"` // YYYY-MM-DD
	// TotalCapitalAUD is deliberately not named like the NSE lane's INR field:
	// a shared name across two currencies is how a portfolio file ends up
	// parsed by the wrong lane and sized in the wrong money.
	TotalCapitalAUD float64   `json:"total_capital_aud"`
	Holdings        []Holding `json:"holdings"`
	Notes           string    `json:"notes,omitempty"`
}

// AsOfTime parses AsOf; zero time when unparseable.
func (p Portfolio) AsOfTime() time.Time {
	t, err := time.Parse("2006-01-02", p.AsOf)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Order is one recommended manual action.
type Order struct {
	Side        string  `json:"side"` // BUY | SELL
	Symbol      string  `json:"symbol"`
	CompanyName string  `json:"company_name,omitempty"`
	Qty         int64   `json:"qty"`
	LastClose   float64 `json:"last_close"`
	ApproxValue float64 `json:"approx_value_aud"`
	MarketCap   float64 `json:"market_cap_aud,omitempty"`
}

// Recommendation is the durable record of one run.
type Recommendation struct {
	RunAt          string `json:"run_at"` // RFC3339
	Month          string `json:"month"`  // YYYY-MM
	RegimeInvested bool   `json:"regime_invested"`
	// RegimeFilter records whether RegimeInvested actually gated this run.
	RegimeFilter *bool      `json:"regime_filter,omitempty"`
	IndexClose   float64    `json:"index_close"`  // ^AXJO
	IndexEMA     float64    `json:"index_ema200"` // ^AXJO EMA(regime_ema_days)
	Orders       []Order    `json:"orders"`
	Holds        []string   `json:"holds"`
	HoldsInfo    []HoldInfo `json:"holds_info,omitempty"`
	// Frozen lists held-but-untradeable symbols: reported for visibility,
	// never present in Orders or Holds.
	Frozen    []string        `json:"frozen,omitempty"`
	TopRanked []Ranked        `json:"top_ranked"`
	Params    RunParamsRecord `json:"params,omitempty"`
	Excluded  []string        `json:"excluded_symbols,omitempty"`
	Warnings  []string        `json:"warnings,omitempty"`
	// BelowMinPrice names dropped by the price floor this run, reported so the
	// screen is visible rather than silent. See Config.MinPriceAUD.
	BelowMinPrice []string `json:"below_min_price,omitempty"`
	MessageText   string   `json:"message_text"` // full Telegram text (manual retrieval fallback)
}

// HoldInfo explains why a holding survived the exit rule: its position on the
// fast (entry) and slow (exit) momentum lists. Ranks are 1-based; 0 = unranked.
type HoldInfo struct {
	Symbol   string `json:"symbol"`
	Rank     int    `json:"rank"`
	RankSlow int    `json:"rank_slow"`
}

// RunParamsRecord stamps the parameters a run used onto its recommendation, so
// a stored record is self-describing and the dashboard can label itself
// without reading config.
type RunParamsRecord struct {
	LookbackMonths     int     `json:"lookback_months"`
	ExitLookbackMonths int     `json:"exit_lookback_months"`
	TopK               int     `json:"top_k"`
	ExitRankN          int     `json:"exit_rank_n"`
	MinPriceAUD        float64 `json:"min_price_aud,omitempty"`
}

// Heartbeat records the last successful run for the dead-man check.
type Heartbeat struct {
	LastSuccess string `json:"last_success"` // RFC3339
	Month       string `json:"month"`
}

// StateStore reads/writes portfolio + recommendation + heartbeat, either on
// GCS (gs://bucket/prefix) or a local directory (dev).
type StateStore struct {
	GCSPrefix string // gs://bucket/asxrotator (no trailing slash)
	LocalDir  string
	Client    *storage.Client
}

func (st *StateStore) uri(name string) string {
	return strings.TrimRight(st.GCSPrefix, "/") + "/" + name
}

// ReadPortfolio loads portfolio.json.
func (st *StateStore) ReadPortfolio(ctx context.Context) (Portfolio, error) {
	raw, err := st.read(ctx, "portfolio.json")
	if err != nil {
		return Portfolio{}, fmt.Errorf("portfolio: %w", err)
	}
	var p Portfolio
	if err := json.Unmarshal(raw, &p); err != nil {
		return Portfolio{}, fmt.Errorf("portfolio parse: %w", err)
	}
	if p.TotalCapitalAUD <= 0 {
		return Portfolio{}, fmt.Errorf("portfolio: total_capital_aud must be > 0")
	}
	return p, nil
}

// WriteRecommendation persists the durable monthly record.
func (st *StateStore) WriteRecommendation(ctx context.Context, rec Recommendation) error {
	return st.write(ctx, "recommendation-"+rec.Month+".json", rec)
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
