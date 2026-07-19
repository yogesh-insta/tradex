package dashboard

import (
	"context"
	"fmt"
	"io"
	"os"
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
