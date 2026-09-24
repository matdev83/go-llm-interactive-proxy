package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.5 correction and dispute-like recovery certification (domain slice).
// These tests compose the already implemented 13.1 import, 13.2 matching and
// 13.3 selected-cost head contracts into the correction/recovery scenarios:
// a corrected statement revision flows through normalized import, matching and
// selection to exactly one idempotent monetary delta; duplicate imports and
// conflicting restatements are inert; unmatched account totals and many-charge
// aggregate coverage stay unallocated. No production behavior is faked: every
// assertion uses the public domain seams. Every price is a synthetic fixture,
// not a provider tariff.

func correctionRecoveryAggregateCovers() []metering.ChargeCoverageRef {
	return []metering.ChargeCoverageRef{
		statementMatchCovered("evidence-observation-1", 1, "charge-1"),
		statementMatchCovered("evidence-observation-2", 1, "charge-2"),
		statementMatchCovered("evidence-observation-3", 1, "charge-3"),
	}
}

func correctionRecoveryAggregateEvidence() []StatementChargeEvidence {
	return []StatementChargeEvidence{
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-1", ChargeItemID: "charge-1", ReconciliationID: "reconciliation-1"}),
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-2", ChargeItemID: "charge-2", ReconciliationID: "reconciliation-2"}),
		statementMatchEvidence(statementMatchEvidenceSpec{Observation: "evidence-observation-3", ChargeItemID: "charge-3", ReconciliationID: "reconciliation-3"}),
	}
}

func correctionRecoveryAggregateLine(amount string) statementMatchLineSpec {
	component := statementMatchSKU("token")
	return statementMatchLineSpec{
		ID: "agg-1", ChargeItemID: "aggregate-1", Kind: metering.ChargeKindAggregate,
		Component: new(component), Amount: amount, Currency: "USD",
		Covers: correctionRecoveryAggregateCovers(),
	}
}

// TestCorrectionRecoveryCertifyCorrectedAggregateStatementProducesOneIdempotentDelta
// locks the 13.5 chain: an imported statement revision is matched to complete
// many-charge aggregate coverage, a corrected restatement (new line revision)
// flows into one selected-cost delta of new-minus-posted, and an exact replay
// of the correction produces no second effect.
func TestCorrectionRecoveryCertifyCorrectedAggregateStatementProducesOneIdempotentDelta(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)
	scope := trustedStatementImportScope()

	original := statementMatchStatement(t, statementMatchStatementSpec{
		Revision: 1,
		Lines:    []statementMatchLineSpec{correctionRecoveryAggregateLine("10")},
	})
	result, err := service.Import(context.Background(), scope, original.Batch)
	require.NoError(t, err)
	require.Equal(t, []string{"agg-1"}, result.Accepted)

	set, err := MatchStatements([]NormalizedStatement{original}, correctionRecoveryAggregateEvidence())
	require.NoError(t, err)
	require.Len(t, set.Results, 1)
	require.Len(t, set.Results[0].Links, 1)
	originalLink := set.Results[0].Links[0]
	require.Equal(t, StatementMatchKindAggregateSKU, originalLink.Kind)
	require.Len(t, originalLink.Charges, 3, "many-charge aggregate coverage retains every covered charge")
	require.Equal(t, []string{"reconciliation-1", "reconciliation-2", "reconciliation-3"}, originalLink.EvidenceIDs)

	// Initial operator COGS posting from the original aggregate amount.
	initial := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-correction-v1", 1), "10")
	initialPlan, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t,
		selectedCostTestHead(t, 0, nil), SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	require.Equal(t, SelectedCostTransitionApplied, initialPlan.Status)
	require.NotNil(t, initialPlan.Journal)
	require.Len(t, initialPlan.Journal.Entries, 2)
	require.Equal(t, uint64(1), initialPlan.NextHead.Version)

	// Corrected statement revision: same coverage, corrected amount, new line
	// revision (a genuine correction rather than a conflicting restatement).
	correctedLine := correctionRecoveryAggregateLine("8")
	correctedLine.Revision = 2
	corrected := statementMatchStatement(t, statementMatchStatementSpec{
		Revision: 2,
		Lines:    []statementMatchLineSpec{correctedLine},
	})
	correctedImport, err := service.Import(context.Background(), scope, corrected.Batch)
	require.NoError(t, err)
	require.Equal(t, []string{"agg-1"}, correctedImport.Accepted)
	require.Equal(t, 2, ledger.appends)

	correctedSet, err := MatchStatements([]NormalizedStatement{corrected}, correctionRecoveryAggregateEvidence())
	require.NoError(t, err)
	require.Equal(t, StatementMatchStatusMatched, correctedSet.Results[0].Lines[0].Status)
	correctedLink := correctedSet.Results[0].Links[0]
	require.Len(t, correctedLink.Charges, 3)
	require.NotEqual(t, originalLink.Key(), correctedLink.Key(), "a corrected statement revision is a distinct coverage link")

	// The corrected aggregate produces exactly one delta: new 8 minus posted 10.
	correction := selectedCostTestUSDValuation(t, selectedCostTestRef(t, "valuation-correction-v2", 2), "8")
	applied, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t,
		selectedCostTestHead(t, 1, &initial), SelectedCostHeadExpectation{Version: 1, Previous: &initial}, correction))
	require.NoError(t, err)
	require.Equal(t, SelectedCostTransitionApplied, applied.Status)
	require.Equal(t, SelectedCostReasonSameNativeCurrency, applied.Reason)
	assertToleranceAmount(t, "correction delta", applied.Delta, "USD", "-2/0")
	require.NotNil(t, applied.Journal)
	require.Len(t, applied.Journal.Entries, 2, "one aggregate adjustment, never one journal per covered charge")
	require.Equal(t, uint64(2), applied.NextHead.Version)
	require.NotNil(t, applied.Link)

	// Exact replay of the same correction against a head that already carries
	// the new selection has no new effects.
	replayed, err := PlanSelectedCostHeadTransition(selectedCostTestInput(t,
		selectedCostTestHead(t, 2, &correction), SelectedCostHeadExpectation{Version: 1, Previous: &initial}, correction))
	require.NoError(t, err)
	require.Equal(t, SelectedCostTransitionReplay, replayed.Status)
	require.Equal(t, applied.OperationKey, replayed.OperationKey)
	require.False(t, replayed.HasEffects())

	// Duplicate import of the corrected revision is a replay with no new ledger
	// append.
	duplicate, err := service.Import(context.Background(), scope, corrected.Batch)
	require.NoError(t, err)
	require.Equal(t, []string{"agg-1"}, duplicate.Replayed)
	require.Empty(t, duplicate.Accepted)
	require.Equal(t, 2, ledger.appends)
}

// TestCorrectionRecoveryCertifyDuplicateImportAndConflictingRestatementAreInert
// proves that a repeated correction import retains one statement evidence
// revision and a changed restatement under the same line revision conflicts
// with no durable change.
func TestCorrectionRecoveryCertifyDuplicateImportAndConflictingRestatementAreInert(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)
	scope := trustedStatementImportScope()

	correctedLine := correctionRecoveryAggregateLine("8")
	correctedLine.Revision = 2
	corrected := statementMatchStatement(t, statementMatchStatementSpec{
		Revision: 2,
		Lines:    []statementMatchLineSpec{correctedLine},
	})
	first, err := service.Import(context.Background(), scope, corrected.Batch)
	require.NoError(t, err)
	require.Equal(t, []string{"agg-1"}, first.Accepted)
	require.Equal(t, 1, ledger.appends)

	replay, err := service.Import(context.Background(), scope, corrected.Batch)
	require.NoError(t, err)
	require.Equal(t, []string{"agg-1"}, replay.Replayed)
	require.Empty(t, replay.Accepted)
	require.Equal(t, 1, ledger.appends)

	restatedLine := correctionRecoveryAggregateLine("7")
	restatedLine.Revision = 2
	restated := statementMatchStatement(t, statementMatchStatementSpec{
		Revision: 2,
		Lines:    []statementMatchLineSpec{restatedLine},
	})
	_, err = service.Import(context.Background(), scope, restated.Batch)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrStatementImportConflict), "changed content under a retained line revision must conflict: %v", err)
	require.Equal(t, 1, ledger.appends, "a conflicting restatement performs no durable change")
}

// TestCorrectionRecoveryCertifyUnmatchedAccountTotalIsNeverAllocated proves
// that an account-scoped total without an explicit SKU component stays
// unmatched through import and matching, while a complete aggregate keeps
// every covered charge in one link.
func TestCorrectionRecoveryCertifyUnmatchedAccountTotalIsNeverAllocated(t *testing.T) {
	t.Parallel()

	ledger := newMemoryStatementLedger()
	service := newStatementImportService(t, ledger)
	scope := trustedStatementImportScope()

	statement := statementMatchStatement(t, statementMatchStatementSpec{
		Revision: 1,
		Lines: []statementMatchLineSpec{
			{
				ID: "total-1", ChargeItemID: "account-total-1", Kind: metering.ChargeKindAggregate,
				Amount: "18", Currency: "USD",
			},
			correctionRecoveryAggregateLine("10"),
		},
	})
	result, err := service.Import(context.Background(), scope, statement.Batch)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"agg-1", "total-1"}, result.Accepted)

	set, err := MatchStatements([]NormalizedStatement{statement}, correctionRecoveryAggregateEvidence())
	require.NoError(t, err)
	require.Len(t, set.Results, 1)
	lines := set.Results[0].Lines
	require.Len(t, lines, 2)
	byID := map[string]StatementMatchLine{}
	for _, line := range lines {
		byID[line.LineID] = line
	}
	total, ok := byID["total-1"]
	require.True(t, ok)
	require.Equal(t, StatementMatchStatusUnmatched, total.Status)
	require.Equal(t, StatementMatchReasonAccountScopedTotal, total.Reason)
	require.Empty(t, total.LinkKey, "an account total is never attached to a guessed charge")

	require.Equal(t, StatementMatchStatusMatched, byID["agg-1"].Status)
	require.Len(t, set.Results[0].Links, 1)
	require.Equal(t, []string{"agg-1"}, set.Results[0].Links[0].LineIDs)
	require.Len(t, set.Results[0].Links[0].Charges, 3, "many-charge coverage retains every charge without splitting the total")
}
