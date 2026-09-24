package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

// Phase 17.3A RED: durable per-store cutover marker, monotonic CAS, crash/restart
// safety, idempotent replay, store isolation, legacy default. No terminal
// claim/worker wiring; this is the narrow port later B consumes.

func newCutoverSQLiteStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	// Reuse the shared SQLite helper by constructing through NewDurableStore
	// over a fresh in-memory handle via the existing test helper path.
	base := newSQLiteTestStore(t)
	// Re-scope to the requested store boundary on the same migrated database
	// handle so isolation tests can share one database across store IDs.
	if base.StoreID() == storeID {
		return base
	}
	store, err := NewDurableStore(context.Background(), base.DB(), Config{StoreID: storeID})
	if err != nil {
		t.Fatalf("NewDurableStore %q: %v", storeID, err)
	}
	// Do not close base separately; both handles share the same bun.DB. Closing
	// via the derived store is sufficient for test cleanup.
	return store
}

func TestAccountingCutoverLegacyDefaultPreservesV1(t *testing.T) {
	t.Parallel()
	store := newCutoverSQLiteStore(t, "cutover-legacy-default")
	ctx := context.Background()
	_, err := store.GetAccountingCutover(ctx)
	if !errors.Is(err, billing.ErrAccountingCutoverNotFound) {
		t.Fatalf("fresh Get err = %v, want ErrAccountingCutoverNotFound", err)
	}
	marker, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if marker.State != billing.AccountingCutoverV1Active {
		t.Fatalf("default state = %q, want v1_active", marker.State)
	}
	if marker.ActivePostingOwner != billing.AccountingPostingOwnerV1 {
		t.Fatalf("default owner = %q, want v1", marker.ActivePostingOwner)
	}
	if marker.Generation != billing.AccountingCutoverGenerationV1 {
		t.Fatalf("default generation = %d, want 1", marker.Generation)
	}
	if marker.Version != 1 || marker.Epoch != 1 {
		t.Fatalf("default version/epoch = %d/%d, want 1/1", marker.Version, marker.Epoch)
	}
	if err := marker.Validate(); err != nil {
		t.Fatalf("default marker must validate: %v", err)
	}
	got, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatalf("Get after Ensure: %v", err)
	}
	if got.TransitionID != marker.TransitionID || got.Version != marker.Version || got.State != marker.State {
		t.Fatalf("Get after Ensure mismatch: %#v vs %#v", got, marker)
	}
}

func TestAccountingCutoverEnsureRaceConverges(t *testing.T) {
	t.Parallel()
	base := newSQLiteTestStore(t)
	const contenders = 16
	var wg sync.WaitGroup
	results := make([]billing.AccountingCutoverMarker, contenders)
	errs := make([]error, contenders)
	for i := range contenders {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s, err := NewDurableStore(context.Background(), base.DB(), Config{StoreID: "cutover-ensure-race"})
			if err != nil {
				errs[idx] = err
				return
			}
			m, err := s.EnsureAccountingCutover(context.Background())
			results[idx] = m
			errs[idx] = err
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("contender %d: %v", i, errs[i])
		}
	}
	for i := 1; i < contenders; i++ {
		if results[i].Version != results[0].Version || results[i].State != results[0].State || results[i].TransitionID != results[0].TransitionID {
			t.Fatalf("race diverged: %#v vs %#v", results[i], results[0])
		}
	}
}

func TestAccountingCutoverMonotonicCASChain(t *testing.T) {
	t.Parallel()
	store := newCutoverSQLiteStore(t, "cutover-chain")
	ctx := context.Background()
	current, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	chain := []billing.AccountingCutoverState{billing.AccountingCutoverV2Shadow, billing.AccountingCutoverV1Draining, billing.AccountingCutoverV2Active}
	for i, next := range chain {
		req := billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: next, TransitionID: fmt.Sprintf("chain-%d", i)}
		advanced, err := store.TransitionAccountingCutover(ctx, req)
		if err != nil {
			t.Fatalf("step %d -> %q: %v", i, next, err)
		}
		if advanced.Version != current.Version+1 || advanced.Epoch != current.Epoch+1 || advanced.State != next {
			t.Fatalf("step %d = %#v, want version %d epoch %d state %q", i, advanced, current.Version+1, current.Epoch+1, next)
		}
		current = advanced
	}
	if current.ActivePostingOwner != billing.AccountingPostingOwnerV2 {
		t.Fatalf("final owner = %q, want v2", current.ActivePostingOwner)
	}
	if current.CompatibilityFloor != billing.AccountingCompatFloorV2 {
		t.Fatalf("final floor = %q, want v2", current.CompatibilityFloor)
	}
}

func TestAccountingCutoverExactReplayIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newCutoverSQLiteStore(t, "cutover-replay")
	ctx := context.Background()
	current, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "replay-exact"}
	first, err := store.TransitionAccountingCutover(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.TransitionAccountingCutover(ctx, req)
	if err != nil {
		t.Fatalf("exact replay must succeed: %v", err)
	}
	if second.Version != first.Version || second.Epoch != first.Epoch || second.State != first.State || second.TransitionID != first.TransitionID {
		t.Fatalf("replay mismatch: %#v vs %#v", second, first)
	}
	// Conflicting payload under the same expected version must be rejected.
	conflict := billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "different-id"}
	if _, err := store.TransitionAccountingCutover(ctx, conflict); !errors.Is(err, billing.ErrAccountingCutoverConflict) && !errors.Is(err, billing.ErrAccountingCutoverFence) {
		t.Fatalf("conflicting replay err = %v, want Conflict/Fence", err)
	}
}

func TestAccountingCutoverStaleCASFailsClassified(t *testing.T) {
	t.Parallel()
	store := newCutoverSQLiteStore(t, "cutover-stale")
	ctx := context.Background()
	current, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stale := billing.AccountingCutoverTransition{ExpectedVersion: current.Version + 100, ExpectedEpoch: current.Epoch + 100, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "stale"}
	if _, err := store.TransitionAccountingCutover(ctx, stale); !errors.Is(err, billing.ErrAccountingCutoverFence) {
		t.Fatalf("stale err = %v, want ErrAccountingCutoverFence", err)
	}
	// Advance once, then retry with the old expected version.
	advanced, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "advance"})
	if err != nil {
		t.Fatal(err)
	}
	_ = advanced
	retryOld := billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "retry-old"}
	if _, err := store.TransitionAccountingCutover(ctx, retryOld); !errors.Is(err, billing.ErrAccountingCutoverFence) && !errors.Is(err, billing.ErrAccountingCutoverConflict) {
		t.Fatalf("retry-old err = %v, want Fence/Conflict", err)
	}
}

func TestAccountingCutoverRejectsInvalidSkippedBackward(t *testing.T) {
	t.Parallel()
	store := newCutoverSQLiteStore(t, "cutover-invalid")
	ctx := context.Background()
	current, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Skipped.
	skipped := billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "skip"}
	if _, err := store.TransitionAccountingCutover(ctx, skipped); !errors.Is(err, billing.ErrAccountingCutoverInvalid) {
		t.Fatalf("skipped err = %v, want ErrAccountingCutoverInvalid", err)
	}
	// Invalid state.
	bogus := billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: billing.AccountingCutoverState("bogus"), TransitionID: "bogus"}
	if _, err := store.TransitionAccountingCutover(ctx, bogus); !errors.Is(err, billing.ErrAccountingCutoverInvalid) {
		t.Fatalf("bogus err = %v, want ErrAccountingCutoverInvalid", err)
	}
	// Backward after advancing.
	advanced, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "fwd"})
	if err != nil {
		t.Fatal(err)
	}
	back := billing.AccountingCutoverTransition{ExpectedVersion: advanced.Version, ExpectedEpoch: advanced.Epoch, NextState: billing.AccountingCutoverV1Active, TransitionID: "back"}
	if _, err := store.TransitionAccountingCutover(ctx, back); !errors.Is(err, billing.ErrAccountingCutoverInvalid) {
		t.Fatalf("backward err = %v, want ErrAccountingCutoverInvalid", err)
	}
}

func TestAccountingCutoverStoreIsolation(t *testing.T) {
	t.Parallel()
	base := newSQLiteTestStore(t)
	ctx := context.Background()
	storeA, err := NewDurableStore(ctx, base.DB(), Config{StoreID: "cutover-iso-a"})
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewDurableStore(ctx, base.DB(), Config{StoreID: "cutover-iso-b"})
	if err != nil {
		t.Fatal(err)
	}
	markerA, err := storeA.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	advancedA, err := storeA.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: markerA.Version, ExpectedEpoch: markerA.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "iso-a"})
	if err != nil {
		t.Fatal(err)
	}
	markerB, err := storeB.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if markerB.State != billing.AccountingCutoverV1Active || markerB.Version != 1 {
		t.Fatalf("store B must remain at default, got %#v", markerB)
	}
	if advancedA.State == markerB.State && advancedA.Version == markerB.Version && advancedA.TransitionID == markerB.TransitionID {
		t.Fatalf("store isolation violated: A %#v equals B %#v", advancedA, markerB)
	}
	gotA, err := storeA.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.State != billing.AccountingCutoverV2Shadow {
		t.Fatalf("store A state = %q, want v2_shadow", gotA.State)
	}
}

func TestAccountingCutoverTwoConcurrentContenders(t *testing.T) {
	t.Parallel()
	store := newCutoverSQLiteStore(t, "cutover-contenders")
	ctx := context.Background()
	current, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 2
	var wg sync.WaitGroup
	outcomes := make([]error, contenders)
	markers := make([]billing.AccountingCutoverMarker, contenders)
	for i := range contenders {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: fmt.Sprintf("contender-%d", idx)}
			m, err := store.TransitionAccountingCutover(context.Background(), req)
			markers[idx] = m
			outcomes[idx] = err
		}(i)
	}
	wg.Wait()
	successes := 0
	for i := range outcomes {
		if outcomes[i] == nil {
			successes++
		} else if !errors.Is(outcomes[i], billing.ErrAccountingCutoverFence) && !errors.Is(outcomes[i], billing.ErrAccountingCutoverConflict) {
			t.Fatalf("contender %d err = %v, want nil/Fence/Conflict", i, outcomes[i])
		}
	}
	if successes != 1 {
		t.Fatalf("contenders successes = %d, want exactly 1 (outcomes %v)", successes, outcomes)
	}
	final, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.Version != current.Version+1 || final.State != billing.AccountingCutoverV2Shadow {
		t.Fatalf("final = %#v, want version %d state v2_shadow", final, current.Version+1)
	}
}

func TestAccountingCutoverRestartSafe(t *testing.T) {
	t.Parallel()
	// File-backed SQLite so close/reopen preserves the durable marker.
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "billing.db")))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	seedTestSchemaIfEmpty(t, bunDB)
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: "cutover-restart"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	current, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "restart"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen the same file and verify the marker survived the restart.
	sqlDB2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB2.SetMaxOpenConns(4)
	bunDB2, err := db.NewBunDB(sqlDB2, db.DialectSQLite)
	if err != nil {
		_ = sqlDB2.Close()
		t.Fatal(err)
	}
	seedTestSchemaIfEmpty(t, bunDB2)
	reopened, err := NewDurableStore(context.Background(), bunDB2, Config{StoreID: "cutover-restart"})
	if err != nil {
		_ = bunDB2.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != advanced.Version || got.State != advanced.State || got.TransitionID != advanced.TransitionID {
		t.Fatalf("restart mismatch: %#v vs %#v", got, advanced)
	}
}

func TestAccountingCutoverContextAware(t *testing.T) {
	t.Parallel()
	store := newCutoverSQLiteStore(t, "cutover-ctx")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.EnsureAccountingCutover(canceled); err == nil {
		t.Fatalf("canceled Ensure must fail")
	}
	if _, _, err := accountingCutoverProbeWithCanceledContext(t, store); err == nil {
		t.Fatalf("canceled Get must fail")
	}
	if _, err := store.TransitionAccountingCutover(canceled, billing.AccountingCutoverTransition{ExpectedVersion: 1, ExpectedEpoch: 1, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "ctx"}); err == nil {
		t.Fatalf("canceled Transition must fail")
	}
	var nilCtx context.Context
	if _, err := store.EnsureAccountingCutover(nilCtx); err == nil {
		t.Fatalf("nil Ensure must fail")
	}
}

func accountingCutoverProbeWithCanceledContext(t *testing.T, store *DurableStore) (billing.AccountingCutoverMarker, bool, error) {
	t.Helper()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	// Use a short timeout wrapper to prove deadline propagation without sleeping.
	ctx, cancel2 := context.WithTimeout(canceled, time.Second)
	defer cancel2()
	marker, err := store.GetAccountingCutover(ctx)
	return marker, err == nil, err
}
