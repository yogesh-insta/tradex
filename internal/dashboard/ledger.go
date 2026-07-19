package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"

	"github.com/yogesh-insta/tradex/internal/oanda"
)

// FileLedger aggregates closed trades from a local JSON array (dev/mock).
type FileLedger struct {
	Path   string
	trades []ClosedTrade // optional in-memory; Path takes precedence when set
}

// LoadFileLedger reads ClosedTrade rows from path.
func LoadFileLedger(path string) (*FileLedger, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var trades []ClosedTrade
	if err := json.Unmarshal(raw, &trades); err != nil {
		return nil, fmt.Errorf("ledger file: %w", err)
	}
	return &FileLedger{Path: path, trades: trades}, nil
}

// MemoryLedger is an in-process ledger for tests and mock mode.
func MemoryLedger(trades []ClosedTrade) *FileLedger {
	return &FileLedger{trades: trades}
}

// DailyPL implements LedgerQuerier.
func (l *FileLedger) DailyPL(_ context.Context, account string, from, to time.Time, loc *time.Location) ([]DailyPL, error) {
	if loc == nil {
		loc = time.UTC
	}
	fromDay := time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 0, 0, 0, 0, loc)
	toDay := time.Date(to.In(loc).Year(), to.In(loc).Month(), to.In(loc).Day(), 0, 0, 0, 0, loc)
	byDay := map[string]*DailyPL{}
	for _, t := range l.trades {
		if t.Account != account || t.CloseTime.IsZero() {
			continue
		}
		local := t.CloseTime.In(loc)
		dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		if dayStart.Before(fromDay) || dayStart.After(toDay) {
			continue
		}
		key := dayStart.Format("2006-01-02")
		row, ok := byDay[key]
		if !ok {
			row = &DailyPL{Day: key}
			byDay[key] = row
		}
		row.RealizedPL += t.RealizedPL
		row.TradeCount++
		if t.RealizedPL > 0 {
			row.Wins++
		} else if t.RealizedPL < 0 {
			row.Losses++
		}
	}
	out := make([]DailyPL, 0, len(byDay))
	for _, v := range byDay {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day > out[j].Day })
	return out, nil
}

// AllTimePL implements LedgerQuerier.
func (l *FileLedger) AllTimePL(_ context.Context, account string) (float64, int64, error) {
	var total float64
	var count int64
	for _, t := range l.trades {
		if t.Account != account || t.CloseTime.IsZero() {
			continue
		}
		total += t.RealizedPL
		count++
	}
	return total, count, nil
}

// BigQueryLedger runs parameterized aggregates against trade_ledger.
type BigQueryLedger struct {
	Client  *bigquery.Client
	Project string
	Dataset string
	Table   string
}

// OANDALedger derives realized P&L from broker ORDER_FILL transactions. It is
// used when the asynchronous BigQuery ledger is not available yet, so a
// production dashboard never falls back to a bundled fixture.
//
// OANDA reports realized P&L on the fill that closes or reduces a trade. An
// opening fill has zero P&L and is excluded from the closed-fill count.
type OANDALedger struct {
	Client     *oanda.Client
	AccountIDs map[string]string // dashboard account name -> OANDA account ID
}

func (l *OANDALedger) accountID(account string) (string, error) {
	id := l.AccountIDs[account]
	if id == "" {
		return "", fmt.Errorf("unknown dashboard account %q", account)
	}
	return id, nil
}

// DailyPL implements LedgerQuerier using OANDA transaction history.
func (l *OANDALedger) DailyPL(ctx context.Context, account string, from, to time.Time, loc *time.Location) ([]DailyPL, error) {
	id, err := l.accountID(account)
	if err != nil {
		return nil, err
	}
	transactions, err := l.Client.Transactions(ctx, id, startOfDay(from, loc), endOfDay(to, loc))
	if err != nil {
		return nil, err
	}
	return dailyPLFromTransactions(transactions.Transactions, from, to, loc), nil
}

// AllTimePL implements LedgerQuerier using the complete available OANDA
// transaction history. Paper accounts are expected to remain below OANDA's
// 1,000-transaction page cap until an external ledger is enabled.
func (l *OANDALedger) AllTimePL(ctx context.Context, account string) (float64, int64, error) {
	id, err := l.accountID(account)
	if err != nil {
		return 0, 0, err
	}
	transactions, err := l.Client.Transactions(ctx, id, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC())
	if err != nil {
		return 0, 0, err
	}
	var total float64
	var count int64
	for _, tx := range transactions.Transactions {
		if tx.Type != "ORDER_FILL" || float64(tx.PL) == 0 {
			continue
		}
		total += float64(tx.PL)
		count++
	}
	return total, count, nil
}

func dailyPLFromTransactions(transactions []oanda.Transaction, from, to time.Time, loc *time.Location) []DailyPL {
	if loc == nil {
		loc = time.UTC
	}
	fromDay := startOfDay(from, loc)
	toDay := startOfDay(to, loc)
	byDay := map[string]*DailyPL{}
	for _, tx := range transactions {
		if tx.Type != "ORDER_FILL" || float64(tx.PL) == 0 {
			continue
		}
		dayStart := startOfDay(tx.Time, loc)
		if dayStart.Before(fromDay) || dayStart.After(toDay) {
			continue
		}
		key := dayStart.Format("2006-01-02")
		row := byDay[key]
		if row == nil {
			row = &DailyPL{Day: key}
			byDay[key] = row
		}
		row.RealizedPL += float64(tx.PL)
		row.TradeCount++
		if tx.PL > 0 {
			row.Wins++
		} else {
			row.Losses++
		}
	}
	out := make([]DailyPL, 0, len(byDay))
	for _, row := range byDay {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day > out[j].Day })
	return out
}

func startOfDay(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

func endOfDay(t time.Time, loc *time.Location) time.Time {
	return startOfDay(t, loc).AddDate(0, 0, 1).Add(-time.Nanosecond)
}

// FallbackLedger uses its primary source whenever it has data; an empty or
// unavailable primary (e.g. deferred stdout-to-BigQuery publisher) falls back
// to real OANDA transaction history.
type FallbackLedger struct {
	Primary  LedgerQuerier
	Fallback LedgerQuerier
}

func (l FallbackLedger) DailyPL(ctx context.Context, account string, from, to time.Time, loc *time.Location) ([]DailyPL, error) {
	days, err := l.Primary.DailyPL(ctx, account, from, to, loc)
	if err == nil && len(days) > 0 {
		return days, nil
	}
	fallback, fallbackErr := l.Fallback.DailyPL(ctx, account, from, to, loc)
	if fallbackErr != nil {
		if err != nil {
			return nil, fmt.Errorf("primary ledger: %v; OANDA fallback: %w", err, fallbackErr)
		}
		return nil, fallbackErr
	}
	return fallback, nil
}

func (l FallbackLedger) AllTimePL(ctx context.Context, account string) (float64, int64, error) {
	total, count, err := l.Primary.AllTimePL(ctx, account)
	if err == nil && count > 0 {
		return total, count, nil
	}
	fallbackTotal, fallbackCount, fallbackErr := l.Fallback.AllTimePL(ctx, account)
	if fallbackErr != nil {
		if err != nil {
			return 0, 0, fmt.Errorf("primary ledger: %v; OANDA fallback: %w", err, fallbackErr)
		}
		return 0, 0, fallbackErr
	}
	return fallbackTotal, fallbackCount, nil
}

func (l *BigQueryLedger) tableRef() string {
	return fmt.Sprintf("`%s.%s.%s`", l.Project, l.Dataset, l.Table)
}

// DailyPL implements LedgerQuerier.
func (l *BigQueryLedger) DailyPL(ctx context.Context, account string, from, to time.Time, loc *time.Location) ([]DailyPL, error) {
	if loc == nil {
		loc = time.UTC
	}
	tz := loc.String()
	q := fmt.Sprintf(`
SELECT
  FORMAT_DATE('%%Y-%%m-%%d', DATE(close_time, @tz)) AS day,
  SUM(realized_pl) AS realized_pl,
  COUNT(*) AS trade_count,
  COUNTIF(realized_pl > 0) AS wins,
  COUNTIF(realized_pl < 0) AS losses
FROM %s
WHERE close_time IS NOT NULL
  AND account = @account
  AND DATE(close_time, @tz) BETWEEN @from_day AND @to_day
GROUP BY day
ORDER BY day DESC`, l.tableRef())
	query := l.Client.Query(q)
	query.Parameters = []bigquery.QueryParameter{
		{Name: "tz", Value: tz},
		{Name: "account", Value: account},
		{Name: "from_day", Value: from.In(loc).Format("2006-01-02")},
		{Name: "to_day", Value: to.In(loc).Format("2006-01-02")},
	}
	it, err := query.Read(ctx)
	if err != nil {
		return nil, err
	}
	var out []DailyPL
	for {
		var row struct {
			Day        string  `bigquery:"day"`
			RealizedPL float64 `bigquery:"realized_pl"`
			TradeCount int64   `bigquery:"trade_count"`
			Wins       int64   `bigquery:"wins"`
			Losses     int64   `bigquery:"losses"`
		}
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, DailyPL{
			Day: row.Day, RealizedPL: row.RealizedPL,
			TradeCount: row.TradeCount, Wins: row.Wins, Losses: row.Losses,
		})
	}
	return out, nil
}

// AllTimePL implements LedgerQuerier.
func (l *BigQueryLedger) AllTimePL(ctx context.Context, account string) (float64, int64, error) {
	q := fmt.Sprintf(`
SELECT
  SUM(realized_pl) AS realized_pl,
  COUNT(*) AS trade_count
FROM %s
WHERE close_time IS NOT NULL AND account = @account`, l.tableRef())
	query := l.Client.Query(q)
	query.Parameters = []bigquery.QueryParameter{
		{Name: "account", Value: account},
	}
	it, err := query.Read(ctx)
	if err != nil {
		return 0, 0, err
	}
	var row struct {
		RealizedPL float64 `bigquery:"realized_pl"`
		TradeCount int64   `bigquery:"trade_count"`
	}
	if err := it.Next(&row); err != nil {
		if err == iterator.Done {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	return row.RealizedPL, row.TradeCount, nil
}

// SumLastNDays sums realized_pl for the first n daily rows (already DESC).
func SumLastNDays(days []DailyPL, n int) float64 {
	var sum float64
	for i, d := range days {
		if i >= n {
			break
		}
		sum += d.RealizedPL
	}
	return sum
}

// WindowBounds returns [from,to] inclusive calendar days in loc for a window label.
func WindowBounds(window string, now time.Time, loc *time.Location, lookback30 int) (from, to time.Time, err error) {
	if loc == nil {
		loc = time.UTC
	}
	now = now.In(loc)
	to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	switch strings.ToLower(window) {
	case "7d", "7":
		from = to.AddDate(0, 0, -6)
	case "30d", "30":
		days := lookback30
		if days <= 0 {
			days = 30
		}
		from = to.AddDate(0, 0, -(days - 1))
	case "all", "":
		from = time.Date(1970, 1, 1, 0, 0, 0, 0, loc)
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("unknown window %q (use 7d|30d|all)", window)
	}
	return from, to, nil
}
