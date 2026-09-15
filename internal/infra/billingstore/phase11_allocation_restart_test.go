package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

func TestPhase11AllocationPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "allocation.db")))
	firstSQL, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	firstSQL.SetMaxOpenConns(4)
	firstDB, err := db.NewBunDB(firstSQL, db.DialectSQLite)
	if err != nil {
		_ = firstSQL.Close()
		t.Fatal(err)
	}
	first, err := NewDurableStore(ctx, firstDB, Config{StoreID: "restart-allocation"})
	if err != nil {
		_ = firstDB.Close()
		t.Fatal(err)
	}
	record := phase11AllocationRecord(t, "allocation-restart", 1)
	record.SourceSubject.StoreID = "restart-allocation"
	for i := range record.Targets {
		if !record.Targets[i].Unallocated {
			record.Targets[i].Target.StoreID = "restart-allocation"
		}
	}
	canonical, err := record.Canonical()
	if err != nil {
		_ = first.Close()
		t.Fatalf("canonical before restart: %v", err)
	}
	correction := canonical.Clone()
	correction.ID = "allocation-restart-replacement"
	correction.Operation = economics.AllocationOperationReplacement
	correction.Supersedes = []economics.AllocationRef{{StoreID: "restart-allocation", AllocationID: canonical.ID, Version: canonical.Version, PayloadHash: canonical.Fingerprint()}}
	if err := first.AppendAllocation(ctx, correction); err != nil {
		_ = first.Close()
		t.Fatalf("append pending replacement before restart: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedSQL, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	reopenedSQL.SetMaxOpenConns(4)
	reopenedDB, err := db.NewBunDB(reopenedSQL, db.DialectSQLite)
	if err != nil {
		_ = reopenedSQL.Close()
		t.Fatal(err)
	}
	reopened, err := NewDurableStore(ctx, reopenedDB, Config{StoreID: "restart-allocation"})
	if err != nil {
		_ = reopenedDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	pending, err := reopened.ResolveAllocationSupersession(ctx)
	if err != nil {
		t.Fatalf("resolve pending after restart: %v", err)
	}
	if pending.Status != economics.AllocationSupersessionPending || len(pending.Effective) != 0 || len(pending.Pending) != 1 {
		t.Fatalf("pending after restart = %+v", pending)
	}
	if err := reopened.AppendAllocation(ctx, record); err != nil {
		t.Fatalf("append predecessor after restart: %v", err)
	}
	if err := reopened.AppendAllocation(ctx, correction); err != nil {
		t.Fatalf("replay replacement after restart: %v", err)
	}
	resolved, err := reopened.ResolveAllocationSupersession(ctx)
	if err != nil {
		t.Fatalf("resolve after predecessor arrival: %v", err)
	}
	if resolved.Status != economics.AllocationSupersessionResolved || !resolved.Complete || !resolved.Payable || len(resolved.Effective) != 1 || resolved.Effective[0].ID != correction.ID {
		t.Fatalf("resolved after restart = %+v", resolved)
	}
	got, err := reopened.GetAllocation(ctx, record.ID, record.Version)
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if got.Fingerprint() != record.Fingerprint() {
		t.Fatalf("fingerprint after restart = %q, want %q", got.Fingerprint(), record.Fingerprint())
	}
}
