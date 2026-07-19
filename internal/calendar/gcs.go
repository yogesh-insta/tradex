package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"cloud.google.com/go/storage"
)

// ParseGSURI splits gs://bucket/object into bucket and object name.
func ParseGSURI(uri string) (bucket, object string, err error) {
	if !strings.HasPrefix(uri, "gs://") {
		return "", "", fmt.Errorf("invalid gs uri %q", uri)
	}
	rest := strings.TrimPrefix(uri, "gs://")
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i == len(rest)-1 {
		return "", "", fmt.Errorf("invalid gs uri %q", uri)
	}
	return rest[:i], rest[i+1:], nil
}

// GCSProvider reads durable calendar-state.json from a GCS object.
// Uses Application Default Credentials (GCE/Cloud Run SA).
type GCSProvider struct {
	Object string
	// Client optional; when nil, Fetch opens a short-lived client.
	Client *storage.Client
	// Timeout bounds a single fetch (default 15s).
	Timeout time.Duration
}

// Fetch implements Provider.
func (p GCSProvider) Fetch() (State, error) {
	if p.Object == "" {
		return State{}, fmt.Errorf("gcs calendar provider: empty object uri")
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := p.Client
	owned := false
	if client == nil {
		c, err := storage.NewClient(ctx)
		if err != nil {
			return State{}, fmt.Errorf("gcs calendar provider: client: %w", err)
		}
		client = c
		owned = true
	}
	if owned {
		defer client.Close()
	}

	bucket, object, err := ParseGSURI(p.Object)
	if err != nil {
		return State{}, err
	}
	r, err := client.Bucket(bucket).Object(object).NewReader(ctx)
	if err != nil {
		return State{}, fmt.Errorf("gcs calendar fetch %s: %w", p.Object, err)
	}
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		return State{}, fmt.Errorf("gcs calendar read %s: %w", p.Object, err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("gcs calendar parse %s: %w", p.Object, err)
	}
	return s, nil
}

// WriteStateGCS writes calendar state to gs://bucket/object (overwrite).
func WriteStateGCS(ctx context.Context, client *storage.Client, uri string, state State) error {
	if client == nil {
		return fmt.Errorf("gcs write: nil client")
	}
	bucket, object, err := ParseGSURI(uri)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	w := client.Bucket(bucket).Object(object).NewWriter(ctx)
	w.ContentType = "application/json"
	w.CacheControl = "no-cache"
	if _, err := w.Write(raw); err != nil {
		_ = w.Close()
		return fmt.Errorf("gcs write %s: %w", uri, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("gcs write close %s: %w", uri, err)
	}
	return nil
}
