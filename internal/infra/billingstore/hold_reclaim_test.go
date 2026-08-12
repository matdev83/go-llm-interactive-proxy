package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteReclaimExpiredHoldsDoesNotReleaseWithoutStaleSafeProof(t *testing.T) {
	runReclaimExpiredHoldsIsNoop(t, newSQLiteTestStore(t), "expire-acct")
}

func TestSQLiteReclaimExpiredHoldsSkipsInFlightExecutionWithoutTUR(t *testing.T) {
	// Authorize then "execute" without sealing a TUR: same durable shape as an
	// abandon, but reclaim must not assume non-execution from expires_at alone.
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	journalAccount(t, store, "inflight-acct")
	input := authorizationInput("inflight-acct", "turn-inflight", "auth-inflight", 40)
	input.ExpiresAt = time.Now().UTC().Add(20 * time.Millisecond)
	if _, err := store.Authorize(ctx, input); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	n, err := store.ReclaimExpiredHolds(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("in-flight reclaim=%d want 0", n)
	}
	account, err := store.GetAccount(ctx, "inflight-acct")
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 40 {
		t.Fatalf("in-flight reserved=%d want 40", account.ReservedNano)
	}
}

func TestSQLiteReclaimExpiredHoldsSkipsWhenProcessingBlocks(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	journalAccount(t, store, "expire-block")
	input := authorizationInput("expire-block", "turn-block", "auth-block", 25)
	input.ExpiresAt = time.Now().UTC().Add(30 * time.Millisecond)
	auth, err := store.Authorize(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord("expire-block", "turn-block", auth.ID, billing.MoneyEvidence{Currency: "USD", Present: false})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	n, err := store.ReclaimExpiredHolds(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("reclaimed=%d want 0 while processing pending", n)
	}
}

func TestSQLiteExplicitStaleSafeReleaseStillFreesAbandonedHold(t *testing.T) {
	runExplicitStaleSafeRelease(t, newSQLiteTestStore(t), "stale-ok")
}

func runReclaimExpiredHoldsIsNoop(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	journalAccount(t, store, accountID)
	input := authorizationInput(accountID, "turn-expire", "auth-expire", 25)
	input.ExpiresAt = time.Now().UTC().Add(30 * time.Millisecond)
	if _, err := store.Authorize(ctx, input); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	n, err := store.ReclaimExpiredHolds(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("reclaimed=%d want 0: expires_at alone must not free reserved exposure (Req 15.6)", n)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 25 {
		t.Fatalf("reserved=%d want 25 after TTL-only reclaim attempt", account.ReservedNano)
	}
}

func runExplicitStaleSafeRelease(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	journalAccount(t, store, accountID)
	input := authorizationInput(accountID, "turn-stale", "auth-stale", 15)
	input.ExpiresAt = time.Now().UTC().Add(time.Hour)
	auth, err := store.Authorize(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(-20 * time.Minute)
	if _, err := store.db.NewRaw(`UPDATE authorization_holds SET created_at = ?, expires_at = ? WHERE account_id = ? AND authorization_id = ?`, created, created.Add(15*time.Minute), accountID, auth.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	inactive := created.Add(2 * time.Minute)
	now := created.Add(20 * time.Minute)
	release, err := store.ReleaseAuthorization(ctx, billing.ReleaseAuthorizationInput{
		AccountID: accountID, AuthorizationID: auth.ID, TURKey: auth.TURKey,
		FullClose: true, Reason: billing.ReleaseStaleSafe, SourceKey: accountID + "-stale-1",
		AlegInactiveAt: inactive, Now: now, MaximumExecutionLife: 10 * time.Minute, SafetyGrace: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if release.After.ReservedNano != 0 {
		t.Fatalf("stale-safe release reserved=%d want 0", release.After.ReservedNano)
	}
}
