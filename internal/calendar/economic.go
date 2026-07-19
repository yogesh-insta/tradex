// Package calendar answers two questions for the rest of the system, per
// docs/specs/10-economic-calendar.md:
//
//  1. "How long until the next high-impact economic event for this region?"
//     (news filter — durable state written by an external poller, read here)
//  2. "Is the market open today / is it a half-day?" (checked-in holiday file)
//
// Both are fail-safe: stale or missing economic state is treated as "event
// imminent" so risk blocks entries rather than trading blind.
package calendar

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Event is one normalized economic-calendar entry (times UTC).
type Event struct {
	Region string    `json:"region"`
	Title  string    `json:"title"`
	Impact string    `json:"impact"` // "high" | "medium" | "low"
	Time   time.Time `json:"time"`
}

// State is the durable calendar-state.json schema.
type State struct {
	AsOf   time.Time `json:"as_of"`
	Events []Event   `json:"events"`
}

// Provider fetches the durable calendar state. Implementations: local file
// (v1) and a GCS stub (wired later; failing provider = fail-safe).
type Provider interface {
	Fetch() (State, error)
}

// FileProvider reads calendar-state.json from local disk.
type FileProvider struct{ Path string }

// Fetch parses the state file.
func (p FileProvider) Fetch() (State, error) {
	raw, err := os.ReadFile(p.Path)
	if err != nil {
		return State{}, fmt.Errorf("calendar state: %w", err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("calendar state parse: %w", err)
	}
	return s, nil
}

// GCSProvider is a stub for the future GCS-backed state object. It always
// errors, which the cache treats fail-safe (event imminent).
type GCSProvider struct{ Object string }

// Fetch is not implemented in v1.
func (p GCSProvider) Fetch() (State, error) {
	return State{}, fmt.Errorf("gcs calendar provider not implemented (object %s)", p.Object)
}

// NoImminent is the duration returned when the state is fresh and no
// high-impact event is upcoming — effectively "no blackout".
const NoImminent = 365 * 24 * time.Hour

// Cache holds the latest calendar state in RAM and answers the news-filter
// question. Refresh is driven externally (scheduler). Safe for concurrent use.
type Cache struct {
	provider     Provider
	stalenessMax time.Duration

	mu    sync.RWMutex
	state State
	ok    bool // a successful fetch has happened
}

// NewCache builds a cache over provider; stalenessMax bounds how old the
// state's as_of may be before the fail-safe kicks in.
func NewCache(provider Provider, stalenessMax time.Duration) *Cache {
	return &Cache{provider: provider, stalenessMax: stalenessMax}
}

// Refresh re-fetches the durable state. Errors leave the previous state in
// place (it will age into the fail-safe on its own).
func (c *Cache) Refresh() error {
	s, err := c.provider.Fetch()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.state, c.ok = s, true
	c.mu.Unlock()
	return nil
}

// SetState injects state directly (tests, backtests).
func (c *Cache) SetState(s State) {
	c.mu.Lock()
	c.state, c.ok = s, true
	c.mu.Unlock()
}

// TimeToHighImpact returns the time until the next high-impact event for the
// region. Fail-safe: if no state was ever loaded, or as_of is older than
// stalenessMax, it returns 0 ("event imminent"). If the state is fresh and no
// upcoming high-impact event exists, it returns NoImminent.
func (c *Cache) TimeToHighImpact(region string, now time.Time) time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.ok || now.Sub(c.state.AsOf) > c.stalenessMax {
		return 0
	}
	next := time.Duration(-1)
	for _, ev := range c.state.Events {
		if ev.Region != region || ev.Impact != "high" {
			continue
		}
		d := ev.Time.Sub(now)
		if d < 0 {
			continue
		}
		if next < 0 || d < next {
			next = d
		}
	}
	if next < 0 {
		return NoImminent
	}
	return next
}

// Fresh reports whether the cached state is usable (loaded and within
// stalenessMax of now).
func (c *Cache) Fresh(now time.Time) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ok && now.Sub(c.state.AsOf) <= c.stalenessMax
}
