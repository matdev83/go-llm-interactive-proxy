//go:build integration

package billingstore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestPostgresSubmissionFeeSettlementR2FixtureParity runs the R2 submission-fee
// owner-replay fixtures directly against an isolated direct PostgreSQL schema.
// PostgreSQL is mandatory for R2 acceptance: run with LIP_REQUIRE_POSTGRES=1 and
// a configured DSN. A missing DSN fails closed rather than deferring to SQLite,
// because the concurrent first-claim contract below depends on real
// cross-connection transaction contention. Every subtest drives the production
// owner-aware V2 admission/terminal/settlement APIs and asserts durable money,
// exposure, pin, journal, and claim state.
func TestPostgresSubmissionFeeSettlementR2FixtureParity(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, schema := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "submission-r2-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f3ActivateEmpty(t, store)

	t.Run("AboveExposureOwnerExactReplay", func(t *testing.T) {
		accountID := "r2-pg-above"
		f3SetupAccount(t, store, accountID, 1_000)
		call, exposure := phase10V2SubmissionCall(t, store, accountID, "r2-pg-above", "b-above", 100)
		result := phase10V2SubmissionResult(t, call, 20, 100)
		input := billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result, PostingOwner: billing.PostingOwnerV2}
		first, err := store.ApplyCallBillingResult(ctx, input)
		if err != nil {
			t.Fatalf("first above-exposure settlement: %v", err)
		}
		if first.Replayed || !first.Breached || first.OverrunNano != 20 || first.Customer.Transaction.ID == "" {
			t.Fatalf("first above-exposure settlement = %+v, want fresh 120-nano posting with 20-nano overrun", first)
		}
		sourceKey, err := billing.CustomerSettlementSourceKey(accountID, call.CallID)
		if err != nil {
			t.Fatal(err)
		}
		firstPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
		if err != nil {
			t.Fatal(err)
		}
		if firstPin.Owner != billing.PostingOwnerV2 || !firstPin.IsCompleted() || firstPin.CompletionTransactionID != first.Customer.Transaction.ID {
			t.Fatalf("above-exposure pin = %+v, want completed V2 owner tx %q", firstPin, first.Customer.Transaction.ID)
		}
		replayed, err := store.ApplyCallBillingResult(ctx, input)
		if err != nil {
			t.Fatalf("exact above-exposure owner replay: %v", err)
		}
		if !replayed.Replayed || !replayed.Breached || replayed.OverrunNano != 20 {
			t.Fatalf("above-exposure replay = %+v, want identical disposition", replayed)
		}
		afterPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
		if err != nil {
			t.Fatal(err)
		}
		afterAccount, err := store.GetAccount(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		transactions, err := store.JournalTransactions(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		if afterPin != firstPin {
			t.Fatalf("above-exposure replay changed pin: before=%+v after=%+v", firstPin, afterPin)
		}
		if afterAccount.BalanceNano != 880 || len(transactions) != 1 || transactions[0].ID != first.Customer.Transaction.ID {
			t.Fatalf("above-exposure replay effects = balance %d transactions %+v, want 880 and original tx %q", afterAccount.BalanceNano, transactions, first.Customer.Transaction.ID)
		}
		phase10AssertExposureState(t, store, call.CallID, exposure.Fingerprint)
	})

	t.Run("BelowExposureOwnerExactReplay", func(t *testing.T) {
		accountID := "r2-pg-below"
		f3SetupAccount(t, store, accountID, 1_000)
		call, exposure := phase10V2SubmissionCall(t, store, accountID, "r2-pg-below", "b-below", 200)
		result := phase10V2SubmissionResult(t, call, 20, 100)
		input := billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result, PostingOwner: billing.PostingOwnerV2}
		first, err := store.ApplyCallBillingResult(ctx, input)
		if err != nil {
			t.Fatalf("first below-exposure settlement: %v", err)
		}
		if first.Replayed || first.Breached || first.OverrunNano != 0 || first.Customer.Transaction.ID == "" {
			t.Fatalf("first below-exposure settlement = %+v, want fresh non-breached posting", first)
		}
		replayed, err := store.ApplyCallBillingResult(ctx, input)
		if err != nil {
			t.Fatalf("exact below-exposure owner replay: %v", err)
		}
		if !replayed.Replayed || replayed.Breached || replayed.OverrunNano != 0 {
			t.Fatalf("below-exposure replay = %+v, want unchanged non-breached replay", replayed)
		}
		account, err := store.GetAccount(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		transactions, err := store.JournalTransactions(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		if account.BalanceNano != 880 || len(transactions) != 1 || transactions[0].ID != first.Customer.Transaction.ID {
			t.Fatalf("below-exposure replay effects = balance %d transactions %+v, want 880 and original tx %q", account.BalanceNano, transactions, first.Customer.Transaction.ID)
		}
		phase10AssertExposureState(t, store, call.CallID, exposure.Fingerprint)
	})

	t.Run("FeeOnlyZeroPricedOwnerReplayAfterZeroEffectiveSibling", func(t *testing.T) {
		accountID := "r2-pg-fee-only"
		submissionID := "r2-pg-fee-only"
		f3SetupAccount(t, store, accountID, 1_000)
		owner, ownerExposure := phase10V2SubmissionCall(t, store, accountID, submissionID, "b-owner", 100)
		ownerResult := phase10V2SubmissionResult(t, owner, 20, 0)
		ownerInput := billing.ApplyCallBillingInput{Call: owner, Exposure: ownerExposure, Result: ownerResult, PostingOwner: billing.PostingOwnerV2}
		ownerSettlement, err := store.ApplyCallBillingResult(ctx, ownerInput)
		if err != nil {
			t.Fatalf("owner fee-only settlement: %v", err)
		}
		if ownerSettlement.Replayed || ownerSettlement.Breached || ownerSettlement.Customer.Transaction.ID == "" {
			t.Fatalf("owner fee-only settlement = %+v, want fresh 20-nano posting", ownerSettlement)
		}
		ownerSourceKey, err := billing.CustomerSettlementSourceKey(accountID, owner.CallID)
		if err != nil {
			t.Fatal(err)
		}
		ownerPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, ownerSourceKey)
		if err != nil {
			t.Fatal(err)
		}
		if sourceCallID, amount := phase10SubmissionClaimRow(t, store, accountID, submissionID); sourceCallID != owner.CallID.String() || amount != 20 {
			t.Fatalf("submission claim = (%q, %d), want owner %q amount 20", sourceCallID, amount, owner.CallID)
		}

		sibling, siblingExposure := phase10V2SubmissionCall(t, store, accountID, submissionID, "b-sibling", 100)
		siblingResult := phase10V2SubmissionResult(t, sibling, 20, 0)
		siblingInput := billing.ApplyCallBillingInput{Call: sibling, Exposure: siblingExposure, Result: siblingResult, PostingOwner: billing.PostingOwnerV2}
		siblingSettlement, err := store.ApplyCallBillingResult(ctx, siblingInput)
		if err != nil {
			t.Fatalf("zero-effective sibling settlement: %v", err)
		}
		if siblingSettlement.Replayed || siblingSettlement.Breached || siblingSettlement.Customer.Transaction.ID != "" {
			t.Fatalf("zero-effective sibling = %+v, want fresh no-transaction sibling", siblingSettlement)
		}
		if count := phase10SubmissionClaimCount(t, store, accountID, submissionID); count != 1 {
			t.Fatalf("submission claim count after sibling = %d, want one", count)
		}
		siblingReplay, err := store.ApplyCallBillingResult(ctx, siblingInput)
		if err != nil {
			t.Fatalf("zero-charge sibling replay: %v", err)
		}
		if !siblingReplay.Replayed || siblingReplay.Customer.Transaction.ID != "" {
			t.Fatalf("zero-charge sibling replay = %+v, want unchanged replay without transaction", siblingReplay)
		}

		ownerReplay, err := store.ApplyCallBillingResult(ctx, ownerInput)
		if err != nil {
			t.Fatalf("owner exact replay after zero-effective sibling: %v", err)
		}
		if !ownerReplay.Replayed || ownerReplay.Breached || ownerReplay.OverrunNano != 0 {
			t.Fatalf("owner replay = %+v, want unchanged non-breached replay", ownerReplay)
		}
		afterPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, ownerSourceKey)
		if err != nil {
			t.Fatal(err)
		}
		if afterPin != ownerPin {
			t.Fatalf("owner replay changed pin: before=%+v after=%+v", ownerPin, afterPin)
		}
		if sourceCallID, amount := phase10SubmissionClaimRow(t, store, accountID, submissionID); sourceCallID != owner.CallID.String() || amount != 20 {
			t.Fatalf("submission claim after owner replay = (%q, %d), want unchanged owner %q amount 20", sourceCallID, amount, owner.CallID)
		}
		account, err := store.GetAccount(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		transactions, err := store.JournalTransactions(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		if account.BalanceNano != 980 || len(transactions) != 1 || transactions[0].ID != ownerSettlement.Customer.Transaction.ID {
			t.Fatalf("owner replay effects = balance %d transactions %+v, want 980 and original tx %q", account.BalanceNano, transactions, ownerSettlement.Customer.Transaction.ID)
		}
		phase10AssertExposureState(t, store, owner.CallID, ownerExposure.Fingerprint)
		phase10AssertExposureState(t, store, sibling.CallID, siblingExposure.Fingerprint)
	})

	t.Run("ChangedValuationConflictsWithoutEffects", func(t *testing.T) {
		accountID := "r2-pg-changed"
		f3SetupAccount(t, store, accountID, 1_000)
		call, exposure := phase10V2SubmissionCall(t, store, accountID, "r2-pg-changed", "b-changed", 100)
		result := phase10V2SubmissionResult(t, call, 20, 0)
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result, PostingOwner: billing.PostingOwnerV2}); err != nil {
			t.Fatalf("first fee-only settlement: %v", err)
		}
		sourceKey, err := billing.CustomerSettlementSourceKey(accountID, call.CallID)
		if err != nil {
			t.Fatal(err)
		}
		beforePin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
		if err != nil {
			t.Fatal(err)
		}
		beforeAccount, err := store.GetAccount(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		beforeTransactions, err := store.JournalTransactions(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		changed := phase10V2SubmissionResult(t, call, 20, 5)
		if changed.Fingerprint == result.Fingerprint {
			t.Fatal("changed valuation must carry a recomputed fingerprint")
		}
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: changed, PostingOwner: billing.PostingOwnerV2}); !errors.Is(err, ErrOperationConflict) {
			t.Fatalf("changed V2 valuation = %v, want ErrOperationConflict", err)
		}
		afterPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
		if err != nil {
			t.Fatal(err)
		}
		afterAccount, err := store.GetAccount(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		afterTransactions, err := store.JournalTransactions(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		if afterPin != beforePin || afterAccount.BalanceNano != beforeAccount.BalanceNano || len(afterTransactions) != len(beforeTransactions) {
			t.Fatalf("changed valuation mutated durable state: pin %+v->%+v balance %d->%d transactions %d->%d", beforePin, afterPin, beforeAccount.BalanceNano, afterAccount.BalanceNano, len(beforeTransactions), len(afterTransactions))
		}
		phase10AssertExposureState(t, store, call.CallID, exposure.Fingerprint)
	})

	t.Run("StoreHandleRecreationExactReplay", func(t *testing.T) {
		accountID := "r2-pg-reopen"
		f3SetupAccount(t, store, accountID, 1_000)
		call, exposure := phase10V2SubmissionCall(t, store, accountID, "r2-pg-reopen", "b-reopen", 100)
		result := phase10V2SubmissionResult(t, call, 20, 100)
		input := billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result, PostingOwner: billing.PostingOwnerV2}
		first, err := store.ApplyCallBillingResult(ctx, input)
		if err != nil {
			t.Fatalf("first settlement before handle recreation: %v", err)
		}
		if first.Replayed || !first.Breached || first.OverrunNano != 20 || first.Customer.Transaction.ID == "" {
			t.Fatalf("first settlement = %+v, want fresh overrun posting", first)
		}
		sourceKey, err := billing.CustomerSettlementSourceKey(accountID, call.CallID)
		if err != nil {
			t.Fatal(err)
		}
		firstPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
		if err != nil {
			t.Fatal(err)
		}
		reopened := openPostgresStoreOnSchema(t, dsn, schema, "submission-r2-pg", 4)
		replayed, err := reopened.ApplyCallBillingResult(ctx, input)
		if err != nil {
			t.Fatalf("lost-ack replay after handle recreation: %v", err)
		}
		if !replayed.Replayed || !replayed.Breached || replayed.OverrunNano != 20 {
			t.Fatalf("recreated-handle replay = %+v, want identical disposition", replayed)
		}
		afterPin, err := reopened.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, sourceKey)
		if err != nil {
			t.Fatal(err)
		}
		afterAccount, err := reopened.GetAccount(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		transactions, err := reopened.JournalTransactions(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		if afterPin != firstPin {
			t.Fatalf("recreated-handle pin = %+v, want %+v", afterPin, firstPin)
		}
		if afterAccount.BalanceNano != 880 || len(transactions) != 1 || transactions[0].ID != first.Customer.Transaction.ID {
			t.Fatalf("recreated-handle effects = balance %d transactions %+v, want 880 and original tx %q", afterAccount.BalanceNano, transactions, first.Customer.Transaction.ID)
		}
		if sourceCallID, amount := phase10SubmissionClaimRow(t, reopened, accountID, "r2-pg-reopen"); sourceCallID != call.CallID.String() || amount != 20 {
			t.Fatalf("recreated-handle claim = (%q, %d), want owner %q amount 20", sourceCallID, amount, call.CallID)
		}
		phase10AssertExposureState(t, reopened, call.CallID, exposure.Fingerprint)
	})
}

// TestPostgresV2SubmissionFeeConcurrentFirstClaimReplayStable is the direct
// PostgreSQL half of the R2 concurrent-first-claim acceptance. It uses two
// independent store/SQL handles over one isolated schema so the first-claim
// attempts genuinely compete under the retained production transaction retry
// semantics, deterministically elects the owner from the immutable claim, and
// then replays both exact inputs on fresh handles. PostgreSQL is mandatory for
// this acceptance; run with LIP_REQUIRE_POSTGRES=1 and a configured DSN.
func TestPostgresV2SubmissionFeeConcurrentFirstClaimReplayStable(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	const storeID = "submission-r2-pg-concurrent"
	bunDB, schema := openIsolatedPostgresBun(t, dsn, 4)
	primary, err := NewDurableStore(ctx, bunDB, Config{StoreID: storeID})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = primary.Close() })
	f3ActivateEmpty(t, primary)

	for iteration := 0; iteration < 2; iteration++ {
		accountID := fmt.Sprintf("r2-pg-concurrent-%d", iteration)
		f3SetupAccount(t, primary, accountID, 1_000)
		inputs := phase10V2ConcurrentSubmissionInputs(t, primary, accountID, accountID)
		settlers := [2]*DurableStore{primary, openPostgresStoreOnSchema(t, dsn, schema, storeID, 4)}
		openReplayers := func() [2]*DurableStore {
			return [2]*DurableStore{
				openPostgresStoreOnSchema(t, dsn, schema, storeID, 4),
				openPostgresStoreOnSchema(t, dsn, schema, storeID, 4),
			}
		}
		phase10AssertV2ConcurrentFirstClaimAndReplay(ctx, t, accountID, accountID, settlers, openReplayers, inputs)
	}
}

// openPostgresStoreOnSchema opens a fresh direct PostgreSQL pool and store
// handle against an already-isolated schema, mirroring the acknowledgement-lost
// scenario where only durable state survives. It is a genuine new SQL and store
// handle over the same schema, not a new schema.
func openPostgresStoreOnSchema(t *testing.T, dsn, schema, storeID string, maxOpen int) *DurableStore {
	t.Helper()
	scopedDSN, hasURLSearchPath := postgresDSNWithSearchPath(dsn, schema)
	openMax := maxOpen
	if !hasURLSearchPath {
		openMax = 1
	}
	bunDB, err := testkit.OpenPostgresBun(scopedDSN, openMax)
	if err != nil {
		t.Fatalf("open direct PostgreSQL handle on isolated schema: %v", err)
	}
	if !hasURLSearchPath {
		if _, err := bunDB.ExecContext(context.Background(), "SET search_path TO "+quotePostgresIdentifier(schema)); err != nil {
			_ = bunDB.Close()
			t.Fatalf("set direct PostgreSQL search_path on isolated schema: %v", err)
		}
	}
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatalf("NewDurableStore on recreated PostgreSQL handle: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
