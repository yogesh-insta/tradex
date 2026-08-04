package yahoo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Retry helpers for the HTTP client. Moved here with client.go: these are the
// only consumers. Telegram delivery keeps its own linear backoff in the lane
// packages, which is a different policy on a different failure mode.

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

// httpStatusError carries an HTTP status so the crumb refresh can tell an
// auth failure (401/403 — session expired, retry once with a new crumb) from
// any other HTTP error. Moved here with the client: nothing else used it.
type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

func isAuthHTTP(err error) bool {
	var he *httpStatusError
	return errors.As(err, &he) && (he.StatusCode == http.StatusUnauthorized || he.StatusCode == http.StatusForbidden)
}
