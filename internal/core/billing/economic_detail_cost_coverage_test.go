package billing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 third-pass Finding 1 core contract: a complete margin requires every
// attributable in-scope executed cost subject (B-leg / provider charge) to be
// covered by the single authoritative selected result, explicitly known-zero,
// customer-BYOK, or proven inclusive. An observed operator-payable charge or an
// executed-but-unpriced B-leg that is not proven covered must keep the margin
// explicitly incomplete instead of being silently omitted while the selected
// subject alone reports complete.

// costCoverageChargeObservation builds one provider-origin component charge
// observation on an explicit subject with an exact amount (or an absent amount
// when amount is empty) and explicit coverage edges. It is the coverage-probe
// counterpart of detailTestPayerChargeObservation that can carry zero amounts
// and inclusive references.
func costCoverageChargeObservation(t *testing.T, id, stream string, sequence uint64, subject metering.SubjectRef, amount string, payer metering.PaymentParty, covers []metering.ChargeCoverageRef) metering.Observation {
	t.Helper()
	charge := metering.ReportedCharge{
		ChargeItemID: id + "-charge",
		Component: &metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		},
		Currency: "USD", Kind: metering.ChargeKindComponent, Payer: payer, Covers: covers,
	}
	if amount != "" {
		value := detailTestDecimal(t, amount)
		charge.Amount = &value
	}
	return detailTestObservation(t, id, metering.OriginProvider, stream, sequence, subject, nil, []metering.ReportedCharge{charge})
}

// costCoverageInclusiveChargeRef returns the exact charge reference of one
// coverage-probe observation's single charge.
func costCoverageInclusiveChargeRef(storeID string, observation metering.Observation) metering.ChargeCoverageRef {
	return metering.ChargeCoverageRef{
		Ref: metering.ChargeRef{
			StoreID: storeID, ObservationID: observation.ID, Revision: observation.Revision,
			ChargeItemID: observation.Charges[0].ChargeItemID,
		},
		Relation: metering.CoverageInclusive,
	}
}

// costCoverageAddInclusiveEdgeToSelectedLeg attaches one inclusive coverage
// edge from the canonical selected B-leg's provider money charge to the given
// target charge, proving the selected result contains that charge.
func costCoverageAddInclusiveEdgeToSelectedLeg(t *testing.T, in EconomicDetailInput, storeID string, target metering.Observation) {
	t.Helper()
	for i := range in.Observations {
		if in.Observations[i].ID != "obs-provider-money-1" {
			continue
		}
		require.NotEmpty(t, in.Observations[i].Charges)
		in.Observations[i].Charges[0].Covers = []metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, target)}
		return
	}
	t.Fatalf("canonical selected-leg money observation not found")
}

// TestAssembleEconomicDetailCostCoverageObservationOnlyChargeIncomplete is the
// exact Finding 1 source-path reproduction: the persisted selected head covers
// b-detail-1 only, while a same-call operator-paid component charge on
// b-detail-2 has no valuation, reconciliation or head. The extra observation is
// valid and returned, but no argument may let the margin stay complete on the
// strength of the first B-leg alone.
func TestAssembleEconomicDetailCostCoverageObservationOnlyChargeIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-2")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	extra := detailTestPayerChargeObservation(t, "obs-cost-2", "stream-cost-2", 1, subject, payerTestOperator)
	in.Observations = append(in.Observations, extra)

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "an uncovered same-call operator charge cannot coexist with a complete margin")
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)

	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, got.Coverage.CostCoverage.UnresolvedCount)
	require.Len(t, got.Coverage.CostCoverage.Subjects, 2)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, got.Coverage.CostCoverage.Subjects[1].State)
	require.Equal(t, "b-detail-2", got.Coverage.CostCoverage.Subjects[1].Subject.BLegID)
}

// TestAssembleEconomicDetailCostCoverageExplicitSelectionIncomplete proves the
// explicit caller-supplied full selection path enforces the same rule: an
// independent observed operator charge outside the selected result keeps the
// margin incomplete.
func TestAssembleEconomicDetailCostCoverageExplicitSelectionIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-explicit-2")

	in := detailTestCompleteInput(t)
	require.NotNil(t, in.Selection, "this control keeps the caller-supplied full selection")
	in.Observations = append(in.Observations, detailTestPayerChargeObservation(t, "obs-cost-explicit-2", "stream-cost-explicit-2", 1, subject, payerTestOperator))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "an explicit selection for one B-leg cannot cover an unrelated observed charge")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
}

// TestAssembleEconomicDetailCostCoverageUnpricedBLegIncomplete proves an
// executed B-leg whose usage is entirely unpriced (no charge, valuation or head)
// is an unresolved cost subject and cannot coexist with a complete margin.
func TestAssembleEconomicDetailCostCoverageUnpricedBLegIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-unpriced")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	in.Observations = append(in.Observations, detailTestObservation(t, "obs-cost-unpriced", metering.OriginProvider, "stream-cost-unpriced", 1, subject,
		[]metering.Measure{detailTestMeasure(t, metering.ComponentInputToken, "77")}, nil))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "an executed but unpriced B-leg cannot support a complete margin")
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete)
}

// TestAssembleEconomicDetailCostCoverageInclusiveControlComplete is the genuine
// coverage control: an independent second subject charge is explicitly marked
// inclusive of the selected B-leg's provider charge, so the selected result
// proves it contains that charge and the margin remains complete.
func TestAssembleEconomicDetailCostCoverageInclusiveControlComplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-inclusive")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	extra := detailTestPayerChargeObservation(t, "obs-cost-inclusive", "stream-cost-inclusive", 1, subject, payerTestOperator)
	in.Observations = append(in.Observations, extra)
	// The selected B-leg's own charge inclusively covers the second charge.
	// Reverse the relation direction: the parent charge covers the child charge.
	for i := range in.Observations {
		if in.Observations[i].ID != "obs-provider-money-1" {
			continue
		}
		in.Observations[i].Charges[0].Covers = []metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, extra)}
	}
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.True(t, got.Coverage.CostCoverage.Complete, "explicit inclusive coverage of the second charge is proven")
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
	require.Equal(t, "complete", got.Margin.Reason)
}

// TestAssembleEconomicDetailCostCoverageKnownZeroAndBYOKDoNotBlock proves a
// proven known-zero operator charge and a customer-BYOK charge observation are
// covered without a valuation or head and must not falsely block completeness.
func TestAssembleEconomicDetailCostCoverageKnownZeroAndBYOKDoNotBlock(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	zeroSubject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-zero")
	byokSubject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-byok")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	in.Observations = append(in.Observations,
		costCoverageChargeObservation(t, "obs-cost-zero", "stream-cost-zero", 1, zeroSubject, "0", payerTestOperator, nil),
		costCoverageChargeObservation(t, "obs-cost-byok", "stream-cost-byok", 1, byokSubject, "9.99", payerTestCustomer, nil),
	)

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.True(t, got.Coverage.CostCoverage.Complete, "known-zero and customer-BYOK costs need no selected-coverage proof")
	require.True(t, got.Margin.Complete)
	require.Equal(t, "complete", got.Margin.Reason)

	states := map[string]EconomicDetailCostCoverageState{}
	for _, subject := range got.Coverage.CostCoverage.Subjects {
		states[subject.Subject.BLegID] = subject.State
	}
	require.Equal(t, EconomicDetailCostCoverageKnownZero, states["b-cost-zero"])
	require.Equal(t, EconomicDetailCostCoverageBYOK, states["b-cost-byok"])
}

// TestAssembleEconomicDetailCostCoverageNonRequestSubjectDoesNotBlock proves a
// genuine account-period (non-request) charge is not attributed to the call and
// therefore cannot block its margin completeness.
func TestAssembleEconomicDetailCostCoverageNonRequestSubjectDoesNotBlock(t *testing.T) {
	t.Parallel()
	storeID, _, _, _ := detailTestScope()
	resourceSubject := metering.SubjectRef{
		Kind: metering.SubjectResource, StoreID: storeID,
		ResourceID: "res-cc", PeriodID: "2026-09",
	}
	resourceCharge := metering.ReportedCharge{
		ChargeItemID: "charge-cc-resource",
		Component: &metering.ComponentKey{
			Direction: metering.DirectionNone, Component: "provider:resource",
			Unit: metering.UnitSecond, SchemaID: "provider:resource:v1",
		},
		Currency: "USD", Kind: metering.ChargeKindComponent,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
	}
	resourceAmount := detailTestDecimal(t, "12.00")
	resourceCharge.Amount = &resourceAmount
	now := time.Unix(1_700_030_000, 0).UTC()
	resource := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "obs-cost-resource", SourceEventKey: "obs-cost-resource-event",
		Revision: 1, StreamID: "stream-cost-resource", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject:   resourceSubject,
		Correlation: metering.CorrelationV2{
			StoreID: resourceSubject.StoreID, ResourceID: resourceSubject.ResourceID, PeriodID: resourceSubject.PeriodID,
		},
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "detail.test.v1",
		Charges: []metering.ReportedCharge{resourceCharge},
	}
	require.NoError(t, resource.Validate())
	canonical, err := resource.Canonical()
	require.NoError(t, err)

	in := detailTestCompleteInput(t)
	in.Selection = nil
	in.Observations = append(in.Observations, canonical)

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Margin.Complete, "account-period economics stay non-attributed until explicitly allocated")
	require.NotNil(t, got.Coverage.CostCoverage)
	require.True(t, got.Coverage.CostCoverage.Complete)
}

// TestAssembleEconomicDetailCostCoverageALegScopeIncomplete proves the same
// unresolved-cost rule through an A-leg query that gathers an independent B-leg
// under a distinct call of the same A-leg.
func TestAssembleEconomicDetailCostCoverageALegScopeIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, _ := detailTestScope()
	otherCall := "bc_" + "2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a"
	subject := detailTestSubject(storeID, aLegID, otherCall, "b-cost-aleg-2")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	in.Query.BillingCallID = ""
	in.Observations = append(in.Observations, detailTestPayerChargeObservation(t, "obs-cost-aleg-2", "stream-cost-aleg-2", 1, subject, payerTestOperator))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Equal(t, aLegID, got.Scope.ALegID)
	require.Empty(t, got.Scope.BillingCallID)
	require.False(t, got.Margin.Complete, "an A-leg scope must include every attributable B-leg cost")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
}

// TestAssembleEconomicDetailCostCoverageALegScopeInclusiveComplete is the A-leg
// control proving explicit inclusive coverage keeps an A-leg margin complete.
func TestAssembleEconomicDetailCostCoverageALegScopeInclusiveComplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, _ := detailTestScope()
	otherCall := "bc_" + "3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b3b"
	subject := detailTestSubject(storeID, aLegID, otherCall, "b-cost-aleg-inclusive")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	in.Query.BillingCallID = ""
	extra := detailTestPayerChargeObservation(t, "obs-cost-aleg-inclusive", "stream-cost-aleg-inclusive", 1, subject, payerTestOperator)
	in.Observations = append(in.Observations, extra)
	costCoverageAddInclusiveEdgeToSelectedLeg(t, in, storeID, extra)
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Coverage.CostCoverage.Complete)
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
}

// TestAssembleEconomicDetailCostCoverageSingleBLegStaysComplete preserves the
// previously valid single-B-leg complete case: one authoritative selected head
// over the only attributable subject remains a complete margin.
func TestAssembleEconomicDetailCostCoverageSingleBLegStaysComplete(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.Selection = nil

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.True(t, got.Coverage.CostCoverage.Complete)
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
}

// Phase 16 latest Finding 2 core contract: monetary coverage is proven only by
// the authoritative selected result and exact inclusive charge identity edges.
// Shared B-leg attribution is ownership, never inclusion: a distinct
// provider-charge event on a selected B-leg, an unselected candidate source,
// and a valuation input observation are all participation, not proof that the
// selected amount contains them.

// costCoverageProviderChargeSubject derives one exact provider-charge subject
// from a B-leg subject so a distinct provider-charge event can be attributed to
// the same B-leg without collapsing its monetary identity into the B-leg.
func costCoverageProviderChargeSubject(base metering.SubjectRef, providerChargeID string) metering.SubjectRef {
	subject := base.Clone()
	subject.Kind = metering.SubjectProviderCharge
	subject.ProviderChargeID = providerChargeID
	subject.ProviderAccountKey = "acct-provider"
	return subject
}

// costCoverageSubjectState returns the projected coverage state of the first
// subject matching the predicate.
func costCoverageSubjectState(t *testing.T, detail EconomicDetail, match func(EconomicDetailCostSubject) bool) (EconomicDetailCostCoverageState, bool) {
	t.Helper()
	require.NotNil(t, detail.Coverage.CostCoverage)
	for _, subject := range detail.Coverage.CostCoverage.Subjects {
		if match(subject) {
			return subject.State, true
		}
	}
	return "", false
}

// TestAssembleEconomicDetailCostCoverageSameBLegProviderChargeIncomplete is the
// exact review reproduction: a distinct provider-charge event on the selected
// B-leg, with no valuation, head or inclusive edge, must stay unresolved
// instead of inheriting coverage solely from the owning B-leg.
func TestAssembleEconomicDetailCostCoverageSameBLegProviderChargeIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	selected := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")
	chargeSubject := costCoverageProviderChargeSubject(selected, "pc-same-bleg")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	in.Observations = append(in.Observations,
		costCoverageChargeObservation(t, "obs-pc-same-bleg", "stream-pc-same-bleg", 1, chargeSubject, "0.25", payerTestOperator, nil))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "a distinct provider-charge event on the selected B-leg is not proven included")
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.Equal(t, 1, got.Coverage.CostCoverage.UnresolvedCount)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool {
		return s.Subject.ProviderChargeID == "pc-same-bleg"
	})
	require.True(t, found, "the provider-charge identity must remain its own subject")
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailCostCoverageProviderChargeAdditiveChildIncomplete
// proves an explicit additive relation does not become inclusion: the selected
// B-leg's charge declares the provider-charge child separately payable, so the
// child stays unresolved.
func TestAssembleEconomicDetailCostCoverageProviderChargeAdditiveChildIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	selected := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")
	chargeSubject := costCoverageProviderChargeSubject(selected, "pc-additive-child")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	child := costCoverageChargeObservation(t, "obs-pc-additive-child", "stream-pc-additive-child", 1, chargeSubject, "0.25", payerTestOperator, nil)
	in.Observations = append(in.Observations, child)
	for i := range in.Observations {
		if in.Observations[i].ID != "obs-provider-money-1" {
			continue
		}
		require.NotEmpty(t, in.Observations[i].Charges)
		in.Observations[i].Charges[0].Covers = []metering.ChargeCoverageRef{{
			Ref: metering.ChargeRef{
				StoreID: storeID, ObservationID: child.ID, Revision: child.Revision,
				ChargeItemID: child.Charges[0].ChargeItemID,
			},
			Relation: metering.CoverageAdditive,
		}}
	}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "an additive child is separately payable, not included")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool {
		return s.Subject.ProviderChargeID == "pc-additive-child"
	})
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailCostCoverageSiblingChargesOnlyIncludedCovered proves
// that resolving one exact inclusive charge reference cannot promote an
// independent sibling charge sharing the same B-leg ownership unit.
func TestAssembleEconomicDetailCostCoverageSiblingChargesOnlyIncludedCovered(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	sibling := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-siblings")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	good := costCoverageChargeObservation(t, "obs-sibling-good", "stream-sibling-good", 1, sibling, "0.10", payerTestOperator, nil)
	bad := costCoverageChargeObservation(t, "obs-sibling-bad", "stream-sibling-bad", 1, sibling, "0.20", payerTestOperator, nil)
	in.Observations = append(in.Observations, good, bad)
	for i := range in.Observations {
		if in.Observations[i].ID != "obs-provider-money-1" {
			continue
		}
		in.Observations[i].Charges[0].Covers = []metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, good)}
	}
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "the uncovered sibling charge must keep the margin incomplete")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.Equal(t, 1, got.Coverage.CostCoverage.UnresolvedCount)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool { return s.Subject.BLegID == "b-cost-siblings" })
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailCostCoverageTransitiveExactIdentityBounded proves
// inclusive propagation follows exact charge/revision identity edges from a
// covered charge through an intermediate charge to a further charge, while an
// unrelated sibling charge on the intermediate B-leg is never promoted.
func TestAssembleEconomicDetailCostCoverageTransitiveExactIdentityBounded(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	intermediate := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-tx-2")
	target := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-tx-3")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	c := costCoverageChargeObservation(t, "obs-tx-c", "stream-tx-c", 1, target, "0.30", payerTestOperator, nil)
	b := costCoverageChargeObservation(t, "obs-tx-b", "stream-tx-b", 1, intermediate, "0.10", payerTestOperator,
		[]metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, c)})
	b2 := costCoverageChargeObservation(t, "obs-tx-b2", "stream-tx-b2", 1, intermediate, "0.20", payerTestOperator, nil)
	in.Observations = append(in.Observations, b, b2, c)
	for i := range in.Observations {
		if in.Observations[i].ID != "obs-provider-money-1" {
			continue
		}
		in.Observations[i].Charges[0].Covers = []metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, b)}
	}
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "the unrelated sibling charge is not proven included")
	require.Equal(t, 1, got.Coverage.CostCoverage.UnresolvedCount)
	intermediateState, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool { return s.Subject.BLegID == "b-cost-tx-2" })
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, intermediateState, "one uncovered sibling keeps its owning unit unresolved")
	targetState, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool { return s.Subject.BLegID == "b-cost-tx-3" })
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageInclusive, targetState, "exact transitive identity edges still propagate inclusive coverage")
}

// TestAssembleEconomicDetailCostCoverageUnselectedCandidateSourceRefUnresolved
// proves only the authoritative selected result carries coverage: an unselected
// candidate's source reference is participation, not inclusion.
func TestAssembleEconomicDetailCostCoverageUnselectedCandidateSourceRefUnresolved(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	other := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-unselected")

	in := detailTestCompleteInput(t)
	require.NotNil(t, in.Selection)
	extra := costCoverageChargeObservation(t, "obs-cost-unselected", "stream-cost-unselected", 1, other, "0.33", payerTestOperator, nil)
	in.Observations = append(in.Observations, extra)
	ref, err := extra.Ref(storeID)
	require.NoError(t, err)
	qAmount := toleranceAmount(t, "USD", "1.10")
	in.Selection.Candidates = append(in.Selection.Candidates, OperatorCostCandidate{
		Basis: OperatorCostBasisQ, ValuationID: "valuation-q", ValuationVersion: 1,
		Currency: "USD", Amount: &qAmount, Completeness: economics.CompletenessComplete,
		Payer:      payerTestOperator,
		SourceRefs: []metering.ObservationRef{ref},
	})

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "an unselected alternative's source is not proof of inclusion")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool { return s.Subject.BLegID == "b-cost-unselected" })
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailCostCoverageValuationInputObservationNotInclusion
// proves a valuation input observation is participation, not monetary
// inclusion: only an explicit inclusive coverage relation may cover a charge.
func TestAssembleEconomicDetailCostCoverageValuationInputObservationNotInclusion(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	other := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-input-only")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	extra := costCoverageChargeObservation(t, "obs-cost-input-only", "stream-cost-input-only", 1, other, "0.44", payerTestOperator, nil)
	in.Observations = append(in.Observations, extra)
	ref, err := extra.Ref(storeID)
	require.NoError(t, err)
	for i := range in.Valuations {
		if in.Valuations[i].ID != "valuation-p" {
			continue
		}
		in.Valuations[i].InputObservations = append(in.Valuations[i].InputObservations, ref)
	}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "a valuation input observation is participation, not inclusion")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool { return s.Subject.BLegID == "b-cost-input-only" })
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailCostCoverageProviderChargeInclusiveControl proves
// the fix does not over-correct: a distinct provider-charge event proven
// contained by an exact inclusive edge keeps the margin complete.
func TestAssembleEconomicDetailCostCoverageProviderChargeInclusiveControl(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	selected := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")
	chargeSubject := costCoverageProviderChargeSubject(selected, "pc-inclusive")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	extra := costCoverageChargeObservation(t, "obs-pc-inclusive", "stream-pc-inclusive", 1, chargeSubject, "0.25", payerTestOperator, nil)
	in.Observations = append(in.Observations, extra)
	for i := range in.Observations {
		if in.Observations[i].ID != "obs-provider-money-1" {
			continue
		}
		in.Observations[i].Charges[0].Covers = []metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, extra)}
	}
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Coverage.CostCoverage.Complete)
	require.True(t, got.Margin.Complete)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool {
		return s.Subject.ProviderChargeID == "pc-inclusive"
	})
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageInclusive, state)
}

// Phase 16 fifth-pass Finding 1 core contract: an exact selected charge atom is
// proven by the selected valuation's payable line/source identity, and only
// then may its inclusive edges prove siblings. A selected B-leg still covers its
// base execution, but every other separately payable charge atom on that same
// B-leg stays unresolved until proven by exact identity.

// costCoverageSelectedLegSubject builds the canonical selected B-leg subject.
func costCoverageSelectedLegSubject(t *testing.T) metering.SubjectRef {
	t.Helper()
	storeID, _, aLegID, billingCallID := detailTestScope()
	return detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")
}

// costCoverageSurchargeCharge returns one independent nonzero operator surcharge
// charge item, the canonical separately payable auxiliary charge.
func costCoverageSurchargeCharge(t *testing.T, itemID string) metering.ReportedCharge {
	t.Helper()
	amount := detailTestDecimal(t, "0.25")
	return metering.ReportedCharge{
		ChargeItemID: itemID,
		Component: &metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		},
		Amount: &amount, Currency: "USD", Kind: metering.ChargeKindComponent,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
	}
}

// TestAssembleEconomicDetailCostCoverageSelectedLegExtraSameSubjectChargeIncomplete
// is the exact Finding 1 source path: a fresh operator-paid charge observation on
// the selected B-leg itself, lacking any inclusive proof, must keep the margin
// incomplete even though the base execution is selected.
func TestAssembleEconomicDetailCostCoverageSelectedLegExtraSameSubjectChargeIncomplete(t *testing.T) {
	t.Parallel()
	selected := costCoverageSelectedLegSubject(t)

	in := detailTestCompleteInput(t)
	in.Selection = nil
	in.Observations = append(in.Observations,
		costCoverageChargeObservation(t, "obs-cost-same-bleg-extra", "stream-cost-same-bleg-extra", 1, selected, "0.25", payerTestOperator, nil))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "an independent same-subject charge is not proven by selected base execution")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.Nil(t, got.Margin.Amount)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.Equal(t, 1, got.Coverage.CostCoverage.UnresolvedCount)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool { return s.Subject.BLegID == "b-detail-1" })
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailCostCoverageSelectedLegSiblingChargeItemIncomplete
// proves one selected charge item never promotes an independent sibling item in
// the same selected B-leg observation.
func TestAssembleEconomicDetailCostCoverageSelectedLegSiblingChargeItemIncomplete(t *testing.T) {
	t.Parallel()
	surcharge := costCoverageSurchargeCharge(t, "charge-p-1-surcharge")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	found := false
	for i := range in.Observations {
		if in.Observations[i].ID != "obs-provider-money-1" {
			continue
		}
		in.Observations[i].Charges = append(in.Observations[i].Charges, surcharge)
		found = true
	}
	require.True(t, found, "canonical selected money observation must exist")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "an unproven sibling charge item keeps the margin incomplete")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.Equal(t, 1, got.Coverage.CostCoverage.UnresolvedCount)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool { return s.Subject.BLegID == "b-detail-1" })
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailCostCoverageSelectedLegStaleRevisionIncomplete
// proves a stale/unselected revision source on the selected B-leg cannot prove
// downstream inclusion indirectly: its outgoing inclusive edge to a later
// charge must not fire because the stale source atom is not itself covered.
func TestAssembleEconomicDetailCostCoverageSelectedLegStaleRevisionIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	selected := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	target := costCoverageChargeObservation(t, "obs-cost-stale-target", "stream-cost-stale-target", 1, selected, "0.30", payerTestOperator, nil)
	stale := costCoverageChargeObservation(t, "obs-provider-money-later", "stream-provider-money-later", 3, selected, "0.20", payerTestOperator,
		[]metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, target)})
	stale.Revision = 2
	in.Observations = append(in.Observations, target, stale)

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "a stale source edge must not prove a later charge")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.Equal(t, 1, got.Coverage.CostCoverage.UnresolvedCount)
}

// TestAssembleEconomicDetailCostCoverageSelectedAtomInclusiveSiblingsComplete is
// the control: once the exact selected atom is proven by the selected line, its
// explicit inclusive edge proves a same-subject sibling and the margin stays
// complete.
func TestAssembleEconomicDetailCostCoverageSelectedAtomInclusiveSiblingsComplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	selected := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-detail-1"}

	in := detailTestCompleteInput(t)
	in.Selection = nil
	extra := costCoverageChargeObservation(t, "obs-cost-same-bleg-inclusive", "stream-cost-same-bleg-inclusive", 1, selected, "0.25", payerTestOperator, nil)
	in.Observations = append(in.Observations, extra)
	for i := range in.Observations {
		if in.Observations[i].ID != "obs-provider-money-1" {
			continue
		}
		require.NotEmpty(t, in.Observations[i].Charges)
		in.Observations[i].Charges[0].Covers = []metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, extra)}
	}
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Coverage.CostCoverage.Complete)
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
}

// TestAssembleEconomicDetailCostCoverageSelectedBaseExecutionOnlyComplete proves
// a selected B-leg carrying only base execution (measures, no charge atom) stays
// complete without any monetary inclusion proof.
func TestAssembleEconomicDetailCostCoverageSelectedBaseExecutionOnlyComplete(t *testing.T) {
	t.Parallel()

	in := detailTestCompleteInput(t)
	in.Selection = nil
	filtered := make([]metering.Observation, 0, len(in.Observations))
	for _, observation := range in.Observations {
		if observation.ID == "obs-provider-money-1" {
			continue
		}
		filtered = append(filtered, observation)
	}
	require.NotEqual(t, len(in.Observations), len(filtered), "the money charge observation must be removed")
	in.Observations = filtered

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.True(t, got.Coverage.CostCoverage.Complete, "base execution coverage needs no charge-atom proof")
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
}

// Phase 16 sixth-pass Finding 2 core contract: a complete margin requires a
// canonically valid, supersession-reduced coverage graph over exact charge
// atoms. Cycles, unavailable targets, ambiguous inclusive parents and stale
// superseded sources must fail closed instead of proving inclusion.

// costCoverageChargeRefFor returns one exact inclusive coverage edge to a named
// charge item of one observation revision.
func costCoverageChargeRefFor(storeID, observationID string, revision uint64, itemID string) metering.ChargeCoverageRef {
	return metering.ChargeCoverageRef{
		Ref: metering.ChargeRef{
			StoreID: storeID, ObservationID: observationID, Revision: revision, ChargeItemID: itemID,
		},
		Relation: metering.CoverageInclusive,
	}
}

// costCoverageSetChargeCovers replaces the Covers edges of one exact charge item
// in the assembly input.
func costCoverageSetChargeCovers(t *testing.T, in *EconomicDetailInput, observationID, itemID string, covers ...metering.ChargeCoverageRef) {
	t.Helper()
	for i := range in.Observations {
		if in.Observations[i].ID != observationID {
			continue
		}
		for j := range in.Observations[i].Charges {
			if in.Observations[i].Charges[j].ChargeItemID != itemID {
				continue
			}
			in.Observations[i].Charges[j].Covers = covers
			return
		}
	}
	t.Fatalf("observation %s charge %s not found", observationID, itemID)
}

// costCoverageAddSelectedAtom appends one payable charge item to an existing
// selected observation and names it with a provider-reported valuation line so
// the selected result proves that exact atom.
func costCoverageAddSelectedAtom(t *testing.T, in *EconomicDetailInput, valuationID, observationID, itemID string) {
	t.Helper()
	for i := range in.Observations {
		if in.Observations[i].ID != observationID {
			continue
		}
		amount := detailTestDecimal(t, "0.10")
		chargeAmount := amount
		charge := metering.ReportedCharge{
			ChargeItemID: itemID,
			Component: &metering.ComponentKey{
				Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
				Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
			},
			Amount: &chargeAmount, Currency: "USD", Kind: metering.ChargeKindComponent,
			Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
		}
		in.Observations[i].Charges = append(in.Observations[i].Charges, charge)
		obsRef, err := in.Observations[i].Ref(in.Observations[i].Subject.StoreID)
		require.NoError(t, err)
		nanos, err := amount.ToNanoUnits()
		require.NoError(t, err)
		for j := range in.Valuations {
			if in.Valuations[j].ID != valuationID {
				continue
			}
			component := charge.Component.Clone()
			in.Valuations[j].Lines = append(in.Valuations[j].Lines, economics.LineItem{
				ID: "line-p-" + itemID, RuleID: "provider_reported", ItemID: itemID,
				Component: &component, Unit: metering.UnitToken,
				Amount:        &amount,
				RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven,
				RoundedAmount:         &economics.Money{NanoUnits: nanos, Currency: "USD", Present: true},
				Status:                economics.RatingLineProviderReported,
				SourceObservationRefs: []metering.ObservationRef{obsRef},
			})
			require.NoError(t, in.Valuations[j].Validate())
		}
		return
	}
	t.Fatalf("observation %s not found", observationID)
}

// TestAssembleEconomicDetailCostCoverageInclusiveCycleIncomplete is the exact
// review reproduction: distinct charges A and B declare each other inclusively
// contained. Each observation validates in isolation, but the canonical graph
// rejects the cycle, so no complete margin may be produced.
func TestAssembleEconomicDetailCostCoverageInclusiveCycleIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-cycle")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	b := costCoverageChargeObservation(t, "obs-cost-cycle", "stream-cost-cycle", 1, subject, "0.42", payerTestOperator, nil)
	in.Observations = append(in.Observations, b)
	costCoverageSetChargeCovers(t, &in, "obs-provider-money-1", "charge-p-1", costCoverageInclusiveChargeRef(storeID, b))
	costCoverageSetChargeCovers(t, &in, b.ID, b.Charges[0].ChargeItemID,
		costCoverageChargeRefFor(storeID, "obs-provider-money-1", 1, "charge-p-1"))
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "a coverage cycle cannot prove inclusion")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete)
}

// TestAssembleEconomicDetailCostCoverageDanglingTargetIncomplete proves a
// selected charge whose inclusive edge names an absent exact target is not
// silently ignored: the missing target fails the coverage graph closed instead
// of leaving an otherwise complete margin.
func TestAssembleEconomicDetailCostCoverageDanglingTargetIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, _, _ := detailTestScope()

	in := detailTestCompleteInput(t)
	in.Selection = nil
	costCoverageSetChargeCovers(t, &in, "obs-provider-money-1", "charge-p-1",
		costCoverageChargeRefFor(storeID, "obs-cost-missing", 1, "charge-missing"))
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "a coverage edge to an unavailable exact target cannot prove inclusion")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete)
}

// TestAssembleEconomicDetailCostCoverageAmbiguousInclusiveParentsIncomplete
// proves two independently selected charge atoms may not both inclusively own
// the same child: the canonical graph rejects ambiguous inclusive parents, so
// the cycle-free but conflicting graph cannot produce a complete margin.
func TestAssembleEconomicDetailCostCoverageAmbiguousInclusiveParentsIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	childSubject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-amb-child")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	child := costCoverageChargeObservation(t, "obs-cost-amb-child", "stream-cost-amb-child", 1, childSubject, "0.42", payerTestOperator, nil)
	in.Observations = append(in.Observations, child)
	costCoverageAddSelectedAtom(t, &in, "valuation-p", "obs-provider-money-1", "charge-p-2")
	costCoverageSetChargeCovers(t, &in, "obs-provider-money-1", "charge-p-1", costCoverageInclusiveChargeRef(storeID, child))
	costCoverageSetChargeCovers(t, &in, "obs-provider-money-1", "charge-p-2", costCoverageInclusiveChargeRef(storeID, child))
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "ambiguous inclusive parents cannot prove coverage")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.False(t, got.Coverage.CostCoverage.Complete)
}

// TestAssembleEconomicDetailCostCoverageSupersededSourceIncomplete proves a
// superseded charge observation may not supply inclusion for a current target
// after its authoritative same-item replacement omits the coverage edge. The
// stale edge is reduced away, so the current target stays unresolved.
func TestAssembleEconomicDetailCostCoverageSupersededSourceIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	staleSubject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-superseded")
	targetSubject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-superseded-target")

	in := detailTestCompleteInput(t)
	in.Selection = nil

	stale := costCoverageChargeObservation(t, "obs-cost-superseded-a1", "stream-cost-superseded", 1, staleSubject, "0.30", payerTestOperator, nil)
	target := costCoverageChargeObservation(t, "obs-cost-superseded-target", "stream-cost-superseded-target", 1, targetSubject, "0.40", payerTestOperator, nil)
	stale.Charges[0].Covers = []metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, target)}

	replacement := stale
	replacement.ID = "obs-cost-superseded-a2"
	replacement.SourceEventKey = replacement.ID + "-event"
	replacement.Revision = 2
	replacement.Semantics = metering.SemanticsReplacement
	staleRef, err := stale.Ref(storeID)
	require.NoError(t, err)
	replacement.Supersedes = []metering.ObservationRef{staleRef}
	replacement.Charges = []metering.ReportedCharge{stale.Charges[0]}
	replacement.Charges[0].Covers = nil
	require.NoError(t, replacement.Validate())

	in.Observations = append(in.Observations, stale, target, replacement)
	costCoverageSetChargeCovers(t, &in, "obs-provider-money-1", "charge-p-1",
		costCoverageInclusiveChargeRef(storeID, stale),
		costCoverageInclusiveChargeRef(storeID, replacement),
	)
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "a superseded source edge cannot prove a current target")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.False(t, got.Coverage.CostCoverage.Complete)
}

// TestAssembleEconomicDetailCostCoverageReverseAdditiveEdgeIncomplete proves a
// reverse edge declared by an uncovered source, and every additive relation,
// remains participation rather than inclusion.
func TestAssembleEconomicDetailCostCoverageReverseAdditiveEdgeIncomplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-reverse")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	reverse := costCoverageChargeObservation(t, "obs-cost-reverse", "stream-cost-reverse", 1, subject, "0.42", payerTestOperator, []metering.ChargeCoverageRef{{
		Ref:      metering.ChargeRef{StoreID: storeID, ObservationID: "obs-provider-money-1", Revision: 1, ChargeItemID: "charge-p-1"},
		Relation: metering.CoverageAdditive,
	}})
	in.Observations = append(in.Observations, reverse)

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "a reverse additive edge from an uncovered source proves nothing")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
}

// Phase 16 latest Finding 3 core contract: a zero or exempt-looking charge may
// not discard canonical evidence authority. Only an authoritative provider
// claim can prove a known zero; a local/estimated claim of exactly zero stays
// unresolved. An out-of-band zero is not proof of no exposure.

// costCoverageLocalEstimatedZeroObservation builds one local estimator claim
// with an exact zero operator amount: a valid estimated claim, never an
// authoritative provider zero.
func costCoverageLocalEstimatedZeroObservation(t *testing.T, id, stream string, subject metering.SubjectRef) metering.Observation {
	t.Helper()
	zero := detailTestDecimal(t, "0")
	charge := metering.ReportedCharge{
		ChargeItemID: id + "-charge",
		Component: &metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		},
		Amount: &zero, Currency: "USD", Kind: metering.ChargeKindComponent,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
	}
	observation := detailTestObservation(t, id, metering.OriginLocal, stream, 1, subject, nil, []metering.ReportedCharge{charge})
	observation.Acquisition = metering.AcquisitionLocalEstimator
	observation.Authority = metering.AuthorityEstimatedClaim
	require.NoError(t, observation.Validate())
	return observation
}

// TestAssembleEconomicDetailCostCoverageLocalEstimatedZeroStaysUnresolved proves
// a local estimated zero on an independent unselected B-leg cannot prove a
// known zero: estimated money is advisory, so completeness stays open.
func TestAssembleEconomicDetailCostCoverageLocalEstimatedZeroStaysUnresolved(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-local-zero")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	in.Observations = append(in.Observations,
		costCoverageLocalEstimatedZeroObservation(t, "obs-cost-local-zero", "stream-cost-local-zero", subject))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "a local estimated zero cannot prove known-zero")
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, got.Coverage.CostCoverage.UnresolvedCount)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool { return s.Subject.BLegID == "b-cost-local-zero" })
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailCostCoverageAuthoritativeProviderZeroKnownZero is the
// canonical policy control: an authoritative provider claim of exactly zero does
// prove a known zero and keeps the margin complete.
func TestAssembleEconomicDetailCostCoverageAuthoritativeProviderZeroKnownZero(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-provider-zero")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	in.Observations = append(in.Observations,
		costCoverageChargeObservation(t, "obs-cost-provider-zero", "stream-cost-provider-zero", 1, subject, "0", payerTestOperator, nil))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Margin.Complete, "an authoritative provider zero proves a known-zero cost")
	require.NotNil(t, got.Margin.Amount)
	state, found := costCoverageSubjectState(t, got, func(s EconomicDetailCostSubject) bool { return s.Subject.BLegID == "b-cost-provider-zero" })
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageKnownZero, state)
}

// TestAssembleEconomicDetailCostCoverageValidInclusiveChainComplete is the
// control: a valid acyclic exact-identity inclusive chain still proves coverage
// and keeps the margin complete.
func TestAssembleEconomicDetailCostCoverageValidInclusiveChainComplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	midSubject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-chain-mid")
	endSubject := detailTestSubject(storeID, aLegID, billingCallID, "b-cost-chain-end")

	in := detailTestCompleteInput(t)
	in.Selection = nil
	end := costCoverageChargeObservation(t, "obs-chain-end", "stream-chain-end", 1, endSubject, "0.20", payerTestOperator, nil)
	mid := costCoverageChargeObservation(t, "obs-chain-mid", "stream-chain-mid", 1, midSubject, "0.10", payerTestOperator,
		[]metering.ChargeCoverageRef{costCoverageInclusiveChargeRef(storeID, end)})
	in.Observations = append(in.Observations, mid, end)
	costCoverageSetChargeCovers(t, &in, "obs-provider-money-1", "charge-p-1", costCoverageInclusiveChargeRef(storeID, mid))
	costCoverageRefreezeLineSources(t, &in, "obs-provider-money-1")

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Coverage.CostCoverage.Complete, "a valid inclusive chain proves coverage")
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
}
