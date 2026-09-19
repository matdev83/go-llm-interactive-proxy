package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// TestSQLiteCostPassThroughRevisionRetainsALegLineage creates the
// cost-pass-through head through the real production settlement path
// (ApplyCallBillingResult) and then advances it with the actual
// DurableStore.ApplyCostPassThroughRevision: first a positive provider-cost
// revision (additional customer debit), then a negative correction/reduction.
// Both immutable adjustment journals must carry the call's A-leg identity,
// and the head must progress version/revision/fence without losing it.
func TestSQLiteCostPassThroughRevisionRetainsALegLineage(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	account := billing.Account{ID: "cost-pass-through-aleg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	const aLegID = "a-leg-pass-through-retention"
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: aLegID,
		SessionID: "session-pass-through-retention", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 100, Currency: account.Currency},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := billing.CostPassThroughSettlement{
		PolicyRef: call.ChargePolicyRef,
		Policy: billing.CostPassThroughPolicy{
			MissingCost: billing.CostPassThroughMissingCostProvisional,
			SafeBound:   &billing.Money{Nano: 100, Currency: account.Currency}, AllowLateAdjustment: true,
		},
		Status:    billing.CostPassThroughSettlementProvisional,
		SafeBound: billing.Money{Nano: 100, Currency: account.Currency}, PostedAmount: billing.Money{Nano: 60, Currency: account.Currency},
	}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure,
		Result: billing.CallRatingResult{CallID: call.CallID, CustomerCharge: state.PostedAmount, Fingerprint: "pass-through-aleg-retention", CostPassThrough: &state},
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Customer.Transaction.ID == "" {
		t.Fatal("provisional pass-through settlement did not post a customer transaction")
	}
	originalTransactionID := settled.Customer.Transaction.ID

	readHead := func() (aLeg string, posted int64, revision int64, version uint64, fence uint64) {
		t.Helper()
		var head struct {
			ALegID           string `bun:"a_leg_id"`
			PostedAmountNano int64  `bun:"posted_amount_nano"`
			ProviderRevision int64  `bun:"provider_revision"`
			HeadVersion      uint64 `bun:"head_version"`
			Fence            uint64 `bun:"fence"`
		}
		if err := store.db.NewRaw(`SELECT a_leg_id, posted_amount_nano, provider_revision, head_version, fence FROM billing_cost_pass_through_heads WHERE account_id = ? AND call_id = ?`, account.ID, callID.String()).Scan(ctx, &head); err != nil {
			t.Fatal(err)
		}
		return head.ALegID, head.PostedAmountNano, head.ProviderRevision, head.HeadVersion, head.Fence
	}

	// The production writer persists the call's A-leg on the durable head.
	if got, _, _, _, _ := readHead(); got != aLegID {
		t.Fatalf("durable pass-through head a_leg_id = %q, want %q", got, aLegID)
	}

	findAdjustment := func(sourceKey string) billing.JournalTransaction {
		t.Helper()
		transactions, err := store.JournalTransactions(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, transaction := range transactions {
			if transaction.OperationKind == CostPassThroughAdjustmentOperationKind && transaction.SourceKey == sourceKey {
				return transaction
			}
		}
		t.Fatalf("adjustment journal with source key %q not found", sourceKey)
		return billing.JournalTransaction{}
	}

	// Positive provider-cost revision: posted 60 -> 80 is a +20 customer debit.
	upCost := phase10ProviderCost(2, 80, "USD")
	upSourceKey, err := billing.CostPassThroughAdjustmentSourceKey(account.ID, callID, upCost)
	if err != nil {
		t.Fatal(err)
	}
	up, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: callID, ProviderCost: upCost})
	if err != nil {
		t.Fatal(err)
	}
	if !up.Applied || up.Replayed || up.Stale || up.Ignored {
		t.Fatalf("positive revision = %+v, want applied only", up)
	}
	if up.PreviousAmount != (billing.Money{Nano: 60, Currency: "USD"}) || up.CurrentAmount != (billing.Money{Nano: 80, Currency: "USD"}) || up.Delta != (billing.Money{Nano: 20, Currency: "USD"}) {
		t.Fatalf("positive revision amounts = previous %+v current %+v delta %+v, want 60/80/+20 USD", up.PreviousAmount, up.CurrentAmount, up.Delta)
	}
	if up.Status != billing.CostPassThroughSettlementFinal {
		t.Fatalf("positive revision status = %q, want final", up.Status)
	}
	if up.ProviderCost != upCost {
		t.Fatalf("positive revision provider cost = %+v, want %+v", up.ProviderCost, upCost)
	}
	if up.Posting.OperationKey != upSourceKey || up.Posting.Transaction.SourceKey != upSourceKey || up.Posting.Transaction.ID != upSourceKey {
		t.Fatalf("positive revision linkage = operation %q source %q id %q, want %q", up.Posting.OperationKey, up.Posting.Transaction.SourceKey, up.Posting.Transaction.ID, upSourceKey)
	}
	upJournal := findAdjustment(upSourceKey)
	if upJournal.AccountID != account.ID || upJournal.TurnID != callID.String() || upJournal.Currency != "USD" {
		t.Fatalf("positive adjustment identity = %+v, want account %q call %q USD", upJournal, account.ID, callID.String())
	}
	if upJournal.ALegID != aLegID {
		t.Fatalf("positive adjustment a_leg_id = %q, want %q (loaded head lost A-leg identity)", upJournal.ALegID, aLegID)
	}
	if upJournal.CorrectionGroupID != originalTransactionID {
		t.Fatalf("positive adjustment correction group = %q, want original %q", upJournal.CorrectionGroupID, originalTransactionID)
	}
	assertEntries := func(transaction billing.JournalTransaction, debit, credit string, amount int64) {
		t.Helper()
		if len(transaction.Entries) != 2 {
			t.Fatalf("adjustment entries = %+v, want exactly two legs", transaction.Entries)
		}
		if transaction.Entries[0] != (billing.JournalEntry{LedgerAccount: debit, Side: billing.JournalDebit, Amount: billing.Money{Nano: amount, Currency: "USD"}}) ||
			transaction.Entries[1] != (billing.JournalEntry{LedgerAccount: credit, Side: billing.JournalCredit, Amount: billing.Money{Nano: amount, Currency: "USD"}}) {
			t.Fatalf("adjustment entries = %+v, want debit %q / credit %q amount %d USD", transaction.Entries, debit, credit, amount)
		}
	}
	assertEntries(upJournal, "customer_financial_account", "customer_adjustment_clearing", 20)
	if _, found, err := loadOperationSnapshot(ctx, store.db, account.ID, billing.CostPassThroughAdjustmentOperationKind, upSourceKey); err != nil || !found {
		t.Fatalf("positive adjustment operation snapshot found = %v, err = %v", found, err)
	}
	if got, posted, revision, version, fence := readHead(); got != aLegID || posted != 80 || revision != 2 || version != 2 || fence != 2 {
		t.Fatalf("head after positive revision = a_leg %q posted %d revision %d version %d fence %d, want %q/80/2/2/2", got, posted, revision, version, fence, aLegID)
	}

	// Negative correction/reduction: posted 80 -> 70 is a -10 customer credit.
	downCost := phase10ProviderCost(3, 70, "USD")
	downSourceKey, err := billing.CostPassThroughAdjustmentSourceKey(account.ID, callID, downCost)
	if err != nil {
		t.Fatal(err)
	}
	down, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: callID, ProviderCost: downCost})
	if err != nil {
		t.Fatal(err)
	}
	if !down.Applied || down.Replayed || down.Stale || down.Ignored {
		t.Fatalf("negative correction = %+v, want applied only", down)
	}
	if down.PreviousAmount != (billing.Money{Nano: 80, Currency: "USD"}) || down.CurrentAmount != (billing.Money{Nano: 70, Currency: "USD"}) || down.Delta != (billing.Money{Nano: -10, Currency: "USD"}) {
		t.Fatalf("negative correction amounts = previous %+v current %+v delta %+v, want 80/70/-10 USD", down.PreviousAmount, down.CurrentAmount, down.Delta)
	}
	if down.ProviderCost != downCost {
		t.Fatalf("negative correction provider cost = %+v, want %+v", down.ProviderCost, downCost)
	}
	downJournal := findAdjustment(downSourceKey)
	if downJournal.AccountID != account.ID || downJournal.TurnID != callID.String() || downJournal.Currency != "USD" {
		t.Fatalf("negative adjustment identity = %+v, want account %q call %q USD", downJournal, account.ID, callID.String())
	}
	if downJournal.ALegID != aLegID {
		t.Fatalf("negative adjustment a_leg_id = %q, want %q (loaded head lost A-leg identity)", downJournal.ALegID, aLegID)
	}
	if downJournal.CorrectionGroupID != originalTransactionID {
		t.Fatalf("negative adjustment correction group = %q, want original %q", downJournal.CorrectionGroupID, originalTransactionID)
	}
	assertEntries(downJournal, "customer_adjustment_clearing", "customer_financial_account", 10)
	if _, found, err := loadOperationSnapshot(ctx, store.db, account.ID, billing.CostPassThroughAdjustmentOperationKind, downSourceKey); err != nil || !found {
		t.Fatalf("negative adjustment operation snapshot found = %v, err = %v", found, err)
	}
	if got, posted, revision, version, fence := readHead(); got != aLegID || posted != 70 || revision != 3 || version != 3 || fence != 3 {
		t.Fatalf("head after negative correction = a_leg %q posted %d revision %d version %d fence %d, want %q/70/3/3/3", got, posted, revision, version, fence, aLegID)
	}

	// Signed orientation is exact in the account balance: 1000 - 60 - 20 + 10.
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 930 {
		t.Fatalf("balance after +20/-10 revisions = %d, want 930", got.BalanceNano)
	}

	// Idempotent replay of the latest revision posts nothing new.
	replay, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: callID, ProviderCost: downCost})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Applied {
		t.Fatalf("revision replay = %+v, want replay without application", replay)
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	var adjustments int
	for _, transaction := range transactions {
		if transaction.OperationKind == CostPassThroughAdjustmentOperationKind {
			adjustments++
			if transaction.ALegID != aLegID {
				t.Fatalf("stored adjustment %q a_leg_id = %q, want %q", transaction.SourceKey, transaction.ALegID, aLegID)
			}
		}
	}
	if adjustments != 2 {
		t.Fatalf("adjustment journal count = %d, want two deltas", adjustments)
	}
	if _, posted, revision, version, fence := readHead(); posted != 70 || revision != 3 || version != 3 || fence != 3 {
		t.Fatalf("head after replay = posted %d revision %d version %d fence %d, want 70/3/3/3", posted, revision, version, fence)
	}
}
