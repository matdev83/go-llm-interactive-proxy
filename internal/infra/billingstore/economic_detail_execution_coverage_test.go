package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 latest Finding 1 durable boundary proof: QueryEconomicDetail must
// carry the authoritative executed B-leg facts it already loaded, so an executed
// leg with no usable embedded observation (observation-free, explicit V2 empty
// evidence or absent/unavailable-only measures) cannot disappear from cost
// completeness. Only canonical never-started/nonbillable/known-zero/BYOK proof
// exempts a leg.

// edTestUnavailableObservation builds one canonical provider-origin observation
// whose only measure is explicitly unavailable, so the B-leg has an observation
// that carries no usable economic evidence.
func edTestUnavailableObservation(t *testing.T, id string, subject metering.SubjectRef) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_020_500, 0).UTC()
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: "stream-" + id, Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject:   subject,
		Correlation: metering.CorrelationV2{
			StoreID: subject.StoreID, TenantID: subject.TenantID, ALegID: subject.ALegID,
			BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now, ReceivedAt: now, MappingRef: "execution-coverage.store.test.v1",
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

// edSetupExecutionCoverageCall persists one complete selected leg plus a second
// leg the mutator shapes (no observations, explicit V2 empty evidence, absent
// measures, or an exempt outcome). It returns the call identity and both
// subjects, mirroring edSetupCostCoverageTwoLegCall without an observations
// builder.
func edSetupExecutionCoverageCall(t *testing.T, store *DurableStore, accountID, aLegID, prefix string, mutate func(subjectTwo metering.SubjectRef, legTwo *billing.CallLegUsageRecord)) (billing.BillingCallID, metering.SubjectRef, metering.SubjectRef) {
	t.Helper()
	ctx := context.Background()
	callID := edTestCallID(t)
	subjectOne := edTestBLegSubject(store.StoreID(), edSupTenant, accountID, aLegID, callID.String(), "b-"+prefix+"-1")
	subjectTwo := edTestBLegSubject(store.StoreID(), edSupTenant, accountID, aLegID, callID.String(), "b-"+prefix+"-2")

	moneyCharge := edTestCharge(t, "charge-"+prefix+"-1", "1.32", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	local := edTestObservation(t, "obs-"+prefix+"-1-local", metering.OriginLocal, "stream-"+prefix+"-1-local", 1, subjectOne,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	provider := edTestObservation(t, "obs-"+prefix+"-1-provider", metering.OriginProvider, "stream-"+prefix+"-1-provider", 1, subjectOne,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	moneyObs := edTestObservation(t, "obs-"+prefix+"-1-money", metering.OriginProvider, "stream-"+prefix+"-1-money", 2, subjectOne, nil, []metering.ReportedCharge{moneyCharge})

	legOne := edTestLeg(t, subjectOne.BLegID, local, provider, moneyObs)
	legTwo := edTestLeg(t, subjectTwo.BLegID)
	legTwo.AttemptSeq = 2
	if mutate != nil {
		mutate(subjectTwo, &legTwo)
	}
	edSetupCall(t, store, accountID, callID, aLegID, legOne, legTwo)

	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), local),
		edObservationRef(t, store.StoreID(), provider),
		edObservationRef(t, store.StoreID(), moneyObs),
	}
	for _, valuation := range []economics.Valuation{
		edTestValuation(t, "val-"+prefix+"-e", economics.BasisLocalExpected, subjectOne, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, "val-"+prefix+"-q", economics.BasisProviderQuantityLocal, subjectOne, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuationWithReportedLine(t, "val-"+prefix+"-p", economics.BasisProviderReported, subjectOne, refs, store.StoreID(), moneyCharge, moneyObs, edTestCurrencyTotal(t, "USD", "1.32")),
		edTestValuation(t, "val-"+prefix+"-r", economics.BasisCustomerPolicy, subjectOne, refs, edTestCurrencyTotal(t, "USD", "2.00")),
	} {
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}
	edTestPostSelectedHead(t, store, accountID, callID, subjectOne, "head-"+prefix,
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	return callID, subjectOne, subjectTwo
}

func edExecutionCoverageStateFor(t *testing.T, detail billing.EconomicDetail, bLegID string) (billing.EconomicDetailCostCoverageState, bool) {
	t.Helper()
	require.NotNil(t, detail.Coverage.CostCoverage)
	for _, subject := range detail.Coverage.CostCoverage.Subjects {
		if subject.Subject.BLegID == bLegID {
			return subject.State, true
		}
	}
	return "", false
}

func edQueryExecutionCoverageBothScopes(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, aLegID string) (billing.EconomicDetail, billing.EconomicDetail) {
	t.Helper()
	ctx := context.Background()
	callDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: accountID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	alegDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: accountID, ALegID: aLegID,
	})
	require.NoError(t, err)
	return callDetail, alegDetail
}

// TestQueryEconomicDetailExecutionCoverageObservationFreeLegIncomplete is the
// durable reproduction of the review's exact source path: the second persisted
// B-leg is a valid cost-bearing winner with authoritative provider input usage
// and no embedded observation. It must keep both the call and A-leg margins
// explicitly incomplete.
func TestQueryEconomicDetailExecutionCoverageObservationFreeLegIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-ec-free", "USD")
	aLegID := "a-ed-ec-free"

	callID, _, subjectTwo := edSetupExecutionCoverageCall(t, store, account.ID, aLegID, "edECFree", func(_ metering.SubjectRef, _ *billing.CallLegUsageRecord) {})

	callDetail, alegDetail := edQueryExecutionCoverageBothScopes(t, store, account.ID, callID, aLegID)
	for _, detail := range []billing.EconomicDetail{callDetail, alegDetail} {
		require.NotNil(t, detail.Coverage.CostCoverage)
		require.False(t, detail.Coverage.CostCoverage.Complete)
		require.False(t, detail.Margin.Complete, "an observation-free executed B-leg cannot coexist with a complete margin")
		require.Nil(t, detail.Margin.Amount)
		require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
		state, found := edExecutionCoverageStateFor(t, detail, subjectTwo.BLegID)
		require.True(t, found, "the executed leg must appear as an attributable cost subject")
		require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, state)
	}
	require.Equal(t, 1, callDetail.Coverage.CostCoverage.UnresolvedCount)
}

// TestQueryEconomicDetailExecutionCoverageEmptyEvidenceIncomplete proves an
// explicitly labelled V2 leg with no observations and no accepted evidence is
// still an attempted execution, not a reconciled zero.
func TestQueryEconomicDetailExecutionCoverageEmptyEvidenceIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-ec-empty", "USD")
	aLegID := "a-ed-ec-empty"

	callID, _, subjectTwo := edSetupExecutionCoverageCall(t, store, account.ID, aLegID, "edECEmpty", func(_ metering.SubjectRef, legTwo *billing.CallLegUsageRecord) {
		legTwo.EvidenceVersion = billing.EvidenceFormatVersionV2
		legTwo.EvidenceProjection = billing.EvidenceProjectionV1
	})

	callDetail, alegDetail := edQueryExecutionCoverageBothScopes(t, store, account.ID, callID, aLegID)
	for _, detail := range []billing.EconomicDetail{callDetail, alegDetail} {
		require.False(t, detail.Margin.Complete)
		require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
		state, found := edExecutionCoverageStateFor(t, detail, subjectTwo.BLegID)
		require.True(t, found)
		require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, state)
	}
}

// TestQueryEconomicDetailExecutionCoverageAbsentMeasureIncomplete proves an
// observation whose only measure is absent/unavailable carries no usable
// economic evidence and cannot hide the executed B-leg.
func TestQueryEconomicDetailExecutionCoverageAbsentMeasureIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-ec-absent", "USD")
	aLegID := "a-ed-ec-absent"

	callID, _, subjectTwo := edSetupExecutionCoverageCall(t, store, account.ID, aLegID, "edECAbsent", func(subjectTwo metering.SubjectRef, legTwo *billing.CallLegUsageRecord) {
		legTwo.Observations = []metering.Observation{edTestUnavailableObservation(t, "obs-edECAbsent-2", subjectTwo)}
	})

	callDetail, alegDetail := edQueryExecutionCoverageBothScopes(t, store, account.ID, callID, aLegID)
	for _, detail := range []billing.EconomicDetail{callDetail, alegDetail} {
		require.False(t, detail.Margin.Complete)
		require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
		state, found := edExecutionCoverageStateFor(t, detail, subjectTwo.BLegID)
		require.True(t, found)
		require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, state)
	}
}

// TestQueryEconomicDetailExecutionCoverageNeverStartedAndNonbillableControl
// proves the canonical never-started and rejected (nonbillable) outcomes need no
// coverage proof and keep both scopes complete.
func TestQueryEconomicDetailExecutionCoverageNeverStartedAndNonbillableControl(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prefix  string
		outcome billing.LegOutcome
		state   billing.EconomicDetailCostCoverageState
	}{
		{name: "never started", prefix: "never-started", outcome: billing.LegOutcomeNeverStarted, state: billing.EconomicDetailCostCoverageNeverStarted},
		{name: "rejected", prefix: "rejected", outcome: billing.LegOutcomeRejected, state: billing.EconomicDetailCostCoverageNonbillable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newSQLiteTestStore(t)
			account := edTestAccount(t, store, "ed-ec-"+tc.prefix, "USD")
			aLegID := "a-ed-ec-" + tc.prefix

			callID, _, subjectTwo := edSetupExecutionCoverageCall(t, store, account.ID, aLegID, "edEC"+tc.prefix, func(_ metering.SubjectRef, legTwo *billing.CallLegUsageRecord) {
				legTwo.Outcome = tc.outcome
				legTwo.Evidence = billing.FinalBillingEvidence{}
			})

			callDetail, alegDetail := edQueryExecutionCoverageBothScopes(t, store, account.ID, callID, aLegID)
			for _, detail := range []billing.EconomicDetail{callDetail, alegDetail} {
				require.True(t, detail.Margin.Complete, "canonical %s proof must not block completeness", tc.name)
				require.NotNil(t, detail.Margin.Amount)
				state, found := edExecutionCoverageStateFor(t, detail, subjectTwo.BLegID)
				require.True(t, found)
				require.Equal(t, tc.state, state)
			}
		})
	}
}

// TestQueryEconomicDetailExecutionCoverageKnownZeroControlComplete proves an
// explicit authoritative zero provider cost on an observation-free leg is a
// definite known zero and keeps both scopes complete.
func TestQueryEconomicDetailExecutionCoverageKnownZeroControlComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-ec-zero", "USD")
	aLegID := "a-ed-ec-zero"

	callID, _, subjectTwo := edSetupExecutionCoverageCall(t, store, account.ID, aLegID, "edECZero", func(_ metering.SubjectRef, legTwo *billing.CallLegUsageRecord) {
		legTwo.Evidence = billing.FinalBillingEvidence{
			Cost:      billing.MoneyEvidence{NanoUnits: 0, Currency: "USD", Present: true},
			Source:    billing.EvidenceSourceProviderReported,
			Authority: billing.EvidenceAuthorityAuthoritative,
		}
	})

	callDetail, alegDetail := edQueryExecutionCoverageBothScopes(t, store, account.ID, callID, aLegID)
	for _, detail := range []billing.EconomicDetail{callDetail, alegDetail} {
		require.True(t, detail.Margin.Complete, "an explicit authoritative zero cost must not block completeness")
		state, found := edExecutionCoverageStateFor(t, detail, subjectTwo.BLegID)
		require.True(t, found)
		require.Equal(t, billing.EconomicDetailCostCoverageKnownZero, state)
	}
}

// TestQueryEconomicDetailExecutionCoverageAuthoritativeNonzeroOutranksOutcome is
// the durable public-boundary proof of the canonical RateProviderCost
// precedence: an observation-free leg carrying authoritative nonzero provider
// cost is payable coverage regardless of a rejected or never-started label, so
// both the call and A-leg margins stay incomplete.
func TestQueryEconomicDetailExecutionCoverageAuthoritativeNonzeroOutranksOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		outcome billing.LegOutcome
	}{
		{name: "rejected", outcome: billing.LegOutcomeRejected},
		{name: "never started", outcome: billing.LegOutcomeNeverStarted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newSQLiteTestStore(t)
			account := edTestAccount(t, store, "ed-ec-auth-"+tc.name, "USD")
			aLegID := "a-ed-ec-auth-" + tc.name

			callID, _, subjectTwo := edSetupExecutionCoverageCall(t, store, account.ID, aLegID, "edECAuth", func(_ metering.SubjectRef, legTwo *billing.CallLegUsageRecord) {
				legTwo.Outcome = tc.outcome
				legTwo.Evidence = billing.FinalBillingEvidence{
					Cost:      billing.MoneyEvidence{NanoUnits: 250_000_000, Currency: "USD", Present: true},
					Source:    billing.EvidenceSourceProviderReported,
					Authority: billing.EvidenceAuthorityAuthoritative,
				}
			})

			callDetail, alegDetail := edQueryExecutionCoverageBothScopes(t, store, account.ID, callID, aLegID)
			for _, detail := range []billing.EconomicDetail{callDetail, alegDetail} {
				require.False(t, detail.Margin.Complete, "accepted authoritative nonzero cost outranks a %s outcome", tc.name)
				require.Nil(t, detail.Margin.Amount)
				require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
				state, found := edExecutionCoverageStateFor(t, detail, subjectTwo.BLegID)
				require.True(t, found)
				require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, state)
			}
		})
	}
}

// TestQueryEconomicDetailExecutionCoverageNewLegBetweenPagesStalesCursor proves
// a leg inserted between pages invalidates the outstanding snapshot even when it
// carries no observation or valuation.
func TestQueryEconomicDetailExecutionCoverageNewLegBetweenPagesStalesCursor(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-ec-snap", "USD")
	aLegID := "a-ed-ec-snap"
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-ed-ec-snap-1")
	obsOne := edTestObservation(t, "obs-ec-snap-a", metering.OriginLocal, "stream-ec-snap", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil)
	obsTwo := edTestObservation(t, "obs-ec-snap-b", metering.OriginLocal, "stream-ec-snap", 2, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "11")}, nil)
	edSetupCall(t, store, account.ID, callID, aLegID, edTestLeg(t, "b-ed-ec-snap-1", obsOne, obsTwo))

	query := billing.EconomicDetailQuery{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID}
	first := edSnapshotPageOne(t, store, query)
	require.NoError(t, edSnapshotContinue(t, store, query, first))

	legTwo := edTestLeg(t, "b-ed-ec-snap-2")
	legTwo.AttemptSeq = 2
	legTwo.CallID = callID
	legTwo.ALegID = aLegID
	require.NoError(t, store.AppendCallLegUsage(ctx, legTwo))

	require.ErrorIs(t, edSnapshotContinue(t, store, query, first), economics.ErrOperatorCursorStale,
		"a new executed leg between pages must invalidate the continuation")
}
