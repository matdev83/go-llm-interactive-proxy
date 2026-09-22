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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 17.3B2a RED: cutover coordinator — draining entry closes gate before
// classification, bounded multi-batch V1 pinning (customer/provider/
// adjustment), pending+leased, restart resume, unclassifiable stays draining,
// draining claims only V1-pinned, activation precondition, V2 authorization,
// store isolation, context/cancel, SQLite (+ PG in postgres file).

func newB2aSQLiteStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	base := newSQLiteTestStore(t)
	if base.StoreID() == storeID {
		return base
	}
	store, err := NewDurableStore(context.Background(), base.DB(), Config{StoreID: storeID})
	if err != nil {
		t.Fatalf("NewDurableStore %q: %v", storeID, err)
	}
	return store
}

func b2aEnsureShadow(t *testing.T, store *DurableStore) billing.AccountingCutoverMarker {
	t.Helper()
	ctx := context.Background()
	marker, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: marker.Version, ExpectedEpoch: marker.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2a-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	return shadow
}

func b2aMustCallID(t *testing.T) billing.BillingCallID {
	t.Helper()
	id, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func b2aAppendCompleteCall(t *testing.T, store *DurableStore, accountID, bLegID string) billing.BillingCallID {
	t.Helper()
	ctx := context.Background()
	callID := b2aMustCallID(t)
	call := testIndependentCallUsageFor(callID, []string{bLegID})
	call.AccountID = accountID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatalf("AppendCallUsage: %v", err)
	}
	leg := testIndependentCallLegFor(callID, bLegID)
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatalf("AppendCallLegUsage: %v", err)
	}
	return callID
}

func TestB2aConcurrentAppendVsBeginDrainClosesGateFirst(t *testing.T) {
	t.Parallel()
	store := newB2aSQLiteStore(t, "b2a-race-gate")
	ctx := context.Background()
	_ = b2aEnsureShadow(t, store)
	// Seed one pre-drain call that must be classified.
	seeded := b2aAppendCompleteCall(t, store, "acct-b2a-race", "b-seed")
	_ = seeded
	const appenders = 8
	var wg sync.WaitGroup
	fenced := make([]error, appenders)
	for i := 0; i < appenders; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			callID, err := billing.NewBillingCallID()
			if err != nil {
				fenced[idx] = err
				return
			}
			call := testIndependentCallUsageFor(callID, []string{"b-race"})
			call.AccountID = "acct-b2a-race"
			// Concurrent append racing the drain gate: either it lands before
			// the gate (then it must be pinned) or it is fenced after.
			fenced[idx] = store.AppendCallUsage(context.Background(), call)
		}(i)
	}
	_, _, err := store.BeginCutoverDraining(ctx, "b2a-race-drain")
	if err != nil {
		t.Fatalf("BeginCutoverDraining: %v", err)
	}
	wg.Wait()
	// Gate must be closed: marker is draining.
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if marker.State != billing.AccountingCutoverV1Draining {
		t.Fatalf("state = %q, want v1_draining", marker.State)
	}
	// Every concurrent append is either pinned V1 (landed before gate) or
	// fenced (landed after gate); none may slip in unpinned.
	status, err := store.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Counts.Unclassifiable != 0 {
		t.Fatalf("unclassifiable = %d, want 0", status.Counts.Unclassifiable)
	}
	// All pending customer work must have V1 pins after classification.
	var pending int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM usage_call_records WHERE claim_status IN ('pending','claimed')`).Scan(ctx, &pending); err != nil {
		t.Fatal(err)
	}
	var pinned int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND owner = ?`, store.StoreID(), string(billing.PostingOperationCustomerSettlement), billing.PostingOwnerV1).Scan(ctx, &pinned); err != nil {
		t.Fatal(err)
	}
	if pinned < 1 {
		t.Fatalf("pinned = %d, want >= 1 (seed + pre-gate appends)", pinned)
	}
	if pinned < pending {
		t.Fatalf("pinned %d < pending %d: concurrent append slipped past gate", pinned, pending)
	}
}

func TestB2aBoundedMultiBatchClassificationPinsAll(t *testing.T) {
	t.Parallel()
	store := newB2aSQLiteStore(t, "b2a-batches")
	ctx := context.Background()
	_ = b2aEnsureShadow(t, store)
	const total = 25
	for i := 0; i < total; i++ {
		b2aAppendCompleteCall(t, store, "acct-b2a-batch", fmt.Sprintf("b-%d", i))
		// Distinct calls need distinct B-legs; each call has one leg above.
		// The loop helper already creates one call+leg per iteration.
	}
	_, status, err := store.BeginCutoverDraining(ctx, "b2a-batch-drain")
	if err != nil {
		t.Fatalf("BeginCutoverDraining: %v", err)
	}
	if status.Counts.CustomerPending < total {
		t.Fatalf("customer pending = %d, want >= %d", status.Counts.CustomerPending, total)
	}
	// Bounded batches: classification used small batches but pinned all.
	if status.Counts.V1Pinned < total {
		t.Fatalf("pinned = %d, want >= %d", status.Counts.V1Pinned, total)
	}
	// Deterministic: second classify is idempotent, pins no duplicates.
	again, err := store.ClassifyCutoverDraining(ctx, 5)
	if err != nil {
		t.Fatalf("re-classify: %v", err)
	}
	if again.Counts.V1Pinned != status.Counts.V1Pinned {
		t.Fatalf("re-classify pinned = %d, want %d (idempotent)", again.Counts.V1Pinned, status.Counts.V1Pinned)
	}
}

func TestB2aPendingAndLeasedCustomerCallsAreClassified(t *testing.T) {
	t.Parallel()
	store := newB2aSQLiteStore(t, "b2a-pending-leased")
	ctx := context.Background()
	_ = b2aEnsureShadow(t, store)
	pending := b2aAppendCompleteCall(t, store, "acct-b2a-pl", "b-pending")
	leased := b2aAppendCompleteCall(t, store, "acct-b2a-pl", "b-leased")
	// Lease the second call via a worker claim (pending -> claimed).
	claimed, err := store.ClaimCompleteCalls(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) == 0 {
		t.Fatalf("expected at least one claimable call")
	}
	_, _, err = store.BeginCutoverDraining(ctx, "b2a-pl-drain")
	if err != nil {
		t.Fatalf("BeginCutoverDraining: %v", err)
	}
	for _, callID := range []billing.BillingCallID{pending, leased} {
		opKey, err := billing.CustomerPostingOperationKey("acct-b2a-pl", callID)
		if err != nil {
			t.Fatal(err)
		}
		pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
		if err != nil {
			t.Fatalf("call %s pin: %v", callID, err)
		}
		if pin.Owner != billing.PostingOwnerV1 {
			t.Fatalf("call %s owner = %q, want v1", callID, pin.Owner)
		}
	}
}

func TestB2aRestartMidClassificationResumes(t *testing.T) {
	t.Parallel()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "b2a-restart.db")))
	open := func(storeID string) (*DurableStore, func()) {
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
		s, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
		if err != nil {
			_ = bunDB.Close()
			t.Fatal(err)
		}
		return s, func() { _ = s.Close() }
	}
	store, closeFn := open("b2a-restart")
	ctx := context.Background()
	marker, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: marker.Version, ExpectedEpoch: marker.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2a-rs-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	_ = shadow
	for i := 0; i < 6; i++ {
		callID := b2aMustCallID(t)
		call := testIndependentCallUsageFor(callID, []string{fmt.Sprintf("b-%d", i)})
		call.AccountID = "acct-b2a-restart"
		if err := store.AppendCallUsage(ctx, call); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, fmt.Sprintf("b-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	// Enter draining (gate closed) then simulate crash before full classify
	// by transitioning directly and classifying only one small batch.
	draining, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: shadow.Version, ExpectedEpoch: shadow.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2a-rs-drain"})
	if err != nil {
		t.Fatal(err)
	}
	_ = draining
	if _, err := store.ClassifyCutoverDraining(ctx, 2); err != nil {
		t.Fatal(err)
	}
	closeFn()
	// Reopen: marker must still be draining (race-safe resume).
	reopened, close2 := open("b2a-restart")
	defer close2()
	got, err := reopened.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != billing.AccountingCutoverV1Draining {
		t.Fatalf("restart state = %q, want v1_draining", got.State)
	}
	resumed, err := reopened.ClassifyCutoverDraining(ctx, 10)
	if err != nil {
		t.Fatalf("resume classify: %v", err)
	}
	if resumed.Counts.V1Pinned < 6 {
		t.Fatalf("resumed pinned = %d, want >= 6", resumed.Counts.V1Pinned)
	}
}

func TestB2aUnclassifiableStaysDrainingWithoutInventedCompletion(t *testing.T) {
	t.Parallel()
	store := newB2aSQLiteStore(t, "b2a-unclass")
	ctx := context.Background()
	_ = b2aEnsureShadow(t, store)
	good := b2aAppendCompleteCall(t, store, "acct-b2a-unclass", "b-good")
	_ = good
	// Inject an unclassifiable customer row directly: empty account cannot be
	// mapped to a canonical V1 pin key.
	badCall := b2aMustCallID(t)
	if _, err := store.DB().NewRaw(`INSERT INTO usage_call_records(usage_call_key, fingerprint, call_id, account_id, a_leg_id, session_id, started_at, finished_at, outcome, expected_b_leg_ids, payload_json, sealed_at, claim_status, claim_attempt_count, next_claim_at, last_claim_error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		badCall.String(), "fp-bad", badCall.String(), "", "a-1", "s-1", time.Unix(100, 0).UTC(), time.Unix(101, 0).UTC(), "completed", `[]`, `{}`, time.Now().UTC(),
		"pending", 0, time.Now().UTC(), "").Exec(ctx); err != nil {
		t.Fatalf("inject bad call: %v", err)
	}
	_, status, err := store.BeginCutoverDraining(ctx, "b2a-unclass-drain")
	if err != nil {
		t.Fatalf("BeginCutoverDraining: %v", err)
	}
	if status.Counts.Unclassifiable == 0 {
		t.Fatalf("unclassifiable = 0, want > 0")
	}
	if len(status.Unclassifiable) == 0 {
		t.Fatalf("unclassifiable samples must be explicit")
	}
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if marker.State != billing.AccountingCutoverV1Draining {
		t.Fatalf("state = %q, want v1_draining (never auto-advance on unclassifiable)", marker.State)
	}
	// Never invent completion: no completed pin for the bad row.
	var completed int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND status = ?`, store.StoreID(), string(billing.PostingPinCompleted)).Scan(ctx, &completed); err != nil {
		t.Fatal(err)
	}
	if completed != 0 {
		t.Fatalf("completed pins = %d, want 0 (never invent completion)", completed)
	}
	// Activation must remain blocked while unclassifiable work exists.
	if _, err := store.ActivateCutoverV2(ctx, "b2a-unclass-activate"); !errors.Is(err, billing.ErrCutoverDrainBlocked) && !errors.Is(err, billing.ErrAccountingCutoverInvalid) {
		t.Fatalf("activate err = %v, want DrainBlocked/Invalid", err)
	}
}

func TestB2aNewV1WorkFencedAfterDrainingButReplayAllowed(t *testing.T) {
	t.Parallel()
	store := newB2aSQLiteStore(t, "b2a-fence")
	ctx := context.Background()
	_ = b2aEnsureShadow(t, store)
	existing := b2aAppendCompleteCall(t, store, "acct-b2a-fence", "b-existing")
	_, _, err := store.BeginCutoverDraining(ctx, "b2a-fence-drain")
	if err != nil {
		t.Fatal(err)
	}
	// New V1 append must be rejected/fenced.
	freshID := b2aMustCallID(t)
	fresh := testIndependentCallUsageFor(freshID, []string{"b-fresh"})
	fresh.AccountID = "acct-b2a-fence"
	if err := store.AppendCallUsage(ctx, fresh); !errors.Is(err, billing.ErrAccountingCutoverFence) && !errors.Is(err, billing.ErrPostingOwnershipFence) && !errors.Is(err, billing.ErrCutoverV1Fenced) {
		t.Fatalf("new append err = %v, want fence", err)
	}
	// New V1 leg must be fenced.
	freshLeg := testIndependentCallLegFor(b2aMustCallID(t), "b-fresh-leg")
	if err := store.AppendCallLegUsage(ctx, freshLeg); !errors.Is(err, billing.ErrAccountingCutoverFence) && !errors.Is(err, billing.ErrPostingOwnershipFence) && !errors.Is(err, billing.ErrCutoverV1Fenced) {
		t.Fatalf("new leg err = %v, want fence", err)
	}
	// Historical replay of already pinned/completed V1 per B1 remains allowed:
	// re-append the exact pre-drain call must succeed (idempotent).
	call, err := store.GetCallUsage(ctx, existing)
	if err != nil {
		t.Fatalf("GetCallUsage existing: %v", err)
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatalf("replay append must succeed: %v", err)
	}
}

func TestB2aClaimsWhileDrainingReturnOnlyPinned(t *testing.T) {
	t.Parallel()
	store := newB2aSQLiteStore(t, "b2a-claims")
	ctx := context.Background()
	_ = b2aEnsureShadow(t, store)
	pinnedCall := b2aAppendCompleteCall(t, store, "acct-b2a-claim", "b-pinned")
	_, _, err := store.BeginCutoverDraining(ctx, "b2a-claim-drain")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimCompleteCalls(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range claimed {
		if c.Closure.CallID == pinnedCall {
			found = true
		}
		// Every draining claim must carry V1 pin metadata for B2b.
		opKey, kerr := billing.CustomerPostingOperationKey(c.Closure.AccountID, c.Closure.CallID)
		if kerr != nil {
			t.Fatal(kerr)
		}
		meta, merr := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
		if merr != nil {
			t.Fatalf("claim %s metadata: %v", c.Closure.CallID, merr)
		}
		if meta.Owner != billing.PostingOwnerV1 {
			t.Fatalf("claim %s owner = %q, want v1", c.Closure.CallID, meta.Owner)
		}
		if meta.MarkerState != billing.AccountingCutoverV1Draining {
			t.Fatalf("claim %s marker = %q, want draining", c.Closure.CallID, meta.MarkerState)
		}
	}
	if !found {
		t.Fatalf("draining claims must include pinned call %s (got %d claims)", pinnedCall, len(claimed))
	}
	// Provider work claims in draining return only V1-pinned work.
	providerWork, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(providerWork) == 0 {
		t.Fatalf("draining provider claims must include pinned legs")
	}
	// No V1 claim in v2_active: complete drain then activate on empty store.
	empty := newB2aSQLiteStore(t, "b2a-claims-active-empty")
	ectx := context.Background()
	em, err := empty.EnsureAccountingCutover(ectx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := empty.TransitionAccountingCutover(ectx, billing.AccountingCutoverTransition{ExpectedVersion: em.Version, ExpectedEpoch: em.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2a-cl-empty-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := empty.TransitionAccountingCutover(ectx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2a-cl-empty-drain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := empty.ActivateCutoverV2(ectx, "b2a-cl-empty-active"); err != nil {
		t.Fatalf("activate empty must succeed: %v", err)
	}
	_ = dr
	activeClaims, err := empty.ClaimCompleteCalls(ectx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(activeClaims) != 0 {
		t.Fatalf("v2_active V1 claims = %d, want 0", len(activeClaims))
	}
}

func TestB2aActivationBlockedUntilDrainedThenUnblocked(t *testing.T) {
	t.Parallel()
	store := newB2aSQLiteStore(t, "b2a-activate")
	ctx := context.Background()
	_ = b2aEnsureShadow(t, store)
	b2aAppendCompleteCall(t, store, "acct-b2a-act", "b-1")
	_, _, err := store.BeginCutoverDraining(ctx, "b2a-act-drain")
	if err != nil {
		t.Fatal(err)
	}
	// Blocked while V1 pins/work remain.
	if _, err := store.ActivateCutoverV2(ctx, "b2a-act-try"); !errors.Is(err, billing.ErrCutoverDrainBlocked) {
		t.Fatalf("activate blocked err = %v, want ErrCutoverDrainBlocked", err)
	}
	// Simulate worker drain: complete all V1 pins and mark legacy rows
	// processed, then activation must succeed via CAS.
	var pins []struct {
		Kind string `bun:"operation_kind"`
		Key  string `bun:"operation_key"`
	}
	if err := store.DB().NewRaw(`SELECT operation_kind, operation_key FROM billing_posting_ownership_pins WHERE store_id = ? AND status = ?`, store.StoreID(), string(billing.PostingPinPinned)).Scan(ctx, &pins); err != nil {
		t.Fatal(err)
	}
	if len(pins) == 0 {
		t.Fatalf("expected pinned pins before drain")
	}
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pins {
		pin, err := store.GetPostingPin(ctx, billing.PostingOperationKind(p.Kind), p.Key)
		if err != nil {
			t.Fatal(err)
		}
		completeReq := billing.CompletePostingPinRequest{Kind: pin.Kind, AccountID: pin.AccountID, CallID: pin.CallID, Subject: pin.Subject, HeadKey: pin.HeadKey, Owner: pin.Owner, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch, CompletionOperationKey: pin.OperationKey, CompletionTransactionID: "tx-drain-sim"}
		if _, err := store.CompletePostingPin(ctx, completeReq); err != nil {
			t.Fatalf("complete pin %s: %v", p.Key, err)
		}
	}
	if _, err := store.DB().NewRaw(`UPDATE usage_call_records SET claim_status = 'processed' WHERE claim_status IN ('pending','claimed')`).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().NewRaw(`UPDATE provider_cost_work SET status = 'processed' WHERE status = 'pending'`).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	activated, err := store.ActivateCutoverV2(ctx, "b2a-act-go")
	if err != nil {
		t.Fatalf("activate after drain: %v", err)
	}
	if activated.State != billing.AccountingCutoverV2Active {
		t.Fatalf("activated state = %q, want v2_active", activated.State)
	}
	// Replay with same transition identity is idempotent (race-safe resume).
	replayed, err := store.ActivateCutoverV2(ctx, "b2a-act-go")
	if err != nil {
		t.Fatalf("activate replay: %v", err)
	}
	if replayed.State != billing.AccountingCutoverV2Active || replayed.TransitionID != activated.TransitionID {
		t.Fatalf("replay mismatch: %#v vs %#v", replayed, activated)
	}
}

func TestB2aV2NewWorkAuthorizedOnlyAfterActivation(t *testing.T) {
	t.Parallel()
	store := newB2aSQLiteStore(t, "b2a-v2auth")
	ctx := context.Background()
	_ = b2aEnsureShadow(t, store)
	if err := store.CheckV2NewWorkAuthorized(ctx); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("pre-active V2 auth err = %v, want NotAuthorized", err)
	}
	// V2 pins rejected before v2_active (B1 fence, surfaced via coordinator).
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v2Call := b2aMustCallID(t)
	if _, err := store.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b2a-v2", CallID: v2Call, Owner: billing.PostingOwnerV2, ExpectedMarkerVersion: marker.Version, ExpectedMarkerEpoch: marker.Epoch}); !errors.Is(err, billing.ErrPostingOwnershipFence) && !errors.Is(err, billing.ErrPostingOwnershipInvalid) {
		t.Fatalf("pre-active V2 pin err = %v, want fence", err)
	}
	_, _, err = store.BeginCutoverDraining(ctx, "b2a-v2auth-drain")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckV2NewWorkAuthorized(ctx); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("draining V2 auth err = %v, want NotAuthorized", err)
	}
	// Drain an empty sibling to active, then V2 is authorized there.
	activeStore := newB2aSQLiteStore(t, "b2a-v2auth-active")
	actx := context.Background()
	em, err := activeStore.EnsureAccountingCutover(actx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := activeStore.TransitionAccountingCutover(actx, billing.AccountingCutoverTransition{ExpectedVersion: em.Version, ExpectedEpoch: em.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2a-v2a-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := activeStore.TransitionAccountingCutover(actx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "b2a-v2a-drain"})
	if err != nil {
		t.Fatal(err)
	}
	_ = dr
	if _, err := activeStore.ActivateCutoverV2(actx, "b2a-v2a-active"); err != nil {
		t.Fatal(err)
	}
	if err := activeStore.CheckV2NewWorkAuthorized(actx); err != nil {
		t.Fatalf("post-active V2 auth must succeed: %v", err)
	}
	// Shadow capture remains no-post and unaffected (sanity: shadow ports still
	// reject money; coordinator does not alter shadow capture).
	_ = metering.SubjectBLeg
}

func TestB2aStoreIsolation(t *testing.T) {
	t.Parallel()
	base := newSQLiteTestStore(t)
	ctx := context.Background()
	storeA, err := NewDurableStore(ctx, base.DB(), Config{StoreID: "b2a-iso-a"})
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewDurableStore(ctx, base.DB(), Config{StoreID: "b2a-iso-b"})
	if err != nil {
		t.Fatal(err)
	}
	markerA, err := storeA.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shadowA, err := storeA.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: markerA.Version, ExpectedEpoch: markerA.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2a-iso-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	_ = shadowA
	if _, _, err := storeA.BeginCutoverDraining(ctx, "b2a-iso-drain"); err != nil {
		t.Fatal(err)
	}
	gotA, err := storeA.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.State != billing.AccountingCutoverV1Draining {
		t.Fatalf("A state = %q, want draining", gotA.State)
	}
	gotB, err := storeB.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.State != billing.AccountingCutoverV1Active {
		t.Fatalf("B state = %q, want v1_active (isolated)", gotB.State)
	}
	// Pins are per-store: A pin must not appear in B.
	callID := b2aMustCallID(t)
	markerAfter, err := storeA.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.AcquirePostingPin(ctx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b2a-iso", CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: markerAfter.Version, ExpectedMarkerEpoch: markerAfter.Epoch}); err != nil {
		// Draining forbids new V1; use a pre-drain pin on a third isolated
		// store to prove isolation instead.
		isoStore := newB2aSQLiteStore(t, "b2a-iso-c")
		ictx := context.Background()
		m, err := isoStore.EnsureAccountingCutover(ictx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := isoStore.AcquirePostingPin(ictx, billing.AcquirePostingPinRequest{Kind: billing.PostingOperationCustomerSettlement, AccountID: "acct-b2a-iso", CallID: callID, Owner: billing.PostingOwnerV1, ExpectedMarkerVersion: m.Version, ExpectedMarkerEpoch: m.Epoch}); err != nil {
			t.Fatal(err)
		}
		opKey, _ := billing.CustomerPostingOperationKey("acct-b2a-iso", callID)
		if _, err := storeB.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey); !errors.Is(err, billing.ErrPostingOwnershipNotFound) {
			t.Fatalf("isolated Get err = %v, want NotFound", err)
		}
		return
	}
	opKey, _ := billing.CustomerPostingOperationKey("acct-b2a-iso", callID)
	if _, err := storeB.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey); !errors.Is(err, billing.ErrPostingOwnershipNotFound) {
		t.Fatalf("isolated Get err = %v, want NotFound", err)
	}
}

func TestB2aContextCancelFailsClosed(t *testing.T) {
	t.Parallel()
	store := newB2aSQLiteStore(t, "b2a-ctx")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.BeginCutoverDraining(canceled, "b2a-ctx-drain"); err == nil {
		t.Fatalf("canceled Begin must fail")
	}
	if _, err := store.ClassifyCutoverDraining(canceled, 10); err == nil {
		t.Fatalf("canceled Classify must fail")
	}
	if _, err := store.CutoverDrainStatus(canceled); err == nil {
		t.Fatalf("canceled Status must fail")
	}
	if _, err := store.ActivateCutoverV2(canceled, "b2a-ctx-active"); err == nil {
		t.Fatalf("canceled Activate must fail")
	}
	var nilCtx context.Context
	if _, _, err := store.BeginCutoverDraining(nilCtx, "x"); err == nil {
		t.Fatalf("nil Begin must fail")
	}
}
