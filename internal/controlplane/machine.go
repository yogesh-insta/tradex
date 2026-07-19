// Package controlplane implements the Master Control Routine state machine
// and the HMAC-signed command webhook per docs/specs/09-control-plane.md.
package controlplane

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/yogesh-insta/tradex/pkg/types"
)

// Machine owns the SystemState transitions. RAM-authoritative for the process.
//
//	ACTIVE ⇄ PAUSED (PAUSE/RESUME, process-wide)
//	* → DISABLED (FLATTEN, process-wide)
//
// Risk breakers are account-scoped locks, deliberately separate from the
// process-wide state. A breaker on one account must not interrupt another
// account's entry lane.
type Machine struct {
	mu           sync.Mutex
	state        types.SystemState
	accountLocks map[string]string
	log          *slog.Logger

	// onTransition fires after every state change (observability/alerts).
	onTransition func(from, to types.SystemState, reason string)
	// onAccountLock fires once when an account breaker is first tripped.
	onAccountLock func(account, reason string)
}

// NewMachine starts in the given process-wide state. Boot recovery applies
// breaker locks afterwards, once each account's ledger has been restored.
func NewMachine(initial types.SystemState, log *slog.Logger, onTransition func(from, to types.SystemState, reason string)) *Machine {
	return &Machine{state: initial, accountLocks: map[string]string{}, log: log, onTransition: onTransition}
}

// SetOnAccountLock registers the breaker-lock observer before the machine is
// shared with the risk engines.
func (m *Machine) SetOnAccountLock(fn func(account, reason string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onAccountLock = fn
}

// State returns the current state (race-safe).
func (m *Machine) State() types.SystemState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Machine) transition(to types.SystemState, reason string) {
	from := m.state
	if from == to {
		return
	}
	m.state = to
	m.log.Warn("system state transition", "from", from, "to", to, "reason", reason)
	if m.onTransition != nil {
		m.onTransition(from, to, reason)
	}
}

// Pause moves ACTIVE → PAUSED. Idempotent when already paused.
func (m *Machine) Pause() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.state {
	case types.StateActive, types.StatePaused:
		m.transition(types.StatePaused, "PAUSE command")
		return nil
	default:
		return fmt.Errorf("cannot PAUSE from %s", m.state)
	}
}

// Resume moves PAUSED → ACTIVE. Locked/disabled states refuse.
func (m *Machine) Resume() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.state {
	case types.StatePaused, types.StateActive:
		m.transition(types.StateActive, "RESUME command")
		return nil
	default:
		return fmt.Errorf("cannot RESUME from %s (RE_ARM required for %s)", m.state, types.StateSystemLock)
	}
}

// ForceLock preserves the legacy process-wide lock for an explicit global
// operator action. Risk breakers must use ForceLockAccount instead.
func (m *Machine) ForceLock(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == types.StateDisabled {
		return
	}
	m.transition(types.StateSystemLock, reason)
}

// ForceLockAccount locks only one account's entry lane. No automatic re-arm
// ever occurs; RE_ARM clears these locks.
func (m *Machine) ForceLockAccount(account, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == types.StateDisabled {
		return
	}
	if _, alreadyLocked := m.accountLocks[account]; alreadyLocked {
		return
	}
	m.accountLocks[account] = reason
	m.log.Warn("account risk lock", "account", account, "reason", reason)
	if m.onAccountLock != nil {
		m.onAccountLock(account, reason)
	}
}

// AccountLock reports whether an account is breaker-locked and why.
func (m *Machine) AccountLock(account string) (locked bool, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reason, locked = m.accountLocks[account]
	return locked, reason
}

// LockedAccounts returns a snapshot of account-to-breaker-reason mappings.
func (m *Machine) LockedAccounts() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	locks := make(map[string]string, len(m.accountLocks))
	for account, reason := range m.accountLocks {
		locks[account] = reason
	}
	return locks
}

// ReArm clears every breaker-locked account and also exits the legacy global
// SYSTEM_LOCKED/DISABLED states. It returns the accounts that need a fresh
// baseline snapshot. It is idempotent when neither kind of lock is present.
func (m *Machine) ReArm() (accounts []string, changed bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for account := range m.accountLocks {
		accounts = append(accounts, account)
	}
	m.accountLocks = map[string]string{}
	switch m.state {
	case types.StateSystemLock, types.StateDisabled:
		m.transition(types.StateActive, "RE_ARM command")
		return accounts, true, nil
	default:
		return accounts, len(accounts) > 0, nil
	}
}

// Disable moves to DISABLED (FLATTEN aftermath) from any state.
func (m *Machine) Disable(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transition(types.StateDisabled, reason)
}
