package billing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 fifth-pass Finding 1 core contract: coverage proof must seed only
// the exact authoritative selected result used for the monetary amount.
// An explicit in.Selection is monetary authority and suppresses retained-head
// ambiguity, but independent retained heads must never close costs omitted
// from the selected amount. A persisted single-head selection seeds only that
// head; ambiguous heads remain ambiguous and prove nothing.

// finding1ExtraValuation builds one complete provider-reported valuation for
// an extra observation's single charge, with an exact payable line naming
// that charge identity.
func finding1ExtraValuation(t *testing.T, valuationID string, subject metering.SubjectRef, storeID string, obs metering.Observation, amount string) economics.Valuation {
	t.Helper()
	val := detailTestValuation(t, valuationID, economics.BasisProviderReported, subject, storeID, detailTestCurrencyTotal(t, "USD", amount))
	ref, err := obs.Ref(storeID)
	require.NoError(t, err)
	lineAmount := detailTestDecimal(t, amount)
	lineNanos, err := lineAmount.ToNanoUnits()
	require.NoError(t, err)
	require.NotEmpty(t, obs.Charges)
	component := obs.Charges[0].Component.Clone()
	val.Lines = append(val.Lines, economics.LineItem{
		ID: "line-" + valuationID, RuleID: "provider_reported", ItemID: obs.Charges[0].ChargeItemID,
		Component: &component, Unit: metering.UnitToken,
		Amount:                &lineAmount,
		RoundingScope:         economics.RoundingScopeLine,
		RoundingPolicy:        economics.RoundingHalfEven,
		RoundedAmount:         &economics.Money{NanoUnits: lineNanos, Currency: "USD", Present: true},
		Status:                economics.RatingLineProviderReported,
		SourceObservationRefs: []metering.ObservationRef{ref},
	})
	require.NoError(t, val.Validate())
	return val
}

// finding1HeadForValuation builds one frozen selected head naming the given
// valuation identity for the given subject.
func finding1HeadForValuation(t *testing.T, accountID string, callID BillingCallID, headKey string, subject metering.SubjectRef, valuationID, amount string) SelectedCostHead {
	t.Helper()
	selection := OperatorCostSelectionResult{
		Status: OperatorCostSelectionStatusFinal, Basis: OperatorCostBasisP,
		Provenance: OperatorCostProvenanceAttempted, Currency: "USD",
	}
	if amount != "" {
		decimal := detailTestDecimal(t, amount)
		selection.Amount = &MonetaryExactAmount{Currency: "USD", Decimal: &decimal}
	}
	ref := SelectedCostValuationRef{ValuationID: valuationID, Revision: 1, InputSetHash: strings.Repeat("d", 64)}
	selected, err := NewSelectedCostValuation(ref, selection)
	require.NoError(t, err)
	head := SelectedCostHead{
		AccountID: accountID, CallID: callID, HeadKey: headKey,
		Subject: subject.Clone(), Version: 1, Selected: &selected,
	}
	require.NoError(t, head.Validate())
	return head
}

// TestAssembleEconomicDetailFinding1ExplicitPlusIndependentHeadSameBLegIncomplete
// is the exact fifth-pass reproduction (same B-leg ownership): explicit USD
// 1.32 selection retained, plus an independently selected provider-charge
// observation on the same B-leg with its own valuation and head. The extra
// charge must stay unresolved; explicit selection disables ambiguity but must
// not let the independent head close a cost omitted from USD 1.32.
func TestAssembleEconomicDetailFinding1ExplicitPlusIndependentHeadSameBLegIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	selected := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")
	chargeSubject := costCoverageProviderChargeSubject(selected, "pc-finding1-same")

	in := detailTestCompleteInput(t)
	require.NotNil(t, in.Selection, "explicit selection is monetary authority for this case")
	extra := costCoverageChargeObservation(t, "obs-finding1-same", "stream-finding1-same", 1, chargeSubject, "0.25", payerTestOperator, nil)
	in.Observations = append(in.Observations, extra)
	extraVal := finding1ExtraValuation(t, "valuation-pc-same", chargeSubject, storeID, extra, "0.25")
	in.Valuations = append(in.Valuations, extraVal)
	callID, err := ParseBillingCallID(billingCallID)
	require.NoError(t, err)
	in.Heads = append(in.Heads, finding1HeadForValuation(t, accountID, callID, "head-pc-same", chargeSubject, extraVal.ID, "0.25"))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "independent same-B-leg head must not close a cost omitted from explicit USD 1.32")
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool {
		return s.Subject.ProviderChargeID == "pc-finding1-same"
	})
	require.True(t, found, "independent provider-charge identity must remain its own subject")
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailFinding1ExplicitPlusIndependentHeadDifferentBLegIncomplete
// is the different-subject variant: explicit USD 1.32 for b-detail-1 plus an
// independently selected B-leg b-detail-2 charge with its own valuation and
// head. The second B-leg must stay unresolved.
func TestAssembleEconomicDetailFinding1ExplicitPlusIndependentHeadDifferentBLegIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	other := detailTestSubject(storeID, aLegID, billingCallID, "b-finding1-other")

	in := detailTestCompleteInput(t)
	require.NotNil(t, in.Selection)
	extra := costCoverageChargeObservation(t, "obs-finding1-other", "stream-finding1-other", 1, other, "0.25", payerTestOperator, nil)
	in.Observations = append(in.Observations, extra)
	extraVal := finding1ExtraValuation(t, "valuation-p-other", other, storeID, extra, "0.25")
	in.Valuations = append(in.Valuations, extraVal)
	callID, err := ParseBillingCallID(billingCallID)
	require.NoError(t, err)
	in.Heads = append(in.Heads, finding1HeadForValuation(t, accountID, callID, "head-p-other", other, extraVal.ID, "0.25"))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "independent B-leg head must not close a cost omitted from explicit USD 1.32")
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool { return s.Subject.BLegID == "b-finding1-other" })
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailFinding1PersistedSingleHeadComplete is the control:
// no explicit selection with exactly one retained head over the only
// attributable subject stays complete.
func TestAssembleEconomicDetailFinding1PersistedSingleHeadComplete(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.Selection = nil

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.True(t, got.Coverage.CostCoverage.Complete)
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
	require.Equal(t, "complete", got.Margin.Reason)
	first := got.SnapshotFingerprint
	again, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Equal(t, first, again.SnapshotFingerprint, "coverage output must be deterministic")
}

// TestAssembleEconomicDetailFinding1ExplicitAggregateInclusiveComplete is the
// provable-inclusion control: explicit USD 1.32 selection plus a second charge
// proven contained by an exact inclusive edge from the selected atom stays
// complete without any second head.
func TestAssembleEconomicDetailFinding1ExplicitAggregateInclusiveComplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	other := detailTestSubject(storeID, aLegID, billingCallID, "b-finding1-agg")

	in := detailTestCompleteInput(t)
	require.NotNil(t, in.Selection)
	extra := costCoverageChargeObservation(t, "obs-finding1-agg", "stream-finding1-agg", 1, other, "0.25", payerTestOperator, nil)
	in.Observations = append(in.Observations, extra)
	costCoverageAddInclusiveEdgeToSelectedLeg(t, in, storeID, extra)
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Coverage.CostCoverage.Complete, "explicit selected atom plus inclusive edge proves both charges")
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
	require.Equal(t, "complete", got.Margin.Reason)
}

// TestAssembleEconomicDetailFinding1MultipleHeadsNeverSummedIncomplete proves
// no summing or fallback: two independently selected heads with no explicit
// selection remain ambiguous and prove nothing; coverage stays unresolved and
// no margin amount is produced.
func TestAssembleEconomicDetailFinding1MultipleHeadsNeverSummedIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	other := detailTestSubject(storeID, aLegID, billingCallID, "b-finding1-nosum")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	extra := costCoverageChargeObservation(t, "obs-finding1-nosum", "stream-finding1-nosum", 1, other, "0.25", payerTestOperator, nil)
	in.Observations = append(in.Observations, extra)
	extraVal := finding1ExtraValuation(t, "valuation-p-nosum", other, storeID, extra, "0.25")
	in.Valuations = append(in.Valuations, extraVal)
	callID, err := ParseBillingCallID(billingCallID)
	require.NoError(t, err)
	in.Heads = append(in.Heads, finding1HeadForValuation(t, accountID, callID, "head-p-nosum", other, extraVal.ID, "0.25"))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Totals.SelectedAmbiguous, "two independent heads must stay ambiguous")
	require.Equal(t, 2, got.Totals.SelectedHeadCount)
	require.Nil(t, got.Selection, "ambiguous scope must not invent a singular selection")
	require.False(t, got.Margin.Complete, "independent heads must never sum into a complete margin")
	require.Nil(t, got.Margin.Amount)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete, "multiple heads must not seed coverage by fallback")
	require.Equal(t, "ambiguous_selected", got.Margin.Reason)
}
