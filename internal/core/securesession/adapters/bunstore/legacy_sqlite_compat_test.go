package bunstore_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/bunstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

func TestLegacySQLiteFixture_ReadCompat(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "testdata", "legacy_sqlite.db")
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	bunDB, err := db.OpenSQLiteBun(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := bunstore.NewContext(ctx, bunDB)
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	got, err := s.LoadByID(ctx, "legacy-sess-1")
	if err != nil {
		t.Fatalf("LoadByID: %v", err)
	}
	if got.Status != domain.SessionStatusQuarantined {
		t.Fatalf("status: got %q want quarantined", got.Status)
	}
	if got.QuarantineReasonCode != "legacy-fixture" {
		t.Fatalf("reason: got %q", got.QuarantineReasonCode)
	}
	if got.ALegID != "a-leg-legacy" {
		t.Fatalf("a_leg_id: got %q", got.ALegID)
	}
	if got.CreatedAt != time.Unix(1, 0) {
		t.Fatalf("created_at: got %v", got.CreatedAt)
	}
}
