//go:build integration

package billingstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestPostgresApplyBillingResultAtomic(t *testing.T) {
	runApplyBillingResultAtomic(t, newPostgresParityStore(t), uniqueAccountID("settle-atomic"))
}

func TestPostgresApplyBillingResultOverageReject(t *testing.T) {
	runApplyBillingResultOverageReject(t, newPostgresParityStore(t), uniqueAccountID("settle-overage"))
}

func TestPostgresApplyBillingResultReplay(t *testing.T) {
	runApplyBillingResultReplay(t, newPostgresParityStore(t), uniqueAccountID("settle-replay"))
}

func TestPostgresSettlementB2BUALURCosts(t *testing.T) {
	runSettlementB2BUALURCosts(t, newPostgresParityStore(t), uniqueAccountID("settle-b2bua"))
}

func TestPostgresSettlementCrashBeforeCommit(t *testing.T) {
	runSettlementCrashBeforeCommit(t, newPostgresParityStore(t), uniqueAccountID("settle-crash"))
}

func TestPostgresSettlementCrashAfterEachMutation(t *testing.T) {
	// Parent skip-before-subtest: runSettlementCrashAfterEachMutation starts t.Run before the store factory can skip.
	_ = testkit.SkipUnlessPostgres(t)
	runSettlementCrashAfterEachMutation(t, func() *DurableStore { return newPostgresParityStore(t) }, uniqueAccountID("fault"))
}

func TestPostgresConcurrentSettlementIdempotent(t *testing.T) {
	runConcurrentSettlementIdempotent(t, newPostgresParityStore(t), uniqueAccountID("settle-conc"))
}

func TestPostgresStaleClaimerCannotSettleAfterLeaseReclaim(t *testing.T) {
	ownerA := newPostgresParityStore(t)
	ownerB := newPostgresSiblingStore(t, ownerA, "parity-settle-b")
	runStaleClaimerCannotSettleAfterLeaseReclaim(t, ownerA, ownerB, uniqueAccountID("stale-settle"))
}

func TestPostgresProcessingClaimRetryAndProcessed(t *testing.T) {
	runProcessingClaimRetryAndProcessed(t, newPostgresParityStore(t), uniqueAccountID("proc-claim"))
}

func TestPostgresProcessingReclaimsExpiredLease(t *testing.T) {
	runProcessingReclaimsExpiredLeaseAndRejectsFingerprintConflict(t, newPostgresParityStore(t), uniqueAccountID("proc-lease"))
}

func TestPostgresStaleClaimerCannotMarkAfterLeaseReclaim(t *testing.T) {
	ownerA := newPostgresParityStore(t)
	ownerB := newPostgresSiblingStore(t, ownerA, "parity-proc-b")
	runStaleClaimerCannotMarkAfterLeaseReclaim(t, ownerA, ownerB, uniqueAccountID("stale-mark"))
}

func TestPostgresTURImmutabilityAndReplayConflict(t *testing.T) {
	store := newPostgresParityStore(t)
	runImmutableBillingRowsRejectUpdateAndDelete(t, store, uniqueAccountID("immutable"))
	runAppendUsageRecordReplayAndFingerprintConflict(t, newPostgresParityStore(t), uniqueAccountID("replay-tur"))
}

func TestPostgresTrustedFundingPaymentAdjustmentAndRelease(t *testing.T) {
	store := newPostgresParityStore(t)
	runTrustedFundingPaymentAdjustment(t, store, uniqueAccountID("trusted"))
	runAuthorizationReleaseClosesHold(t, newPostgresParityStore(t), uniqueAccountID("release"))
}

func TestPostgresReclaimTTLNoopAndStaleSafeRelease(t *testing.T) {
	store := newPostgresParityStore(t)
	runReclaimExpiredHoldsIsNoop(t, store, uniqueAccountID("reclaim"))
	runExplicitStaleSafeRelease(t, newPostgresParityStore(t), uniqueAccountID("stale-safe"))
}

func TestPostgresReconcileRebuildAndAdmissionBlock(t *testing.T) {
	store := newPostgresParityStore(t)
	runReconcileAccountRebuildsSettlement(t, store, uniqueAccountID("reconcile"))
	runReconcileRequiredBlocksAdmission(t, newPostgresParityStore(t), uniqueAccountID("reconcile-block"))
}

func TestPostgresReportsAccountTurnOperatorTrialSession(t *testing.T) {
	store := newPostgresParityStore(t)
	runPhase7Reports(t, store, uniqueAccountID("reports"))
	runSessionReportAggregatesAuthoritativeSession(t, newPostgresParityStore(t), uniqueAccountID("session"))
}

func TestPostgresReplayAccountMatchesDeterministicOperationFixtures(t *testing.T) {
	store := newPostgresParityStore(t)
	for _, fixture := range replayStoreFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			runReplayStoreFixture(t, store, uniqueAccountID("replay"), fixture)
		})
	}
}

func TestPostgresPhase6OpeningAndSnapshotImmutability(t *testing.T) {
	store := newPostgresParityStore(t)
	ctx := context.Background()
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}
	accountID := uniqueAccountID("phase6")
	journalAccount(t, store, accountID)
	if _, err := store.db.NewRaw(`UPDATE billing_account_openings SET opening_balance_nano = 1 WHERE account_id = ?`, accountID).Exec(ctx); err == nil {
		t.Fatal("opening-balance row must be immutable")
	}
	if _, err := store.db.NewRaw(`DELETE FROM billing_account_openings WHERE account_id = ?`, accountID).Exec(ctx); err == nil {
		t.Fatal("opening-balance delete must be rejected")
	}
}

func newPostgresParityStore(t *testing.T) *DurableStore {
	t.Helper()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	t.Cleanup(cancel)
	bunDB, err := db.OpenPostgresBun(ctx, dsn, db.PoolSettings{MaxOpenConns: 8, MaxIdleConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "parity-postgres"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newPostgresSiblingStore(t *testing.T, primary *DurableStore, storeID string) *DurableStore {
	t.Helper()
	store, err := OpenStore(context.Background(), primary.db, Config{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func uniqueAccountID(prefix string) string {
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
}
