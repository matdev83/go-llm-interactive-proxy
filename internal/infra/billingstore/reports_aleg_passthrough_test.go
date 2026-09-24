package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/stretchr/testify/require"
)

// Cycle 2 (Task 5.3 C2A2) report regressions for trusted pass-through
// authority. Heads and revisions flow through the real production
// writers; drift fixtures use raw INSERT/UPDATE only for writer-rejected
// states (legacy empty-A-leg lineage, foreign heads, amount mismatches,
// stale/duplicate lineage). Pass-through stays a distinct adjustment
// plane: it never enters retail totals.

func seedALegPassThroughCall(t *testing.T, store *DurableStore, accountID, aLegID string, postedNano int64) (billing.BillingCallID, string) {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: accountID, ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v1"},
	}
	require.NoError(t, store.AppendCallUsage(ctx, call))
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:             billing.Money{Nano: 50_000, Currency: "USD"},
		PricingRef:      call.CustomerPricingRef,
		ChargePolicyRef: call.ChargePolicyRef,
	})
	require.NoError(t, err)
	state := billing.CostPassThroughSettlement{
		PolicyRef: call.ChargePolicyRef,
		Policy: billing.CostPassThroughPolicy{
			MissingCost: billing.CostPassThroughMissingCostProvisional,
			SafeBound:   &billing.Money{Nano: 100, Currency: "USD"}, AllowLateAdjustment: true,
		},
		Status:       billing.CostPassThroughSettlementProvisional,
		SafeBound:    billing.Money{Nano: 100, Currency: "USD"},
		PostedAmount: billing.Money{Nano: postedNano, Currency: "USD"},
	}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure,
		Result: billing.CallRatingResult{
			CallID: callID, Fingerprint: "fp-pt-" + callID.String(),
			CustomerCharge: state.PostedAmount, CostPassThrough: &state,
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, settled.Customer.Transaction.ID)
	return callID, settled.Customer.Transaction.ID
}

func applyALegPassThroughRevision(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, provider billing.CostPassThroughProviderCost) billing.CostPassThroughRevisionResult {
	t.Helper()
	result, err := store.ApplyCostPassThroughRevision(context.Background(), billing.ApplyCostPassThroughRevisionInput{
		AccountID: accountID, CallID: callID, ProviderCost: provider,
	})
	require.NoError(t, err)
	require.True(t, result.Applied, "revision %+v must apply", result)
	return result
}

// plantALegPassThroughJournal inserts one sealed adjustment journal row
// with full lineage control. The sealed struct and the row must agree on
// every fingerprinted field.
func plantALegPassThroughJournal(t *testing.T, store *DurableStore, id, source, accountID, turnID, aLegID, debitLedger, creditLedger string, amount int64, seq uint64, group string, balBefore, balAfter int64) {
	t.Helper()
	ctx := context.Background()
	sealed, err := billing.JournalTransaction{
		ID: id, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: accountID, TurnID: turnID, ALegID: aLegID, AccountSequence: seq,
		CorrectionGroupID: group, OperationKind: billing.CostPassThroughAdjustmentOperationKind,
		BalanceBefore: balBefore, BalanceAfter: balAfter,
		SpendableBefore: balBefore, SpendableAfter: balAfter, Mode: "prepaid",
		SnapshotVersionBefore: 1, SnapshotVersionAfter: 2,
		Entries: []billing.JournalEntry{
			{LedgerAccount: debitLedger, Side: billing.JournalDebit, Amount: billing.Money{Nano: amount, Currency: "USD"}},
			{LedgerAccount: creditLedger, Side: billing.JournalCredit, Amount: billing.Money{Nano: amount, Currency: "USD"}},
		},
	}.Seal()
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_transactions(
		transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
		turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
		correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
		reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
		credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, accountID, "financial", "USD", source, sealed.SemanticFingerprint,
		turnID, aLegID, "", seq, "", "", group, billing.CostPassThroughAdjustmentOperationKind,
		balBefore, balAfter, 0, 0, balBefore, balAfter, 0, 0, "prepaid", 1, 2, "2020-01-01T00:00:00Z").Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		id, 0, debitLedger, "debit", "USD", amount).Exec(ctx)
	require.NoError(t, err)
	_, err = store.db.NewRaw(`INSERT INTO journal_entries(transaction_id, ordinal, ledger_account, side, currency, amount_nano) VALUES (?,?,?,?,?,?)`,
		id, 1, creditLedger, "credit", "USD", amount).Exec(ctx)
	require.NoError(t, err)
}

// plantALegPassThroughSnapshot inserts one canonical adjustment operation
// snapshot with recomputed integrity over self-consistent balances.
func plantALegPassThroughSnapshot(t *testing.T, store *DurableStore, accountID, source, fp string, balBefore, balAfter int64, seq uint64) {
	t.Helper()
	before := billing.AccountSnapshot{BalanceNano: balBefore, SpendableNano: balBefore, Mode: billing.AccountPrepaid, Currency: "USD", Version: 1}
	after := billing.AccountSnapshot{BalanceNano: balAfter, SpendableNano: balAfter, Mode: billing.AccountPrepaid, Currency: "USD", Version: 2}
	integrity := snapshotIntegrity(source+":snapshot", accountID, billing.CostPassThroughAdjustmentOperationKind, source, fp, before, after, seq, seq)
	_, err := store.db.NewRaw(`INSERT INTO billing_operation_snapshots(operation_key, account_id, operation_kind, source_key, fingerprint, integrity_fingerprint, currency, mode, balance_before_nano, balance_after_nano, reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano, credit_floor_nano, credit_limit_nano, version_before, version_after, account_sequence_start, account_sequence_end, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		source+":snapshot", accountID, billing.CostPassThroughAdjustmentOperationKind, source, fp, integrity,
		"USD", "prepaid", balBefore, balAfter, 0, 0, balBefore, balAfter, 0, 0, 1, 2, seq, seq, time.Now().UTC()).Exec(context.Background())
	require.NoError(t, err)
}

// driftALegPassThroughHead rewrites head revision state the way no writer
// can: writers advance heads only through the atomic revision path.
func driftALegPassThroughHead(t *testing.T, store *DurableStore, accountID, callID, status string, posted int64, lur, valuation, inputHash string, revision, version, fence uint64) {
	t.Helper()
	_, err := store.db.NewRaw(`UPDATE billing_cost_pass_through_heads SET status = ?, posted_amount_nano = ?, provider_lur_key = ?, provider_valuation_id = ?, provider_revision = ?, provider_input_hash = ?, head_version = ?, fence = ? WHERE account_id = ? AND call_id = ?`,
		status, posted, lur, valuation, revision, inputHash, version, fence, accountID, callID).Exec(context.Background())
	require.NoError(t, err)
}

func requireALegNoIssue(t *testing.T, report billing.ALegReport, code string) {
	t.Helper()
	for _, issue := range report.Issues {
		require.NotEqual(t, code, issue.Code, "unexpected issue %+v in %+v", issue, report.Issues)
	}
}

// TestALegReportPassThroughRevisionsKnown proves the production
// writer-to-report join: a provisional head stays pending, then a
// positive provider-cost revision and a later negative correction each
// resolve the call known with validated adjustment lineage while retail
// totals carry the settlement charge only.
func TestALegReportPassThroughRevisionsKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2-pt", "a-leg-c2"
	seedALegCustomerAccount(t, store, accountID)
	callID, _ := seedALegPassThroughCall(t, store, accountID, aLegID, 60)

	provisional := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, provisional, callID)
	require.Equal(t, billing.ALegCallPending, summary.Status,
		"provisional head is incomplete pass-through authority, never known")
	require.False(t, summary.CustomerChargeKnown)
	requireALegIssue(t, provisional, "customer_adjustment_pending")
	require.Empty(t, summary.Adjustments)
	require.Equal(t, int64(0), provisional.Retail.KnownSubtotal.Nano)

	up := phase10ProviderCost(2, 80, "USD")
	upSource, err := billing.CostPassThroughAdjustmentSourceKey(accountID, callID, up)
	require.NoError(t, err)
	applyALegPassThroughRevision(t, store, accountID, callID, up)

	positive := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary = findALegCall(t, positive, callID)
	require.Equal(t, billing.ALegCallKnown, summary.Status)
	require.True(t, summary.CustomerChargeKnown, "retail settlement stays proven")
	require.Equal(t, int64(60), summary.CustomerCharge.Nano)
	source, err := billing.CustomerSettlementSourceKey(accountID, callID)
	require.NoError(t, err)
	require.Equal(t, source+":customer_call_settlement", summary.CustomerOperationKey)
	require.Len(t, summary.Adjustments, 1)
	require.Equal(t, upSource, summary.Adjustments[0].TransactionID)
	require.Equal(t, int64(-20), summary.Adjustments[0].Amount.Nano)
	require.True(t, summary.Adjustments[0].Validated)
	require.Equal(t, int64(60), positive.Retail.KnownSubtotal.Nano,
		"pass-through adjustments must never enter retail totals")
	require.Equal(t, 1, positive.Retail.SettledCalls)
	require.Equal(t, 0, positive.Retail.PendingCalls)
	requireALegNoIssue(t, positive, "customer_adjustment_pending")
	requireALegNoIssue(t, positive, "customer_adjustment_unresolved")

	down := phase10ProviderCost(3, 70, "USD")
	downSource, err := billing.CostPassThroughAdjustmentSourceKey(accountID, callID, down)
	require.NoError(t, err)
	applyALegPassThroughRevision(t, store, accountID, callID, down)

	corrected := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary = findALegCall(t, corrected, callID)
	require.Equal(t, billing.ALegCallKnown, summary.Status)
	require.True(t, summary.CustomerChargeKnown)
	require.Equal(t, int64(60), summary.CustomerCharge.Nano)
	require.Len(t, summary.Adjustments, 2)
	require.Equal(t, upSource, summary.Adjustments[0].TransactionID)
	require.Equal(t, int64(-20), summary.Adjustments[0].Amount.Nano)
	require.Equal(t, downSource, summary.Adjustments[1].TransactionID)
	require.Equal(t, int64(10), summary.Adjustments[1].Amount.Nano)
	for _, ref := range summary.Adjustments {
		require.True(t, ref.Validated)
	}
	require.Equal(t, int64(60), corrected.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, corrected.Retail.SettledCalls)
	require.Equal(t, 0, corrected.Retail.UnknownCalls)
}

// TestALegReportPassThroughLateRevisionVisible proves a revision appended
// after a first snapshot naturally appears in a later snapshot: rolling
// projections follow durable revisions with no finality trigger.
func TestALegReportPassThroughLateRevisionVisible(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2-late", "a-leg-c2"
	seedALegCustomerAccount(t, store, accountID)
	callID, _ := seedALegPassThroughCall(t, store, accountID, aLegID, 60)
	up := phase10ProviderCost(2, 80, "USD")
	applyALegPassThroughRevision(t, store, accountID, callID, up)

	first := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, billing.ALegCallKnown, findALegCall(t, first, callID).Status)
	require.Len(t, findALegCall(t, first, callID).Adjustments, 1)

	down := phase10ProviderCost(3, 70, "USD")
	applyALegPassThroughRevision(t, store, accountID, callID, down)

	second := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.False(t, second.AsOf.Before(first.AsOf))
	summary := findALegCall(t, second, callID)
	require.Equal(t, billing.ALegCallKnown, summary.Status)
	require.Len(t, summary.Adjustments, 2, "late revision must surface without any retirement trigger")
	require.Equal(t, int64(60), second.Retail.KnownSubtotal.Nano)
}

// TestALegReportPassThroughLegacyJoinKnown proves a pre-C2A1 immutable
// journal with empty A-leg lineage is accepted exactly through its
// trusted head: the head names the queried A-leg and points at the
// journal's operation/source lineage. The old journal row itself is
// never mutated or backfilled.
func TestALegReportPassThroughLegacyJoinKnown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	const accountID, aLegID = "aleg-c2-legacy", "a-leg-c2"
	seedALegCustomerAccount(t, store, accountID)
	callID, originalTx := seedALegPassThroughCall(t, store, accountID, aLegID, 60)

	up := phase10ProviderCost(2, 80, "USD")
	upSource, err := billing.CostPassThroughAdjustmentSourceKey(accountID, callID, up)
	require.NoError(t, err)
	// Reproduce the pre-C2A1 post-revision state: the head is final and
	// names the A-leg, but the durable journal carries empty A-leg.
	driftALegPassThroughHead(t, store, accountID, callID.String(), "final", 80,
		up.LURKey, up.ValuationID, up.InputHash, 2, 2, 2)
	plantALegPassThroughJournal(t, store, upSource, upSource, accountID, callID.String(), "",
		"customer_financial_account", "customer_adjustment_clearing", 20, 7, originalTx, 1000, 980)
	plantALegPassThroughSnapshot(t, store, accountID, upSource, "fp-legacy-1", 1000, 980, 7)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallKnown, summary.Status)
	require.True(t, summary.CustomerChargeKnown)
	require.Equal(t, int64(60), summary.CustomerCharge.Nano)
	require.Len(t, summary.Adjustments, 1)
	require.Equal(t, upSource, summary.Adjustments[0].TransactionID)
	require.True(t, summary.Adjustments[0].Validated)
	require.Equal(t, int64(60), report.Retail.KnownSubtotal.Nano)

	var storedALeg string
	require.NoError(t, store.db.NewRaw(`SELECT a_leg_id FROM journal_transactions WHERE transaction_id = ?`, upSource).Scan(ctx, &storedALeg))
	require.Equal(t, "", storedALeg, "legacy journal lineage must never be mutated or backfilled")
}

// TestALegReportPassThroughForeignHeadUnknown proves a head naming
// another A-leg cannot vouch for this scope even when the retail plane
// is canonical.
func TestALegReportPassThroughForeignHeadUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2-fhead", "a-leg-c2"
	seedALegCustomerAccount(t, store, accountID)
	callID, _ := seedALegPassThroughCall(t, store, accountID, aLegID, 60)
	up := phase10ProviderCost(2, 80, "USD")
	applyALegPassThroughRevision(t, store, accountID, callID, up)
	_, err := store.db.NewRaw(`UPDATE billing_cost_pass_through_heads SET a_leg_id = ? WHERE account_id = ? AND call_id = ?`,
		"a-leg-foreign", accountID, callID.String()).Exec(context.Background())
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	require.False(t, summary.CustomerChargeKnown, "unknown is never a charge")
	requireALegIssue(t, report, "customer_adjustment_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
	require.Equal(t, 1, report.Retail.UnknownCalls)
}

// TestALegReportPassThroughAmountMismatchUnknown proves a head whose
// posted amount disagrees with the journal telescope stays unresolved.
func TestALegReportPassThroughAmountMismatchUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2-mismatch", "a-leg-c2"
	seedALegCustomerAccount(t, store, accountID)
	callID, _ := seedALegPassThroughCall(t, store, accountID, aLegID, 60)
	up := phase10ProviderCost(2, 80, "USD")
	applyALegPassThroughRevision(t, store, accountID, callID, up)
	_, err := store.db.NewRaw(`UPDATE billing_cost_pass_through_heads SET posted_amount_nano = ? WHERE account_id = ? AND call_id = ?`,
		81, accountID, callID.String()).Exec(context.Background())
	require.NoError(t, err)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	requireALegIssue(t, report, "customer_adjustment_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportPassThroughStaleRevisionUnknown proves a journal from a
// revision beyond the trusted head revision is writer-impossible and
// stays unresolved.
func TestALegReportPassThroughStaleRevisionUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2-stale", "a-leg-c2"
	seedALegCustomerAccount(t, store, accountID)
	callID, originalTx := seedALegPassThroughCall(t, store, accountID, aLegID, 60)
	up := phase10ProviderCost(2, 80, "USD")
	applyALegPassThroughRevision(t, store, accountID, callID, up)
	future := phase10ProviderCost(3, 75, "USD")
	futureSource, err := billing.CostPassThroughAdjustmentSourceKey(accountID, callID, future)
	require.NoError(t, err)
	plantALegPassThroughJournal(t, store, futureSource, futureSource, accountID, callID.String(), aLegID,
		"customer_financial_account", "customer_adjustment_clearing", 5, 99, originalTx, 1000, 995)
	plantALegPassThroughSnapshot(t, store, accountID, futureSource, "fp-future", 1000, 995, 99)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	requireALegIssue(t, report, "customer_adjustment_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportPassThroughCompetingRevisionUnknown proves two journals
// on one revision with different valuation lineage are ambiguous and
// stay unresolved even when each validates alone. (Same-source
// duplication is already rejected by the durable UNIQUE on
// (account, book, source_key); the core evaluator still defends it in
// depth and covers it with unit tests.)
func TestALegReportPassThroughCompetingRevisionUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2-dup", "a-leg-c2"
	seedALegCustomerAccount(t, store, accountID)
	callID, originalTx := seedALegPassThroughCall(t, store, accountID, aLegID, 60)
	up := phase10ProviderCost(2, 80, "USD")
	applyALegPassThroughRevision(t, store, accountID, callID, up)
	competing := phase10ProviderCost(2, 75, "USD")
	competing.ValuationID = "val-competitor"
	competingSource, err := billing.CostPassThroughAdjustmentSourceKey(accountID, callID, competing)
	require.NoError(t, err)
	plantALegPassThroughJournal(t, store, competingSource, competingSource, accountID, callID.String(), aLegID,
		"customer_financial_account", "customer_adjustment_clearing", 15, 99, originalTx, 1000, 985)
	plantALegPassThroughSnapshot(t, store, accountID, competingSource, "fp-competing", 1000, 985, 99)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	requireALegIssue(t, report, "customer_adjustment_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportPassThroughUnrelatedLegacyUnknown proves an empty-A-leg
// journal outside the head-pointed lineage stays unresolved.
func TestALegReportPassThroughUnrelatedLegacyUnknown(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2-unrel", "a-leg-c2"
	seedALegCustomerAccount(t, store, accountID)
	callID, originalTx := seedALegPassThroughCall(t, store, accountID, aLegID, 60)
	up := phase10ProviderCost(2, 80, "USD")
	applyALegPassThroughRevision(t, store, accountID, callID, up)
	plantALegPassThroughJournal(t, store, "tx-unrelated", "src-unrelated", accountID, callID.String(), "",
		"customer_financial_account", "customer_adjustment_clearing", 10, 99, originalTx, 1000, 990)

	report := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	summary := findALegCall(t, report, callID)
	require.Equal(t, billing.ALegCallUnknown, summary.Status)
	requireALegIssue(t, report, "customer_adjustment_unresolved")
	require.Equal(t, int64(0), report.Retail.KnownSubtotal.Nano)
}

// TestALegReportPassThroughBoundedFactLoading proves pass-through fact
// loads (heads, adjustment snapshots, policy exposures) stream in
// chunk-bounded statements with no scope-wide IN list and no N+1: every
// recorded IN-list stays chunk-bounded while every call still resolves.
func TestALegReportPassThroughBoundedFactLoading(t *testing.T) {
	// No t.Parallel: this test mutates the package chunk size; sequential
	// execution keeps the override and its Cleanup restore deterministic.
	store := newSQLiteTestStore(t)
	const accountID, aLegID = "aleg-c2-bound", "a-leg-c2"
	seedALegCustomerAccount(t, store, accountID)
	for i := 0; i < 3; i++ {
		callID, _ := seedALegPassThroughCall(t, store, accountID, aLegID, 60)
		applyALegPassThroughRevision(t, store, accountID, callID, phase10ProviderCost(2, 80, "USD"))
		applyALegPassThroughRevision(t, store, accountID, callID, phase10ProviderCost(3, 70, "USD"))
	}
	recorder := &alegQueryRecorder{}
	store.db.AddQueryHook(recorder)

	const chunk = 2
	oldChunk := alegReportScopeChunkSize
	alegReportScopeChunkSize = chunk
	t.Cleanup(func() { alegReportScopeChunkSize = oldChunk })

	report := queryALegCustomerReport(t, store, accountID, aLegID, 10, "")
	require.Equal(t, 3, report.CallCount)
	require.Equal(t, 3, report.Retail.SettledCalls)
	require.Equal(t, int64(180), report.Retail.KnownSubtotal.Nano,
		"retail totals carry settlement charges only, never adjustments")
	for _, row := range report.Calls {
		require.Equal(t, billing.ALegCallKnown, row.Status)
		require.Len(t, row.Adjustments, 2)
		for _, ref := range row.Adjustments {
			require.True(t, ref.Validated)
		}
	}
	require.LessOrEqual(t, recorder.maxInListLenMatching("billing_cost_pass_through_heads"), chunk,
		"head loads must stay chunk-bounded")
	require.LessOrEqual(t, recorder.maxInListLenMatching("billing_operation_snapshots"), chunk,
		"marker and adjustment-snapshot loads must stay chunk-bounded")
	require.LessOrEqual(t, recorder.maxInListLenMatching("call_exposures"), chunk,
		"policy loads must stay chunk-bounded")
	require.LessOrEqual(t, recorder.maxInListLenMatching("journal_entries"), chunk*(alegReportMaxJournalsPerCall+1),
		"entry loads scale only with capped per-call journal density, never scope size")
	require.GreaterOrEqual(t, recorder.countMatching("billing_cost_pass_through_heads"), 2,
		"head loads must stream in chunks, got %d head queries", recorder.countMatching("billing_cost_pass_through_heads"))
}
