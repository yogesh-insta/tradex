package calendar

import (
	"fmt"
	"strings"
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
