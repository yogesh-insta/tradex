// Package dashboard implements the read-only Cloud Run ops UI and JSON APIs
// per docs/specs/14-dashboard.md. It is off the money path: no order placement
// and no control-plane commands.
package dashboard

import (
	"time"
)

// StatusDoc is the optional GCS status.json schema published by the trader.
type StatusDoc struct {
	AsOf     time.Time       `json:"as_of"`
	Accounts []AccountStatus `json:"accounts"`
}

// AccountStatus is one engine/account heartbeat row inside status.json.
type AccountStatus struct {
	Name            string    `json:"name"`
	State           string    `json:"state"` // ACTIVE | PAUSED | SYSTEM_LOCKED | DISABLED
	StreamUp        bool      `json:"stream_up"`
	LastTickAgeMs   int64     `json:"last_tick_age_ms"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
	LastReconcileOK bool      `json:"last_reconcile_ok"`
	MarketDataStale bool      `json:"market_data_stale"`
}

// HealthLevel is a traffic-light for the UI.
type HealthLevel string

const (
	HealthGreen   HealthLevel = "green"
	HealthAmber   HealthLevel = "amber"
	HealthRed     HealthLevel = "red"
	HealthUnknown HealthLevel = "unknown"
)

// OverviewResponse is GET /api/overview.
type OverviewResponse struct {
	AsOf     time.Time         `json:"as_of"`
	Accounts []AccountOverview `json:"accounts"`
	Trades   []OpenTradeView   `json:"trades"`
	Health   []EngineHealth    `json:"health"`
	Errors   []string          `json:"errors,omitempty"`
	Cached   bool              `json:"cached"`
}

// AccountOverview is per-account summary from OANDA (+ state from status.json).
type AccountOverview struct {
	Name            string  `json:"name"`
	Currency        string  `json:"currency,omitempty"`
	NAV             float64 `json:"nav"`
	Balance         float64 `json:"balance"`
	UnrealizedPL    float64 `json:"unrealized_pl"`
	RealizedPLToday float64 `json:"realized_pl_today"` // resettablePL when available
	MarginUsed      float64 `json:"margin_used"`
	MarginAvailable float64 `json:"margin_available"`
	SystemState     string  `json:"system_state,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// OpenTradeView is one open trade row for the UI table.
type OpenTradeView struct {
	Account      string    `json:"account"`
	TradeID      string    `json:"trade_id"`
	Instrument   string    `json:"instrument"`
	Direction    string    `json:"direction"` // LONG | SHORT
	Units        float64   `json:"units"`
	Entry        float64   `json:"entry"`
	StopLoss     float64   `json:"stop_loss,omitempty"`
	TakeProfit   float64   `json:"take_profit,omitempty"`
	UnrealizedPL float64   `json:"unrealized_pl"`
	OpenTime     time.Time `json:"open_time"`
}

// EngineHealth is the health panel row for one account/engine.
type EngineHealth struct {
	Account           string      `json:"account"`
	State             string      `json:"state"`
	Stream            string      `json:"stream"` // up | stale | unknown
	LastTickAgeMs     int64       `json:"last_tick_age_ms,omitempty"`
	HeartbeatAgeMs    int64       `json:"heartbeat_age_ms,omitempty"`
	LastReconcileOK   *bool       `json:"last_reconcile_ok,omitempty"`
	CalendarAsOfAgeMs int64       `json:"calendar_as_of_age_ms,omitempty"`
	CalendarFresh     bool        `json:"calendar_fresh"`
	Level             HealthLevel `json:"level"`
	StatusSource      string      `json:"status_source"` // status.json | derived | missing
	Notes             []string    `json:"notes,omitempty"`
}

// CalendarResponse is GET /api/calendar.
type CalendarResponse struct {
	AsOf    time.Time       `json:"as_of"`
	Fresh   bool            `json:"fresh"`
	Stale   bool            `json:"stale"`
	Missing bool            `json:"missing"`
	Warning string          `json:"warning,omitempty"`
	Events  []CalendarEvent `json:"events"`
	Cached  bool            `json:"cached"`
	Errors  []string        `json:"errors,omitempty"`
}

// CalendarEvent is a high-impact upcoming event with dual wall times.
type CalendarEvent struct {
	Region     string    `json:"region"`
	Title      string    `json:"title"`
	Impact     string    `json:"impact"`
	TimeUTC    time.Time `json:"time_utc"`
	TimeBerlin string    `json:"time_berlin"` // Europe/Berlin wall clock
}

// PLResponse is GET /api/pl.
type PLResponse struct {
	Account     string    `json:"account"`
	Window      string    `json:"window"` // 7d | 30d | all
	ReportingTZ string    `json:"reporting_tz"`
	TotalPL     float64   `json:"total_pl"`
	TradeCount  int64     `json:"trade_count"`
	Days        []DailyPL `json:"days,omitempty"` // included for 7d/30d windows
	Cached      bool      `json:"cached"`
	Errors      []string  `json:"errors,omitempty"`
}

// DailyPLResponse is GET /api/pl/daily.
type DailyPLResponse struct {
	Account       string    `json:"account"`
	From          string    `json:"from"` // YYYY-MM-DD in reporting TZ
	To            string    `json:"to"`
	ReportingTZ   string    `json:"reporting_tz"`
	Days          []DailyPL `json:"days"`
	Total7d       float64   `json:"total_7d"`
	TotalAll      float64   `json:"total_all"`
	TradeCountAll int64     `json:"trade_count_all"`
	Cached        bool      `json:"cached"`
	Errors        []string  `json:"errors,omitempty"`
}

// DailyPL is one calendar-day realized P&L bucket.
type DailyPL struct {
	Day        string  `json:"day"` // YYYY-MM-DD
	RealizedPL float64 `json:"realized_pl"`
	TradeCount int64   `json:"trade_count"`
	Wins       int64   `json:"wins,omitempty"`
	Losses     int64   `json:"losses,omitempty"`
}

// ClosedTrade is a ledger row used by file/mock backends (and BQ mapping).
type ClosedTrade struct {
	TradeID    string    `json:"trade_id"`
	Account    string    `json:"account"`
	RealizedPL float64   `json:"realized_pl"`
	CloseTime  time.Time `json:"close_time"`
	Win        *bool     `json:"win,omitempty"`
}
