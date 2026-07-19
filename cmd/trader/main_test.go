package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/oanda"
	"github.com/yogesh-insta/tradex/internal/portfolio"
	"github.com/yogesh-insta/tradex/internal/risk"
	"github.com/yogesh-insta/tradex/pkg/types"
)

type fakeTransactionHistory struct {
	response oanda.TransactionsResponse
	from     time.Time
	to       time.Time
}

func (f *fakeTransactionHistory) Transactions(_ context.Context, _ string, from, to time.Time) (oanda.TransactionsResponse, error) {
	f.from, f.to = from, to
	return f.response, nil
}

func TestRestoreDailyRiskRebuildsCountersInTransactionOrder(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	history := &fakeTransactionHistory{response: oanda.TransactionsResponse{Transactions: []oanda.Transaction{
		{Type: "ORDER_FILL", Time: now.Add(-time.Hour), PL: oanda.Num(-25)},
		{Type: "ORDER_FILL", Time: now.Add(-3 * time.Hour), PL: oanda.Num(-10)},
		{Type: "ORDER_FILL", Time: now.Add(-2 * time.Hour), PL: oanda.Num(15)},
	}}}
	acct := portfolio.NewAccount("fx", "account-1")
	acct.SetEquity(980)
	var locked string
	eng := risk.NewEngine(config.RiskConfig{DailyLossLimit: 100, ConsecutiveLossHalt: 3}, acct, "FX", risk.Deps{
		SystemState:      func() types.SystemState { return types.StateActive },
		AccountLock:      func(string) (bool, string) { return false, "" },
		ForceLockAccount: func(_ string, reason string) { locked = reason },
	})

	if err := restoreDailyRisk(context.Background(), history, "account-1", acct, eng, time.UTC, now); err != nil {
		t.Fatal(err)
	}
	if got := acct.DailyRealizedPL(); got != -20 {
		t.Fatalf("daily realized = %.2f, want -20", got)
	}
	if got := acct.ConsecutiveLosses(); got != 1 {
		t.Fatalf("consecutive losses = %d, want 1", got)
	}
	if locked != "" {
		t.Fatalf("unexpected lock: %q", locked)
	}
	if !history.from.Equal(time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)) || !history.to.Equal(now) {
		t.Fatalf("range = %s to %s", history.from, history.to)
	}
}

func TestRestoreDailyRiskLocksBreachedAccount(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	history := &fakeTransactionHistory{response: oanda.TransactionsResponse{Transactions: []oanda.Transaction{
		{Type: "ORDER_FILL", Time: now.Add(-time.Hour), PL: oanda.Num(-60)},
		{Type: "ORDER_FILL", Time: now.Add(-2 * time.Hour), PL: oanda.Num(-50)},
	}}}
	acct := portfolio.NewAccount("eu", "account-2")
	acct.SetEquity(890)
	var locked string
	var lockedAccount string
	eng := risk.NewEngine(config.RiskConfig{DailyLossLimit: 100, ConsecutiveLossHalt: 3}, acct, "EU", risk.Deps{
		SystemState:      func() types.SystemState { return types.StateActive },
		AccountLock:      func(string) (bool, string) { return false, "" },
		ForceLockAccount: func(account, reason string) { lockedAccount, locked = account, reason },
	})

	if err := restoreDailyRisk(context.Background(), history, "account-2", acct, eng, time.UTC, now); err != nil {
		t.Fatal(err)
	}
	if locked != risk.ReasonDailyLossBreaker {
		t.Fatalf("lock = %q, want %q", locked, risk.ReasonDailyLossBreaker)
	}
	if lockedAccount != "eu" {
		t.Fatalf("locked account = %q, want eu", lockedAccount)
	}
}

func TestRestoreDailyRiskUsesAccountSessionDay(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	history := &fakeTransactionHistory{}
	acct := portfolio.NewAccount("fx", "account-3")
	acct.SetEquity(1000)
	eng := risk.NewEngine(config.RiskConfig{DailyLossLimit: 100, ConsecutiveLossHalt: 3}, acct, "FX", risk.Deps{
		ForceLockAccount: func(string, string) {},
	})

	if err := restoreDailyRisk(context.Background(), history, "account-3", acct, eng, tokyo, now); err != nil {
		t.Fatal(err)
	}

	wantStart := time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)
	if !history.from.Equal(wantStart) || !history.to.Equal(now) {
		t.Fatalf("range = %s to %s, want %s to %s", history.from, history.to, wantStart, now)
	}
}

func TestDailyBaselineJobRollsAccountsInOwnSessionTimezone(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	pm := &portfolio.Manager{}
	cfg := &config.Config{Accounts: []config.AccountConfig{
		{Name: "eu-indices", OANDAID: "eu", Active: true, Strategy: "eu_love", Instruments: []string{"DE30_EUR"}},
		{Name: "fx-usdjpy", OANDAID: "fx", Active: true, Strategy: "fx_trld", Instruments: []string{"USD_JPY"}},
	}}
	pm, err = portfolio.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	eu, _ := pm.Account("eu-indices")
	fx, _ := pm.Account("fx-usdjpy")
	eu.SetEquity(1000)
	fx.SetEquity(1000)

	now := time.Date(2026, 7, 19, 14, 30, 0, 0, time.UTC) // 23:30 Tokyo, 16:30 Berlin.
	job := newDailyBaselineJobAt(pm, accountSessionLocations(pm, berlin, tokyo),
		slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })

	eu.RecordClose(-10)
	eu.MarkTraded("DE30_EUR", "2026-07-19")
	fx.RecordClose(-20)
	fx.MarkTraded("USD_JPY", "2026-07-19")
	now = now.Add(time.Hour) // 00:30 Tokyo next day, still 17:30 Berlin.
	if err := job(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fx.DailyRealizedPL(); got != 0 || fx.TradedOn("USD_JPY", "2026-07-19") {
		t.Fatalf("FX was not reset at Tokyo midnight: realized %.2f, traded %v", got, fx.TradedOn("USD_JPY", "2026-07-19"))
	}
	if got := eu.DailyRealizedPL(); got != -10 || !eu.TradedOn("DE30_EUR", "2026-07-19") {
		t.Fatalf("EU reset at Tokyo midnight: realized %.2f, traded %v", got, eu.TradedOn("DE30_EUR", "2026-07-19"))
	}

	fx.RecordClose(-30)
	fx.MarkTraded("USD_JPY", "2026-07-20")
	now = time.Date(2026, 7, 19, 22, 30, 0, 0, time.UTC) // 00:30 Berlin next day, 07:30 Tokyo.
	if err := job(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := eu.DailyRealizedPL(); got != 0 || eu.TradedOn("DE30_EUR", "2026-07-19") {
		t.Fatalf("EU was not reset at Berlin midnight: realized %.2f, traded %v", got, eu.TradedOn("DE30_EUR", "2026-07-19"))
	}
	if got := fx.DailyRealizedPL(); got != -30 || !fx.TradedOn("USD_JPY", "2026-07-20") {
		t.Fatalf("FX reset at Berlin midnight: realized %.2f, traded %v", got, fx.TradedOn("USD_JPY", "2026-07-20"))
	}
}
