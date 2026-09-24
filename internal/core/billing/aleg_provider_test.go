package billing

import (
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// Cycle 2 (Task 5.3 C2B) characterization for the single core provider
// COGS authority evaluator. The rolling report shell owns SQL and fact
// loading; all per-leg provider proof lives here as one side-effect-free
// pure function. Provider economics never enters retail totals.

func alegPVScope(t *testing.T) (ALegAuthorityScope, BillingCallID) {
	t.Helper()
	callID, err := NewBillingCallID()
	require.NoError(t, err)
	return ALegAuthorityScope{StoreID: "test", AccountID: "acct-pv", ALegID: "a-pv", CallID: callID.String(), Currency: "USD"}, callID
}

func alegPVSubject(scope ALegAuthorityScope, bLegID string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "test", AccountID: scope.AccountID,
		ALegID: scope.ALegID, BillingCallID: scope.CallID, BLegID: bLegID,
	}
}

func alegPVLegKey(t *testing.T, scope ALegAuthorityScope, bLegID string) string {
	t.Helper()
	callID, err := ParseBillingCallID(scope.CallID)
	require.NoError(t, err)
	key, err := CallLegUsageKey(callID, bLegID)
	require.NoError(t, err)
	return key
}

func alegPVHeadKey(bLegID string) string { return "head-" + bLegID }

func alegPVOpKey(t *testing.T, scope ALegAuthorityScope, tag string) string {
	t.Helper()
	return ScopedOperationKey("provider_call_cogs", scope.AccountID, "pv-"+tag+"-"+scope.CallID)
}

// alegPVJournal seals one writer-shaped provider COGS journal. Balances
// never move for provider postings; snapshots mirror them exactly.
func alegPVJournal(t *testing.T, scope ALegAuthorityScope, id, bLegID, debitLedger, creditLedger string, amount int64, seq uint64, group, reversalOf string) JournalTransaction {
	t.Helper()
	sealed, err := JournalTransaction{
		ID: id, Book: JournalBookFinancial, Currency: scope.Currency, SourceKey: id,
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: scope.ALegID, BLegID: bLegID,
		AccountSequence: seq, ReversalOf: reversalOf, CorrectsTransactionID: reversalOf, CorrectionGroupID: group,
		OperationKind: "provider_call_cogs",
		BalanceBefore: 5000, BalanceAfter: 5000, SpendableBefore: 5000, SpendableAfter: 5000,
		Mode: string(AccountPrepaid), SnapshotVersionBefore: 3, SnapshotVersionAfter: 3,
		Entries: []JournalEntry{
			{LedgerAccount: debitLedger, Side: JournalDebit, Amount: Money{Nano: amount, Currency: scope.Currency}},
			{LedgerAccount: creditLedger, Side: JournalCredit, Amount: Money{Nano: amount, Currency: scope.Currency}},
		},
	}.Seal()
	require.NoError(t, err)
	return sealed
}

func alegPVSnapshot(scope ALegAuthorityScope, journal JournalTransaction, source string) ALegProviderSnapshot {
	before := AccountSnapshot{
		BalanceNano: journal.BalanceBefore, SpendableNano: journal.SpendableBefore,
		Mode: AccountMode(journal.Mode), Currency: journal.Currency, Version: journal.SnapshotVersionBefore,
	}
	after := AccountSnapshot{
		BalanceNano: journal.BalanceAfter, SpendableNano: journal.SpendableAfter,
		Mode: AccountMode(journal.Mode), Currency: journal.Currency, Version: journal.SnapshotVersionAfter,
	}
	return ALegProviderSnapshot{
		AccountID: scope.AccountID, OperationKind: "provider_call_cogs",
		OperationKey: journal.ID, SourceKey: source, Fingerprint: "fp-" + journal.ID,
		IntegrityFingerprint: alegMarkerIntegrity(journal.ID, scope.AccountID, "provider_call_cogs",
			source, "fp-"+journal.ID, before, after, journal.AccountSequence, journal.AccountSequence),
		Currency: scope.Currency, Mode: journal.Mode,
		Before: before, After: after,
		SequenceStart: journal.AccountSequence, SequenceEnd: journal.AccountSequence,
	}
}

type alegPVChain struct {
	head      ALegProviderHead
	fence     ALegProviderFence
	execution ALegProviderExecutionFence
	journals  []JournalTransaction
	snapshots []ALegProviderSnapshot
}

// alegPVChainTwo builds the canonical two-posting chain: revision 1
// accrues 50, revision 2 corrects to 40 (-10). Amounts telescope from
// zero exactly like the writer posts them.
func alegPVChainTwo(t *testing.T, scope ALegAuthorityScope, bLegID string) alegPVChain {
	t.Helper()
	subject := alegPVSubject(scope, bLegID)
	headKey := alegPVHeadKey(bLegID)
	legKey := alegPVLegKey(t, scope, bLegID)
	op1 := alegPVOpKey(t, scope, "r1")
	op2 := alegPVOpKey(t, scope, "r2")
	j1 := alegPVJournal(t, scope, op1, bLegID, "inference_provider_cogs", "provider_payable_clearing", 50, 7, headKey, "")
	j2 := alegPVJournal(t, scope, op2, bLegID, "provider_payable_clearing", "inference_provider_cogs", 10, 8, headKey, op1)
	return alegPVChain{
		head: ALegProviderHead{
			StoreID:   scope.StoreID,
			AccountID: scope.AccountID, CallID: scope.CallID, HeadKey: headKey,
			SubjectKind: string(metering.SubjectBLeg), SubjectID: bLegID, Subject: subject,
			EvidenceRevision: 2, InputSetHash: strings.Repeat("c", 64), ValuationID: "val-2",
			CurrentAmount: Money{Nano: 40, Currency: scope.Currency},
			HeadVersion:   2, Fence: 2,
			LastOperationKey: op2, OriginalTransactionID: op1, LastTransactionID: op2,
		},
		fence: ALegProviderFence{
			StoreID:    scope.StoreID,
			LineageKey: legKey, Authority: "revision", HeadKey: headKey,
			EvidenceRevision: 2, InputSetHash: strings.Repeat("c", 64), Fingerprint: "fp-fence",
			Amount: Money{Nano: 40, Currency: scope.Currency}, Fence: 2,
			LastOperationKey: op2, OriginalTransactionID: op1, LastTransactionID: op2,
		},
		execution: ALegProviderExecutionFence{
			StoreID:    scope.StoreID,
			LineageKey: legKey, Authority: "revision",
			OwnerSubjectKind: string(metering.SubjectBLeg), OwnerHeadKey: headKey,
			OwnerRevision: 2, OwnerInputSetHash: strings.Repeat("c", 64), OwnerFingerprint: "fp-fence",
			Fence: 3, LastOperationKey: op2, LastTransactionID: op2,
		},
		journals:  []JournalTransaction{j1, j2},
		snapshots: []ALegProviderSnapshot{alegPVSnapshot(scope, j1, "rev-src-1"), alegPVSnapshot(scope, j2, "rev-src-2")},
	}
}

func alegPVFacts(bLegID string, outcome LegOutcome, chain *alegPVChain) ALegProviderLegFacts {
	facts := ALegProviderLegFacts{BLegID: bLegID, Outcome: outcome}
	if chain != nil {
		facts.Heads = []ALegProviderHead{chain.head}
		facts.PostingFences = []ALegProviderFence{chain.fence}
		facts.ExecutionFences = []ALegProviderExecutionFence{chain.execution}
		facts.Snapshots = chain.snapshots
		facts.Journals = chain.journals
	}
	return facts
}

func TestEvaluateALegProviderPendingNoEvidence(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	for _, outcome := range []LegOutcome{LegOutcomeWinner, LegOutcomeFailed, LegOutcomeLoser, LegOutcomeCanceled} {
		verdict, issues, err := EvaluateALegProviderLeg(scope, alegPVFacts("b-1", outcome, nil))
		require.NoError(t, err)
		require.Equal(t, ALegProviderPending, verdict.Status, "outcome %q with no evidence stays pending", outcome)
		require.Len(t, issues, 1)
		require.Equal(t, ALegProviderIssuePending, issues[0].Code)
		require.Empty(t, verdict.ZeroBasis)
	}
}

func TestEvaluateALegProviderPendingFreshWork(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	facts := alegPVFacts("b-1", LegOutcomeNeverStarted, nil)
	facts.WorkPending = true
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderPending, verdict.Status)
	require.Len(t, issues, 1)
	require.Equal(t, ALegProviderIssuePending, issues[0].Code)
}

func TestEvaluateALegProviderPendingDeferredWorkOverHead(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	chain := alegPVChainTwo(t, scope, "b-1")
	facts := alegPVFacts("b-1", LegOutcomeWinner, &chain)
	facts.WorkPending = true
	verdict, _, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderPending, verdict.Status,
		"deferred work takes precedence even over a current head")
}

func TestEvaluateALegProviderPendingRevisionOverHead(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	chain := alegPVChainTwo(t, scope, "b-1")
	facts := alegPVFacts("b-1", LegOutcomeWinner, &chain)
	facts.RevisionPending = true
	verdict, _, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderPending, verdict.Status,
		"an in-flight newer revision takes precedence over the older head")
}

func TestEvaluateALegProviderKnownNonzero(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	chain := alegPVChainTwo(t, scope, "b-1")
	facts := alegPVFacts("b-1", LegOutcomeWinner, &chain)
	// Input-order independence: reversed journal order must not matter.
	facts.Journals[0], facts.Journals[1] = facts.Journals[1], facts.Journals[0]

	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnown, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, int64(40), verdict.Cost.Nano)
	require.Equal(t, "USD", verdict.Cost.Currency)
	require.Equal(t, chain.head.LastOperationKey, verdict.OperationKey)
	require.Equal(t, chain.head.LastTransactionID, verdict.TransactionID)
	require.Empty(t, verdict.ZeroBasis)
}

func TestEvaluateALegProviderKnownSingleRevision(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	chain := alegPVChainTwo(t, scope, "b-1")
	chain.journals = chain.journals[:1]
	chain.snapshots = chain.snapshots[:1]
	chain.head.EvidenceRevision, chain.head.HeadVersion, chain.head.Fence = 1, 1, 1
	chain.head.ValuationID = "val-1"
	chain.head.CurrentAmount = Money{Nano: 50, Currency: "USD"}
	chain.head.LastOperationKey, chain.head.LastTransactionID = chain.journals[0].ID, chain.journals[0].ID
	chain.fence.EvidenceRevision, chain.fence.Fence = 1, 1
	chain.fence.Amount = Money{Nano: 50, Currency: "USD"}
	chain.fence.LastOperationKey, chain.fence.LastTransactionID = chain.journals[0].ID, chain.journals[0].ID
	chain.execution.OwnerRevision = 1
	chain.execution.LastOperationKey, chain.execution.LastTransactionID = chain.journals[0].ID, chain.journals[0].ID
	facts := alegPVFacts("b-1", LegOutcomeFailed, &chain)

	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnown, verdict.Status, "failed attempts contribute when authority is complete")
	require.Equal(t, int64(50), verdict.Cost.Nano)
	require.Empty(t, issues)
}

func TestEvaluateALegProviderKnownZeroRecorded(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	chain := alegPVChainTwo(t, scope, "b-1")
	// Payer correction reverses the full 50 via a second 50 delta.
	j1, j2 := chain.journals[0], chain.journals[1]
	j2.Entries[0].Amount, j2.Entries[1].Amount = Money{Nano: 50, Currency: "USD"}, Money{Nano: 50, Currency: "USD"}
	resealed, err := j2.Seal()
	require.NoError(t, err)
	chain.journals[1] = resealed
	chain.snapshots[1] = alegPVSnapshot(scope, resealed, "rev-src-2")
	chain.head.CurrentAmount = Money{Nano: 0, Currency: "USD"}
	chain.fence.Amount = Money{Nano: 0, Currency: "USD"}
	facts := alegPVFacts("b-1", LegOutcomeWinner, &chain)
	_ = j1

	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnownZero, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, ALegProviderZeroRecorded, verdict.ZeroBasis)
	require.Zero(t, verdict.Cost.Nano)
}

func TestEvaluateALegProviderKnownZeroExcluded(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	legKey := alegPVLegKey(t, scope, "b-1")
	facts := ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		PostingFences: []ALegProviderFence{{
			StoreID:    scope.StoreID,
			LineageKey: legKey, Authority: "revision", HeadKey: "head-excluded",
			EvidenceRevision: 1, InputSetHash: strings.Repeat("d", 64), Fingerprint: "fp-excl",
			Amount: Money{Nano: 0, Currency: "USD"}, Fence: 1,
			LastOperationKey: alegPVOpKey(t, scope, "excl"),
		}},
		ExecutionFences: []ALegProviderExecutionFence{{
			StoreID:    scope.StoreID,
			LineageKey: legKey, Authority: "revision",
			OwnerSubjectKind: string(metering.SubjectBLeg), OwnerHeadKey: "head-excluded",
			OwnerRevision: 1, OwnerInputSetHash: strings.Repeat("d", 64), OwnerFingerprint: "fp-excl",
			Fence: 1, LastOperationKey: alegPVOpKey(t, scope, "excl"),
		}},
	}
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnownZero, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, ALegProviderZeroExcluded, verdict.ZeroBasis)
}

func TestEvaluateALegProviderKnownZeroOutcome(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	for _, tc := range []struct {
		outcome LegOutcome
		basis   string
	}{
		{LegOutcomeNeverStarted, ALegProviderZeroNeverStarted},
		{LegOutcomeRejected, ALegProviderZeroRejected},
	} {
		verdict, issues, err := EvaluateALegProviderLeg(scope, ALegProviderLegFacts{
			BLegID: "b-1", Outcome: tc.outcome,
		})
		require.NoError(t, err)
		require.Equal(t, ALegProviderKnownZero, verdict.Status)
		require.Empty(t, issues)
		require.Equal(t, tc.basis, verdict.ZeroBasis, "exact zero basis must be captured")
	}
}

func TestEvaluateALegProviderLegacyStaysPending(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	legKey := alegPVLegKey(t, scope, "b-1")
	legacySource, err := ProviderCostSourceKey(legKey)
	require.NoError(t, err)
	legacyOp := ScopedOperationKey("provider_call_cogs", scope.AccountID, legacySource)
	journal := alegPVJournal(t, scope, legacyOp, "b-1", "inference_provider_cogs", "provider_payable_clearing", 30, 5, legKey, "")
	facts := ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		PostingFences: []ALegProviderFence{{
			LineageKey: legKey, Authority: "legacy", HeadKey: legKey,
			EvidenceRevision: 1, InputSetHash: strings.Repeat("e", 64), Fingerprint: "fp-leg",
			Amount: Money{Nano: 30, Currency: "USD"}, Fence: 1,
			LastOperationKey: legacyOp, OriginalTransactionID: legacyOp, LastTransactionID: legacyOp,
		}},
		Snapshots: []ALegProviderSnapshot{alegPVSnapshot(scope, journal, legKey)},
		Journals:  []JournalTransaction{journal},
	}
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderPending, verdict.Status,
		"legacy aggregates never resolve known without revision cutover")
	require.Len(t, issues, 1)
	require.Equal(t, ALegProviderIssuePending, issues[0].Code)
}

func TestEvaluateALegProviderAdversarial(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	setup := func(t *testing.T) (ALegAuthorityScope, ALegProviderLegFacts) {
		t.Helper()
		chain := alegPVChainTwo(t, scope, "b-1")
		facts := alegPVFacts("b-1", LegOutcomeWinner, &chain)
		return scope, facts
	}
	for _, tc := range []struct {
		name       string
		wantStatus ALegLegProviderStatus
		wantIssue  string
		mutate     func(t *testing.T, facts *ALegProviderLegFacts)
	}{
		{name: "ambiguous heads", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			dup := facts.Heads[0]
			dup.HeadKey = "head-other"
			facts.Heads = append(facts.Heads, dup)
		}},
		{name: "head subject bleg mismatch", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].Subject.BLegID = "b-other"
		}},
		{name: "head subject aleg mismatch", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].Subject.ALegID = "a-other"
		}},
		{name: "head subject call mismatch", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].Subject.BillingCallID = "bc_00000000000000000000000000000000"
		}},
		{name: "head currency mismatch", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].CurrentAmount.Currency = "EUR"
		}},
		{name: "head revision zero", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].EvidenceRevision = 0
		}},
		{name: "head valuation empty", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].ValuationID = ""
		}},
		{name: "head hash empty", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].InputSetHash = ""
		}},
		{name: "head version fence drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].Fence++
		}},
		{name: "missing posting fence", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.PostingFences = nil
		}},
		{name: "legacy fence with head", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.PostingFences[0].Authority = "legacy"
		}},
		{name: "fence revision drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.PostingFences[0].EvidenceRevision = 1
		}},
		{name: "fence hash drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.PostingFences[0].InputSetHash = strings.Repeat("f", 64)
		}},
		{name: "fence amount drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.PostingFences[0].Amount.Nano = 41
		}},
		{name: "fence head key drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.PostingFences[0].HeadKey = "head-other"
		}},
		{name: "fence operation drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.PostingFences[0].LastOperationKey = "op-other"
		}},
		{name: "missing execution fence", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences = nil
		}},
		{name: "execution owner kind drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].OwnerSubjectKind = string(metering.SubjectProviderCharge)
		}},
		{name: "execution owner head drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].OwnerHeadKey = "head-other"
		}},
		{name: "foreign execution authority", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].Authority = "bogus"
		}},
		{name: "stale execution owner revision", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].OwnerRevision = 1
		}},
		{name: "stale execution owner hash", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].OwnerInputSetHash = strings.Repeat("b", 64)
		}},
		{name: "stale execution owner fingerprint", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].OwnerFingerprint = "fp-stale"
		}},
		{name: "stale execution owner operation", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].LastOperationKey = "op-stale"
		}},
		{name: "stale execution owner transaction", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].LastTransactionID = "tx-stale"
		}},
		{name: "execution fence zero", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].Fence = 0
		}},
		{name: "legacy execution authority with head", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].Authority = "legacy"
		}},
		{name: "head subject kind drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].SubjectKind = string(metering.SubjectProviderCharge)
		}},
		{name: "head subject id drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].SubjectID = "b-other"
		}},
		{name: "head subject aleg missing", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].Subject.ALegID = ""
		}},
		{name: "head subject call missing", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].Subject.BillingCallID = ""
			facts.Heads[0].Subject.CallID = ""
		}},
		{name: "head subject account missing", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].Subject.AccountID = ""
		}},
		{name: "root original transaction drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].OriginalTransactionID = "tx-other"
		}},
		{name: "head last transaction drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].LastTransactionID = "tx-other"
		}},
		{name: "diverged current operation without snapshot", wantStatus: ALegProviderPending, wantIssue: ALegProviderIssuePending, mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].LastOperationKey = alegPVOpKey(t, scope, "r3")
			facts.PostingFences[0].LastOperationKey = facts.Heads[0].LastOperationKey
			facts.ExecutionFences[0].LastOperationKey = facts.Heads[0].LastOperationKey
		}},
		{name: "journal without lineage", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			stray := alegPVJournal(t, scope, alegPVOpKey(t, scope, "stray"), "b-1",
				"inference_provider_cogs", "provider_payable_clearing", 5, 9, "head-stray", "")
			facts.Journals = append(facts.Journals, stray)
			facts.Snapshots = append(facts.Snapshots, alegPVSnapshot(scope, stray, "rev-src-stray"))
		}},
		{name: "duplicate source key", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			dup := facts.Journals[0]
			dup.ID = "tx-duplicate-id"
			dup.AccountSequence = 9
			resealed, err := dup.Seal()
			require.NoError(t, err)
			facts.Journals = append(facts.Journals, resealed)
			facts.Snapshots = append(facts.Snapshots, alegPVSnapshot(scope, resealed, "rev-src-1"))
		}},
		{name: "competing same revision", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			competing := alegPVJournal(t, scope, alegPVOpKey(t, scope, "competing"), "b-1",
				"inference_provider_cogs", "provider_payable_clearing", 15, 9, facts.Heads[0].HeadKey, "")
			facts.Journals = append(facts.Journals, competing)
			facts.Snapshots = append(facts.Snapshots, alegPVSnapshot(scope, competing, "rev-src-competing"))
		}},
		{name: "unrelated empty aleg journal", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			unrelated := alegPVJournal(t, scope, "tx-unrelated", "b-1",
				"inference_provider_cogs", "provider_payable_clearing", 5, 9, "head-ghost", "")
			unrelated.ALegID = ""
			resealed, err := unrelated.Seal()
			require.NoError(t, err)
			facts.Journals = append(facts.Journals, resealed)
		}},
		{name: "correction group drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			journal := facts.Journals[1]
			journal.CorrectionGroupID = "tx-other"
			resealed, err := journal.Seal()
			require.NoError(t, err)
			facts.Journals[1] = resealed
			facts.Snapshots[1] = alegPVSnapshot(scope, resealed, "rev-src-2")
		}},
		{name: "broken correction link", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Journals[1].ReversalOf = "tx-missing"
			facts.Journals[1].CorrectsTransactionID = "tx-missing"
			resealed, err := facts.Journals[1].Seal()
			require.NoError(t, err)
			facts.Journals[1] = resealed
			facts.Snapshots[1] = alegPVSnapshot(scope, resealed, "rev-src-2")
		}},
		{name: "branched correction chain", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			branch := alegPVJournal(t, scope, alegPVOpKey(t, scope, "branch"), "b-1",
				"inference_provider_cogs", "provider_payable_clearing", 5, 9, facts.Heads[0].HeadKey, facts.Journals[0].ID)
			// The group contract holds so only the branch ambiguity remains.
			resealed, err := branch.Seal()
			require.NoError(t, err)
			branch = resealed
			facts.Journals = append(facts.Journals, branch)
			facts.Snapshots = append(facts.Snapshots, alegPVSnapshot(scope, branch, "rev-src-branch"))
		}},
		{name: "telescope mismatch", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Heads[0].CurrentAmount.Nano = 41
		}},
		{name: "journal aleg mismatch", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			journal := facts.Journals[0]
			journal.ALegID = "a-other"
			resealed, err := journal.Seal()
			require.NoError(t, err)
			facts.Journals[0] = resealed
			facts.Snapshots[0] = alegPVSnapshot(scope, resealed, "rev-src-1")
		}},
		{name: "journal bleg mismatch", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			journal := facts.Journals[1]
			journal.BLegID = "b-other"
			resealed, err := journal.Seal()
			require.NoError(t, err)
			// Re-keyed outside the leg scope: the evaluator must not consume it,
			// so the chain breaks at the missing successor.
			facts.Journals[1] = resealed
		}},
		{name: "journal currency mismatch", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			journal := facts.Journals[0]
			journal.Currency = "EUR"
			for i := range journal.Entries {
				journal.Entries[i].Amount.Currency = "EUR"
			}
			resealed, err := journal.Seal()
			require.NoError(t, err)
			facts.Journals[0] = resealed
			facts.Snapshots[0] = alegPVSnapshot(scope, resealed, "rev-src-1")
		}},
		{name: "journal book conflict", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Journals[0].Book = "authorization"
			facts.Journals[0].SemanticFingerprint = ""
		}},
		{name: "journal shape drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			journal := facts.Journals[0]
			journal.Entries[1].LedgerAccount = "usage_revenue"
			resealed, err := journal.Seal()
			require.NoError(t, err)
			facts.Journals[0] = resealed
			facts.Snapshots[0] = alegPVSnapshot(scope, resealed, "rev-src-1")
		}},
		{name: "journal fingerprint drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Journals[0].SemanticFingerprint = "fp-drift"
		}},
		{name: "snapshot integrity drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Snapshots[0].IntegrityFingerprint = "snapshot:v1:drift"
		}},
		{name: "snapshot sequence drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Snapshots[1].SequenceEnd++
		}},
		{name: "snapshot balance drift", mutate: func(t *testing.T, facts *ALegProviderLegFacts) {
			facts.Snapshots[0].After.BalanceNano++
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope, facts := setup(t)
			tc.mutate(t, &facts)
			verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
			require.NoError(t, err)
			wantStatus, wantIssue := tc.wantStatus, tc.wantIssue
			if wantStatus == "" {
				wantStatus = ALegProviderUnknown
			}
			if wantIssue == "" {
				wantIssue = ALegProviderIssueUnresolved
			}
			require.Equal(t, wantStatus, verdict.Status, "fail-closed, never known or zero")
			require.NotEqual(t, ALegProviderKnownZero, verdict.Status)
			require.Empty(t, verdict.ZeroBasis)
			codes := make([]string, 0, len(issues))
			for _, issue := range issues {
				codes = append(codes, issue.Code)
			}
			require.Contains(t, codes, wantIssue)
		})
	}
}

func TestEvaluateALegProviderPendingMissingSnapshot(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	chain := alegPVChainTwo(t, scope, "b-1")
	facts := alegPVFacts("b-1", LegOutcomeWinner, &chain)
	facts.Snapshots = facts.Snapshots[1:]
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderPending, verdict.Status, "absent evidence stays pending")
	require.Len(t, issues, 1)
	require.Equal(t, ALegProviderIssuePending, issues[0].Code)
}

func TestEvaluateALegProviderTelescopeOverflowFailsQuery(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	chain := alegPVChainTwo(t, scope, "b-1")
	const maxNano = int64(1<<63 - 1)
	chain.journals[0].Entries[0].Amount.Nano, chain.journals[0].Entries[1].Amount.Nano = maxNano, maxNano
	resealed, err := chain.journals[0].Seal()
	require.NoError(t, err)
	chain.journals[0] = resealed
	chain.snapshots[0] = alegPVSnapshot(scope, resealed, "rev-src-1")
	// A second positive delta overflows the running total before any
	// telescoping comparison can succeed.
	plus := alegPVJournal(t, scope, alegPVOpKey(t, scope, "plus"), "b-1",
		"inference_provider_cogs", "provider_payable_clearing", 20, 9, chain.head.HeadKey, chain.journals[1].ID)
	chain.journals = append(chain.journals, plus)
	chain.snapshots = append(chain.snapshots, alegPVSnapshot(scope, plus, "rev-src-plus"))
	chain.head.CurrentAmount.Nano = maxNano
	chain.fence.Amount.Nano = maxNano
	facts := alegPVFacts("b-1", LegOutcomeWinner, &chain)
	_, _, err = EvaluateALegProviderLeg(scope, facts)
	require.ErrorIs(t, err, ErrMoneyOverflow)
}

func TestEvaluateALegProviderChildSubjectKnown(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	chain := alegPVChainTwo(t, scope, "b-1")
	chain.head.Subject.Kind = metering.SubjectProviderCharge
	chain.head.Subject.ProviderChargeID = "charge-7"
	chain.head.Subject.ProviderAccountKey = "provider-acct"
	chain.head.SubjectKind = string(metering.SubjectProviderCharge)
	chain.head.SubjectID = "charge-7"
	chain.execution.OwnerSubjectKind = string(metering.SubjectProviderCharge)
	// A provider-charge child owns its own posting lineage while the
	// execution fence stays per B-leg: mirror the writer lineage keys.
	legKey := alegPVLegKey(t, scope, "b-1")
	chain.fence.LineageKey = legKey + ":provider-charge:charge-7"
	facts := alegPVFacts("b-1", LegOutcomeWinner, &chain)
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnown, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, int64(40), verdict.Cost.Nano)
}

// alegPVZeroSnapshot builds the journal-less current-operation proof
// for a zero-delta revision: the snapshot keyed by the head's current
// operation carries the fence fingerprint, zero sequence, and
// identical balance snapshots.
func alegPVZeroSnapshot(t *testing.T, scope ALegAuthorityScope, operationKey, source, fingerprint string) ALegProviderSnapshot {
	t.Helper()
	before := AccountSnapshot{BalanceNano: 7000, SpendableNano: 7000, Mode: AccountPrepaid, Currency: scope.Currency, Version: 5}
	return ALegProviderSnapshot{
		AccountID: scope.AccountID, OperationKind: "provider_call_cogs",
		OperationKey: operationKey, SourceKey: source, Fingerprint: fingerprint,
		IntegrityFingerprint: alegMarkerIntegrity(operationKey, scope.AccountID, "provider_call_cogs",
			source, fingerprint, before, before, 0, 0),
		Currency: scope.Currency, Mode: string(AccountPrepaid),
		Before: before, After: before,
		SequenceStart: 0, SequenceEnd: 0,
	}
}

// alegPVRevisionSource derives the test-expected canonical revision
// source through the unit under proof. Derivation correctness itself
// is pinned independently by TestAlegProviderRevisionSourceMatchesWriter.
func alegPVRevisionSource(t *testing.T, scope ALegAuthorityScope, headKey string, revision uint64, inputHash string) string {
	t.Helper()
	callID, err := ParseBillingCallID(scope.CallID)
	require.NoError(t, err)
	source, err := alegProviderRevisionSource(scope.AccountID, callID, headKey, revision, inputHash)
	require.NoError(t, err)
	return source
}

// TestEvaluateALegProviderSameAmountRevisionKnown proves a same-amount
// later revision resolves known with the new current operation and the
// last monetary transaction separately: the chain still ends at the
// prior journal while the zero-delta snapshot proves the new operation.
// It characterizes the writer's zero-delta shape rather than manufacturing
// a behavioral RED.
func TestEvaluateALegProviderSameAmountRevisionKnown(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	chain := alegPVChainTwo(t, scope, "b-1")
	chain.journals = chain.journals[:1]
	chain.snapshots = chain.snapshots[:1]
	op2 := alegPVOpKey(t, scope, "r2same")
	chain.head.EvidenceRevision = 2
	chain.head.InputSetHash = strings.Repeat("d", 64)
	chain.head.ValuationID = "val-2same"
	chain.head.CurrentAmount = Money{Nano: 50, Currency: "USD"}
	chain.head.HeadVersion, chain.head.Fence = 2, 2
	chain.head.LastOperationKey = op2
	chain.head.LastTransactionID = chain.journals[0].ID
	chain.fence.EvidenceRevision = 2
	chain.fence.InputSetHash = strings.Repeat("d", 64)
	chain.fence.Fingerprint = "fp-fence-2same"
	chain.fence.Amount = Money{Nano: 50, Currency: "USD"}
	chain.fence.Fence = 2
	chain.fence.LastOperationKey = op2
	chain.fence.LastTransactionID = chain.journals[0].ID
	chain.execution.OwnerRevision = 2
	chain.execution.OwnerInputSetHash = strings.Repeat("d", 64)
	chain.execution.OwnerFingerprint = "fp-fence-2same"
	chain.execution.Fence = 3
	chain.execution.LastOperationKey = op2
	chain.execution.LastTransactionID = chain.journals[0].ID
	facts := alegPVFacts("b-1", LegOutcomeWinner, &chain)
	facts.Snapshots = append(facts.Snapshots, alegPVZeroSnapshot(t, scope, op2,
		alegPVRevisionSource(t, scope, alegPVHeadKey("b-1"), 2, strings.Repeat("d", 64)), "fp-fence-2same"))
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnown, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, int64(50), verdict.Cost.Nano)
	require.Equal(t, op2, verdict.OperationKey)
	require.Equal(t, chain.journals[0].ID, verdict.TransactionID,
		"no transaction is fabricated for a no-journal revision")
}

// TestEvaluateALegProviderFirstZeroKnown proves a first writer-recorded
// payable zero — head, fences, and operation snapshot with no monetary
// journal — reports known_zero with the recorded basis.
func TestEvaluateALegProviderFirstZeroKnown(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	subject := alegPVSubject(scope, "b-1")
	headKey := alegPVHeadKey("b-1")
	legKey := alegPVLegKey(t, scope, "b-1")
	op1 := alegPVOpKey(t, scope, "z1")
	facts := ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		Heads: []ALegProviderHead{{
			StoreID:   scope.StoreID,
			AccountID: scope.AccountID, CallID: scope.CallID, HeadKey: headKey,
			SubjectKind: string(metering.SubjectBLeg), SubjectID: "b-1", Subject: subject,
			EvidenceRevision: 1, InputSetHash: strings.Repeat("e", 64), ValuationID: "val-z1",
			CurrentAmount: Money{Nano: 0, Currency: scope.Currency},
			HeadVersion:   1, Fence: 1,
			LastOperationKey: op1, OriginalTransactionID: "", LastTransactionID: "",
		}},
		PostingFences: []ALegProviderFence{{
			StoreID:    scope.StoreID,
			LineageKey: legKey, Authority: "revision", HeadKey: headKey,
			EvidenceRevision: 1, InputSetHash: strings.Repeat("e", 64), Fingerprint: "fp-fence-z1",
			Amount: Money{Nano: 0, Currency: scope.Currency}, Fence: 1,
			LastOperationKey: op1, OriginalTransactionID: "", LastTransactionID: "",
		}},
		ExecutionFences: []ALegProviderExecutionFence{{
			StoreID:    scope.StoreID,
			LineageKey: legKey, Authority: "revision",
			OwnerSubjectKind: string(metering.SubjectBLeg), OwnerHeadKey: headKey,
			OwnerRevision: 1, OwnerInputSetHash: strings.Repeat("e", 64), OwnerFingerprint: "fp-fence-z1",
			Fence: 2, LastOperationKey: op1, LastTransactionID: "",
		}},
		Snapshots: []ALegProviderSnapshot{alegPVZeroSnapshot(t, scope, op1,
			alegPVRevisionSource(t, scope, headKey, 1, strings.Repeat("e", 64)), "fp-fence-z1")},
	}
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnownZero, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, ALegProviderZeroRecorded, verdict.ZeroBasis)
}

// TestEvaluateALegProviderFirstZeroMissingSnapshotPending proves an
// incomplete first zero stays pending rather than resolving or failing.
func TestEvaluateALegProviderFirstZeroMissingSnapshotPending(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	subject := alegPVSubject(scope, "b-1")
	headKey := alegPVHeadKey("b-1")
	legKey := alegPVLegKey(t, scope, "b-1")
	op1 := alegPVOpKey(t, scope, "z1")
	facts := ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		Heads: []ALegProviderHead{{
			StoreID:   scope.StoreID,
			AccountID: scope.AccountID, CallID: scope.CallID, HeadKey: headKey,
			SubjectKind: string(metering.SubjectBLeg), SubjectID: "b-1", Subject: subject,
			EvidenceRevision: 1, InputSetHash: strings.Repeat("e", 64), ValuationID: "val-z1",
			CurrentAmount: Money{Nano: 0, Currency: scope.Currency},
			HeadVersion:   1, Fence: 1,
			LastOperationKey: op1, OriginalTransactionID: "", LastTransactionID: "",
		}},
		PostingFences: []ALegProviderFence{{
			StoreID:    scope.StoreID,
			LineageKey: legKey, Authority: "revision", HeadKey: headKey,
			EvidenceRevision: 1, InputSetHash: strings.Repeat("e", 64), Fingerprint: "fp-fence-z1",
			Amount: Money{Nano: 0, Currency: scope.Currency}, Fence: 1,
			LastOperationKey: op1, OriginalTransactionID: "", LastTransactionID: "",
		}},
		ExecutionFences: []ALegProviderExecutionFence{{
			StoreID:    scope.StoreID,
			LineageKey: legKey, Authority: "revision",
			OwnerSubjectKind: string(metering.SubjectBLeg), OwnerHeadKey: headKey,
			OwnerRevision: 1, OwnerInputSetHash: strings.Repeat("e", 64), OwnerFingerprint: "fp-fence-z1",
			Fence: 2, LastOperationKey: op1, LastTransactionID: "",
		}},
	}
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderPending, verdict.Status)
	require.Len(t, issues, 1)
	require.Equal(t, ALegProviderIssuePending, issues[0].Code)
}

// TestEvaluateALegProviderLegacyAdoptedChainKnown proves a legacy
// aggregate root adopted by a revision delta resolves known: the delta
// inherits the legacy root's group and the chain telescopes from zero.
func TestEvaluateALegProviderLegacyAdoptedChainKnown(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	subject := alegPVSubject(scope, "b-1")
	headKey := alegPVHeadKey("b-1")
	legKey := alegPVLegKey(t, scope, "b-1")
	legacySource, err := ProviderCostSourceKey(legKey)
	require.NoError(t, err)
	legacyOp := ScopedOperationKey("provider_call_cogs", scope.AccountID, legacySource)
	op2 := alegPVOpKey(t, scope, "r2cut")
	legacy := alegPVJournal(t, scope, legacyOp, "b-1", "inference_provider_cogs", "provider_payable_clearing", 30, 5, legKey, "")
	delta := alegPVJournal(t, scope, op2, "b-1", "inference_provider_cogs", "provider_payable_clearing", 10, 8, legKey, legacyOp)
	facts := ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		Heads: []ALegProviderHead{{
			StoreID:   scope.StoreID,
			AccountID: scope.AccountID, CallID: scope.CallID, HeadKey: headKey,
			SubjectKind: string(metering.SubjectBLeg), SubjectID: "b-1", Subject: subject,
			EvidenceRevision: 2, InputSetHash: strings.Repeat("f", 64), ValuationID: "val-2cut",
			CurrentAmount: Money{Nano: 40, Currency: scope.Currency},
			HeadVersion:   2, Fence: 2,
			LastOperationKey: op2, OriginalTransactionID: legacyOp, LastTransactionID: op2,
		}},
		PostingFences: []ALegProviderFence{{
			StoreID:    scope.StoreID,
			LineageKey: legKey, Authority: "revision", HeadKey: headKey,
			EvidenceRevision: 2, InputSetHash: strings.Repeat("f", 64), Fingerprint: "fp-fence-cut",
			Amount: Money{Nano: 40, Currency: scope.Currency}, Fence: 2,
			LastOperationKey: op2, OriginalTransactionID: legacyOp, LastTransactionID: op2,
		}},
		ExecutionFences: []ALegProviderExecutionFence{{
			StoreID:    scope.StoreID,
			LineageKey: legKey, Authority: "revision",
			OwnerSubjectKind: string(metering.SubjectBLeg), OwnerHeadKey: headKey,
			OwnerRevision: 2, OwnerInputSetHash: strings.Repeat("f", 64), OwnerFingerprint: "fp-fence-cut",
			Fence: 3, LastOperationKey: op2, LastTransactionID: op2,
		}},
		Snapshots: []ALegProviderSnapshot{
			alegPVSnapshot(scope, legacy, legKey),
			alegPVSnapshot(scope, delta, "rev-src-2cut"),
		},
		Journals: []JournalTransaction{legacy, delta},
	}
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnown, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, int64(40), verdict.Cost.Nano)
	require.Equal(t, op2, verdict.OperationKey)
	require.Equal(t, op2, verdict.TransactionID)
}

// TestAlegProviderRevisionSourceMatchesWriter pins the report-side
// revision-source derivation to the exact writer derivation: a valid
// revision input through ProviderCostRevisionSourceKey must equal the
// digest over its normalized identity fields.
func TestAlegProviderRevisionSourceMatchesWriter(t *testing.T) {
	t.Parallel()
	scope, callID := alegPVScope(t)
	subject := alegPVSubject(scope, "b-1")
	amountDecimal := metering.DecimalFromNanoUnits(10)
	input := ProviderCostRevisionInput{
		AccountID: scope.AccountID, CallID: callID, Subject: subject, HeadKey: "head-src-check",
		EvidenceRevision: 3, InputSetHash: strings.Repeat("a", 64), ValuationID: "val-src-check",
		Cost: OperatorCOGSResult{
			KnownSubtotalByCurrency: map[string]Money{"USD": {Nano: 10, Currency: "USD"}},
			KnownSubtotal:           Money{Nano: 10, Currency: "USD"},
			Completeness:            CostCompletenessKnown,
			Payable:                 true,
			IncludedLegKeys:         []string{"b-1"},
		},
		Authoritative: true,
		Evidence: economics.PostUsageRatingInput{
			Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
			Subject: subject, Scope: "src-check", Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator},
			Observations: []metering.Observation{{
				Version: metering.ObservationVersionV2, ID: "src-check-charge",
				SourceEventKey: "src-check-charge", Revision: 3,
				StreamID: "src-check-stream", Sequence: 3, Origin: metering.OriginProvider,
				Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
				Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
				Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
				Correlation: metering.CorrelationV2{
					StoreID: subject.StoreID, ALegID: subject.ALegID,
					BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
				},
				Semantics: metering.SemanticsCumulative, ObservedAt: time.Unix(43, 0).UTC(),
				ReceivedAt: time.Unix(43, 0).UTC(), MappingRef: "src.check",
				Charges: []metering.ReportedCharge{{
					ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate,
					Amount: &amountDecimal, Currency: "USD", Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator},
				}},
			}},
			Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "src-check-rater", Version: "v1"}, RaterID: "reference"},
		},
	}
	writerSource, err := ProviderCostRevisionSourceKey(input)
	require.NoError(t, err)
	derived, err := alegProviderRevisionSource(scope.AccountID, callID, "head-src-check", 3, strings.Repeat("a", 64))
	require.NoError(t, err)
	require.Equal(t, writerSource, derived)
	for _, tc := range []struct {
		name      string
		accountID string
		headKey   string
		revision  uint64
		inputHash string
	}{
		{name: "empty account", accountID: "", headKey: "head-src-check", revision: 3, inputHash: strings.Repeat("a", 64)},
		{name: "empty head", accountID: scope.AccountID, headKey: "", revision: 3, inputHash: strings.Repeat("a", 64)},
		{name: "zero revision", accountID: scope.AccountID, headKey: "head-src-check", revision: 0, inputHash: strings.Repeat("a", 64)},
		{name: "empty hash", accountID: scope.AccountID, headKey: "head-src-check", revision: 3, inputHash: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := alegProviderRevisionSource(tc.accountID, callID, tc.headKey, tc.revision, tc.inputHash)
			require.ErrorIs(t, err, ErrProviderCostRevisionInvalid)
		})
	}
}

// TestEvaluateALegProviderFirstZeroHardening proves malformed current
// zero snapshots stay unresolved: empty/foreign/wrong-revision source,
// nonzero sequence, and balance movement each fail the proof.
func TestEvaluateALegProviderFirstZeroHardening(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	setup := func(t *testing.T) (ALegAuthorityScope, ALegProviderLegFacts) {
		t.Helper()
		subject := alegPVSubject(scope, "b-1")
		headKey := alegPVHeadKey("b-1")
		legKey := alegPVLegKey(t, scope, "b-1")
		op1 := alegPVOpKey(t, scope, "z1")
		facts := ALegProviderLegFacts{
			BLegID: "b-1", Outcome: LegOutcomeWinner,
			Heads: []ALegProviderHead{{
				StoreID:   scope.StoreID,
				AccountID: scope.AccountID, CallID: scope.CallID, HeadKey: headKey,
				SubjectKind: string(metering.SubjectBLeg), SubjectID: "b-1", Subject: subject,
				EvidenceRevision: 1, InputSetHash: strings.Repeat("e", 64), ValuationID: "val-z1",
				CurrentAmount: Money{Nano: 0, Currency: scope.Currency},
				HeadVersion:   1, Fence: 1,
				LastOperationKey: op1, OriginalTransactionID: "", LastTransactionID: "",
			}},
			PostingFences: []ALegProviderFence{{
				StoreID:    scope.StoreID,
				LineageKey: legKey, Authority: "revision", HeadKey: headKey,
				EvidenceRevision: 1, InputSetHash: strings.Repeat("e", 64), Fingerprint: "fp-fence-z1",
				Amount: Money{Nano: 0, Currency: scope.Currency}, Fence: 1,
				LastOperationKey: op1, OriginalTransactionID: "", LastTransactionID: "",
			}},
			ExecutionFences: []ALegProviderExecutionFence{{
				StoreID:    scope.StoreID,
				LineageKey: legKey, Authority: "revision",
				OwnerSubjectKind: string(metering.SubjectBLeg), OwnerHeadKey: headKey,
				OwnerRevision: 1, OwnerInputSetHash: strings.Repeat("e", 64), OwnerFingerprint: "fp-fence-z1",
				Fence: 2, LastOperationKey: op1, LastTransactionID: "",
			}},
			Snapshots: []ALegProviderSnapshot{alegPVZeroSnapshot(t, scope, op1,
				alegPVRevisionSource(t, scope, headKey, 1, strings.Repeat("e", 64)), "fp-fence-z1")},
		}
		return scope, facts
	}
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts)
	}{
		{name: "empty source", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			facts.Snapshots[0].SourceKey = ""
		}},
		{name: "foreign source", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			facts.Snapshots[0].SourceKey = "provider-cost-revision:v1:" + strings.Repeat("f", 64)
		}},
		{name: "wrong revision source", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			facts.Snapshots[0].SourceKey = alegPVRevisionSource(t, scope, alegPVHeadKey("b-1"), 2, strings.Repeat("e", 64))
		}},
		{name: "fingerprint drift", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			facts.Snapshots[0].Fingerprint = "fp-drift"
		}},
		{name: "nonzero sequence", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			facts.Snapshots[0].SequenceStart, facts.Snapshots[0].SequenceEnd = 1, 1
		}},
		{name: "balance movement", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			facts.Snapshots[0].After.BalanceNano++
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope, facts := setup(t)
			tc.mutate(t, scope, &facts)
			// Recompute integrity over the mutated shape so each
			// targeted rule fires distinctly instead of collapsing
			// into a digest mismatch: removing any single rule below
			// must resolve the drifted snapshot as known.
			snap := &facts.Snapshots[0]
			snap.IntegrityFingerprint = alegMarkerIntegrity(snap.OperationKey, scope.AccountID, "provider_call_cogs",
				snap.SourceKey, snap.Fingerprint, snap.Before, snap.After, snap.SequenceStart, snap.SequenceEnd)
			verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
			require.NoError(t, err)
			require.Equal(t, ALegProviderUnknown, verdict.Status)
			codes := make([]string, 0, len(issues))
			for _, issue := range issues {
				codes = append(codes, issue.Code)
			}
			require.Contains(t, codes, ALegProviderIssueUnresolved)
		})
	}
}

// alegPVChildChain builds one provider-charge child unit: head, fence,
// journal, and snapshot with writer-consistent lineage. Amounts are
// chosen by the caller; revisions default to 1.
func alegPVChildChain(t *testing.T, scope ALegAuthorityScope, bLegID, chargeID string, amount int64) (ALegProviderHead, ALegProviderFence, JournalTransaction, ALegProviderSnapshot) {
	t.Helper()
	subject := alegPVSubject(scope, bLegID)
	subject.Kind = metering.SubjectProviderCharge
	subject.ProviderChargeID = chargeID
	subject.ProviderAccountKey = "provider-acct"
	headKey := "head-" + chargeID
	legKey := alegPVLegKey(t, scope, bLegID)
	op := alegPVOpKey(t, scope, chargeID+"-r1")
	journal := alegPVJournal(t, scope, op, bLegID, "inference_provider_cogs", "provider_payable_clearing", amount, 7, headKey, "")
	head := ALegProviderHead{
		StoreID:   scope.StoreID,
		AccountID: scope.AccountID, CallID: scope.CallID, HeadKey: headKey,
		SubjectKind: string(metering.SubjectProviderCharge), SubjectID: chargeID, Subject: subject,
		EvidenceRevision: 1, InputSetHash: strings.Repeat("c", 64), ValuationID: "val-" + chargeID,
		CurrentAmount: Money{Nano: amount, Currency: scope.Currency},
		HeadVersion:   1, Fence: 1,
		LastOperationKey: op, OriginalTransactionID: op, LastTransactionID: op,
	}
	fence := ALegProviderFence{
		StoreID:    scope.StoreID,
		LineageKey: legKey + ":provider-charge:" + chargeID, Authority: "revision", HeadKey: headKey,
		EvidenceRevision: 1, InputSetHash: strings.Repeat("c", 64), Fingerprint: "fp-fence-" + chargeID,
		Amount: Money{Nano: amount, Currency: scope.Currency}, Fence: 1,
		LastOperationKey: op, OriginalTransactionID: op, LastTransactionID: op,
	}
	return head, fence, journal, alegPVSnapshot(scope, journal, "rev-src-"+chargeID)
}

func alegPVChildExecution(t *testing.T, scope ALegAuthorityScope, bLegID, headKey, op, tx string, revision uint64, hash, fp string, fence int64) ALegProviderExecutionFence {
	t.Helper()
	return ALegProviderExecutionFence{
		StoreID:    scope.StoreID,
		LineageKey: alegPVLegKey(t, scope, bLegID), Authority: "revision",
		OwnerSubjectKind: string(metering.SubjectProviderCharge), OwnerHeadKey: headKey,
		OwnerRevision: revision, OwnerInputSetHash: hash, OwnerFingerprint: fp,
		Fence: uint64(fence), LastOperationKey: op, LastTransactionID: tx,
	}
}

// TestEvaluateALegProviderTwoChildrenKnown proves two additive
// provider-charge children resolve known with the checked sum and
// exact per-child lineage in deterministic charge order.
func TestEvaluateALegProviderTwoChildrenKnown(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	headA, fenceA, journalA, snapA := alegPVChildChain(t, scope, "b-1", "charge-a", 30)
	headB, fenceB, journalB, snapB := alegPVChildChain(t, scope, "b-1", "charge-b", 20)
	facts := ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		Heads:           []ALegProviderHead{headA, headB},
		PostingFences:   []ALegProviderFence{fenceA, fenceB},
		ExecutionFences: []ALegProviderExecutionFence{alegPVChildExecution(t, scope, "b-1", "head-charge-b", journalB.ID, journalB.ID, 1, strings.Repeat("c", 64), "fp-fence-charge-b", 3)},
		Snapshots:       []ALegProviderSnapshot{snapA, snapB},
		Journals:        []JournalTransaction{journalA, journalB},
	}
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnown, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, int64(50), verdict.Cost.Nano)
	require.Equal(t, journalB.ID, verdict.OperationKey)
	require.Equal(t, journalB.ID, verdict.TransactionID)
	require.Len(t, verdict.Children, 2)
	require.Equal(t, "charge-a", verdict.Children[0].ChargeID)
	require.Equal(t, "charge-b", verdict.Children[1].ChargeID)
	require.Equal(t, int64(30), verdict.Children[0].Amount.Nano)
	require.Equal(t, int64(20), verdict.Children[1].Amount.Nano)
	require.Equal(t, journalA.ID, verdict.Children[0].OperationKey)
	require.Equal(t, journalA.ID, verdict.Children[0].TransactionID)
	require.Equal(t, uint64(1), verdict.Children[0].EvidenceRevision)
	require.Equal(t, "val-charge-a", verdict.Children[0].ValuationID)
	for _, child := range verdict.Children {
		require.Equal(t, ALegProviderKnown, child.Status)
		require.Empty(t, child.ZeroBasis)
	}
}

// TestEvaluateALegProviderChildrenPermutationStable proves input
// order never changes the aggregate or lineage ordering.
func TestEvaluateALegProviderChildrenPermutationStable(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	headA, fenceA, journalA, snapA := alegPVChildChain(t, scope, "b-1", "charge-a", 30)
	headB, fenceB, journalB, snapB := alegPVChildChain(t, scope, "b-1", "charge-b", 20)
	execution := alegPVChildExecution(t, scope, "b-1", "head-charge-b", journalB.ID, journalB.ID, 1, strings.Repeat("c", 64), "fp-fence-charge-b", 3)
	first, _, err := EvaluateALegProviderLeg(scope, ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		Heads: []ALegProviderHead{headA, headB}, PostingFences: []ALegProviderFence{fenceA, fenceB},
		ExecutionFences: []ALegProviderExecutionFence{execution},
		Snapshots:       []ALegProviderSnapshot{snapA, snapB}, Journals: []JournalTransaction{journalA, journalB},
	})
	require.NoError(t, err)
	second, _, err := EvaluateALegProviderLeg(scope, ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		Heads: []ALegProviderHead{headB, headA}, PostingFences: []ALegProviderFence{fenceB, fenceA},
		ExecutionFences: []ALegProviderExecutionFence{execution},
		Snapshots:       []ALegProviderSnapshot{snapB, snapA}, Journals: []JournalTransaction{journalB, journalA},
	})
	require.NoError(t, err)
	require.Equal(t, first, second)
}

// TestEvaluateALegProviderChildrenAdversarial proves fail-closed
// multi-child rules: partial pending, gate-owner mismatch, mixed
// modes, duplicate children, and overflow never expose a partial
// subtotal.
func TestEvaluateALegProviderChildrenAdversarial(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	setup := func(t *testing.T) (ALegAuthorityScope, ALegProviderLegFacts) {
		t.Helper()
		headA, fenceA, journalA, snapA := alegPVChildChain(t, scope, "b-1", "charge-a", 30)
		headB, fenceB, journalB, snapB := alegPVChildChain(t, scope, "b-1", "charge-b", 20)
		execution := alegPVChildExecution(t, scope, "b-1", "head-charge-b", journalB.ID, journalB.ID, 1, strings.Repeat("c", 64), "fp-fence-charge-b", 3)
		return scope, ALegProviderLegFacts{
			BLegID: "b-1", Outcome: LegOutcomeWinner,
			Heads: []ALegProviderHead{headA, headB}, PostingFences: []ALegProviderFence{fenceA, fenceB},
			ExecutionFences: []ALegProviderExecutionFence{execution},
			Snapshots:       []ALegProviderSnapshot{snapA, snapB}, Journals: []JournalTransaction{journalA, journalB},
		}
	}
	for _, tc := range []struct {
		name       string
		wantStatus ALegLegProviderStatus
		wantIssue  string
		mutate     func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts)
	}{
		{name: "pending sibling blocks subtotal", wantStatus: ALegProviderPending, wantIssue: ALegProviderIssuePending, mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			facts.Snapshots = facts.Snapshots[:1]
		}},
		{name: "gate owner missing from set", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].OwnerHeadKey = "head-ghost"
		}},
		{name: "gate owner wrong charge", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			facts.ExecutionFences[0].OwnerHeadKey = "head-charge-a"
			facts.ExecutionFences[0].LastOperationKey = facts.Journals[0].ID
			facts.ExecutionFences[0].LastTransactionID = facts.Journals[0].ID
		}},
		{name: "mixed aggregate and child", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			chain := alegPVChainTwo(t, scope, "b-1")
			facts.Heads = append(facts.Heads, chain.head)
		}},
		{name: "duplicate child identity", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			dup := facts.Heads[0]
			dup.HeadKey = "head-duplicate"
			facts.Heads = append(facts.Heads, dup)
		}},
		{name: "stray journal group", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegProviderLegFacts) {
			stray := alegPVJournal(t, scope, alegPVOpKey(t, scope, "stray"), "b-1",
				"inference_provider_cogs", "provider_payable_clearing", 5, 9, "head-ghost", "")
			facts.Journals = append(facts.Journals, stray)
			facts.Snapshots = append(facts.Snapshots, alegPVSnapshot(scope, stray, "rev-src-stray"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope, facts := setup(t)
			tc.mutate(t, scope, &facts)
			verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
			require.NoError(t, err)
			wantStatus, wantIssue := tc.wantStatus, tc.wantIssue
			if wantStatus == "" {
				wantStatus = ALegProviderUnknown
			}
			if wantIssue == "" {
				wantIssue = ALegProviderIssueUnresolved
			}
			require.Equal(t, wantStatus, verdict.Status)
			require.Empty(t, verdict.Children, "no partial lineage on a non-known leg")
			require.Zero(t, verdict.Cost.Nano, "no partial subtotal")
			codes := make([]string, 0, len(issues))
			for _, issue := range issues {
				codes = append(codes, issue.Code)
			}
			require.Contains(t, codes, wantIssue)
		})
	}
}

// TestEvaluateALegProviderChildrenOverflowFailsQuery proves checked
// child summation fails the query instead of wrapping the subtotal.
func TestEvaluateALegProviderChildrenOverflowFailsQuery(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	const maxNano = int64(1<<63 - 1)
	headA, fenceA, journalA, snapA := alegPVChildChain(t, scope, "b-1", "charge-a", maxNano)
	headB, fenceB, journalB, snapB := alegPVChildChain(t, scope, "b-1", "charge-b", 20)
	execution := alegPVChildExecution(t, scope, "b-1", "head-charge-b", journalB.ID, journalB.ID, 1, strings.Repeat("c", 64), "fp-fence-charge-b", 3)
	_, _, err := EvaluateALegProviderLeg(scope, ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		Heads: []ALegProviderHead{headA, headB}, PostingFences: []ALegProviderFence{fenceA, fenceB},
		ExecutionFences: []ALegProviderExecutionFence{execution},
		Snapshots:       []ALegProviderSnapshot{snapA, snapB}, Journals: []JournalTransaction{journalA, journalB},
	})
	require.ErrorIs(t, err, ErrMoneyOverflow)
}

// alegPVZeroChild builds one first-zero provider-charge child: head,
// fence, and current zero snapshot with no monetary journals.
func alegPVZeroChild(t *testing.T, scope ALegAuthorityScope, bLegID, chargeID string) (ALegProviderHead, ALegProviderFence, ALegProviderSnapshot) {
	t.Helper()
	subject := alegPVSubject(scope, bLegID)
	subject.Kind = metering.SubjectProviderCharge
	subject.ProviderChargeID = chargeID
	subject.ProviderAccountKey = "provider-acct"
	headKey := "head-" + chargeID
	legKey := alegPVLegKey(t, scope, bLegID)
	op := alegPVOpKey(t, scope, chargeID+"-z1")
	head := ALegProviderHead{
		StoreID:   scope.StoreID,
		AccountID: scope.AccountID, CallID: scope.CallID, HeadKey: headKey,
		SubjectKind: string(metering.SubjectProviderCharge), SubjectID: chargeID, Subject: subject,
		EvidenceRevision: 1, InputSetHash: strings.Repeat("e", 64), ValuationID: "val-" + chargeID,
		CurrentAmount: Money{Nano: 0, Currency: scope.Currency},
		HeadVersion:   1, Fence: 1,
		LastOperationKey: op, OriginalTransactionID: "", LastTransactionID: "",
	}
	fence := ALegProviderFence{
		StoreID:    scope.StoreID,
		LineageKey: legKey + ":provider-charge:" + chargeID, Authority: "revision", HeadKey: headKey,
		EvidenceRevision: 1, InputSetHash: strings.Repeat("e", 64), Fingerprint: "fp-fence-" + chargeID,
		Amount: Money{Nano: 0, Currency: scope.Currency}, Fence: 1,
		LastOperationKey: op, OriginalTransactionID: "", LastTransactionID: "",
	}
	return head, fence, alegPVZeroSnapshot(t, scope, op,
		alegPVRevisionSource(t, scope, headKey, 1, strings.Repeat("e", 64)), "fp-fence-"+chargeID)
}

// TestEvaluateALegProviderAllZeroChildrenKnownZero proves every
// evaluated payable child proven zero aggregates to known_zero with
// an explicit deterministic basis instead of known with a zero cost.
func TestEvaluateALegProviderAllZeroChildrenKnownZero(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	headA, fenceA, snapA := alegPVZeroChild(t, scope, "b-1", "charge-a")
	headB, fenceB, snapB := alegPVZeroChild(t, scope, "b-1", "charge-b")
	execution := alegPVChildExecution(t, scope, "b-1", "head-charge-b",
		headB.LastOperationKey, "", 1, strings.Repeat("e", 64), "fp-fence-charge-b", 3)
	facts := ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		Heads: []ALegProviderHead{headA, headB}, PostingFences: []ALegProviderFence{fenceA, fenceB},
		ExecutionFences: []ALegProviderExecutionFence{execution},
		Snapshots:       []ALegProviderSnapshot{snapA, snapB},
	}
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnownZero, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, ALegProviderZeroAllChildrenZero, verdict.ZeroBasis)
	require.Zero(t, verdict.Cost.Nano)
	require.Equal(t, headB.LastOperationKey, verdict.OperationKey)
	require.Empty(t, verdict.TransactionID)
	require.Len(t, verdict.Children, 2)
	for _, child := range verdict.Children {
		require.Equal(t, ALegProviderKnownZero, child.Status)
		require.Zero(t, child.Amount.Nano)
	}
}

// TestEvaluateALegProviderMixedZeroChildrenKnown proves mixed zero
// plus nonzero children resolve known with the correct sum and
// deterministic lineage.
func TestEvaluateALegProviderMixedZeroChildrenKnown(t *testing.T) {
	t.Parallel()
	scope, _ := alegPVScope(t)
	headA, fenceA, journalA, snapA := alegPVChildChain(t, scope, "b-1", "charge-a", 25)
	headZ, fenceZ, snapZ := alegPVZeroChild(t, scope, "b-1", "charge-b")
	execution := alegPVChildExecution(t, scope, "b-1", "head-charge-b",
		headZ.LastOperationKey, "", 1, strings.Repeat("e", 64), "fp-fence-charge-b", 3)
	facts := ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		Heads: []ALegProviderHead{headA, headZ}, PostingFences: []ALegProviderFence{fenceA, fenceZ},
		ExecutionFences: []ALegProviderExecutionFence{execution},
		Snapshots:       []ALegProviderSnapshot{snapA, snapZ}, Journals: []JournalTransaction{journalA},
	}
	verdict, issues, err := EvaluateALegProviderLeg(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegProviderKnown, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, int64(25), verdict.Cost.Nano)
	require.Len(t, verdict.Children, 2)
	require.Equal(t, "charge-a", verdict.Children[0].ChargeID)
	require.Equal(t, ALegProviderKnown, verdict.Children[0].Status)
	require.Equal(t, "charge-b", verdict.Children[1].ChargeID)
	require.Equal(t, ALegProviderKnownZero, verdict.Children[1].Status)
}

// TestEvaluateALegProviderChildrenCapUnknown proves child fanout
// beyond the bound resolves unknown rather than a partial sum.
func TestEvaluateALegProviderChildrenCapUnknown(t *testing.T) {
	// No t.Parallel: this test mutates the package child cap; sequential
	// execution keeps the override and its Cleanup restore deterministic.
	scope, _ := alegPVScope(t)
	headA, fenceA, journalA, snapA := alegPVChildChain(t, scope, "b-1", "charge-a", 30)
	headB, fenceB, journalB, snapB := alegPVChildChain(t, scope, "b-1", "charge-b", 20)
	headC, fenceC, journalC, snapC := alegPVChildChain(t, scope, "b-1", "charge-c", 10)
	execution := alegPVChildExecution(t, scope, "b-1", "head-charge-c", journalC.ID, journalC.ID, 1, strings.Repeat("c", 64), "fp-fence-charge-c", 4)
	oldCap := alegProviderMaxChildren
	alegProviderMaxChildren = 2
	t.Cleanup(func() { alegProviderMaxChildren = oldCap })
	verdict, issues, err := EvaluateALegProviderLeg(scope, ALegProviderLegFacts{
		BLegID: "b-1", Outcome: LegOutcomeWinner,
		Heads:           []ALegProviderHead{headA, headB, headC},
		PostingFences:   []ALegProviderFence{fenceA, fenceB, fenceC},
		ExecutionFences: []ALegProviderExecutionFence{execution},
		Snapshots:       []ALegProviderSnapshot{snapA, snapB, snapC},
		Journals:        []JournalTransaction{journalA, journalB, journalC},
	})
	require.NoError(t, err)
	require.Equal(t, ALegProviderUnknown, verdict.Status)
	require.Empty(t, verdict.Children)
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.Code)
	}
	require.Contains(t, codes, ALegProviderIssueUnresolved)
}
