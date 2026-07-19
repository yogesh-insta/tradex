package session

import (
	"context"
	"fmt"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
)

// Hub multiplexes EU and FX session controllers so the trader can OnCandle /
// State without sharing mutable state between markets (spec 16).
type Hub struct {
	byInstrument map[string]*Controller
	controllers  []*Controller
}

// NewHub builds an empty hub.
func NewHub() *Hub {
	return &Hub{byInstrument: map[string]*Controller{}}
}

// Add registers a controller for its configured instruments. Instruments must
// not overlap across controllers.
func (h *Hub) Add(c *Controller) error {
	for _, inst := range c.cfg.Instruments {
		if prev, ok := h.byInstrument[inst]; ok {
			return fmt.Errorf("session hub: instrument %s already on controller tz=%s", inst, prev.cfg.TZ)
		}
		h.byInstrument[inst] = c
	}
	h.controllers = append(h.controllers, c)
	return nil
}

// OnCandle routes to the instrument's controller.
func (h *Hub) OnCandle(ev types.MarketEvent) {
	if c, ok := h.byInstrument[ev.Instrument]; ok {
		c.OnCandle(ev)
	}
}

// State returns SessionState from the instrument's controller.
func (h *Hub) State(instrument string, now time.Time) (types.SessionState, bool) {
	c, ok := h.byInstrument[instrument]
	if !ok {
		return types.SessionState{}, false
	}
	return c.State(instrument, now)
}

// ControllerFor returns the controller owning the instrument.
func (h *Hub) ControllerFor(instrument string) (*Controller, bool) {
	c, ok := h.byInstrument[instrument]
	return c, ok
}

// RefreshATR refreshes ATR on every registered controller.
func (h *Hub) RefreshATR(ctx context.Context) error {
	var first error
	for _, c := range h.controllers {
		if err := c.RefreshATR(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// RecoverRange recovers mid-session state on every controller.
func (h *Hub) RecoverRange(ctx context.Context, now time.Time) error {
	var first error
	for _, c := range h.controllers {
		if err := c.RecoverRange(ctx, now); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Controllers lists registered controllers (for scheduler jobs).
func (h *Hub) Controllers() []*Controller { return h.controllers }
