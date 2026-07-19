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
//	ACTIVE ⇄ PAUSED (PAUSE/RESUME)
//	* → SYSTEM_LOCKED (breaker) → ACTIVE (only via signed RE_ARM)
//	* → DISABLED (FLATTEN)
type Machine struct {
	mu    sync.Mutex
	state types.SystemState
	log   *slog.Logger

	// onTransition fires after every state change (observability/alerts).
	onTransition func(from, to types.SystemState, reason string)
}

// NewMachine starts in the given state (boot recovery may start LOCKED if the
// daily loss was already breached).
func NewMachine(initial types.SystemState, log *slog.Logger, onTransition func(from, to types.SystemState, reason string)) *Machine {
	return &Machine{state: initial, log: log, onTransition: onTransition}
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

// ForceLock trips the breaker → SYSTEM_LOCKED (from any non-disabled state).
// No automatic re-arm ever.
func (m *Machine) ForceLock(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == types.StateDisabled {
		return
	}
	m.transition(types.StateSystemLock, reason)
}

// ReArm is the only exit from SYSTEM_LOCKED. Idempotent no-op when not locked
// and not disabled (spec: explicit response, no error).
func (m *Machine) ReArm() (changed bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.state {
	case types.StateSystemLock, types.StateDisabled:
		m.transition(types.StateActive, "RE_ARM command")
		return true, nil
	default:
		return false, nil
	}
}

// Disable moves to DISABLED (FLATTEN aftermath) from any state.
func (m *Machine) Disable(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transition(types.StateDisabled, reason)
}
