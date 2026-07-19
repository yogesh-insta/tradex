// Package status publishes the trader heartbeat document for the dashboard
// and external liveness checks. It is intentionally off the trading path.
package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/yogesh-insta/tradex/internal/dashboard"
)

// Writer persists the canonical dashboard StatusDoc locally and/or to GCS.
type Writer struct {
	LocalFile string
	GCSObject string
	Timeout   time.Duration
}

// Write serializes doc once, then best-effort writes each configured target.
// A failed target does not prevent the other target from being updated.
func (w Writer) Write(ctx context.Context, doc dashboard.StatusDoc) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	var errs []error
	if w.LocalFile != "" {
		if err := writeLocal(w.LocalFile, raw); err != nil {
			errs = append(errs, err)
		}
	}
	if w.GCSObject != "" {
		if err := w.writeGCS(ctx, raw); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func writeLocal(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("status mkdir %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".status-*.json")
	if err != nil {
		return fmt.Errorf("status create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("status write temp: %w", err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("status chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("status close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("status rename: %w", err)
	}
	return nil
}

func (w Writer) writeGCS(ctx context.Context, raw []byte) error {
	bucket, object, err := parseGSURI(w.GCSObject)
	if err != nil {
		return err
	}
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := storage.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("status gcs client: %w", err)
	}
	defer client.Close()
	out := client.Bucket(bucket).Object(object).NewWriter(ctx)
	out.ContentType = "application/json"
	out.CacheControl = "no-cache"
	if _, err := out.Write(raw); err != nil {
		_ = out.Close()
		return fmt.Errorf("status gcs write %s: %w", w.GCSObject, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("status gcs close %s: %w", w.GCSObject, err)
	}
	return nil
}

func parseGSURI(uri string) (bucket, object string, err error) {
	if !strings.HasPrefix(uri, "gs://") {
		return "", "", fmt.Errorf("invalid status gcs uri %q", uri)
	}
	rest := strings.TrimPrefix(uri, "gs://")
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i == len(rest)-1 {
		return "", "", fmt.Errorf("invalid status gcs uri %q", uri)
	}
	return rest[:i], rest[i+1:], nil
}
