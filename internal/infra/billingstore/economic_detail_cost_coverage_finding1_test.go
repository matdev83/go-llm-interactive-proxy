package billingstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 fifth-pass Finding 1 durable contract: an independent retained
// head must never seed coverage beyond the exact authoritative selected
// result. Two independently selected heads remain ambiguous and prove
// nothing; no summing or fallback is permitted.

// TestQueryEconomicDetailFinding1IndependentHeadDifferentBLegIncomplete is the
// durable different-subject reproduction: a second in-scope B-leg carries its
// own complete provider valuation with an exact payable line plus its own
// selected head. Both call and A-leg coverage must stay unresolved and no
// margin amount may be produced.
func TestQueryEconomicDetailFinding1IndependentHeadDifferentBLegIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f1-diff", "USD")
	aLegID := "a-ed-f1-diff"

	callID, subjectOne, subjectTwo := edSetupCostCoverageTwoLegCall(t, store, account.ID, aLegID, "edF1Diff", func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
		return []metering.Observation{edCostCoverageChargeObservation(t, "obs-edF1Diff-2", "stream-edF1Diff-2", subject, "0.25", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"})}
	}, false)
	_ = subjectOne

	secondObs := edCostCoverageChargeObservation(t, "obs-edF1Diff-2", "stream-edF1Diff-2", subjectTwo, "0.25", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"})
	secondRefs := []metering.ObservationRef{edObservationRef(t, store.StoreID(), secondObs)}
	require.Len(t, secondObs.Charges, 1)
	secondVal := edTestValuationWithReportedLine(t, "val-edF1Diff-p2", economics.BasisProviderReported, subjectTwo, secondRefs, store.StoreID(), secondObs.Charges[0], secondObs, edTestCurrencyTotal(t, "USD", "0.25"))
	require.NoError(t, store.AppendValuation(ctx, secondVal))
	edTestPostSelectedHead(t, store, account.ID, callID, subjectTwo, "head-edF1Diff-2",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "0.25")

	for _, query := range []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID},
	} {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.True(t, detail.Totals.SelectedAmbiguous, "two independent heads must stay ambiguous")
		require.Equal(t, 2, detail.Totals.SelectedHeadCount)
		require.Nil(t, detail.Selection)
		require.NotNil(t, detail.Coverage.CostCoverage)
		require.False(t, detail.Coverage.CostCoverage.Complete, "independent head must not seed coverage by fallback")
		require.False(t, detail.Margin.Complete, "independent heads must never sum into a complete margin")
		require.Nil(t, detail.Margin.Amount)
		require.Equal(t, "ambiguous_selected", detail.Margin.Reason)
	}
}

// TestQueryEconomicDetailFinding1IndependentHeadSameBLegProviderChargeIncomplete
// is the durable same-B-leg-ownership reproduction: a distinct provider-charge
// event on the selected B-leg carries its own valuation and head. Both scopes
// must stay unresolved with no summed margin.
func TestQueryEconomicDetailFinding1IndependentHeadSameBLegProviderChargeIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f1-same", "USD")
	aLegID := "a-ed-f1-same"

	callID, subject := edSetupCostCoverageSameBLegProviderCharge(t, store, account.ID, aLegID, "edF1Same", false)
	chargeSubject := subject.Clone()
	chargeSubject.Kind = metering.SubjectProviderCharge
	chargeSubject.ProviderChargeID = "pc-edF1Same"
	chargeSubject.ProviderAccountKey = "acct-provider"
	extraCharge := edTestCharge(t, "charge-edF1Same-pc", "0.25", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	extra := edTestObservation(t, "obs-edF1Same-pc", metering.OriginProvider, "stream-edF1Same-pc", 1, chargeSubject, nil, []metering.ReportedCharge{extraCharge})
	extraRefs := []metering.ObservationRef{edObservationRef(t, store.StoreID(), extra)}
	extraVal := edTestValuationWithReportedLine(t, "val-edF1Same-pc", economics.BasisProviderReported, chargeSubject, extraRefs, store.StoreID(), extraCharge, extra, edTestCurrencyTotal(t, "USD", "0.25"))
	require.NoError(t, store.AppendValuation(ctx, extraVal))
	edTestPostSelectedHead(t, store, account.ID, callID, chargeSubject, "head-edF1Same-pc",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "0.25")

	for _, query := range []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID},
	} {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.True(t, detail.Totals.SelectedAmbiguous, "B-leg plus provider-charge heads must stay ambiguous")
		require.Equal(t, 2, detail.Totals.SelectedHeadCount)
		require.Nil(t, detail.Selection)
		require.NotNil(t, detail.Coverage.CostCoverage)
		require.False(t, detail.Coverage.CostCoverage.Complete, "independent provider-charge head must not seed coverage")
		require.False(t, detail.Margin.Complete)
		require.Nil(t, detail.Margin.Amount)
		require.Equal(t, "ambiguous_selected", detail.Margin.Reason)
	}
}

// TestQueryEconomicDetailFinding1PersistedSingleHeadComplete is the control:
// one authoritative head over the only attributable subject keeps both scopes
// complete and deterministic.
func TestQueryEconomicDetailFinding1PersistedSingleHeadComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f1-single", "USD")
	aLegID := "a-ed-f1-single"

	callID, _, _ := edSetupCostCoverageTwoLegCall(t, store, account.ID, aLegID, "edF1Single", func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
		return []metering.Observation{edCostCoverageChargeObservation(t, "obs-edF1Single-2", "stream-edF1Single-2", subject, "0", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"})}
	}, false)

	callDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.True(t, callDetail.Coverage.CostCoverage.Complete)
	require.True(t, callDetail.Margin.Complete)
	again, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.Equal(t, callDetail.SnapshotFingerprint, again.SnapshotFingerprint, "durable coverage output must be deterministic")
}
