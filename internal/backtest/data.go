package backtest

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/yogesh-insta/tradex/internal/oanda"
	"github.com/yogesh-insta/tradex/pkg/types"
)

// CandleFetcher is the OANDA-candles slice needed to download history.
type CandleFetcher interface {
	Candles(ctx context.Context, instrument, granularity string, count int, from, to time.Time) (oanda.CandlesResponse, error)
}

// maxCandlesPerRequest is OANDA's documented per-request cap.
const maxCandlesPerRequest = 5000

// LoadOrFetch returns historical candles for [from, to), reading the local
// CSV cache when present and downloading + caching from OANDA otherwise.
// fetcher may be nil for cache-only operation.
func LoadOrFetch(ctx context.Context, fetcher CandleFetcher, dir, instrument string, tf types.Timeframe, from, to time.Time) ([]types.Candle, error) {
	path := cachePath(dir, instrument, tf, from, to)
	if cs, err := ReadCandlesCSV(path, instrument, tf); err == nil {
		return cs, nil
	}
	if fetcher == nil {
		return nil, fmt.Errorf("backtest: no cached data at %s and no OANDA fetcher (set OANDA_API_TOKEN)", path)
	}
	cs, err := fetchRange(ctx, fetcher, instrument, tf, from, to)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := WriteCandlesCSV(path, cs); err != nil {
		return nil, err
	}
	return cs, nil
}

func cachePath(dir, instrument string, tf types.Timeframe, from, to time.Time) string {
	return filepath.Join(dir, fmt.Sprintf("%s_%s_%s_%s.csv",
		instrument, tf, from.UTC().Format("20060102"), to.UTC().Format("20060102")))
}

// fetchRange paginates [from, to) in chunks under OANDA's 5000-candle cap.
func fetchRange(ctx context.Context, fetcher CandleFetcher, instrument string, tf types.Timeframe, from, to time.Time) ([]types.Candle, error) {
	step := tf.Duration() * (maxCandlesPerRequest - 10)
	var out []types.Candle
	seen := map[time.Time]bool{}
	for cursor := from; cursor.Before(to); cursor = cursor.Add(step) {
		end := cursor.Add(step)
		if end.After(to) {
			end = to
		}
		resp, err := fetcher.Candles(ctx, instrument, string(tf), 0, cursor, end)
		if err != nil {
			return nil, fmt.Errorf("backtest: fetch %s %s %s: %w", instrument, tf, cursor.Format("2006-01-02"), err)
		}
		for _, rc := range resp.Candles {
			if !rc.Complete || seen[rc.Time.UTC()] {
				continue
			}
			seen[rc.Time.UTC()] = true
			out = append(out, types.Candle{
				Instrument: instrument,
				Timeframe:  tf,
				Start:      rc.Time.UTC(),
				Open:       float64(rc.Mid.O),
				High:       float64(rc.Mid.H),
				Low:        float64(rc.Mid.L),
				Close:      float64(rc.Mid.C),
				Volume:     rc.Volume,
				Complete:   true,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// WriteCandlesCSV persists candles as time,open,high,low,close,volume.
func WriteCandlesCSV(path string, cs []types.Candle) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := w.Write([]string{"time", "open", "high", "low", "close", "volume"}); err != nil {
		return err
	}
	for _, c := range cs {
		rec := []string{
			c.Start.UTC().Format(time.RFC3339),
			strconv.FormatFloat(c.Open, 'f', -1, 64),
			strconv.FormatFloat(c.High, 'f', -1, 64),
			strconv.FormatFloat(c.Low, 'f', -1, 64),
			strconv.FormatFloat(c.Close, 'f', -1, 64),
			strconv.FormatInt(c.Volume, 10),
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// ReadCandlesCSV loads a cached candle file.
func ReadCandlesCSV(path, instrument string, tf types.Timeframe) ([]types.Candle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var out []types.Candle
	for i, rec := range recs {
		if i == 0 || len(rec) < 6 {
			continue // header
		}
		ts, err := time.Parse(time.RFC3339, rec[0])
		if err != nil {
			return nil, fmt.Errorf("%s row %d: %w", path, i, err)
		}
		o, _ := strconv.ParseFloat(rec[1], 64)
		h, _ := strconv.ParseFloat(rec[2], 64)
		l, _ := strconv.ParseFloat(rec[3], 64)
		cl, _ := strconv.ParseFloat(rec[4], 64)
		v, _ := strconv.ParseInt(rec[5], 10, 64)
		out = append(out, types.Candle{
			Instrument: instrument, Timeframe: tf, Start: ts.UTC(),
			Open: o, High: h, Low: l, Close: cl, Volume: v, Complete: true,
		})
	}
	return out, nil
}

// HistorySource serves preloaded candles through the same RESTSource
// signature the live session controller uses — bounded at the simulation
// clock so nothing can look ahead.
type HistorySource struct {
	data map[string][]types.Candle // key: instrument|granularity, sorted
	Now  func() time.Time
}

// NewHistorySource builds an empty source.
func NewHistorySource(now func() time.Time) *HistorySource {
	return &HistorySource{data: map[string][]types.Candle{}, Now: now}
}

// Add registers candles for instrument+granularity (sorted by Start).
func (h *HistorySource) Add(instrument, granularity string, cs []types.Candle) {
	h.data[instrument+"|"+granularity] = cs
}

// Candles implements the session/candles RESTSource against history.
func (h *HistorySource) Candles(_ context.Context, instrument, granularity string, count int, from, to time.Time) (oanda.CandlesResponse, error) {
	all := h.data[instrument+"|"+granularity]
	simNow := h.Now()
	gran := types.Timeframe(granularity).Duration()
	var picked []types.Candle
	for _, c := range all {
		if !c.Start.Add(gran).After(simNow) { // fully closed before simNow
			if !from.IsZero() && c.Start.Before(from) {
				continue
			}
			if !to.IsZero() && !c.Start.Before(to) {
				continue
			}
			picked = append(picked, c)
		}
	}
	if count > 0 && len(picked) > count {
		picked = picked[len(picked)-count:]
	}
	resp := oanda.CandlesResponse{Instrument: instrument, Granularity: granularity}
	for _, c := range picked {
		resp.Candles = append(resp.Candles, oanda.RESTCandle{
			Complete: true,
			Volume:   c.Volume,
			Time:     c.Start,
			Mid: oanda.OHLC{
				O: oanda.Num(c.Open), H: oanda.Num(c.High),
				L: oanda.Num(c.Low), C: oanda.Num(c.Close),
			},
		})
	}
	return resp, nil
}
