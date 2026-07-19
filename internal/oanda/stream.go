package oanda

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// StreamHandler receives parsed pricing-stream messages. Called from the
// stream goroutine; must not block for long.
type StreamHandler func(msg StreamMessage)

// StreamConfig tunes reconnect/heartbeat behavior (02-market-data-stream.md).
type StreamConfig struct {
	Instruments      []string
	HeartbeatTimeout time.Duration
	BackoffBase      time.Duration
	BackoffMax       time.Duration
}

// RunPricingStream consumes the OANDA HTTP pricing stream for the account,
// invoking handler for every PRICE/HEARTBEAT message. It reconnects with
// exponential backoff + full jitter and forces a reconnect when no message
// (tick or heartbeat) arrives within HeartbeatTimeout. Blocks until ctx is
// cancelled.
func (c *Client) RunPricingStream(ctx context.Context, accountID string, cfg StreamConfig, log *slog.Logger, handler StreamHandler) error {
	backoff := cfg.BackoffBase
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := c.streamOnce(ctx, accountID, cfg, handler)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Warn("pricing stream disconnected; reconnecting", "error", err, "backoff", backoff.String())
		// Full jitter: sleep U(0, backoff), then grow toward BackoffMax.
		sleep := time.Duration(rand.Int63n(int64(backoff) + 1))
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff *= 2
		if backoff > cfg.BackoffMax {
			backoff = cfg.BackoffMax
		}
	}
}

func (c *Client) streamOnce(ctx context.Context, accountID string, cfg StreamConfig, handler StreamHandler) error {
	q := url.Values{}
	q.Set("instruments", strings.Join(cfg.Instruments, ","))
	u := c.streamBase + "/v3/accounts/" + accountID + "/pricing/stream?" + q.Encode()

	// Streaming request: no overall timeout, only the heartbeat watchdog.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept-Datetime-Format", "RFC3339")

	client := &http.Client{} // no Timeout: long-lived stream
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pricing stream: HTTP %d", resp.StatusCode)
	}

	// Heartbeat watchdog: cancel the read if the stream goes silent.
	lastMsg := make(chan struct{}, 1)
	go func() {
		timer := time.NewTimer(cfg.HeartbeatTimeout)
		defer timer.Stop()
		for {
			select {
			case <-streamCtx.Done():
				return
			case <-lastMsg:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(cfg.HeartbeatTimeout)
			case <-timer.C:
				cancel() // no message within timeout: force reconnect
				return
			}
		}
	}()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		select {
		case lastMsg <- struct{}{}:
		default:
		}
		var msg StreamMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// Malformed line: skip, don't crash (spec 02 failure mode).
			continue
		}
		handler(msg)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("pricing stream: connection closed")
}
