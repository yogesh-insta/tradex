// Package scheduler is a lightweight ticker-based job runner for housekeeping
// (calendar refresh, equity reconcile, pre-session ATR). It is explicitly NOT
// the signal trigger — signals fire on M5 candle close.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Job is a named periodic task. Errors are logged, never fatal.
type Job struct {
	Name     string
	Interval time.Duration
	// RunAtStart runs the job once immediately when the scheduler starts.
	RunAtStart bool
	Fn         func(ctx context.Context) error
}

// Scheduler runs jobs on independent tickers until the context is cancelled.
type Scheduler struct {
	jobs []Job
	log  *slog.Logger
}

// New builds an empty scheduler.
func New(log *slog.Logger) *Scheduler {
	return &Scheduler{log: log}
}

// Add registers a job. Call before Run.
func (s *Scheduler) Add(job Job) { s.jobs = append(s.jobs, job) }

// Run blocks until ctx is done, running each job on its own goroutine/ticker.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, job := range s.jobs {
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			if j.RunAtStart {
				s.exec(ctx, j)
			}
			ticker := time.NewTicker(j.Interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.exec(ctx, j)
				}
			}
		}(job)
	}
	wg.Wait()
}

func (s *Scheduler) exec(ctx context.Context, j Job) {
	if err := j.Fn(ctx); err != nil {
		s.log.Error("scheduled job failed", "job", j.Name, "error", err)
	}
}
