package backtest

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"time"
)

// EquityPoint is one step of the equity curve (after a trade close).
type EquityPoint struct {
	Time   time.Time
	Equity float64
}

// Metrics summarizes a backtest run.
type Metrics struct {
	InitialEquity  float64
	FinalEquity    float64
	NetPnL         float64
	Trades         int
	Wins           int
	Losses         int
	WinRate        float64 // wins / trades
	GrossProfit    float64
	GrossLoss      float64 // positive magnitude
	ProfitFactor   float64 // grossProfit / grossLoss (Inf if no losses)
	AvgR           float64 // average risk/reward multiple (RealizedPL / RiskDistance)
	MaxDrawdown    float64 // peak-to-trough equity drop (currency)
	MaxDrawdownPct float64 // relative to the peak
	EquityCurve    []EquityPoint
}

// Compute derives all metrics from the closed-trade sequence (chronological).
func Compute(initialEquity float64, closed []ClosedTrade) Metrics {
	m := Metrics{InitialEquity: initialEquity, FinalEquity: initialEquity}
	equity := initialEquity
	peak := initialEquity
	m.EquityCurve = append(m.EquityCurve, EquityPoint{Equity: initialEquity})

	var totalR float64
	for _, ct := range closed {
		m.Trades++
		pl := ct.RealizedPL
		equity += pl
		m.NetPnL += pl
		switch {
		case pl > 0:
			m.Wins++
			m.GrossProfit += pl
		case pl < 0:
			m.Losses++
			m.GrossLoss += -pl
		}
		if equity > peak {
			peak = equity
		}
		if dd := peak - equity; dd > m.MaxDrawdown {
			m.MaxDrawdown = dd
			if peak > 0 {
				m.MaxDrawdownPct = dd / peak
			}
		}
		// Calculate R multiple: realized P&L / risk distance
		if ct.Trade.RiskDistance > 0 {
			r := pl / ct.Trade.RiskDistance
			totalR += r
		}
		m.EquityCurve = append(m.EquityCurve, EquityPoint{Time: ct.ClosedAt, Equity: equity})
	}
	m.FinalEquity = equity
	if m.Trades > 0 {
		m.WinRate = float64(m.Wins) / float64(m.Trades)
		m.AvgR = totalR / float64(m.Trades)
	}
	if m.GrossLoss > 0 {
		m.ProfitFactor = m.GrossProfit / m.GrossLoss
	} else if m.GrossProfit > 0 {
		m.ProfitFactor = math.Inf(1)
	}
	return m
}

// WriteEquityCurveCSV writes "time,equity" rows.
func (m Metrics) WriteEquityCurveCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"time", "equity"}); err != nil {
		return err
	}
	for _, p := range m.EquityCurve {
		ts := ""
		if !p.Time.IsZero() {
			ts = p.Time.UTC().Format(time.RFC3339)
		}
		if err := cw.Write([]string{ts, fmt.Sprintf("%.2f", p.Equity)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// Summary renders the stdout report.
func (m Metrics) Summary() string {
	pf := "n/a"
	if !math.IsInf(m.ProfitFactor, 1) && m.ProfitFactor > 0 {
		pf = fmt.Sprintf("%.2f", m.ProfitFactor)
	} else if math.IsInf(m.ProfitFactor, 1) {
		pf = "inf (no losses)"
	}
	return fmt.Sprintf(
		"trades: %d  wins: %d  losses: %d  win rate: %.1f%%  avg R: %.2f\n"+
			"net P&L: %.2f  gross profit: %.2f  gross loss: %.2f  profit factor: %s\n"+
			"max drawdown: %.2f (%.1f%%)\n"+
			"equity: %.2f -> %.2f",
		m.Trades, m.Wins, m.Losses, m.WinRate*100, m.AvgR,
		m.NetPnL, m.GrossProfit, m.GrossLoss, pf,
		m.MaxDrawdown, m.MaxDrawdownPct*100,
		m.InitialEquity, m.FinalEquity)
}
