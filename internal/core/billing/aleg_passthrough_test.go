package billing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Cycle 2 (Task 5.3 C2A2) characterization for the single core
// pass-through adjustment authority evaluator. The rolling report shell
// owns SQL and fact loading; all pass-through financial proof lives here
// as one side-effect-free pure function. Pass-through stays a distinct
// customer adjustment plane: it never enters retail totals.

func alegPTProvider(revision uint64, amount int64) CostPassThroughProviderCost {
	return CostPassThroughProviderCost{
		LURKey: "lur-pt", ValuationID: "val-pt", Revision: revision,
		InputHash: strings.Repeat("ab", 32), Amount: Money{Nano: amount, Currency: "USD"},
		AmountPresent: true, Reconciled: true, Authoritative: true,
	}
}

func alegPTSource(t *testing.T, scope ALegAuthorityScope, callID BillingCallID, provider CostPassThroughProviderCost) string {
	t.Helper()
	source, err := CostPassThroughAdjustmentSourceKey(scope.AccountID, callID, provider)
	require.NoError(t, err)
	return source
}

func alegPTJournal(t *testing.T, scope ALegAuthorityScope, source, aLegID, debitLedger, creditLedger string, amount int64, seq uint64, group string) JournalTransaction {
	t.Helper()
	sealed, err := JournalTransaction{
		ID: source, Book: JournalBookFinancial, Currency: scope.Currency, SourceKey: source,
		AccountID: scope.AccountID, TurnID: scope.CallID, ALegID: aLegID,
		AccountSequence: seq, CorrectionGroupID: group, OperationKind: CostPassThroughAdjustmentOperationKind,
		BalanceBefore: 1000 + amount, BalanceAfter: 1000,
		SpendableBefore: 1000 + amount, SpendableAfter: 1000,
		Mode: string(AccountPrepaid),
		Entries: []JournalEntry{
			{LedgerAccount: debitLedger, Side: JournalDebit, Amount: Money{Nano: amount, Currency: scope.Currency}},
			{LedgerAccount: creditLedger, Side: JournalCredit, Amount: Money{Nano: amount, Currency: scope.Currency}},
		},
	}.Seal()
	require.NoError(t, err)
	return sealed
}

func alegPTSnapshot(scope ALegAuthorityScope, journal JournalTransaction, fingerprint string) ALegPassThroughSnapshot {
	before := AccountSnapshot{BalanceNano: journal.BalanceBefore, SpendableNano: journal.SpendableBefore, Mode: AccountMode(journal.Mode), Currency: journal.Currency, Version: journal.SnapshotVersionBefore}
	after := AccountSnapshot{BalanceNano: journal.BalanceAfter, SpendableNano: journal.SpendableAfter, Mode: AccountMode(journal.Mode), Currency: journal.Currency, Version: journal.SnapshotVersionAfter}
	return ALegPassThroughSnapshot{
		OperationKey: journal.SourceKey + ":snapshot", SourceKey: journal.SourceKey,
		Fingerprint: fingerprint,
		IntegrityFingerprint: alegMarkerIntegrity(journal.SourceKey+":snapshot", scope.AccountID,
			CostPassThroughAdjustmentOperationKind, journal.SourceKey, fingerprint, before, after,
			journal.AccountSequence, journal.AccountSequence),
		Currency: scope.Currency, Mode: journal.Mode,
		Before: before, After: after,
		SequenceStart: journal.AccountSequence, SequenceEnd: journal.AccountSequence,
	}
}

func alegPTHead(scope ALegAuthorityScope, policy VersionRef, settlementFp string, status CostPassThroughSettlementStatus, posted int64, provider *CostPassThroughProviderCost, originalTx string) *ALegPassThroughHead {
	head := &ALegPassThroughHead{
		AccountID: scope.AccountID, CallID: scope.CallID, ALegID: scope.ALegID,
		SettlementOperationKey: "op-pt", OriginalTransactionID: originalTx,
		PolicyRef: policy, MissingCost: CostPassThroughMissingCostProvisional,
		SafeBound: Money{Nano: 100, Currency: scope.Currency}, AllowLateAdjustment: true,
		Status: status, PostedAmount: Money{Nano: posted, Currency: scope.Currency},
		HeadVersion: 2, Fence: 2, SettlementFingerprint: settlementFp,
	}
	if provider != nil {
		head.ProviderLURKey, head.ProviderValuationID = provider.LURKey, provider.ValuationID
		head.ProviderRevision, head.ProviderInputHash = provider.Revision, provider.InputHash
	}
	return head
}

// alegPTFacts builds the shared final-head scenario: provisional posted 60
// (marker delta), positive revision 2 to 80 (+20), negative correction
// revision 3 to 70 (-10). Both journals carry the queried A-leg.
func alegPTFacts(t *testing.T, scope ALegAuthorityScope, callID BillingCallID, source, markerFp string, policy VersionRef) (ALegPassThroughFacts, *ALegPassThroughHead, []JournalTransaction) {
	t.Helper()
	up := alegPTProvider(2, 80)
	down := alegPTProvider(3, 70)
	upSource := alegPTSource(t, scope, callID, up)
	downSource := alegPTSource(t, scope, callID, down)
	head := alegPTHead(scope, policy, markerFp, CostPassThroughSettlementFinal, 70, &down, "tx-orig")
	head.HeadVersion, head.Fence = 3, 3
	upJournal := alegPTJournal(t, scope, upSource, scope.ALegID, "customer_financial_account", "customer_adjustment_clearing", 20, 7, "tx-orig")
	downJournal := alegPTJournal(t, scope, downSource, scope.ALegID, "customer_adjustment_clearing", "customer_financial_account", 10, 8, "tx-orig")
	facts := ALegPassThroughFacts{
		Head: head, Policy: policy,
		Snapshots: []ALegPassThroughSnapshot{alegPTSnapshot(scope, upJournal, "fp-up"), alegPTSnapshot(scope, downJournal, "fp-down")},
		Journals:  []JournalTransaction{upJournal, downJournal},
	}
	return facts, head, []JournalTransaction{upJournal, downJournal}
}

func alegPTMarkerFacts(t *testing.T, scope ALegAuthorityScope, callID BillingCallID, source string, delta int64) ALegMarker {
	t.Helper()
	before := AccountSnapshot{BalanceNano: 100 + delta, SpendableNano: 100 + delta, Mode: AccountPrepaid, Currency: scope.Currency, Version: 1}
	after := AccountSnapshot{BalanceNano: 100, SpendableNano: 100, Mode: AccountPrepaid, Currency: scope.Currency, Version: 2}
	return alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
}

func TestParseCostPassThroughAdjustmentSourceKey(t *testing.T) {
	t.Parallel()
	scope, callID, _ := alegTestScope(t, "USD")
	provider := alegPTProvider(2, 80)
	canonical, err := CostPassThroughAdjustmentSourceKey(scope.AccountID, callID, provider)
	require.NoError(t, err)
	accountID, parsedCall, lur, revision, valuation, err := ParseCostPassThroughAdjustmentSourceKey(canonical)
	require.NoError(t, err)
	require.Equal(t, scope.AccountID, accountID)
	require.Equal(t, callID.String(), parsedCall)
	require.Equal(t, "lur-pt", lur)
	require.Equal(t, uint64(2), revision)
	require.Equal(t, "val-pt", valuation)

	for _, tc := range []struct {
		name  string
		munge func(string) string
	}{
		{name: "empty", munge: func(string) string { return "" }},
		{name: "bad prefix", munge: func(s string) string { return "bogus" + s[strings.Index(s, ":"):] }},
		{name: "bad version", munge: func(s string) string { return strings.Replace(s, ":v1:", ":v9:", 1) }},
		{name: "truncated", munge: func(s string) string { return s[:strings.LastIndex(s, ":")] }},
		{name: "extended", munge: func(s string) string { return s + ":extra" }},
		{name: "empty valuation", munge: func(s string) string { return s[:strings.LastIndex(s, ":")] + ":" }},
		{name: "zero revision", munge: func(s string) string {
			parts := strings.Split(s, ":")
			parts[5] = "0"
			return strings.Join(parts, ":")
		}},
		{name: "non-numeric revision", munge: func(s string) string {
			parts := strings.Split(s, ":")
			parts[5] = "two"
			return strings.Join(parts, ":")
		}},
		{name: "bad call identity", munge: func(s string) string {
			parts := strings.Split(s, ":")
			parts[3] = "not-a-call"
			return strings.Join(parts, ":")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, _, _, err := ParseCostPassThroughAdjustmentSourceKey(tc.munge(canonical))
			require.ErrorIs(t, err, ErrCostPassThroughSettlementInvalid)
		})
	}
}

func TestEvaluateALegPassThroughNone(t *testing.T) {
	t.Parallel()
	scope, _, _ := alegTestScope(t, "USD")
	verdict, issues, err := EvaluateALegPassThroughAuthority(scope, ALegPassThroughFacts{})
	require.NoError(t, err)
	require.Equal(t, ALegPassThroughNone, verdict.Status)
	require.Empty(t, issues)
	require.Empty(t, verdict.Adjustments)
}

func TestEvaluateALegPassThroughPendingWithoutHead(t *testing.T) {
	t.Parallel()
	scope, _, _ := alegTestScope(t, "USD")
	journal := alegPTJournal(t, scope, "src-legacy-unresolved", scope.ALegID,
		"customer_financial_account", "customer_adjustment_clearing", 500, 9, "")
	verdict, issues, err := EvaluateALegPassThroughAuthority(scope, ALegPassThroughFacts{
		Journals: []JournalTransaction{journal},
	})
	require.NoError(t, err)
	require.Equal(t, ALegPassThroughPending, verdict.Status)
	require.Len(t, issues, 1)
	require.Equal(t, ALegIssueAdjustmentPending, issues[0].Code)
	require.Len(t, verdict.Adjustments, 1)
	require.Equal(t, journal.ID, verdict.Adjustments[0].TransactionID)
	require.False(t, verdict.Adjustments[0].Validated, "unresolved lineage must not read validated")
}

func TestEvaluateALegPassThroughPendingProvisionalHead(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	policy := VersionRef{ID: "policy", Version: "v1"}
	marker := alegPTMarkerFacts(t, scope, callID, source, 60)
	head := alegPTHead(scope, policy, marker.Fingerprint, CostPassThroughSettlementProvisional, 60, nil, "tx-orig")
	head.HeadVersion, head.Fence = 1, 1
	verdict, issues, err := EvaluateALegPassThroughAuthority(scope, ALegPassThroughFacts{
		Head: head, Policy: policy, Marker: marker, HasMarker: true,
	})
	require.NoError(t, err)
	require.Equal(t, ALegPassThroughPending, verdict.Status)
	require.Len(t, issues, 1)
	require.Equal(t, ALegIssueAdjustmentPending, issues[0].Code)
	require.Empty(t, verdict.Adjustments)
}

func TestEvaluateALegPassThroughKnownFinal(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	policy := VersionRef{ID: "policy", Version: "v1"}
	marker := alegPTMarkerFacts(t, scope, callID, source, 60)
	facts, _, journals := alegPTFacts(t, scope, callID, source, marker.Fingerprint, policy)
	facts.Marker, facts.HasMarker = marker, true
	// Input-order independence: reversed journal/snapshot order must not matter.
	facts.Journals[0], facts.Journals[1] = facts.Journals[1], facts.Journals[0]
	facts.Snapshots[0], facts.Snapshots[1] = facts.Snapshots[1], facts.Snapshots[0]

	verdict, issues, err := EvaluateALegPassThroughAuthority(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegPassThroughKnown, verdict.Status)
	require.Empty(t, issues)
	require.Equal(t, int64(70), verdict.PostedAmount.Nano)
	require.Equal(t, uint64(3), verdict.Revision)
	require.Len(t, verdict.Adjustments, 2)
	require.Equal(t, journals[0].ID, verdict.Adjustments[0].TransactionID)
	require.Equal(t, int64(-20), verdict.Adjustments[0].Amount.Nano,
		"validated refs keep the Cycle 1 financial-side signing")
	require.Equal(t, int64(10), verdict.Adjustments[1].Amount.Nano)
	for _, ref := range verdict.Adjustments {
		require.True(t, ref.Validated)
		require.Equal(t, "USD", ref.Amount.Currency)
	}
}

func TestEvaluateALegPassThroughKnownBornFinal(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	policy := VersionRef{ID: "policy", Version: "v1"}
	marker := alegPTMarkerFacts(t, scope, callID, source, 60)
	provider := alegPTProvider(1, 60)
	head := alegPTHead(scope, policy, marker.Fingerprint, CostPassThroughSettlementFinal, 60, &provider, "tx-orig")
	head.HeadVersion, head.Fence = 1, 1
	verdict, issues, err := EvaluateALegPassThroughAuthority(scope, ALegPassThroughFacts{
		Head: head, Policy: policy, Marker: marker, HasMarker: true,
	})
	require.NoError(t, err)
	require.Equal(t, ALegPassThroughKnown, verdict.Status)
	require.Empty(t, issues)
	require.Empty(t, verdict.Adjustments)
	require.Equal(t, uint64(1), verdict.Revision)
}

func TestEvaluateALegPassThroughLegacyJoin(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	policy := VersionRef{ID: "policy", Version: "v1"}
	marker := alegPTMarkerFacts(t, scope, callID, source, 60)
	facts, _, _ := alegPTFacts(t, scope, callID, source, marker.Fingerprint, policy)
	// Pre-C2A1 writer state: the durable journal carries empty A-leg
	// lineage while the trusted head names the queried A-leg.
	for i := range facts.Journals {
		unsealed := facts.Journals[i]
		unsealed.ALegID = ""
		resealed, err := unsealed.Seal()
		require.NoError(t, err)
		facts.Journals[i] = resealed
		facts.Snapshots[i] = alegPTSnapshot(scope, resealed, "fp-legacy")
	}
	facts.Marker, facts.HasMarker = marker, true

	verdict, issues, err := EvaluateALegPassThroughAuthority(scope, facts)
	require.NoError(t, err)
	require.Equal(t, ALegPassThroughKnown, verdict.Status)
	require.Empty(t, issues)
	require.Len(t, verdict.Adjustments, 2)
	for _, ref := range verdict.Adjustments {
		require.True(t, ref.Validated, "head-joined legacy lineage validates without mutating the journal")
	}
}

func TestEvaluateALegPassThroughAdversarial(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	policy := VersionRef{ID: "policy", Version: "v1"}

	setup := func(t *testing.T) (ALegAuthorityScope, ALegPassThroughFacts) {
		t.Helper()
		marker := alegPTMarkerFacts(t, scope, callID, source, 60)
		facts, _, _ := alegPTFacts(t, scope, callID, source, marker.Fingerprint, policy)
		facts.Marker, facts.HasMarker = marker, true
		return scope, facts
	}
	mutateJournal := func(facts *ALegPassThroughFacts, idx int, fn func(*JournalTransaction)) {
		unsealed := facts.Journals[idx]
		unsealed.SemanticFingerprint = ""
		fn(&unsealed)
		resealed, err := unsealed.Seal()
		if err == nil {
			facts.Journals[idx] = resealed
			for i := range facts.Snapshots {
				if facts.Snapshots[i].SourceKey == resealed.SourceKey {
					facts.Snapshots[i] = alegPTSnapshot(scope, resealed, "fp-mut")
				}
			}
		} else {
			facts.Journals[idx] = unsealed
		}
	}

	for _, tc := range []struct {
		name      string
		wantIssue string
		mutate    func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts)
	}{
		{name: "foreign head aleg", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.ALegID = "a-leg-foreign"
		}},
		{name: "head account mismatch", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.AccountID = "acct-other"
		}},
		{name: "head call mismatch", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.CallID = "bc_00000000000000000000000000000000"
		}},
		{name: "head currency mismatch", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.PostedAmount.Currency = "EUR"
		}},
		{name: "head policy mismatch", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Policy = VersionRef{ID: "policy", Version: "v2"}
		}},
		{name: "missing exposure policy", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Policy = VersionRef{}
			facts.Head.PolicyRef = VersionRef{}
		}},
		{name: "head fingerprint mismatch", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.SettlementFingerprint = "fp-drift"
		}},
		{name: "head version zero", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.HeadVersion = 0
		}},
		{name: "head fence drift", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.Fence = facts.Head.HeadVersion + 1
		}},
		{name: "final with zero revision", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.ProviderRevision = 0
			facts.Journals, facts.Snapshots = nil, nil
		}},
		{name: "posted above bound", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.PostedAmount.Nano = 101
		}},
		{name: "amount mismatch", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.PostedAmount.Nano = 71
		}},
		{name: "stale future journal", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			future := alegPTProvider(4, 65)
			futureSource := alegPTSource(t, scope, callID, future)
			journal := alegPTJournal(t, scope, futureSource, scope.ALegID,
				"customer_financial_account", "customer_adjustment_clearing", 5, 9, "tx-orig")
			facts.Journals = append(facts.Journals, journal)
			facts.Snapshots = append(facts.Snapshots, alegPTSnapshot(scope, journal, "fp-future"))
		}},
		{name: "duplicate source key", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			dup := facts.Journals[0]
			dup.ID = "tx-duplicate-id"
			dup.AccountSequence = 9
			resealed, err := dup.Seal()
			require.NoError(t, err)
			facts.Journals = append(facts.Journals, resealed)
			facts.Snapshots = append(facts.Snapshots, alegPTSnapshot(scope, resealed, "fp-dup"))
		}},
		{name: "competing same revision", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			// A second historical claim on revision 2 (different valuation
			// lineage) is ambiguous even though each journal validates
			// alone: at most one journal per revision may be admitted.
			competing := alegPTProvider(2, 75)
			competing.ValuationID = "val-old"
			competingSource := alegPTSource(t, scope, callID, competing)
			journal := alegPTJournal(t, scope, competingSource, scope.ALegID,
				"customer_financial_account", "customer_adjustment_clearing", 15, 9, "tx-orig")
			facts.Journals = append(facts.Journals, journal)
			facts.Snapshots = append(facts.Snapshots, alegPTSnapshot(scope, journal, "fp-competing"))
		}},
		{name: "unrelated empty aleg journal", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			stray := alegPTJournal(t, scope, "src-unrelated", "", "customer_financial_account",
				"customer_adjustment_clearing", 10, 9, "tx-orig")
			facts.Journals = append(facts.Journals, stray)
		}},
		{name: "foreign journal aleg", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			mutateJournal(facts, 0, func(j *JournalTransaction) { j.ALegID = "a-leg-foreign" })
		}},
		{name: "journal currency mismatch", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			mutateJournal(facts, 0, func(j *JournalTransaction) {
				j.Currency = "EUR"
				for i := range j.Entries {
					j.Entries[i].Amount.Currency = "EUR"
				}
			})
		}},
		{name: "journal book conflict", wantIssue: ALegIssueBookConflict, mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			mutateJournal(facts, 0, func(j *JournalTransaction) { j.Book = "authorization" })
		}},
		{name: "journal shape drift", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			mutateJournal(facts, 0, func(j *JournalTransaction) {
				j.Entries[1].LedgerAccount = "usage_revenue"
			})
		}},
		{name: "journal fingerprint drift", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Journals[0].SemanticFingerprint = "fp-drift"
		}},
		{name: "missing operation snapshot", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Snapshots = facts.Snapshots[1:]
		}},
		{name: "snapshot integrity drift", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Snapshots[0].IntegrityFingerprint = "snapshot:v1:drift"
		}},
		{name: "snapshot sequence drift", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Snapshots[0].SequenceEnd++
		}},
		{name: "correction group drift", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			mutateJournal(facts, 0, func(j *JournalTransaction) { j.CorrectionGroupID = "tx-other" })
		}},
		{name: "provisional head with journals", mutate: func(t *testing.T, scope ALegAuthorityScope, facts *ALegPassThroughFacts) {
			facts.Head.Status = CostPassThroughSettlementProvisional
			facts.Head.ProviderRevision = 0
			facts.Head.PostedAmount.Nano = 60
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope, facts := setup(t)
			tc.mutate(t, scope, &facts)
			verdict, issues, err := EvaluateALegPassThroughAuthority(scope, facts)
			require.NoError(t, err)
			require.Equal(t, ALegPassThroughUnknown, verdict.Status, "fail-closed, never known")
			require.False(t, verdict.PostedAmount.Nano == 70 && len(verdict.Adjustments) == 2)
			want := tc.wantIssue
			if want == "" {
				want = ALegIssueAdjustmentUnresolved
			}
			codes := make([]string, 0, len(issues))
			for _, issue := range issues {
				codes = append(codes, issue.Code)
			}
			require.Contains(t, codes, want)
			for _, ref := range verdict.Adjustments {
				require.False(t, ref.Validated)
			}
		})
	}
}

func TestEvaluateALegPassThroughTelescopeOverflowFailsQuery(t *testing.T) {
	t.Parallel()
	scope, callID, source := alegTestScope(t, "USD")
	policy := VersionRef{ID: "policy", Version: "v1"}
	const maxNano = int64(1<<63 - 1)
	before := AccountSnapshot{BalanceNano: maxNano, SpendableNano: maxNano, Mode: AccountPrepaid, Currency: "USD", Version: 1}
	after := AccountSnapshot{BalanceNano: 10, SpendableNano: 10, Mode: AccountPrepaid, Currency: "USD", Version: 2}
	marker := alegTestMarker(t, scope, callID, source, ALegSettlementKind, before, after, 1, 2)
	provider := alegPTProvider(1, maxNano)
	head := alegPTHead(scope, policy, marker.Fingerprint, CostPassThroughSettlementFinal, maxNano, &provider, "tx-orig")
	head.SafeBound = Money{Nano: maxNano, Currency: "USD"}
	head.HeadVersion, head.Fence = 1, 1
	upSource := alegPTSource(t, scope, callID, provider)
	journal := alegPTJournal(t, scope, upSource, scope.ALegID,
		"customer_financial_account", "customer_adjustment_clearing", 20, 7, "tx-orig")
	_, _, err := EvaluateALegPassThroughAuthority(scope, ALegPassThroughFacts{
		Head: head, Policy: policy, Marker: marker, HasMarker: true,
		Snapshots: []ALegPassThroughSnapshot{alegPTSnapshot(scope, journal, "fp-up")},
		Journals:  []JournalTransaction{journal},
	})
	require.ErrorIs(t, err, ErrMoneyOverflow)
}
