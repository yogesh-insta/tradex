// Package strategy defines the pluggable Strategy interface, the registry of
// named strategy constructors, and the per-instrument 1:1 router. Strategies
// are pure decision functions: (MarketEvent, SessionState) in, *Signal out.
package strategy

import (
	"fmt"
	"sort"
	"sync"

	"github.com/yogesh-insta/tradex/pkg/types"
)

// Strategy is the pure decision interface. Analyze must be deterministic and
// side-effect free; nil means "no setup" (the common case).
type Strategy interface {
	Name() string
	Analyze(ev types.MarketEvent, st types.SessionState) *types.Signal
}

// Factory builds a strategy from its config section. Registered under the
// registry key used in account config (e.g. "eu_love").
type Factory func() (Strategy, error)

var (
	regMu    sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds a named strategy factory. Called from strategy packages'
// wiring (not init-magic: the trader registers explicitly with its config).
func Register(name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	registry[name] = f
}

// New instantiates a registered strategy by name.
func New(name string) (Strategy, error) {
	regMu.RLock()
	f, ok := registry[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("strategy %q not registered (have %v)", name, Names())
	}
	return f()
}

// Names lists registered strategy keys (sorted, for error messages).
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Router dispatches each MarketEvent to exactly ONE strategy — the
// instrument's 1:1 match (architecture §4).
type Router struct {
	byInstrument map[string]Strategy
}

// NewRouter builds a router from instrument→strategy assignments.
func NewRouter() *Router {
	return &Router{byInstrument: map[string]Strategy{}}
}

// Assign maps an instrument to its strategy. Reassignment is an error —
// dispatch must stay 1:1.
func (r *Router) Assign(instrument string, s Strategy) error {
	if prev, ok := r.byInstrument[instrument]; ok {
		return fmt.Errorf("instrument %s already routed to %s", instrument, prev.Name())
	}
	r.byInstrument[instrument] = s
	return nil
}

// Dispatch routes the event to the instrument's strategy. Returns nil if no
// strategy is assigned or no setup fired.
func (r *Router) Dispatch(ev types.MarketEvent, st types.SessionState) *types.Signal {
	s, ok := r.byInstrument[ev.Instrument]
	if !ok {
		return nil
	}
	return s.Analyze(ev, st)
}

// StrategyFor returns the assigned strategy for an instrument.
func (r *Router) StrategyFor(instrument string) (Strategy, bool) {
	s, ok := r.byInstrument[instrument]
	return s, ok
}
