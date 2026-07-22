package nserotator

import (
	"context"
	"time"
)

func retryCount(configured int) int {
	if configured <= 0 {
		return 3
	}
	return configured
}

// retryExpBackoff waits 2^attempt seconds (attempt >= 1) or returns ctx.Err().
func retryExpBackoff(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	d := time.Duration(1<<attempt) * time.Second
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryLinearBackoff waits attempt*2 seconds (attempt >= 1) or returns ctx.Err().
func retryLinearBackoff(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	d := time.Duration(attempt) * 2 * time.Second
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func retryDo(ctx context.Context, attempts int, fn func() error) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if err := retryExpBackoff(ctx, i); err != nil {
				return err
			}
		}
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}
