package dashboard

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"cloud.google.com/go/storage"
)

// FileOrGCSFetcher reads local paths or gs://bucket/object.
type FileOrGCSFetcher struct {
	GCS *storage.Client // nil ⇒ gs:// unsupported
}

// Fetch implements ObjectFetcher.
func (f FileOrGCSFetcher) Fetch(ctx context.Context, uri string) ([]byte, error) {
	if uri == "" {
		return nil, fmt.Errorf("empty object uri")
	}
	if strings.HasPrefix(uri, "gs://") {
		if f.GCS == nil {
			return nil, fmt.Errorf("gcs client not configured for %s", uri)
		}
		bucket, object, err := parseGSURI(uri)
		if err != nil {
			return nil, err
		}
		r, err := f.GCS.Bucket(bucket).Object(object).NewReader(ctx)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	}
	return os.ReadFile(uri)
}

// Put implements ObjectPutter. GCS writes are the live equity-history path;
// a local path is for dev. Failures are returned to the caller, which must
// treat them as best-effort.
func (f FileOrGCSFetcher) Put(ctx context.Context, uri string, data []byte) error {
	if uri == "" {
		return fmt.Errorf("empty object uri")
	}
	if strings.HasPrefix(uri, "gs://") {
		if f.GCS == nil {
			return fmt.Errorf("gcs client not configured for %s", uri)
		}
		bucket, object, err := parseGSURI(uri)
		if err != nil {
			return err
		}
		w := f.GCS.Bucket(bucket).Object(object).NewWriter(ctx)
		w.ContentType = "application/json"
		w.CacheControl = "no-cache"
		if _, err := w.Write(data); err != nil {
			_ = w.Close()
			return fmt.Errorf("gcs write %s: %w", uri, err)
		}
		return w.Close()
	}
	if err := os.MkdirAll(filepath.Dir(uri), 0o755); err != nil {
		return err
	}
	return os.WriteFile(uri, data, 0o644)
}

func parseGSURI(uri string) (bucket, object string, err error) {
	rest := strings.TrimPrefix(uri, "gs://")
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i == len(rest)-1 {
		return "", "", fmt.Errorf("invalid gs uri %q", uri)
	}
	return rest[:i], rest[i+1:], nil
}

// StaticObjectFetcher returns fixed bytes per URI (tests / mock).
type StaticObjectFetcher map[string][]byte

// Fetch implements ObjectFetcher.
func (s StaticObjectFetcher) Fetch(_ context.Context, uri string) ([]byte, error) {
	b, ok := s[uri]
	if !ok {
		return nil, fmt.Errorf("object not found: %s", uri)
	}
	return b, nil
}

// Put implements ObjectPutter so tests can assert equity-history writes.
func (s StaticObjectFetcher) Put(_ context.Context, uri string, data []byte) error {
	s[uri] = append([]byte(nil), data...)
	return nil
}
