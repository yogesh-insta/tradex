// Package metrics provides counters and gauges behind a small interface.
// v1 ships an in-memory implementation that can dump to logs; a Prometheus
// or Cloud Monitoring implementation can be swapped in later.
package metrics

import (
	"sync"
)

// Registry records counters and gauges. Implementations must be safe for
// concurrent use.
type Registry interface {
	Inc(name string)
	Add(name string, delta float64)
	Set(name string, value float64)
	Snapshot() map[string]float64
}

// Noop discards all metrics.
type Noop struct{}

func (Noop) Inc(string)          {}
func (Noop) Add(string, float64) {}
func (Noop) Set(string, float64) {}
func (Noop) Snapshot() map[string]float64 {
	return nil
}

// Memory is a race-safe in-memory registry.
type Memory struct {
	mu sync.Mutex
	m  map[string]float64
}

// NewMemory returns an empty in-memory registry.
func NewMemory() *Memory { return &Memory{m: map[string]float64{}} }

func (r *Memory) Inc(name string) { r.Add(name, 1) }

func (r *Memory) Add(name string, delta float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[name] += delta
}

func (r *Memory) Set(name string, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[name] = value
}

// Snapshot returns a copy of all current values.
func (r *Memory) Snapshot() map[string]float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]float64, len(r.m))
	for k, v := range r.m {
		out[k] = v
	}
	return out
}
