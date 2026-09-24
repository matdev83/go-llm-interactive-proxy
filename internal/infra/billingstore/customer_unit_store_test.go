package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestDurableStoreCustomerUnitLedgerGrantDebitReplayAndConflict(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := customerUnitTestKey("account-1", "pool-1", "period-1")

	grant := customerUnitOperation("grant-1", key, billing.CustomerUnitOperationGrant, "", "5", 0, 1)
	got, err := store.ApplyCustomerUnitOperation(ctx, grant)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if got.Status != billing.CustomerUnitOperationApplied || got.After.Available == nil || got.After.Available.CanonicalString() != "5/0" {
		t.Fatalf("grant result = %#v", got)
	}

	debit := customerUnitOperation("debit-1", key, billing.CustomerUnitOperationDebit, "", "3", 1, 1)
	got, err = store.ApplyCustomerUnitOperation(ctx, debit)
	if err != nil {
		t.Fatalf("debit: %v", err)
	}
	if got.After.Available == nil || got.After.Available.CanonicalString() != "2/0" || got.After.Consumed == nil || got.After.Consumed.CanonicalString() != "3/0" {
		t.Fatalf("debit result = %#v", got)
	}

	debit.ExpectedVersion = got.After.Version
	debit.Fence = got.After.Fence + 1
	replayed, err := store.ApplyCustomerUnitOperation(ctx, debit)
	if err != nil {
		t.Fatalf("exact debit replay: %v", err)
	}
	if replayed.Status != billing.CustomerUnitOperationReplayed || !replayed.Replayed || replayed.After.Available.CanonicalString() != "2/0" {
		t.Fatalf("replay result = %#v", replayed)
	}

	conflict := debit
	conflict.Quantity = metering.Decimal{Coefficient: "4"}
	if _, err := store.ApplyCustomerUnitOperation(ctx, conflict); !errors.Is(err, billing.ErrCustomerUnitOperationConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrCustomerUnitOperationConflict", err)
	}
}

func TestDurableStoreCustomerUnitLedgerReservationLifecycleAndRollback(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := customerUnitTestKey("account-reserve", "pool", "period")

	grant := customerUnitOperation("grant-reserve", key, billing.CustomerUnitOperationGrant, "", "10", 0, 7)
	grantResult, err := store.ApplyCustomerUnitOperation(ctx, grant)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	reserve := customerUnitOperation("reserve-1", key, billing.CustomerUnitOperationReserve, "res-1", "4", grantResult.After.Version, 7)
	reserveResult, err := store.ApplyCustomerUnitOperation(ctx, reserve)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if reserveResult.After.Available.CanonicalString() != "6/0" || reserveResult.After.Reserved.CanonicalString() != "4/0" {
		t.Fatalf("reserve result = %#v", reserveResult)
	}
	commit := customerUnitOperation("commit-1", key, billing.CustomerUnitOperationCommit, "res-1", "4", reserveResult.After.Version, 7)
	commitResult, err := store.ApplyCustomerUnitOperation(ctx, commit)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if commitResult.After.Reserved.CanonicalString() != "0/0" || commitResult.After.Consumed.CanonicalString() != "4/0" {
		t.Fatalf("commit result = %#v", commitResult)
	}

	reserve2 := customerUnitOperation("reserve-2", key, billing.CustomerUnitOperationReserve, "res-2", "2", commitResult.After.Version, 7)
	reserve2Result, err := store.ApplyCustomerUnitOperation(ctx, reserve2)
	if err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	release := customerUnitOperation("release-2", key, billing.CustomerUnitOperationRelease, "res-2", "2", reserve2Result.After.Version, 7)
	releaseResult, err := store.ApplyCustomerUnitOperation(ctx, release)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if releaseResult.After.Available.CanonicalString() != "6/0" || releaseResult.After.Reserved.CanonicalString() != "0/0" {
		t.Fatalf("release result = %#v", releaseResult)
	}

	badCommit := customerUnitOperation("commit-bad", key, billing.CustomerUnitOperationCommit, "res-2", "2", releaseResult.After.Version, 7)
	if _, err := store.ApplyCustomerUnitOperation(ctx, badCommit); !errors.Is(err, billing.ErrCustomerUnitReservationInvalid) {
		t.Fatalf("closed reservation error = %v, want ErrCustomerUnitReservationInvalid", err)
	}
}

func TestDurableStoreCustomerUnitLedgerFallbackIsolationAndAuthority(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := customerUnitTestKey("account-fallback", "pool", "period")
	otherKey := customerUnitTestKey("account-fallback", "other-pool", "period")

	grant := customerUnitOperation("grant-fallback", key, billing.CustomerUnitOperationGrant, "", "2", 0, 1)
	grantResult, err := store.ApplyCustomerUnitOperation(ctx, grant)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	fallback := customerUnitOperation("debit-fallback", key, billing.CustomerUnitOperationDebit, "", "5", grantResult.After.Version, 1)
	fallback.MonetaryFallbackBound = &billing.Money{Nano: 100, Currency: "USD"}
	fallbackResult, err := store.ApplyCustomerUnitOperation(ctx, fallback)
	if err != nil {
		t.Fatalf("bounded fallback debit: %v", err)
	}
	if !fallbackResult.FallbackRequired || fallbackResult.AppliedQuantity.CanonicalString() != "2/0" || fallbackResult.UncoveredQuantity.CanonicalString() != "3/0" {
		t.Fatalf("fallback result = %#v", fallbackResult)
	}
	if fallbackResult.After.Available.CanonicalString() != "0/0" || fallbackResult.After.Consumed.CanonicalString() != "2/0" {
		t.Fatalf("fallback balance = %#v", fallbackResult.After)
	}

	otherGrant := customerUnitOperation("grant-other", otherKey, billing.CustomerUnitOperationGrant, "", "1", 0, 1)
	if _, err := store.ApplyCustomerUnitOperation(ctx, otherGrant); err != nil {
		t.Fatalf("isolated pool grant: %v", err)
	}
	providerGauge := customerUnitOperation("provider-gauge", key, billing.CustomerUnitOperationDebit, "", "1", fallbackResult.After.Version, 1)
	providerGauge.Source = billing.CustomerUnitOperationSource("provider_account_window")
	if _, err := store.ApplyCustomerUnitOperation(ctx, providerGauge); !errors.Is(err, billing.ErrCustomerUnitAuthority) {
		t.Fatalf("provider gauge source error = %v, want ErrCustomerUnitAuthority", err)
	}
}

func TestDurableStoreCustomerUnitLedgerConcurrentVersionFencePreventsDoubleSpend(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := customerUnitTestKey("account-concurrent", "pool", "period")
	grant := customerUnitOperation("grant-concurrent", key, billing.CustomerUnitOperationGrant, "", "1", 0, 1)
	grantResult, err := store.ApplyCustomerUnitOperation(ctx, grant)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	const workers = 8
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			op := customerUnitOperation("concurrent-debit-"+string(rune('a'+i)), key, billing.CustomerUnitOperationDebit, "", "1", grantResult.After.Version, 1)
			_, applyErr := store.ApplyCustomerUnitOperation(ctx, op)
			results <- applyErr
		}(i)
	}
	wg.Wait()
	close(results)

	var applied, stale int
	for err := range results {
		switch {
		case err == nil:
			applied++
		case errors.Is(err, billing.ErrCustomerUnitStaleVersion):
			stale++
		default:
			t.Fatalf("concurrent debit error = %v", err)
		}
	}
	if applied != 1 || stale != workers-1 {
		t.Fatalf("concurrent outcomes applied=%d stale=%d, want 1/%d", applied, stale, workers-1)
	}
}

func TestDurableStoreCustomerUnitLedgerRejectsStaleVersionAndFence(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	key := customerUnitTestKey("account-stale", "pool", "period")
	grant := customerUnitOperation("grant-stale", key, billing.CustomerUnitOperationGrant, "", "3", 0, 4)
	grantResult, err := store.ApplyCustomerUnitOperation(ctx, grant)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	valid := customerUnitOperation("debit-stale-valid", key, billing.CustomerUnitOperationDebit, "", "1", grantResult.After.Version, grantResult.After.Fence)
	validResult, err := store.ApplyCustomerUnitOperation(ctx, valid)
	if err != nil {
		t.Fatalf("valid debit: %v", err)
	}
	staleVersion := customerUnitOperation("debit-stale-version", key, billing.CustomerUnitOperationDebit, "", "1", grantResult.After.Version, validResult.After.Fence)
	if _, err := store.ApplyCustomerUnitOperation(ctx, staleVersion); !errors.Is(err, billing.ErrCustomerUnitStaleVersion) {
		t.Fatalf("stale version error = %v, want ErrCustomerUnitStaleVersion", err)
	}
	staleFence := customerUnitOperation("debit-stale-fence", key, billing.CustomerUnitOperationDebit, "", "1", validResult.After.Version, grantResult.After.Fence-1)
	if _, err := store.ApplyCustomerUnitOperation(ctx, staleFence); !errors.Is(err, billing.ErrCustomerUnitStaleFence) {
		t.Fatalf("stale fence error = %v, want ErrCustomerUnitStaleFence", err)
	}
	balance, err := store.CustomerUnitBalance(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Available.CanonicalString() != "2/0" || balance.Consumed.CanonicalString() != "1/0" {
		t.Fatalf("balance after stale operations = %#v", balance)
	}
}

func TestDurableStoreCustomerUnitLedgerPersistsAcrossStoreReopen(t *testing.T) {
	ctx := context.Background()
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
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "restart"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	key := customerUnitTestKey("account-restart", "pool", "period")
	grant := customerUnitOperation("grant-restart", key, billing.CustomerUnitOperationGrant, "", "7", 0, 2)
	if _, err := store.ApplyCustomerUnitOperation(ctx, grant); err != nil {
		_ = store.Close()
		t.Fatalf("grant before reopen: %v", err)
	}
	if err := store.Close(); err != nil {
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
	seedTestSchemaIfEmpty(t, reopenedDB)
	reopened, err := NewDurableStore(ctx, reopenedDB, Config{StoreID: "restart"})
	if err != nil {
		_ = reopenedDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replay, err := reopened.ApplyCustomerUnitOperation(ctx, grant)
	if err != nil {
		t.Fatalf("replay after reopen: %v", err)
	}
	if replay.Status != billing.CustomerUnitOperationReplayed || !replay.Replayed || replay.After.Available.CanonicalString() != "7/0" {
		t.Fatalf("replay after reopen = %#v", replay)
	}
	conflict := grant
	conflict.Quantity = metering.Decimal{Coefficient: "8"}
	if _, err := reopened.ApplyCustomerUnitOperation(ctx, conflict); !errors.Is(err, billing.ErrCustomerUnitOperationConflict) {
		t.Fatalf("reopen conflict error = %v, want ErrCustomerUnitOperationConflict", err)
	}
}

// runCustomerUnitLedgerContract is included by both SQLite and PostgreSQL
// billing-store contracts so the customer-owned authority has one shared
// behavioral contract across dialects.
func runCustomerUnitLedgerContract(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	key := customerUnitTestKey(accountID, "contract-unit-pool", "contract-unit-period")
	grant := customerUnitOperation(accountID+"-unit-grant", key, billing.CustomerUnitOperationGrant, "", "5", 0, 1)
	grantResult, err := store.ApplyCustomerUnitOperation(ctx, grant)
	if err != nil {
		t.Fatalf("customer-unit grant: %v", err)
	}
	reserve := customerUnitOperation(accountID+"-unit-reserve", key, billing.CustomerUnitOperationReserve, "contract-unit-reservation", "2", grantResult.After.Version, 1)
	reserveResult, err := store.ApplyCustomerUnitOperation(ctx, reserve)
	if err != nil {
		t.Fatalf("customer-unit reserve: %v", err)
	}
	if reserveResult.After.Available.CanonicalString() != "3/0" || reserveResult.After.Reserved.CanonicalString() != "2/0" {
		t.Fatalf("customer-unit reserve balance: %#v", reserveResult.After)
	}
	commit := customerUnitOperation(accountID+"-unit-commit", key, billing.CustomerUnitOperationCommit, reserve.ReservationID, "2", reserveResult.After.Version, 1)
	if _, err := store.ApplyCustomerUnitOperation(ctx, commit); err != nil {
		t.Fatalf("customer-unit commit: %v", err)
	}
	if _, err := store.ApplyCustomerUnitOperation(ctx, commit); err != nil {
		t.Fatalf("customer-unit commit replay: %v", err)
	}
}

func customerUnitTestKey(account, pool, period string) billing.CustomerUnitKey {
	return billing.CustomerUnitKey{
		AccountID: account,
		PoolID:    pool,
		PeriodID:  period,
		Component: metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentCredit, Unit: metering.UnitCredit},
	}
}

func customerUnitOperation(id string, key billing.CustomerUnitKey, kind billing.CustomerUnitOperationKind, reservation, quantity string, expected, fence uint64) billing.CustomerUnitOperation {
	return billing.CustomerUnitOperation{
		Version:         billing.CustomerUnitOperationVersionV1,
		OperationID:     id,
		Key:             key,
		Kind:            kind,
		Source:          billing.CustomerUnitOperationSourceCustomerProvisioning,
		Quantity:        metering.Decimal{Coefficient: quantity},
		ReservationID:   reservation,
		ExpectedVersion: expected,
		Fence:           fence,
	}
}
