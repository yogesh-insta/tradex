// Package portfolio owns per-account trading state: the equity baseline,
// daily realized P&L, and the consecutive-loss counter — the aggregates the
// risk gates consume. One Account per OANDA sub-account (per market/category).
package portfolio

import (
	"fmt"
	"sync"

	"github.com/yogesh-insta/tradex/internal/config"
)

// Account tracks one OANDA sub-account. Implements risk.AccountState.
type Account struct {
	name    string
	oandaID string

	mu             sync.Mutex
	equity         float64
	equityKnown    bool
	baselineEquity float64
	dailyRealized  float64
	consecLosses   int
}

// NewAccount builds an account in the "state unknown" condition; the first
// equity refresh (boot) makes it usable.
func NewAccount(name, oandaID string) *Account {
	return &Account{name: name, oandaID: oandaID}
}

// Name implements risk.AccountState.
func (a *Account) Name() string { return a.name }

// OANDAID returns the broker account id.
func (a *Account) OANDAID() string { return a.oandaID }

// Equity implements risk.AccountState.
func (a *Account) Equity() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.equity
}

// EquityKnown implements risk.AccountState (false → risk rejects everything).
func (a *Account) EquityKnown() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.equityKnown
}

// DailyRealizedPL implements risk.AccountState (negative = loss vs baseline).
func (a *Account) DailyRealizedPL() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dailyRealized
}

// ConsecutiveLosses implements risk.AccountState.
func (a *Account) ConsecutiveLosses() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.consecLosses
}

// SetEquity records a fresh equity read (reconcile / boot).
func (a *Account) SetEquity(equity float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.equity = equity
	a.equityKnown = true
	if a.baselineEquity == 0 {
		a.baselineEquity = equity
	}
}

// MarkUnknown flags the account state as unreadable (risk rejects new entries).
func (a *Account) MarkUnknown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.equityKnown = false
}

// RecordClose settles one closed trade into the daily counters: realized P&L
// accumulates; a loss increments the consecutive-loss counter, any win resets it.
func (a *Account) RecordClose(realizedPL float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dailyRealized += realizedPL
	if realizedPL < 0 {
		a.consecLosses++
	} else if realizedPL > 0 {
		a.consecLosses = 0
	}
}

// SnapshotBaseline resets the daily tracker against current equity. Called at
// session start and by RE_ARM (spec 09: "snapshots new baseline equity").
func (a *Account) SnapshotBaseline() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.baselineEquity = a.equity
	a.dailyRealized = 0
	a.consecLosses = 0
}

// BaselineEquity returns the last snapshotted baseline.
func (a *Account) BaselineEquity() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.baselineEquity
}

// Manager maps accounts → instrument groups → strategies.
type Manager struct {
	accounts     map[string]*Account
	byInstrument map[string]*Account
	riskCfg      map[string]config.RiskConfig // merged per account
	strategyOf   map[string]string            // account name -> strategy key
}

// NewManager builds accounts from config (active accounts only).
func NewManager(cfg *config.Config) (*Manager, error) {
	m := &Manager{
		accounts:     map[string]*Account{},
		byInstrument: map[string]*Account{},
		riskCfg:      map[string]config.RiskConfig{},
		strategyOf:   map[string]string{},
	}
	for _, ac := range cfg.Accounts {
		if !ac.Active {
			continue
		}
		if _, dup := m.accounts[ac.Name]; dup {
			return nil, fmt.Errorf("portfolio: duplicate account name %q", ac.Name)
		}
		acct := NewAccount(ac.Name, ac.OANDAID)
		m.accounts[ac.Name] = acct
		m.riskCfg[ac.Name] = cfg.Risk.Merged(ac.Risk)
		m.strategyOf[ac.Name] = ac.Strategy
		for _, inst := range ac.Instruments {
			if prev, dup := m.byInstrument[inst]; dup {
				return nil, fmt.Errorf("portfolio: instrument %s in both %s and %s", inst, prev.Name(), ac.Name)
			}
			m.byInstrument[inst] = acct
		}
	}
	return m, nil
}

// Account returns the named account.
func (m *Manager) Account(name string) (*Account, bool) {
	a, ok := m.accounts[name]
	return a, ok
}

// AccountFor returns the account trading the instrument.
func (m *Manager) AccountFor(instrument string) (*Account, bool) {
	a, ok := m.byInstrument[instrument]
	return a, ok
}

// Accounts lists all active accounts.
func (m *Manager) Accounts() []*Account {
	out := make([]*Account, 0, len(m.accounts))
	for _, a := range m.accounts {
		out = append(out, a)
	}
	return out
}

// RiskConfig returns the merged risk config for an account.
func (m *Manager) RiskConfig(name string) config.RiskConfig { return m.riskCfg[name] }

// StrategyOf returns the strategy key assigned to an account.
func (m *Manager) StrategyOf(name string) string { return m.strategyOf[name] }
