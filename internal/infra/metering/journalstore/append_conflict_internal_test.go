package journalstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestResolveAppendConflictMissingWinner(t *testing.T) {
	t.Parallel()
	store := newSQLiteConflictStore(t, "missing-winner")

	fact := metering.Fact{
		StreamID: "stream-missing",
		FactID:   "fact-missing",
		Sequence: 1,
		Kind:     metering.FactKindCumulative,
	}
	err := store.resolveAppendConflict(context.Background(), "stream-missing\x00fact-missing", fact)
	if !errors.Is(err, ErrUniqueRaceMissingRow) {
		t.Fatalf("error=%v want ErrUniqueRaceMissingRow", err)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("error=%v want wrapped sql.ErrNoRows", err)
	}
}

func TestResolveAppendConflictIdenticalWinnerIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newSQLiteConflictStore(t, "identical-winner")
	ctx := context.Background()
	fact := conflictTestFact("fact-identical", "stream-identical", 1)
	if err := store.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	key := "stream-identical\x00fact-identical"
	if err := store.resolveAppendConflict(ctx, key, fact); err != nil {
		t.Fatalf("identical winner must be idempotent: %v", err)
	}
}

func TestResolveAppendConflictDifferentPayloadIsCollision(t *testing.T) {
	t.Parallel()
	store := newSQLiteConflictStore(t, "collision-winner")
	ctx := context.Background()
	fact := conflictTestFact("fact-collision", "stream-collision", 1)
	if err := store.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	diff := fact
	diff.Kind = metering.FactKindDelta
	key := "stream-collision\x00fact-collision"
	err := store.resolveAppendConflict(ctx, key, diff)
	if !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("error=%v want ErrIdentityCollision", err)
	}
}

func newSQLiteConflictStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), storeID+".db")
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewDurableStore(context.Background(), bunDB, DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func conflictTestFact(factID, streamID string, seq int64) metering.Fact {
	return metering.Fact{
		FactID:      factID,
		StreamID:    streamID,
		Sequence:    seq,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{RequestID: "req-1", ALegID: "a-1", BLegID: "b-1"},
		Source:      metering.SourceObserved,
		Authority:   metering.AuthorityAuthoritative,
		Presence:    metering.PresencePresent,
		RecordedAt:  time.Unix(10, 0).UTC(),
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     1,
			Present:   true,
		}},
	}
}
