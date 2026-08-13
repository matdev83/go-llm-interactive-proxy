package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteApplyBillingResultPostsCustomerAndPerLURCOGSAtomically(t *testing.T) {
	runApplyBillingResultAtomic(t, newSQLiteTestStore(t), "settle-account")
}

func TestSQLiteApplyBillingResultRejectsChargeExceedingHold(t *testing.T) {
	runApplyBillingResultOverageReject(t, newSQLiteTestStore(t), "settle-overage")
}

func TestSQLiteApplyBillingResultReplayAndConflictDoNotDoublePost(t *testing.T) {
	runApplyBillingResultReplay(t, newSQLiteTestStore(t), "settle-replay")
}

func TestSQLiteApplyBillingResultRejectsReconcileRequiredAccount(t *testing.T) {
	runApplyBillingResultRejectsReconcileRequired(t, newSQLiteTestStore(t), "settle-blocked")
}

func TestSQLiteReleaseAuthorizationBlockedWhileProcessingPending(t *testing.T) {
	runReleaseAuthorizationBlockedWhileProcessingPending(t, newSQLiteTestStore(t), "release-blocked")
}

func runApplyBillingResultAtomic(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "settle-auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "settle-auth", billing.MoneyEvidence{NanoUnits: 7, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	result := billing.Result{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 12, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 7, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}}}
	settlement, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	if settlement.TURKey != sealed.Key || settlement.Customer.Transaction.Book != billing.JournalBookFinancial || settlement.AuthorizationRelease.Transaction.Book != billing.JournalBookAuthorization || len(settlement.ProviderCosts) != 1 {
		t.Fatalf("settlement = %+v", settlement)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != 88 || account.ReservedNano != 0 || account.Version != 3 {
		t.Fatalf("settled account = %+v", account)
	}
	journals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != 4 {
		t.Fatalf("journal count = %d, want auth/customer/provider/release", len(journals))
	}
	var customer, provider, release bool
	for _, journal := range journals {
		if err := journal.Validate(); err != nil {
			t.Fatalf("invalid journal: %v", err)
		}
		if journal.SourceKey == "customer-settlement:v1:"+sealed.Key {
			customer = true
		}
		if journal.SourceKey == "provider-cost:v1:"+sealed.Legs[0].Key && journal.BLegID == sealed.Legs[0].BLegID {
			provider = true
		}
		if journal.OperationKind == "authorization_release" {
			release = true
			if journal.TurnID != sealed.TurnID || journal.ALegID != sealed.ALegID {
				t.Fatalf("authorization release correlation = turn %q aleg %q, want turn %q aleg %q", journal.TurnID, journal.ALegID, sealed.TurnID, sealed.ALegID)
			}
		}
	}
	if !customer || !provider || !release {
		t.Fatalf("customer/provider/release settlement identities not found")
	}
	processing, err := store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if processing.Status != billing.ProcessingProcessed {
		t.Fatalf("processing = %+v", processing)
	}
}

func runApplyBillingResultOverageReject(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "overage-auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "overage-auth", billing.MoneyEvidence{NanoUnits: 7, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	result := billing.Result{
		TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 41, Currency: "USD"},
		OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 7, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}},
	}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); !errors.Is(err, billing.ErrSettlementInvalid) {
		t.Fatalf("overage settlement = %v, want ErrSettlementInvalid", err)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != 100 || account.ReservedNano != 40 {
		t.Fatalf("overage must leave hold intact, account=%+v", account)
	}
	journals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, journal := range journals {
		if journal.SourceKey == "customer-settlement:v1:"+sealed.Key {
			t.Fatal("overage must not post customer settlement")
		}
	}
	processing, err := store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if processing.Status != billing.ProcessingPending {
		t.Fatalf("overage processing = %+v, want pending", processing)
	}
}

func runApplyBillingResultReplay(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPostpaid, CreditLimit: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 30))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "auth", billing.MoneyEvidence{NanoUnits: 0, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	input := billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: billing.Result{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 5, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 0, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}}}}
	first, err := store.ApplyBillingResult(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ApplyBillingResult(ctx, input)
	if err != nil || !second.Replayed || first.Customer.Transaction.ID != second.Customer.Transaction.ID {
		t.Fatalf("replay = %+v, %v", second, err)
	}
	releaseSource := "authorization-release:v1:settled:" + sealed.Key
	if second.AuthorizationRelease.OperationKey != releaseSource {
		t.Fatalf("replay release operation key = %q, want %q", second.AuthorizationRelease.OperationKey, releaseSource)
	}
	providerSource := "provider-cost:v1:" + sealed.Legs[0].Key
	if len(second.ProviderCosts) != 1 || second.ProviderCosts[0].OperationKey != providerSource {
		t.Fatalf("replay provider costs = %+v", second.ProviderCosts)
	}
	var zeroCostSnapshots int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind = 'provider_cogs'`, accountID).Scan(ctx, &zeroCostSnapshots); err != nil {
		t.Fatal(err)
	}
	if zeroCostSnapshots != 1 {
		t.Fatalf("zero-cost provider snapshots = %d, want 1", zeroCostSnapshots)
	}
	var zeroSeqEnd uint64
	if err := store.db.NewRaw(`SELECT account_sequence_end FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind = 'provider_cogs'`, accountID).Scan(ctx, &zeroSeqEnd); err != nil {
		t.Fatal(err)
	}
	if zeroSeqEnd == 0 {
		t.Fatal("zero-cost provider snapshot must inherit settlement sequence range")
	}
	wrongAuthorization := auth
	wrongAuthorization.Fingerprint = "different-authorization"
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: wrongAuthorization, Result: input.Result}); !errors.Is(err, billing.ErrSettlementConflict) {
		t.Fatalf("authorization replay conflict = %v", err)
	}
	conflict := input
	conflict.Result.CustomerCharge.Nano = 6
	if _, err := store.ApplyBillingResult(ctx, conflict); !errors.Is(err, ErrOperationConflict) && !errors.Is(err, billing.ErrSettlementConflict) {
		t.Fatalf("settlement conflict = %v", err)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != -5 || account.ReservedNano != 0 {
		t.Fatalf("replay/conflict mutated account = %+v", account)
	}
}

func runApplyBillingResultRejectsReconcileRequired(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 20))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "auth", billing.MoneyEvidence{NanoUnits: 1, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccountReconcileRequired(ctx, accountID); err != nil {
		t.Fatal(err)
	}
	result := billing.Result{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 5, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 1, Currency: "USD"}, AmountPresent: true, Reconciled: true}}}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); !errors.Is(err, billing.ErrAccountNotReady) {
		t.Fatalf("settlement on reconcile_required = %v", err)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != 100 || account.ReservedNano != 20 {
		t.Fatalf("blocked settlement mutated account = %+v", account)
	}
}

func runReleaseAuthorizationBlockedWhileProcessingPending(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 25)); err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "auth", billing.MoneyEvidence{NanoUnits: 1, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseAuthorization(ctx, billing.ReleaseAuthorizationInput{AccountID: accountID, AuthorizationID: "auth", TURKey: sealed.Key, FullClose: true, Reason: billing.ReleaseOperator, SourceKey: "op-1"}); !errors.Is(err, billing.ErrHoldReleaseBlocked) {
		t.Fatalf("pending processing release = %v", err)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedNano != 25 {
		t.Fatalf("blocked release mutated reserved = %+v", account)
	}
}

func settlementStoreRecord(accountID, turnID, authID string, cost billing.MoneyEvidence) billing.TurnUsageRecord {
	now := time.Unix(200, 0).UTC()
	return billing.TurnUsageRecord{SchemaVersion: billing.CurrentRecordSchemaVersion, AccountID: accountID, TurnID: turnID, ALegID: "a-1", AuthorizationID: authID, StartedAt: now, FinishedAt: now.Add(time.Second), Outcome: billing.TurnOutcomeCompleted, CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"}, Legs: []billing.LegUsageRecord{{ALegID: "a-1", BLegID: "b-1", Seq: 1, BackendID: "backend", ProviderID: "provider", ModelID: "model", StartedAt: now, FinishedAt: now.Add(time.Second), Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes, Evidence: billing.FinalBillingEvidence{Cost: cost}}}}
}
