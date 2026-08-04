package nserotator

import (
	"context"
	"time"
)

// retryLinearBackoff waits attempt*2 seconds (attempt >= 1) or returns ctx.Err().
// Telegram delivery only. The Yahoo client's exponential backoff (retryDo /
// retryCount / retryExpBackoff) moved to internal/yahoo along with the client —
// they had no other caller here.
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
