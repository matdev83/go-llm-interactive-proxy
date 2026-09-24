package billing

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 latest Finding 1 core contract: an authoritative executed/attempted
// B-leg with no usable embedded economic evidence must not disappear from cost
// completeness. Execution facts are carried from the already-loaded leg records
// as provider-neutral identity/outcome/evidence-presence facts (no measurement,
// no amount, missing evidence is never zero). Only canonical never-started,
// nonbillable, known-zero or BYOK proof exempts a leg; attempted/possibly
// executed work without accepted evidence stays unresolved.

func executionCoverageLeg(t *testing.T, storeID, accountID, aLegID, billingCallID, bLegID string, outcome LegOutcome, evidence FinalBillingEvidence) EconomicDetailExecutionLeg {
	t.Helper()
	return NewEconomicDetailExecutionLeg(
		metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: accountID,
			ALegID: aLegID, BillingCallID: billingCallID, BLegID: bLegID,
		},
		2, outcome, SurfacedNo, evidence,
	)
}

func executionCoverageAcceptedEvidence() FinalBillingEvidence {
	return FinalBillingEvidence{
		InputTokens: Quantity{Value: 7, Present: true},
		Source:      EvidenceSourceProviderReported,
		Authority:   EvidenceAuthorityAuthoritative,
	}
}

// executionCoverageAbsentMeasureObservation builds one canonical provider-origin
// observation whose only measure is explicitly unavailable (no value), so the
// observation carries no usable economic evidence yet the B-leg still exists.
func executionCoverageAbsentMeasureObservation(t *testing.T, id string, subject metering.SubjectRef) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_030_000, 0).UTC()
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: "stream-" + id, Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject:   subject,
		Correlation: metering.CorrelationV2{
			StoreID: subject.StoreID, ALegID: subject.ALegID,
			BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now, ReceivedAt: now, MappingRef: "execution-coverage.test.v1",
		Measures: []metering.Measure{{
			Key: metering.ComponentKey{
				Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
				Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
			},
			Quality: metering.QualityUnavailable, Reason: "provider_not_reported",
		}},
	}
	require.NoError(t, observation.Validate())
	canonical, err := observation.Canonical()
	require.NoError(t, err)
	return canonical
}

func executionCostCoverageStateFor(t *testing.T, detail EconomicDetail, bLegID string) (EconomicDetailCostCoverageState, bool) {
	t.Helper()
	require.NotNil(t, detail.Coverage.CostCoverage)
	for _, subject := range detail.Coverage.CostCoverage.Subjects {
		if subject.Subject.BLegID == bLegID {
			return subject.State, true
		}
	}
	return "", false
}

// TestAssembleEconomicDetailExecutionCoverageObservationFreeLegIncomplete is the
// exact Finding 1 source-path reproduction at the core boundary: the singular
// complete selected result covers b-detail-1 only, while an authoritative
// executed second B-leg carries accepted provider evidence and no embedded
// observation at all. The margin must not stay complete.
func TestAssembleEconomicDetailExecutionCoverageObservationFreeLegIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	leg := executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-observation-free", LegOutcomeWinner, executionCoverageAcceptedEvidence())
	require.True(t, leg.AcceptedEvidence, "the authoritative leg carries accepted provider evidence")
	in.ExecutionCoverage = []EconomicDetailExecutionLeg{leg}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "an executed observation-free B-leg cannot coexist with a complete margin")
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, got.Coverage.CostCoverage.UnresolvedCount)
	state, found := executionCostCoverageStateFor(t, got, "b-exec-observation-free")
	require.True(t, found, "the executed leg must appear as an attributable cost subject")
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailExecutionCoverageEmptyEvidenceIncomplete proves an
// explicitly labelled V2 leg carrying no observations and no accepted evidence
// is still an attempted execution, not a reconciled zero.
func TestAssembleEconomicDetailExecutionCoverageEmptyEvidenceIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	leg := executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-empty-evidence", LegOutcomeWinner, FinalBillingEvidence{})
	require.False(t, leg.AcceptedEvidence)
	in.ExecutionCoverage = []EconomicDetailExecutionLeg{leg}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "attempted work with empty evidence is unknown, not zero")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	state, found := executionCostCoverageStateFor(t, got, "b-exec-empty-evidence")
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailExecutionCoverageAbsentMeasureIncomplete proves an
// observation whose only measure is absent/unavailable carries no usable
// economic evidence and cannot hide the executed B-leg.
func TestAssembleEconomicDetailExecutionCoverageAbsentMeasureIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-exec-absent-measure")
	in := detailTestCompleteInput(t)
	in.Observations = append(in.Observations, executionCoverageAbsentMeasureObservation(t, "obs-exec-absent", subject))
	in.ExecutionCoverage = []EconomicDetailExecutionLeg{
		executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-absent-measure", LegOutcomeWinner, executionCoverageAcceptedEvidence()),
	}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "an absent/unavailable-only observation cannot stand in for accepted evidence")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	state, found := executionCostCoverageStateFor(t, got, "b-exec-absent-measure")
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailExecutionCoverageNeverStartedAndNonbillableControl
// is the canonical distinction control: an authoritative never-started leg and a
// rejected (nonbillable) leg carry no operator exposure and cannot block
// completeness, while an attempted leg on the same scope does.
func TestAssembleEconomicDetailExecutionCoverageNeverStartedAndNonbillableControl(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	t.Run("never started is known zero exposure", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		in.ExecutionCoverage = []EconomicDetailExecutionLeg{
			executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-never-started", LegOutcomeNeverStarted, FinalBillingEvidence{}),
		}
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Margin.Complete)
		require.Equal(t, "complete", got.Margin.Reason)
		state, found := executionCostCoverageStateFor(t, got, "b-exec-never-started")
		require.True(t, found)
		require.Equal(t, EconomicDetailCostCoverageNeverStarted, state)
	})

	t.Run("rejected is nonbillable", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		in.ExecutionCoverage = []EconomicDetailExecutionLeg{
			executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-rejected", LegOutcomeRejected, FinalBillingEvidence{}),
		}
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Margin.Complete)
		state, found := executionCostCoverageStateFor(t, got, "b-exec-rejected")
		require.True(t, found)
		require.Equal(t, EconomicDetailCostCoverageNonbillable, state)
	})

	t.Run("attempted is unresolved", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		in.ExecutionCoverage = []EconomicDetailExecutionLeg{
			executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-attempted", LegOutcomeLoser, FinalBillingEvidence{}),
		}
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.False(t, got.Margin.Complete)
		require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	})
}

// TestAssembleEconomicDetailExecutionCoverageKnownZeroAndBYOKControl proves an
// explicit authoritative zero cost and an explicit customer-BYOK charge remain
// exempt with execution facts present.
func TestAssembleEconomicDetailExecutionCoverageKnownZeroAndBYOKControl(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	t.Run("authoritative zero cost", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		leg := executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-known-zero", LegOutcomeWinner, FinalBillingEvidence{
			Cost:      MoneyEvidence{NanoUnits: 0, Currency: "USD", Present: true},
			Authority: EvidenceAuthorityAuthoritative,
			Source:    EvidenceSourceProviderReported,
		})
		require.True(t, leg.AuthoritativeZeroCost)
		in.ExecutionCoverage = []EconomicDetailExecutionLeg{leg}
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Margin.Complete)
		state, found := executionCostCoverageStateFor(t, got, "b-exec-known-zero")
		require.True(t, found)
		require.Equal(t, EconomicDetailCostCoverageKnownZero, state)
	})

	t.Run("customer BYOK observation governs", func(t *testing.T) {
		t.Parallel()
		subject := detailTestSubject(storeID, aLegID, billingCallID, "b-exec-byok")
		in := detailTestCompleteInput(t)
		in.Observations = append(in.Observations,
			costCoverageChargeObservation(t, "obs-exec-byok", "stream-exec-byok", 1, subject, "9.99", payerTestCustomer, nil))
		in.ExecutionCoverage = []EconomicDetailExecutionLeg{
			executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-byok", LegOutcomeWinner, executionCoverageAcceptedEvidence()),
		}
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Margin.Complete)
		state, found := executionCostCoverageStateFor(t, got, "b-exec-byok")
		require.True(t, found)
		require.Equal(t, EconomicDetailCostCoverageBYOK, state)
	})
}

// TestAssembleEconomicDetailExecutionCoverageScopeEnforced proves a foreign
// execution fact fails closed instead of leaking foreign execution into the
// scope's completeness.
func TestAssembleEconomicDetailExecutionCoverageScopeEnforced(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	for _, tc := range []struct {
		name   string
		mutate func(leg *EconomicDetailExecutionLeg)
	}{
		{name: "foreign store", mutate: func(leg *EconomicDetailExecutionLeg) { leg.Subject.StoreID = "foreign-store" }},
		{name: "foreign account", mutate: func(leg *EconomicDetailExecutionLeg) { leg.Subject.AccountID = "foreign-account" }},
		{name: "foreign call", mutate: func(leg *EconomicDetailExecutionLeg) {
			leg.Subject.BillingCallID = "bc_" + "9" + strings.Repeat("a", 31)
		}},
		{name: "foreign A-leg", mutate: func(leg *EconomicDetailExecutionLeg) { leg.Subject.ALegID = "foreign-aleg" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := detailTestCompleteInput(t)
			leg := executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-scope", LegOutcomeWinner, executionCoverageAcceptedEvidence())
			tc.mutate(&leg)
			in.ExecutionCoverage = []EconomicDetailExecutionLeg{leg}
			_, err := AssembleEconomicDetail(in)
			require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
		})
	}
}

// TestAssembleEconomicDetailExecutionCoverageBoundFails proves the shared
// leg-record budget is enforced before any growth.
func TestAssembleEconomicDetailExecutionCoverageBoundFails(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.ExecutionCoverage = make([]EconomicDetailExecutionLeg, MaxEconomicDetailExecutionLegs+1)
	_, err := AssembleEconomicDetail(in)
	require.ErrorIs(t, err, ErrEconomicDetailBoundExceeded)
}

// executionCoverageAuthoritativeCostEvidence builds an authoritative
// provider-reported nonzero cost: the canonical payable provider money that
// RateProviderCost checks before any outcome-only exemption.
func executionCoverageAuthoritativeCostEvidence(nanoUnits int64) FinalBillingEvidence {
	return FinalBillingEvidence{
		Cost:      MoneyEvidence{NanoUnits: nanoUnits, Currency: "USD", Present: true},
		Source:    EvidenceSourceProviderReported,
		Authority: EvidenceAuthorityAuthoritative,
	}
}

// TestAssembleEconomicDetailExecutionCoverageRejectedAuthoritativeNonzeroIncomplete
// proves the canonical RateProviderCost precedence: an accepted authoritative
// nonzero provider cost is payable and cannot be hidden behind a rejected
// outcome label.
func TestAssembleEconomicDetailExecutionCoverageRejectedAuthoritativeNonzeroIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	leg := executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-rejected-cost", LegOutcomeRejected, executionCoverageAuthoritativeCostEvidence(250_000_000))
	require.True(t, leg.AcceptedEvidence, "authoritative cost is accepted evidence")
	require.False(t, leg.AuthoritativeZeroCost, "a nonzero authoritative cost is not a known zero")
	in.ExecutionCoverage = []EconomicDetailExecutionLeg{leg}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "accepted authoritative nonzero cost outranks a rejected outcome")
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	state, found := executionCostCoverageStateFor(t, got, "b-exec-rejected-cost")
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailExecutionCoverageNeverStartedAuthoritativeNonzeroIncomplete
// proves the precedence is not specific to a rejected outcome: an accepted
// authoritative nonzero provider cost also outranks a never-started label.
func TestAssembleEconomicDetailExecutionCoverageNeverStartedAuthoritativeNonzeroIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	leg := executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-never-started-cost", LegOutcomeNeverStarted, executionCoverageAuthoritativeCostEvidence(120_000_000))
	require.True(t, leg.AcceptedEvidence)
	require.False(t, leg.AuthoritativeZeroCost)
	in.ExecutionCoverage = []EconomicDetailExecutionLeg{leg}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "accepted authoritative nonzero cost outranks a never-started outcome")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	state, found := executionCostCoverageStateFor(t, got, "b-exec-never-started-cost")
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailExecutionCoverageAcceptedQuantityOutranksOutcome
// proves any accepted provider evidence, not only an authoritative cost, keeps
// the outcome-only exemption from applying: accepted quantity evidence is
// exposure the selected result must cover.
func TestAssembleEconomicDetailExecutionCoverageAcceptedQuantityOutranksOutcome(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	leg := executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-rejected-qty", LegOutcomeRejected, executionCoverageAcceptedEvidence())
	require.True(t, leg.AcceptedEvidence)
	require.False(t, leg.AuthoritativeZeroCost)
	in.ExecutionCoverage = []EconomicDetailExecutionLeg{leg}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "accepted provider quantity evidence cannot be exempted by a rejected outcome")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	state, found := executionCostCoverageStateFor(t, got, "b-exec-rejected-qty")
	require.True(t, found)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, state)
}

// TestAssembleEconomicDetailExecutionCoverageSnapshotInvalidates proves even an
// exempt execution fact participates in the repeated full-scope snapshot, so a
// leg added between pages invalidates an outstanding continuation.
func TestAssembleEconomicDetailExecutionCoverageSnapshotInvalidates(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	before, err := AssembleEconomicDetail(detailTestCompleteInput(t))
	require.NoError(t, err)

	in := detailTestCompleteInput(t)
	in.ExecutionCoverage = []EconomicDetailExecutionLeg{
		executionCoverageLeg(t, storeID, accountID, aLegID, billingCallID, "b-exec-snapshot", LegOutcomeNeverStarted, FinalBillingEvidence{}),
	}
	after, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotEqual(t, before.SnapshotFingerprint, after.SnapshotFingerprint,
		"an execution fact added between pages must change the full-scope snapshot fingerprint")
}
