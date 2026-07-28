package bunstore_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuity/bunstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
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

	got, err := s.ResolveALeg(ctx, "legacy-ck")
	if err != nil {
		t.Fatalf("ResolveALeg: %v", err)
	}
	if got.ContinuityKey != "legacy-ck" {
		t.Fatalf("continuity_key: got %q", got.ContinuityKey)
	}
	if got.ALegID == "" {
		t.Fatal("expected non-empty a_leg_id")
	}
	rows, err := s.LoadAttempts(ctx, got.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("attempts: got %d want 1", len(rows))
	}
	if rows[0].BackendID != "stub" || rows[0].Outcome != lipapi.AttemptSuccess {
		t.Fatalf("attempt row: %+v", rows[0])
	}
}
