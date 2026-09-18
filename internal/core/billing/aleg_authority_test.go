package billing

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// R12 characterization for the single core customer settlement authority
// evaluator. The report shell must delegate to EvaluateALegCallAuthority;
// no divergent report-local proof engine may exist.

func alegTestIntegrity(opKey, accountID, kind, source, fp string, before, after AccountSnapshot, seqStart, seqEnd uint64) string {
	payload, _ := json.Marshal(struct {
		Version                                                        string
		OperationKey, AccountID, OperationKind, SourceKey, Fingerprint string
		Before, After                                                  AccountSnapshot
		SequenceStart, SequenceEnd                                     uint64
	}{"snapshot:v1", opKey, accountID, kind, source, fp, before, after, seqStart, seqEnd})
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("snapshot:v1:%x", digest[:])
}

func alegTestScope(t *testing.T, currency string) (ALegAuthorityScope, BillingCallID, string) {
	t.Helper()
	callID, err := NewBillingCallID()
	require.NoError(t, err)
	source, err := CustomerSettlementSourceKey("acct-r12", callID)
	require.NoError(t, err)
	return ALegAuthorityScope{AccountID: "acct-r12", ALegID: "a-r12", CallID: callID.String(), Currency: currency}, callID, source
}

func alegTestMarker(t *testing.T, scope ALegAuthorityScope, callID BillingCallID, source, kind string, before, after AccountSnapshot, seqStart, seqEnd uint64) ALegMarker {
	t.Helper()
	opKey := source + ":" + kind
	fp := "fp-" + callID.String()
	integrity := alegTestIntegrity(opKey, scope.AccountID, kind, callID.String(), fp, before, after, seqStart, seqEnd)
	return ALegMarker{
		OperationKey: opKey, AccountID: scope.AccountID, OperationKind: kind,
		SourceKey: callID.String(), Fingerprint: fp, IntegrityFingerprint: integrity,
		Currency: scope.Currency, Mode: string(AccountPrepaid),
		Before: before, After: after,
		SequenceStart: seqStart, SequenceEnd: seqEnd,
	}
}

func alegTestCanonical(t *testing.T, scope ALegAuthorityScope, source, kind string, delta int64, seq uint64) JournalTransaction {
	t.Helper()
	sealed, err := JournalTransaction{
		ID: source, Book: JournalBookFinancial, Currency: scope.Currency, SourceKey: source,
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: scope.ALegID,
		AccountSequence: seq, OperationKind: kind,
		BalanceBefore: 100 + delta, BalanceAfter: 100,
		SpendableBefore: 100 + delta, SpendableAfter: 100,
		Mode: string(AccountPrepaid),
		Entries: []JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalDebit, Amount: Money{Nano: delta, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalCredit, Amount: Money{Nano: delta, Currency: scope.Currency}},
		},
	}.Seal()
	require.NoError(t, err)
	// Bind snapshots to the marker path: tests set matching balances.
	return sealed
}

func TestEvaluateALegCallAuthorityKnownPositive(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	journal := alegTestCanonical(t, scope, source, ALegSettlementKind, 20, 7)
	// Align journal snapshots with the marker.
	journal.BalanceBefore, journal.BalanceAfter = 120, 100
	journal.SpendableBefore, journal.SpendableAfter = 120, 100
	journal.SnapshotVersionBefore, journal.SnapshotVersionAfter = 1, 2
	sealed, err := journal.Seal()
	require.NoError(t, err)
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, []JournalTransaction{sealed})
	require.NoError(t, err)
	require.Empty(t, issues)
	require.Equal(t, ALegCallKnown, verdict.Status)
	require.True(t, verdict.Known)
	require.Equal(t, int64(20), verdict.Charge.Nano)
	require.Equal(t, marker.OperationKey, verdict.OpKey)
}

func TestEvaluateALegCallAuthorityKnownZero(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	snap := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, snap, snap, 1, 1)
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, nil)
	require.NoError(t, err)
	require.Empty(t, issues)
	require.Equal(t, ALegCallKnown, verdict.Status)
	require.True(t, verdict.Known)
	require.Zero(t, verdict.Charge.Nano)
}

func TestEvaluateALegCallAuthorityPendingWhenMarkerMissing(t *testing.T) {
	t.Parallel()
	scope, _, _ := alegTestScope(t, "USD")
	verdict, issues, err := EvaluateALegCallAuthority(scope, nil, nil)
	require.NoError(t, err)
	require.Equal(t, ALegCallPending, verdict.Status)
	require.Len(t, issues, 1)
	require.Equal(t, ALegIssueMarkerMissing, issues[0].Code)
}

func TestEvaluateALegCallAuthorityMissingCanonical(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, nil)
	require.NoError(t, err)
	require.Equal(t, ALegCallUnknown, verdict.Status)
	require.Equal(t, ALegIssueJournalMissing, issues[0].Code)
}

func TestEvaluateALegCallAuthorityCorrectionOnlyUnresolved(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	snap := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, snap, snap, 1, 1)
	correction, err := JournalTransaction{
		ID: "corr-1", Book: JournalBookFinancial, Currency: "USD", SourceKey: "src-corr-1",
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: scope.ALegID,
		AccountSequence: 8, ReversalOf: "missing-target", OperationKind: ALegSettlementKind,
		CorrectionGroupID: "g-1",
		Entries: []JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalDebit, Amount: Money{Nano: 5, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: JournalCredit, Amount: Money{Nano: 5, Currency: "USD"}},
		},
	}.Seal()
	require.NoError(t, err)
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, []JournalTransaction{correction})
	require.NoError(t, err)
	require.Equal(t, ALegCallUnknown, verdict.Status)
	require.Equal(t, ALegIssueCorrectionUnresolved, issues[0].Code)
}

func TestEvaluateALegCallAuthorityValidCorrectionNets(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	canonical := alegTestCanonical(t, scope, source, ALegSettlementKind, 20, 7)
	canonical.BalanceBefore, canonical.BalanceAfter = 120, 100
	canonical.SpendableBefore, canonical.SpendableAfter = 120, 100
	canonical.SnapshotVersionBefore, canonical.SnapshotVersionAfter = 1, 2
	canonical, err := canonical.Seal()
	require.NoError(t, err)
	reversal, err := JournalTransaction{
		ID: "rev-1", Book: JournalBookFinancial, Currency: "USD", SourceKey: "src-rev-1",
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: scope.ALegID,
		AccountSequence: 8, ReversalOf: source, OperationKind: ALegSettlementKind,
		CorrectionGroupID: source,
		Entries: []JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: 8, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: 8, Currency: "USD"}},
		},
	}.Seal()
	require.NoError(t, err)
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, []JournalTransaction{canonical, reversal})
	require.NoError(t, err)
	require.Empty(t, issues)
	require.Equal(t, ALegCallKnown, verdict.Status)
	require.Equal(t, int64(12), verdict.Charge.Nano)
}

func TestEvaluateALegCallAuthorityCrossPlaneUnknown(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	canonical := alegTestCanonical(t, scope, source, ALegSettlementKind, 20, 7)
	canonical.BalanceBefore, canonical.BalanceAfter = 120, 100
	canonical.SpendableBefore, canonical.SpendableAfter = 120, 100
	canonical.SnapshotVersionBefore, canonical.SnapshotVersionAfter = 1, 2
	canonical, err := canonical.Seal()
	require.NoError(t, err)
	repairClaim, err := JournalTransaction{
		ID: "repair-1", Book: JournalBookFinancial, Currency: "USD", SourceKey: "src-repair-1",
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: scope.ALegID,
		AccountSequence: 9, ReversalOf: source, OperationKind: ALegRepairKind,
		CorrectionGroupID: source,
		Entries: []JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: 5, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: 5, Currency: "USD"}},
		},
	}.Seal()
	require.NoError(t, err)
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, []JournalTransaction{canonical, repairClaim})
	require.NoError(t, err)
	require.Equal(t, ALegCallUnknown, verdict.Status)
	require.NotEmpty(t, issues)
}

func TestEvaluateALegCallAuthorityScopeMismatch(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	journal := alegTestCanonical(t, scope, source, ALegSettlementKind, 20, 7)
	journal.BalanceBefore, journal.BalanceAfter = 120, 100
	journal.SpendableBefore, journal.SpendableAfter = 120, 100
	journal.SnapshotVersionBefore, journal.SnapshotVersionAfter = 1, 2
	journal.ALegID = "foreign-a-leg"
	journal, err := journal.Seal()
	require.NoError(t, err)
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, []JournalTransaction{journal})
	require.NoError(t, err)
	require.Equal(t, ALegCallUnknown, verdict.Status)
	require.Equal(t, ALegIssueJournalMismatch, issues[0].Code)
}

func TestEvaluateALegCallAuthorityPaddedReferenceNeverResolves(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	canonical := alegTestCanonical(t, scope, source, ALegSettlementKind, 20, 7)
	canonical.BalanceBefore, canonical.BalanceAfter = 120, 100
	canonical.SpendableBefore, canonical.SpendableAfter = 120, 100
	canonical.SnapshotVersionBefore, canonical.SnapshotVersionAfter = 1, 2
	canonical, err := canonical.Seal()
	require.NoError(t, err)
	padded, err := JournalTransaction{
		ID: "pad-1", Book: JournalBookFinancial, Currency: "USD", SourceKey: "src-pad-1",
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: scope.ALegID,
		AccountSequence: 9, ReversalOf: " " + source + " ", OperationKind: ALegSettlementKind,
		CorrectionGroupID: source,
		Entries: []JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: 5, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: 5, Currency: "USD"}},
		},
	}.Seal()
	require.NoError(t, err)
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, []JournalTransaction{canonical, padded})
	require.NoError(t, err)
	require.Equal(t, ALegCallUnknown, verdict.Status)
	require.Equal(t, ALegIssueCorrectionUnresolved, issues[0].Code)
}

func TestEvaluateALegCallAuthorityMalformedSemantics(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	marker.Mode = "bogus-mode"
	marker.IntegrityFingerprint = alegTestIntegrity(marker.OperationKey, marker.AccountID, marker.OperationKind, marker.SourceKey, marker.Fingerprint, before, after, marker.SequenceStart, marker.SequenceEnd)
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, nil)
	require.NoError(t, err)
	require.Equal(t, ALegCallUnknown, verdict.Status)
	require.Equal(t, ALegIssueMarkerInvalid, issues[0].Code)
}

func TestEvaluateALegCallAuthorityDuplicateMarkersConflict(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	snap := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	first := alegTestMarker(t, scope, callID, source, ALegSettlementKind, snap, snap, 1, 1)
	second := first
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{first, second}, nil)
	require.NoError(t, err)
	require.Equal(t, ALegCallUnknown, verdict.Status)
	require.Equal(t, ALegIssueMarkersConflicting, issues[0].Code)
}

func TestEvaluateALegCallAuthorityBookConflict(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	// Nonfinancial settlement-shaped journal: Validate rejects nonfinancial
	// books, so plant the shape without Seal and sign nothing.
	journal := JournalTransaction{
		ID: "drift-1", Book: "authorization", Currency: "USD", SourceKey: "src-drift",
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: scope.ALegID,
		AccountSequence: 3, OperationKind: ALegSettlementKind,
		Entries: []JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalDebit, Amount: Money{Nano: 5, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: JournalCredit, Amount: Money{Nano: 5, Currency: "USD"}},
		},
	}
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, []JournalTransaction{journal})
	require.NoError(t, err)
	require.Equal(t, ALegCallUnknown, verdict.Status)
	require.Equal(t, ALegIssueBookConflict, issues[0].Code)
}

func TestEvaluateALegCallAuthorityAdjustmentDefersPending(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	canonical := alegTestCanonical(t, scope, source, ALegSettlementKind, 20, 7)
	canonical.BalanceBefore, canonical.BalanceAfter = 120, 100
	canonical.SpendableBefore, canonical.SpendableAfter = 120, 100
	canonical.SnapshotVersionBefore, canonical.SnapshotVersionAfter = 1, 2
	canonical, err := canonical.Seal()
	require.NoError(t, err)
	adjustment, err := JournalTransaction{
		ID: "pt-1", Book: JournalBookFinancial, Currency: "USD", SourceKey: "src-pt-1",
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: scope.ALegID,
		AccountSequence: 9, OperationKind: CostPassThroughAdjustmentOperationKind,
		Entries: []JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalDebit, Amount: Money{Nano: 3, Currency: "USD"}},
			{LedgerAccount: "provider_payable_clearing", Side: JournalCredit, Amount: Money{Nano: 3, Currency: "USD"}},
		},
	}.Seal()
	require.NoError(t, err)
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker}, []JournalTransaction{canonical, adjustment})
	require.NoError(t, err)
	require.Equal(t, ALegCallPending, verdict.Status)
	require.Len(t, issues, 1)
	require.Equal(t, ALegIssueAdjustmentPending, issues[0].Code)
	require.Len(t, verdict.Adjustments, 1)
}

func TestEvaluateALegCallAuthorityCorrectionOverflowFailsQuery(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	canonical := alegTestCanonical(t, scope, source, ALegSettlementKind, 20, 7)
	canonical.BalanceBefore, canonical.BalanceAfter = 120, 100
	canonical.SpendableBefore, canonical.SpendableAfter = 120, 100
	canonical.SnapshotVersionBefore, canonical.SnapshotVersionAfter = 1, 2
	canonical, err := canonical.Seal()
	require.NoError(t, err)
	overflow, err := JournalTransaction{
		ID: "corr-overflow", Book: JournalBookFinancial, Currency: "USD", SourceKey: "src-corr-overflow",
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: scope.ALegID,
		AccountSequence: 8, ReversalOf: source, OperationKind: ALegSettlementKind,
		CorrectionGroupID: source,
		Entries: []JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalDebit, Amount: Money{Nano: math.MaxInt64, Currency: "USD"}},
			{LedgerAccount: "usage_revenue", Side: JournalCredit, Amount: Money{Nano: math.MaxInt64, Currency: "USD"}},
		},
	}.Seal()
	require.NoError(t, err)
	_, _, err = EvaluateALegCallAuthority(scope, []ALegMarker{marker}, []JournalTransaction{canonical, overflow})
	require.ErrorIs(t, err, ErrMoneyOverflow)
}

// R14 characterization: the correction graph must be canonical-rooted,
// deterministic, and shape-exact. Every case below is unresolved with
// customer_correction_unresolved and contributes no known subtotal.

func alegTestSettledCall(t *testing.T) (ALegAuthorityScope, ALegMarker, JournalTransaction) {
	t.Helper()
	scope, callID, source := alegTestScope(t, "USD")
	before := AccountSnapshot{BalanceNano: 120, SpendableNano: 120, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	canonical := alegTestCanonical(t, scope, source, ALegSettlementKind, 20, 7)
	canonical.BalanceBefore, canonical.BalanceAfter = 120, 100
	canonical.SpendableBefore, canonical.SpendableAfter = 120, 100
	canonical.SnapshotVersionBefore, canonical.SnapshotVersionAfter = 1, 2
	canonical, err := canonical.Seal()
	require.NoError(t, err)
	return scope, marker, canonical
}

func alegTestClaim(t *testing.T, scope ALegAuthorityScope, id string, seq uint64, reversalOf, corrects, group string, entries []JournalEntry) JournalTransaction {
	t.Helper()
	sealed, err := JournalTransaction{
		ID: id, Book: JournalBookFinancial, Currency: scope.Currency, SourceKey: "src-" + id,
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: scope.ALegID,
		AccountSequence: seq, ReversalOf: reversalOf, CorrectsTransactionID: corrects,
		CorrectionGroupID: group, OperationKind: ALegSettlementKind,
		Entries: entries,
	}.Seal()
	require.NoError(t, err)
	return sealed
}

func alegTestPair(debitLedger, creditLedger string, amount int64, currency string) []JournalEntry {
	return []JournalEntry{
		{LedgerAccount: debitLedger, Side: JournalDebit, Amount: Money{Nano: amount, Currency: currency}},
		{LedgerAccount: creditLedger, Side: JournalCredit, Amount: Money{Nano: amount, Currency: currency}},
	}
}

func requireALegCorrectionUnresolved(t *testing.T, verdict ALegCallVerdict, issues []ReconciliationIssue, err error) {
	t.Helper()
	require.NoError(t, err)
	require.Equal(t, ALegCallUnknown, verdict.Status)
	require.False(t, verdict.Known)
	require.Zero(t, verdict.Charge.Nano)
	require.Len(t, issues, 1)
	require.Equal(t, ALegIssueCorrectionUnresolved, issues[0].Code)
}

func TestEvaluateALegCallAuthorityUnrelatedComponentUnresolved(t *testing.T) {
	t.Parallel()
	scope, marker, canonical := alegTestSettledCall(t)
	unrelated := alegTestClaim(t, scope, "tx-unrelated-root", 8, "", "", "",
		alegTestPair("customer_financial_account", "usage_revenue", 20, scope.Currency))
	// Mirror orientation, sealed as built: the claim pays back, so the
	// customer side is a credit and revenue is a debit.
	reversal := alegTestClaim(t, scope, "tx-unrelated-rev", 9, unrelated.ID, "", unrelated.ID,
		[]JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: 5, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: 5, Currency: scope.Currency}},
		})
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker},
		[]JournalTransaction{canonical, unrelated, reversal})
	requireALegCorrectionUnresolved(t, verdict, issues, err)
}

func TestEvaluateALegCallAuthorityCompetingReplacementsUnresolved(t *testing.T) {
	t.Parallel()
	scope, marker, canonical := alegTestSettledCall(t)
	call, err := ParseBillingCallID(scope.CallID)
	require.NoError(t, err)
	source, err := CustomerSettlementSourceKey(scope.AccountID, call)
	require.NoError(t, err)
	reversal := alegTestClaim(t, scope, "tx-rev", 8, source, "", source,
		[]JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: 8, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: 8, Currency: scope.Currency}},
		})
	replacementOne := alegTestClaim(t, scope, "tx-rep-one", 9, "", source, source,
		[]JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: 3, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: 3, Currency: scope.Currency}},
		})
	replacementTwo := alegTestClaim(t, scope, "tx-rep-two", 10, "", source, source,
		[]JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: 4, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: 4, Currency: scope.Currency}},
		})
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker},
		[]JournalTransaction{canonical, reversal, replacementOne, replacementTwo})
	requireALegCorrectionUnresolved(t, verdict, issues, err)
}

func TestEvaluateALegCallAuthorityReversalCycleUnresolved(t *testing.T) {
	t.Parallel()
	scope, marker, canonical := alegTestSettledCall(t)
	cycleOne := alegTestClaim(t, scope, "tx-cycle-one", 8, "tx-cycle-two", "", "g-f1-cyc",
		[]JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: 5, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: 5, Currency: scope.Currency}},
		})
	cycleTwo := alegTestClaim(t, scope, "tx-cycle-two", 9, "tx-cycle-one", "", "g-f1-cyc",
		alegTestPair("customer_financial_account", "usage_revenue", 2, scope.Currency))
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker},
		[]JournalTransaction{canonical, cycleOne, cycleTwo})
	requireALegCorrectionUnresolved(t, verdict, issues, err)
}

func TestEvaluateALegCallAuthorityDualLinkClaimUnresolved(t *testing.T) {
	t.Parallel()
	scope, marker, canonical := alegTestSettledCall(t)
	call, err := ParseBillingCallID(scope.CallID)
	require.NoError(t, err)
	source, err := CustomerSettlementSourceKey(scope.AccountID, call)
	require.NoError(t, err)
	dual := alegTestClaim(t, scope, "tx-dual-link", 8, source, source, source,
		[]JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: 5, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: 5, Currency: scope.Currency}},
		})
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker},
		[]JournalTransaction{canonical, dual})
	requireALegCorrectionUnresolved(t, verdict, issues, err)
}

func TestEvaluateALegCallAuthorityDuplicateLinksDeterministic(t *testing.T) {
	t.Parallel()
	scope, marker, canonical := alegTestSettledCall(t)
	call, err := ParseBillingCallID(scope.CallID)
	require.NoError(t, err)
	source, err := CustomerSettlementSourceKey(scope.AccountID, call)
	require.NoError(t, err)
	mirror := func(amount int64) []JournalEntry {
		return []JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: amount, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: amount, Currency: scope.Currency}},
		}
	}
	first := alegTestClaim(t, scope, "tx-dup-one", 8, source, "", source, mirror(8))
	second := alegTestClaim(t, scope, "tx-dup-two", 9, source, "", source, mirror(7))
	forward, forwardIssues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker},
		[]JournalTransaction{canonical, first, second})
	requireALegCorrectionUnresolved(t, forward, forwardIssues, err)
	reversed, reversedIssues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker},
		[]JournalTransaction{canonical, second, first})
	requireALegCorrectionUnresolved(t, reversed, reversedIssues, err)
	require.Equal(t, forward, reversed)
	require.Equal(t, forwardIssues, reversedIssues)
}

func TestEvaluateALegCallAuthorityMalformedCorrectionShapeUnresolved(t *testing.T) {
	t.Parallel()
	scope, marker, canonical := alegTestSettledCall(t)
	call, err := ParseBillingCallID(scope.CallID)
	require.NoError(t, err)
	source, err := CustomerSettlementSourceKey(scope.AccountID, call)
	require.NoError(t, err)
	clearing := alegTestClaim(t, scope, "tx-shape-clearing", 8, source, "", source,
		[]JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalDebit, Amount: Money{Nano: 5, Currency: scope.Currency}},
			{LedgerAccount: "arbitrary_clearing", Side: JournalCredit, Amount: Money{Nano: 5, Currency: scope.Currency}},
		})
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker},
		[]JournalTransaction{canonical, clearing})
	requireALegCorrectionUnresolved(t, verdict, issues, err)

	padded := alegTestClaim(t, scope, "tx-shape-extra", 8, source, "", source,
		[]JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalDebit, Amount: Money{Nano: 5, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalCredit, Amount: Money{Nano: 3, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalCredit, Amount: Money{Nano: 2, Currency: scope.Currency}},
		})
	verdict, issues, err = EvaluateALegCallAuthority(scope, []ALegMarker{marker},
		[]JournalTransaction{canonical, padded})
	requireALegCorrectionUnresolved(t, verdict, issues, err)
}

// R15 positive coverage: a valid same-scope reversal-plus-replacement
// chain under the writer contract nets the replacement. The canonical
// root carries 20, a full mirror reversal removes 20, and a
// CorrectsTransactionID-only replacement re-asserts 12: known 12.
// Journals ride in scrambled input order to exercise evaluator sorting
// and deterministic graph traversal.
func TestEvaluateALegCallAuthorityValidReplacementChainKnown(t *testing.T) {
	t.Parallel()
	scope, marker, canonical := alegTestSettledCall(t)
	call, err := ParseBillingCallID(scope.CallID)
	require.NoError(t, err)
	source, err := CustomerSettlementSourceKey(scope.AccountID, call)
	require.NoError(t, err)
	reversal := alegTestClaim(t, scope, "tx-full-rev", 8, source, "", source,
		[]JournalEntry{
			{LedgerAccount: "customer_financial_account", Side: JournalCredit, Amount: Money{Nano: 20, Currency: scope.Currency}},
			{LedgerAccount: "usage_revenue", Side: JournalDebit, Amount: Money{Nano: 20, Currency: scope.Currency}},
		})
	replacement := alegTestClaim(t, scope, "tx-replacement", 9, "", source, source,
		alegTestPair("customer_financial_account", "usage_revenue", 12, scope.Currency))
	verdict, issues, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker},
		[]JournalTransaction{replacement, canonical, reversal})
	require.NoError(t, err)
	require.Equal(t, ALegCallKnown, verdict.Status)
	require.True(t, verdict.Known)
	require.Equal(t, Money{Nano: 12, Currency: scope.Currency}, verdict.Charge)
	require.Equal(t, marker.OperationKey, verdict.OpKey)
	require.Empty(t, issues)
	// Input order permutation yields the identical result.
	verdictReordered, issuesReordered, err := EvaluateALegCallAuthority(scope, []ALegMarker{marker},
		[]JournalTransaction{reversal, replacement, canonical})
	require.NoError(t, err)
	require.Equal(t, verdict, verdictReordered)
	require.Equal(t, issues, issuesReordered)
}
