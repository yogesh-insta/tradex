// Command backtester replays OANDA historical candles (downloaded and cached
// locally as CSV) through the SAME strategy + risk + trade-management code
// path as the live trader, with a simulated fill model (spread + slippage).
//
// Usage:
//
//	backtester --config config/config.dev.yaml \
//	  --instrument DE30_EUR --from 2026-05-01 --to 2026-07-01
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yogesh-insta/tradex/internal/backtest"
	"github.com/yogesh-insta/tradex/internal/calendar"
	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/logging"
	"github.com/yogesh-insta/tradex/internal/oanda"
	"github.com/yogesh-insta/tradex/pkg/types"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  string
		instruments string
		fromStr     string
		toStr       string
		verbose     bool
	)
	flag.StringVar(&configPath, "config", "config/config.dev.yaml", "path to environment config YAML")
	flag.StringVar(&instruments, "instrument", "DE30_EUR", "instrument(s), comma-separated")
	flag.StringVar(&fromStr, "from", "", "start date YYYY-MM-DD (UTC), required")
	flag.StringVar(&toStr, "to", "", "end date YYYY-MM-DD (UTC), required")
	flag.BoolVar(&verbose, "v", false, "verbose logging (debug)")
	flag.Parse()

	if fromStr == "" || toStr == "" {
		return fmt.Errorf("--from and --to are required (YYYY-MM-DD)")
	}
	from, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}
	if !to.After(from) {
		return fmt.Errorf("--to must be after --from")
	}

	cfg, err := config.Load(configPath, config.EnvResolver{})
	if err != nil {
		return err
	}
	if err := cfg.ValidateBacktest(); err != nil {
		return fmt.Errorf("config validation failed:\n%w", err)
	}

	level := slog.LevelWarn
	if verbose {
		level = slog.LevelDebug
	}
	log := logging.New("backtester", cfg.Env, level)
	ctx := context.Background()

	// OANDA fetcher is optional: cache-only runs work without credentials.
	var fetcher backtest.CandleFetcher
	if cfg.OANDA.Token != "" {
		fetcher = oanda.NewClient(cfg.OANDA.Host, cfg.OANDA.StreamHost, cfg.OANDA.Token, oanda.Options{
			RequestTimeout: 30 * time.Second,
		})
	}

	// Daily candles start earlier to seed the 14-day Wilder ATR.
	dailyFrom := from.AddDate(0, 0, -cfg.EUSession.ATRPeriodDays*3)

	data := map[string]backtest.InstrumentData{}
	for _, inst := range strings.Split(instruments, ",") {
		inst = strings.TrimSpace(inst)
		m5, err := backtest.LoadOrFetch(ctx, fetcher, cfg.Backtest.DataDir, inst, types.M5, from, to)
		if err != nil {
			return err
		}
		h1, err := backtest.LoadOrFetch(ctx, fetcher, cfg.Backtest.DataDir, inst, types.H1, from, to)
		if err != nil {
			return err
		}
		daily, err := backtest.LoadOrFetch(ctx, fetcher, cfg.Backtest.DataDir, inst, types.D, dailyFrom, to)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %d M5, %d H1, %d daily candles\n", inst, len(m5), len(h1), len(daily))
		data[inst] = backtest.InstrumentData{M5: m5, H1: h1, Daily: daily}
	}

	var holidays *calendar.Holidays
	if cfg.Calendar.Holidays.File != "" {
		holidays, err = calendar.LoadHolidays(cfg.Calendar.Holidays.File)
		if err != nil {
			return err
		}
	}

	result, err := backtest.Run(ctx, backtest.Params{
		Cfg:      cfg,
		Data:     data,
		Log:      log,
		Holidays: holidays,
	})
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("=== trades ===")
	fmt.Print(backtest.FormatTrades(result.Closed))
	fmt.Println()
	fmt.Println("=== summary ===")
	fmt.Println(result.Metrics.Summary())

	if err := os.MkdirAll(cfg.Backtest.OutputDir, 0o755); err != nil {
		return err
	}
	curvePath := filepath.Join(cfg.Backtest.OutputDir,
		fmt.Sprintf("equity_%s_%s_%s.csv", strings.ReplaceAll(instruments, ",", "+"), fromStr, toStr))
	f, err := os.Create(curvePath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := result.Metrics.WriteEquityCurveCSV(f); err != nil {
		return err
	}
	fmt.Printf("\nequity curve written to %s\n", curvePath)
	return nil
}
