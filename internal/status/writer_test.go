package status

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/dashboard"
)

func TestWriterWritesCanonicalStatusDocumentLocally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "status.json")
	asOf := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	doc := dashboard.StatusDoc{
		AsOf: asOf,
		Accounts: []dashboard.AccountStatus{{
			Name: "fx-usdjpy", State: "ACTIVE", StreamUp: true,
			LastTickAgeMs: 1200, LastHeartbeatAt: asOf, LastReconcileOK: true,
		}},
	}
	if err := (Writer{LocalFile: path}).Write(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	var got dashboard.StatusDoc
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.AsOf.Equal(asOf) || len(got.Accounts) != 1 || got.Accounts[0].Name != "fx-usdjpy" || !got.Accounts[0].LastReconcileOK {
		t.Fatalf("status = %+v", got)
	}
}
