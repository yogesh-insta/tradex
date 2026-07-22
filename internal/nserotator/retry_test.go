package nserotator

import (
	"context"
	"errors"
	"testing"
)

func TestRetryDoHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryDo(ctx, 3, func() error {
		calls++
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d want 1 (no retry after cancelled ctx)", calls)
	}
}

func TestRetryCountDefault(t *testing.T) {
	if retryCount(0) != 3 || retryCount(-1) != 3 {
		t.Fatal("expected default retry count 3")
	}
	if retryCount(5) != 5 {
		t.Fatal("expected configured retry count")
	}
}
