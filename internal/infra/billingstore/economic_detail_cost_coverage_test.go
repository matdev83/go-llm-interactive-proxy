package billingstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 third-pass Finding 1 public boundary proof: the durable reader loads
// every executed in-scope B-leg observation and only the economic facts that
// exist, so an observation-only charge or an entirely unpriced B-leg must reach
// QueryEconomicDetail as an unresolved cost subject instead of leaving a
// complete margin over the selected B-leg alone.

// edCostCoverageSecondObservations builds the second leg's observations once the
// helper has assigned the real call identity and B-leg subject.
type edCostCoverageSecondObservations func(callID billing.BillingCallID, subject metering.SubjectRef) []metering.Observation

// edSetupCostCoverageTwoLegCall persists one complete call leg plus a second leg
// built from the supplied builder. When inclusive is true the first leg's
// operator money charge declares the second leg's single charge explicitly
// inclusive, proving the selected result contains it. It returns the call
// identity and both B-leg subjects.
func edSetupCostCoverageTwoLegCall(t *testing.T, store *DurableStore, accountID, aLegID, prefix string, buildSecond edCostCoverageSecondObservations, inclusive bool) (billing.BillingCallID, metering.SubjectRef, metering.SubjectRef) {
	t.Helper()
	ctx := context.Background()
	callID := edTestCallID(t)
	subjectOne := edTestBLegSubject(store.StoreID(), edSupTenant, accountID, aLegID, callID.String(), "b-"+prefix+"-1")
	subjectTwo := edTestBLegSubject(store.StoreID(), edSupTenant, accountID, aLegID, callID.String(), "b-"+prefix+"-2")
	secondObservations := buildSecond(callID, subjectTwo)

	moneyCharge := edTestCharge(t, "charge-"+prefix+"-1", "1.32", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	if inclusive {
		require.Len(t, secondObservations, 1)
		require.Len(t, secondObservations[0].Charges, 1)
		moneyCharge.Covers = []metering.ChargeCoverageRef{{
			Ref: metering.ChargeRef{
				StoreID: store.StoreID(), ObservationID: secondObservations[0].ID,
				Revision: secondObservations[0].Revision, ChargeItemID: secondObservations[0].Charges[0].ChargeItemID,
			},
			Relation: metering.CoverageInclusive,
		}}
	}
	local := edTestObservation(t, "obs-"+prefix+"-1-local", metering.OriginLocal, "stream-"+prefix+"-1-local", 1, subjectOne,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	provider := edTestObservation(t, "obs-"+prefix+"-1-provider", metering.OriginProvider, "stream-"+prefix+"-1-provider", 1, subjectOne,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	moneyObs := edTestObservation(t, "obs-"+prefix+"-1-money", metering.OriginProvider, "stream-"+prefix+"-1-money", 2, subjectOne, nil, []metering.ReportedCharge{moneyCharge})

	legOne := edTestLeg(t, subjectOne.BLegID, local, provider, moneyObs)
	legTwo := edTestLeg(t, subjectTwo.BLegID, secondObservations...)
	legTwo.AttemptSeq = 2
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

// edCostCoverageChargeObservation builds one provider-origin operator/customer
// charge observation for the second leg with an exact amount.
func edCostCoverageChargeObservation(t *testing.T, id, stream string, subject metering.SubjectRef, amount string, payer metering.PaymentParty) metering.Observation {
	t.Helper()
	charge := edTestCharge(t, id+"-charge", amount, "USD", payer, false)
	return edTestObservation(t, id, metering.OriginProvider, stream, 1, subject, nil, []metering.ReportedCharge{charge})
}

// TestQueryEconomicDetailCostCoverageUnresolvedChargeIncomplete is the durable
// reproduction of the review's exact source path: an operator-paid component
// charge on an independent in-scope B-leg with no valuation, reconciliation or
// head must keep the public call and A-leg margins explicitly incomplete.
func TestQueryEconomicDetailCostCoverageUnresolvedChargeIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-charge", "USD")
	aLegID := "a-ed-cc-charge"

	callID, _, _ := edSetupCostCoverageTwoLegCall(t, store, account.ID, aLegID, "edCCCh", func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
		return []metering.Observation{edCostCoverageChargeObservation(t, "obs-edCCCh-2", "stream-edCCCh-2", subject, "0.42", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"})}
	}, false)

	callDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.NotNil(t, callDetail.Coverage.CostCoverage)
	require.False(t, callDetail.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, callDetail.Coverage.CostCoverage.UnresolvedCount)
	require.False(t, callDetail.Margin.Complete, "an uncovered independent provider charge cannot coexist with a complete call margin")
	require.Nil(t, callDetail.Margin.Amount)
	require.Equal(t, "cost_coverage_unresolved", callDetail.Margin.Reason)

	alegDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID,
	})
	require.NoError(t, err)
	require.False(t, alegDetail.Margin.Complete, "an uncovered independent provider charge cannot coexist with a complete A-leg margin")
	require.Equal(t, "cost_coverage_unresolved", alegDetail.Margin.Reason)
}

// TestQueryEconomicDetailCostCoverageUnpricedBLegIncomplete proves an executed
// in-scope B-leg with usage but no charge, valuation or head is unresolved for
// both the call and A-leg query scopes.
func TestQueryEconomicDetailCostCoverageUnpricedBLegIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-unpriced", "USD")
	aLegID := "a-ed-cc-unpriced"

	callID, _, _ := edSetupCostCoverageTwoLegCall(t, store, account.ID, aLegID, "edCCUp", func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
		return []metering.Observation{edTestObservation(t, "obs-edCCUp-2", metering.OriginProvider, "stream-edCCUp-2", 1, subject,
			[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "77")}, nil)}
	}, false)

	callDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.False(t, callDetail.Margin.Complete, "an executed but unpriced B-leg cannot support a complete call margin")
	require.Equal(t, "cost_coverage_unresolved", callDetail.Margin.Reason)

	alegDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID,
	})
	require.NoError(t, err)
	require.False(t, alegDetail.Margin.Complete, "an executed but unpriced B-leg cannot support a complete A-leg margin")
	require.Equal(t, "cost_coverage_unresolved", alegDetail.Margin.Reason)
}

// TestQueryEconomicDetailCostCoverageInclusiveControlComplete proves explicit
// inclusive coverage of an independent second charge keeps the durable call and
// A-leg margins complete.
func TestQueryEconomicDetailCostCoverageInclusiveControlComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-inclusive", "USD")
	aLegID := "a-ed-cc-inclusive"

	callID, _, _ := edSetupCostCoverageTwoLegCall(t, store, account.ID, aLegID, "edCCIn", func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
		return []metering.Observation{edCostCoverageChargeObservation(t, "obs-edCCIn-2", "stream-edCCIn-2", subject, "0.42", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"})}
	}, true)

	callDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.NotNil(t, callDetail.Coverage.CostCoverage)
	require.True(t, callDetail.Coverage.CostCoverage.Complete)
	require.True(t, callDetail.Margin.Complete)
	require.NotNil(t, callDetail.Margin.Amount)

	alegDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID,
	})
	require.NoError(t, err)
	require.True(t, alegDetail.Margin.Complete)
}

// edSetupCostCoverageSameBLegProviderCharge persists one complete call whose
// single B-leg also carries a distinct provider-charge observation. When
// inclusive is true the B-leg's base money charge declares the provider charge
// explicitly contained, so the selected result proves inclusion.
func edSetupCostCoverageSameBLegProviderCharge(t *testing.T, store *DurableStore, accountID, aLegID, prefix string, inclusive bool) (billing.BillingCallID, metering.SubjectRef) {
	t.Helper()
	ctx := context.Background()
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, accountID, aLegID, callID.String(), "b-"+prefix)
	chargeSubject := subject.Clone()
	chargeSubject.Kind = metering.SubjectProviderCharge
	chargeSubject.ProviderChargeID = "pc-" + prefix
	chargeSubject.ProviderAccountKey = "acct-provider"
	extraCharge := edTestCharge(t, "charge-"+prefix+"-pc", "0.25", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	extra := edTestObservation(t, "obs-"+prefix+"-pc", metering.OriginProvider, "stream-"+prefix+"-pc", 1, chargeSubject, nil, []metering.ReportedCharge{extraCharge})

	moneyCharge := edTestCharge(t, "charge-"+prefix+"-p", "1.32", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	if inclusive {
		moneyCharge.Covers = []metering.ChargeCoverageRef{{
			Ref: metering.ChargeRef{
				StoreID: store.StoreID(), ObservationID: extra.ID, Revision: extra.Revision,
				ChargeItemID: extraCharge.ChargeItemID,
			},
			Relation: metering.CoverageInclusive,
		}}
	}
	local := edTestObservation(t, "obs-"+prefix+"-local", metering.OriginLocal, "stream-"+prefix+"-local", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	provider := edTestObservation(t, "obs-"+prefix+"-provider", metering.OriginProvider, "stream-"+prefix+"-provider", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	moneyObs := edTestObservation(t, "obs-"+prefix+"-money", metering.OriginProvider, "stream-"+prefix+"-money", 2, subject, nil, []metering.ReportedCharge{moneyCharge})
	edSetupCall(t, store, accountID, callID, aLegID, edTestLeg(t, subject.BLegID, local, provider, moneyObs, extra))

	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), local),
		edObservationRef(t, store.StoreID(), provider),
		edObservationRef(t, store.StoreID(), moneyObs),
	}
	for _, valuation := range []economics.Valuation{
		edTestValuation(t, "val-"+prefix+"-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, "val-"+prefix+"-q", economics.BasisProviderQuantityLocal, subject, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuationWithReportedLine(t, "val-"+prefix+"-p", economics.BasisProviderReported, subject, refs, store.StoreID(), moneyCharge, moneyObs, edTestCurrencyTotal(t, "USD", "1.32")),
		edTestValuation(t, "val-"+prefix+"-r", economics.BasisCustomerPolicy, subject, refs, edTestCurrencyTotal(t, "USD", "2.00")),
	} {
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}
	edTestPostSelectedHead(t, store, accountID, callID, subject, "head-"+prefix,
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	return callID, subject
}

// TestQueryEconomicDetailCostCoverageSameBLegProviderChargeIncomplete is the
// durable public-path proof of the review's Finding 2: a distinct provider-charge
// event attributed to the selected B-leg is not contained in the selected amount
// merely because it shares B-leg ownership, so both the call and A-leg margins
// stay incomplete.
func TestQueryEconomicDetailCostCoverageSameBLegProviderChargeIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-same-bleg", "USD")
	aLegID := "a-ed-cc-same-bleg"

	callID, _ := edSetupCostCoverageSameBLegProviderCharge(t, store, account.ID, aLegID, "edCCSB", false)

	for _, query := range []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID},
	} {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.NotNil(t, detail.Coverage.CostCoverage)
		require.False(t, detail.Coverage.CostCoverage.Complete, "a same-B-leg provider charge is not proven included")
		require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
		require.Nil(t, detail.Margin.Amount)
	}
}

// TestQueryEconomicDetailCostCoverageSameBLegProviderChargeInclusiveComplete is
// the durable control proving an explicit inclusive edge from the selected
// B-leg's base charge to the provider charge makes it contained and keeps the
// public call/A-leg margins complete.
func TestQueryEconomicDetailCostCoverageSameBLegProviderChargeInclusiveComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-same-bleg-inc", "USD")
	aLegID := "a-ed-cc-same-bleg-inc"

	callID, _ := edSetupCostCoverageSameBLegProviderCharge(t, store, account.ID, aLegID, "edCCSBI", true)

	for _, query := range []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID},
	} {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.True(t, detail.Margin.Complete, "an explicit inclusive provider-charge edge keeps the margin complete")
		require.NotNil(t, detail.Margin.Amount)
	}
}

// TestQueryEconomicDetailCostCoverageKnownZeroComplete proves a durable
// known-zero operator charge on an independent B-leg needs no selected-coverage
// proof and keeps both the call and A-leg margins complete.
func TestQueryEconomicDetailCostCoverageKnownZeroComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-known-zero", "USD")
	aLegID := "a-ed-cc-known-zero"

	callID, _, _ := edSetupCostCoverageTwoLegCall(t, store, account.ID, aLegID, "edCCKZ", func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
		return []metering.Observation{
			edCostCoverageChargeObservation(t, "obs-edCCKZ-2", "stream-edCCKZ-2", subject, "0", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}),
		}
	}, false)

	for _, query := range []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID},
	} {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.True(t, detail.Margin.Complete, "a known-zero cost must not block completeness")
		require.NotNil(t, detail.Margin.Amount)
	}
}

// TestQueryEconomicDetailCostCoverageBYOKComplete proves a durable customer-BYOK
// charge on an independent B-leg needs no operator selected-coverage proof and
// keeps both the call and A-leg margins complete.
func TestQueryEconomicDetailCostCoverageBYOKComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-byok", "USD")
	aLegID := "a-ed-cc-byok"

	callID, _, _ := edSetupCostCoverageTwoLegCall(t, store, account.ID, aLegID, "edCCBY", func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
		return []metering.Observation{
			edCostCoverageChargeObservation(t, "obs-edCCBY-2", "stream-edCCBY-2", subject, "9.99", metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-creds"}),
		}
	}, false)

	for _, query := range []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID},
	} {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.True(t, detail.Margin.Complete, "a customer-BYOK cost must not block completeness")
		require.NotNil(t, detail.Margin.Amount)
	}
}

// TestQueryEconomicDetailCostCoverageForeignAccountDoesNotAffectScope proves a
// foreign-account unpriced charge under the same A-leg never affects the trusted
// account's call or A-leg margin.
func TestQueryEconomicDetailCostCoverageForeignAccountDoesNotAffectScope(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-owned", "USD")
	foreign := edTestAccount(t, store, "ed-cc-foreign", "USD")
	aLegID := "a-ed-cc-foreign"

	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edCCOwn")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edCCOwn")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-edCCOwn",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	// A foreign account's call shares the A-leg identity but carries an unpriced
	// charge; the trusted account must never see it.
	foreignCall := edTestCallID(t)
	foreignSubject := edTestBLegSubject(store.StoreID(), edSupTenant, foreign.ID, aLegID, foreignCall.String(), "b-edCCForeign")
	foreignObs := edTestObservation(t, "obs-edCCForeign", metering.OriginProvider, "stream-edCCForeign", 1, foreignSubject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "5")}, nil)
	foreignLeg := edTestLeg(t, foreignSubject.BLegID, foreignObs)
	edSetupCall(t, store, foreign.ID, foreignCall, aLegID, foreignLeg)

	callDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.True(t, callDetail.Margin.Complete)

	alegDetail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID,
	})
	require.NoError(t, err)
	require.True(t, alegDetail.Margin.Complete, "a foreign account's charge must not affect the trusted A-leg scope")
	for _, observation := range alegDetail.Observations {
		require.NotEqual(t, "obs-edCCForeign", observation.ID)
	}
}

// Phase 16 fifth-pass Finding 1 durable public boundary proof: the selected
// B-leg's base execution is proven by the selected subject, but an independent
// charge atom on that same B-leg is proven only by an exact selected payable
// line/source identity or a validated inclusive edge — never by B-leg ownership.

// edSetupCostCoverageSelectedLegCharge persists one complete selected call whose
// single B-leg carries the selected base money charge plus one independent
// second charge observation that keeps the ordinary SubjectBLeg subject (never a
// provider-charge child), so the review's exact B-leg variant is exercised.
// When inclusive is true the selected valuation's own payable charge declares
// the extra charge explicitly contained. When staleRevision is true the extra
// charge is a later revision of the selected money observation, proving the
// selected source identity binds one exact revision.
func edSetupCostCoverageSelectedLegCharge(t *testing.T, store *DurableStore, accountID, aLegID, prefix string, inclusive, staleRevision bool) (billing.BillingCallID, metering.SubjectRef) {
	t.Helper()
	ctx := context.Background()
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, accountID, aLegID, callID.String(), "b-"+prefix)

	extraCharge := edTestCharge(t, "charge-"+prefix+"-extra", "0.25", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	extraID := "obs-" + prefix + "-extra"
	extraStream := "stream-" + prefix + "-extra"
	extraRevision := uint64(1)
	extraSequence := uint64(1)
	if staleRevision {
		extraID = "obs-" + prefix + "-money"
		extraStream = "stream-" + prefix + "-money"
		extraRevision = 2
		extraSequence = 3
	}

	moneyCharge := edTestCharge(t, "charge-"+prefix+"-p", "1.32", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	if inclusive {
		moneyCharge.Covers = []metering.ChargeCoverageRef{{
			Ref: metering.ChargeRef{
				StoreID: store.StoreID(), ObservationID: extraID, Revision: extraRevision,
				ChargeItemID: extraCharge.ChargeItemID,
			},
			Relation: metering.CoverageInclusive,
		}}
	}

	local := edTestObservation(t, "obs-"+prefix+"-local", metering.OriginLocal, "stream-"+prefix+"-local", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	provider := edTestObservation(t, "obs-"+prefix+"-provider", metering.OriginProvider, "stream-"+prefix+"-provider", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	moneyObs := edTestObservation(t, "obs-"+prefix+"-money", metering.OriginProvider, "stream-"+prefix+"-money", 2, subject, nil, []metering.ReportedCharge{moneyCharge})
	extra := edTestObservation(t, extraID, metering.OriginProvider, extraStream, extraSequence, subject, nil, []metering.ReportedCharge{extraCharge})
	extra.Revision = extraRevision

	edSetupCall(t, store, accountID, callID, aLegID, edTestLeg(t, subject.BLegID, local, provider, moneyObs, extra))

	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), local),
		edObservationRef(t, store.StoreID(), provider),
		edObservationRef(t, store.StoreID(), moneyObs),
	}
	for _, valuation := range []economics.Valuation{
		edTestValuation(t, "val-"+prefix+"-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, "val-"+prefix+"-q", economics.BasisProviderQuantityLocal, subject, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuationWithReportedLine(t, "val-"+prefix+"-p", economics.BasisProviderReported, subject, refs, store.StoreID(), moneyCharge, moneyObs, edTestCurrencyTotal(t, "USD", "1.32")),
		edTestValuation(t, "val-"+prefix+"-r", economics.BasisCustomerPolicy, subject, refs, edTestCurrencyTotal(t, "USD", "2.00")),
	} {
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}
	edTestPostSelectedHead(t, store, accountID, callID, subject, "head-"+prefix,
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	return callID, subject
}

// edSetupCostCoverageBaseExecutionOnly persists one complete selected call whose
// B-leg carries only base execution (measures) and no separate charge atom.
func edSetupCostCoverageBaseExecutionOnly(t *testing.T, store *DurableStore, accountID, aLegID, prefix string) (billing.BillingCallID, metering.SubjectRef) {
	t.Helper()
	ctx := context.Background()
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, accountID, aLegID, callID.String(), "b-"+prefix)
	local := edTestObservation(t, "obs-"+prefix+"-local", metering.OriginLocal, "stream-"+prefix+"-local", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	provider := edTestObservation(t, "obs-"+prefix+"-provider", metering.OriginProvider, "stream-"+prefix+"-provider", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	edSetupCall(t, store, accountID, callID, aLegID, edTestLeg(t, subject.BLegID, local, provider))

	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), local),
		edObservationRef(t, store.StoreID(), provider),
	}
	for _, valuation := range []economics.Valuation{
		edTestValuation(t, "val-"+prefix+"-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, "val-"+prefix+"-q", economics.BasisProviderQuantityLocal, subject, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuation(t, "val-"+prefix+"-p", economics.BasisProviderReported, subject, refs, edTestCurrencyTotal(t, "USD", "1.32")),
		edTestValuation(t, "val-"+prefix+"-r", economics.BasisCustomerPolicy, subject, refs, edTestCurrencyTotal(t, "USD", "2.00")),
	} {
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}
	edTestPostSelectedHead(t, store, accountID, callID, subject, "head-"+prefix,
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	return callID, subject
}

// TestQueryEconomicDetailCostCoverageSelectedLegExtraChargeIncomplete is the
// durable reproduction of the fifth-pass Finding 1: an independent operator-paid
// charge on the selected B-leg's own subject, with no inclusive proof, keeps
// both the call and A-leg margins explicitly incomplete.
func TestQueryEconomicDetailCostCoverageSelectedLegExtraChargeIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-sel-extra", "USD")
	aLegID := "a-ed-cc-sel-extra"

	callID, subject := edSetupCostCoverageSelectedLegCharge(t, store, account.ID, aLegID, "edCCSelExtra", false, false)

	for _, query := range []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID},
	} {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.NotNil(t, detail.Coverage.CostCoverage)
		require.False(t, detail.Coverage.CostCoverage.Complete, "a same-subject B-leg charge is not proven by selected base execution")
		require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
		require.Nil(t, detail.Margin.Amount)
		state, found := edExecutionCoverageStateFor(t, detail, subject.BLegID)
		require.True(t, found)
		require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, state)
	}
}

// TestQueryEconomicDetailCostCoverageSelectedLegSiblingChargeItemIncomplete
// proves the sibling atom (not the selected base charge) is what remains
// unresolved on the selected B-leg unit.
func TestQueryEconomicDetailCostCoverageSelectedLegSiblingChargeItemIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-sel-sibling", "USD")
	aLegID := "a-ed-cc-sel-sibling"

	callID, subject := edSetupCostCoverageSelectedLegCharge(t, store, account.ID, aLegID, "edCCSelSibling", false, false)

	detail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.False(t, detail.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, detail.Coverage.CostCoverage.UnresolvedCount)
	state, found := edExecutionCoverageStateFor(t, detail, subject.BLegID)
	require.True(t, found)
	require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, state)
}

// TestQueryEconomicDetailCostCoverageSelectedLegExtraChargeInclusiveComplete is
// the durable control: an explicit inclusive edge from the exact selected atom
// to the same-subject extra charge keeps the call and A-leg margins complete.
func TestQueryEconomicDetailCostCoverageSelectedLegExtraChargeInclusiveComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-sel-inc", "USD")
	aLegID := "a-ed-cc-sel-inc"

	callID, _ := edSetupCostCoverageSelectedLegCharge(t, store, account.ID, aLegID, "edCCSelInc", true, false)

	for _, query := range []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID},
	} {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.True(t, detail.Margin.Complete, "an explicit inclusive edge from the selected atom keeps the margin complete")
		require.NotNil(t, detail.Margin.Amount)
	}
}

// TestQueryEconomicDetailCostCoverageSelectedLegStaleRevisionIncomplete proves a
// later revision charge on the selected B-leg is not covered by the stale
// selected source identity.
func TestQueryEconomicDetailCostCoverageSelectedLegStaleRevisionIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-sel-stale", "USD")
	aLegID := "a-ed-cc-sel-stale"

	callID, subject := edSetupCostCoverageSelectedLegCharge(t, store, account.ID, aLegID, "edCCSelStale", false, true)

	for _, query := range []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID},
	} {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.False(t, detail.Margin.Complete, "a later revision charge is not covered by a stale selected source")
		require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
	}
	detail, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	state, found := edExecutionCoverageStateFor(t, detail, subject.BLegID)
	require.True(t, found)
	require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, state)
}

// TestQueryEconomicDetailCostCoverageBaseExecutionOnlyComplete proves a selected
// B-leg carrying only base execution stays complete without monetary inclusion
// proof for any charge atom.
func TestQueryEconomicDetailCostCoverageBaseExecutionOnlyComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-base-only", "USD")
	aLegID := "a-ed-cc-base-only"

	callID, _ := edSetupCostCoverageBaseExecutionOnly(t, store, account.ID, aLegID, "edCCBaseOnly")

	for _, query := range []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID},
	} {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.NotNil(t, detail.Coverage.CostCoverage)
		require.True(t, detail.Coverage.CostCoverage.Complete, "base execution coverage needs no charge-atom proof")
		require.True(t, detail.Margin.Complete)
	}
}

// Phase 16 sixth-pass Finding 2 durable public boundary proof: an invalid,
// dangling or superseded coverage graph must reach QueryEconomicDetail as an
// incomplete margin for both the BillingCallID and A-leg scopes, while a valid
// acyclic inclusive chain stays complete.

// edCostCoverageExtraObservations builds the extra charge observations of one
// graph probe after the helper has assigned the real call identity and subject.
type edCostCoverageExtraObservations func(callID billing.BillingCallID, subject metering.SubjectRef) []metering.Observation

// edSetupCostCoverageGraphCase persists one complete selected call whose single
// B-leg carries the selected money charge plus extra graph observations. The
// covers builder receives the assigned call/subject and the extra observations
// so it can name exact charge identities, and returns the selected money
// charge's coverage edges.
func edSetupCostCoverageGraphCase(t *testing.T, store *DurableStore, accountID, aLegID, prefix string, buildExtra edCostCoverageExtraObservations, covers func(subject metering.SubjectRef, extra []metering.Observation) []metering.ChargeCoverageRef) (billing.BillingCallID, metering.SubjectRef) {
	t.Helper()
	ctx := context.Background()
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, accountID, aLegID, callID.String(), "b-"+prefix)
	extra := buildExtra(callID, subject)

	moneyCharge := edTestCharge(t, "charge-"+prefix+"-p", "1.32", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	moneyCharge.Covers = covers(subject, extra)

	local := edTestObservation(t, "obs-"+prefix+"-local", metering.OriginLocal, "stream-"+prefix+"-local", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	provider := edTestObservation(t, "obs-"+prefix+"-provider", metering.OriginProvider, "stream-"+prefix+"-provider", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	moneyObs := edTestObservation(t, "obs-"+prefix+"-money", metering.OriginProvider, "stream-"+prefix+"-money", 2, subject, nil, []metering.ReportedCharge{moneyCharge})

	legObservations := append([]metering.Observation{local, provider, moneyObs}, extra...)
	edSetupCall(t, store, accountID, callID, aLegID, edTestLeg(t, subject.BLegID, legObservations...))

	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), local),
		edObservationRef(t, store.StoreID(), provider),
		edObservationRef(t, store.StoreID(), moneyObs),
	}
	for _, valuation := range []economics.Valuation{
		edTestValuation(t, "val-"+prefix+"-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, "val-"+prefix+"-q", economics.BasisProviderQuantityLocal, subject, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuationWithReportedLine(t, "val-"+prefix+"-p", economics.BasisProviderReported, subject, refs, store.StoreID(), moneyObs.Charges[0], moneyObs, edTestCurrencyTotal(t, "USD", "1.32")),
		edTestValuation(t, "val-"+prefix+"-r", economics.BasisCustomerPolicy, subject, refs, edTestCurrencyTotal(t, "USD", "2.00")),
	} {
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}
	edTestPostSelectedHead(t, store, accountID, callID, subject, "head-"+prefix,
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	return callID, subject
}

func edCostCoverageScopeQueries(store *DurableStore, accountID string, callID billing.BillingCallID, aLegID string) []billing.EconomicDetailQuery {
	return []billing.EconomicDetailQuery{
		{StoreID: store.StoreID(), AccountID: accountID, BillingCallID: callID.String(), ALegID: aLegID},
		{StoreID: store.StoreID(), AccountID: accountID, ALegID: aLegID},
	}
}

// TestQueryEconomicDetailCostCoverageInclusiveCycleIncomplete persists a
// two-leg cycle where each leg's charge declares the other's charge inclusively
// contained. Each leg seals individually; the durable call and A-leg reads must
// fail the canonical graph closed instead of returning a complete margin.
func TestQueryEconomicDetailCostCoverageInclusiveCycleIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-cycle", "USD")
	aLegID := "a-ed-cc-cycle"

	callID, _, _ := edSetupCostCoverageTwoLegCall(t, store, account.ID, aLegID, "edCCCy", func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
		second := edCostCoverageChargeObservation(t, "obs-edCCCy-2", "stream-edCCCy-2", subject, "0.42", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"})
		second.Charges[0].Covers = []metering.ChargeCoverageRef{{
			Ref:      metering.ChargeRef{StoreID: store.StoreID(), ObservationID: "obs-edCCCy-1-money", Revision: 1, ChargeItemID: "charge-edCCCy-1"},
			Relation: metering.CoverageInclusive,
		}}
		return []metering.Observation{second}
	}, true)

	for _, query := range edCostCoverageScopeQueries(store, account.ID, callID, aLegID) {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.False(t, detail.Margin.Complete, "a durable coverage cycle cannot produce a complete margin")
		require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
	}
}

// TestQueryEconomicDetailCostCoverageDanglingTargetIncomplete persists a
// selected charge whose inclusive edge names an absent exact target. The missing
// target must make both scopes incomplete instead of being silently ignored.
func TestQueryEconomicDetailCostCoverageDanglingTargetIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-dangling", "USD")
	aLegID := "a-ed-cc-dangling"

	callID, _ := edSetupCostCoverageGraphCase(t, store, account.ID, aLegID, "edCCDg",
		func(_ billing.BillingCallID, _ metering.SubjectRef) []metering.Observation { return nil },
		func(_ metering.SubjectRef, _ []metering.Observation) []metering.ChargeCoverageRef {
			return []metering.ChargeCoverageRef{{
				Ref:      metering.ChargeRef{StoreID: store.StoreID(), ObservationID: "obs-edCCDg-missing", Revision: 1, ChargeItemID: "charge-edCCDg-missing"},
				Relation: metering.CoverageInclusive,
			}}
		})

	for _, query := range edCostCoverageScopeQueries(store, account.ID, callID, aLegID) {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.False(t, detail.Margin.Complete, "a dangling exact target cannot produce a complete margin")
		require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
	}
}

// TestQueryEconomicDetailCostCoverageSupersededSourceIncomplete persists a stale
// charge whose inclusive edge is removed by its authoritative same-item
// replacement. The stale edge cannot prove the current target.
func TestQueryEconomicDetailCostCoverageSupersededSourceIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-superseded", "USD")
	aLegID := "a-ed-cc-superseded"

	callID, _ := edSetupCostCoverageGraphCase(t, store, account.ID, aLegID, "edCCSp",
		func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
			targetSubject := subject.Clone()
			targetSubject.Kind = metering.SubjectProviderCharge
			targetSubject.ProviderChargeID = "pc-edCCSp-target"
			targetSubject.ProviderAccountKey = "acct-provider"
			target := edCostCoverageChargeObservation(t, "obs-edCCSp-target", "stream-edCCSp-target", targetSubject, "0.40", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"})
			stale := edCostCoverageChargeObservation(t, "obs-edCCSp-stale", "stream-edCCSp-stale", subject, "0.30", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"})
			stale.Charges[0].Covers = []metering.ChargeCoverageRef{{
				Ref:      metering.ChargeRef{StoreID: store.StoreID(), ObservationID: target.ID, Revision: target.Revision, ChargeItemID: target.Charges[0].ChargeItemID},
				Relation: metering.CoverageInclusive,
			}}
			staleRef, err := stale.Ref(store.StoreID())
			require.NoError(t, err)
			replacement := stale
			replacement.ID = "obs-edCCSp-replacement"
			replacement.SourceEventKey = replacement.ID + "-event"
			replacement.Revision = 2
			replacement.Sequence = 3
			replacement.Semantics = metering.SemanticsReplacement
			replacement.Supersedes = []metering.ObservationRef{staleRef}
			replacement.Charges = []metering.ReportedCharge{stale.Charges[0]}
			replacement.Charges[0].Covers = nil
			require.NoError(t, replacement.Validate())
			return []metering.Observation{stale, target, replacement}
		},
		func(_ metering.SubjectRef, extra []metering.Observation) []metering.ChargeCoverageRef {
			require.Len(t, extra, 3)
			return []metering.ChargeCoverageRef{
				{Ref: metering.ChargeRef{StoreID: store.StoreID(), ObservationID: extra[0].ID, Revision: extra[0].Revision, ChargeItemID: extra[0].Charges[0].ChargeItemID}, Relation: metering.CoverageInclusive},
				{Ref: metering.ChargeRef{StoreID: store.StoreID(), ObservationID: extra[2].ID, Revision: extra[2].Revision, ChargeItemID: extra[2].Charges[0].ChargeItemID}, Relation: metering.CoverageInclusive},
			}
		})

	for _, query := range edCostCoverageScopeQueries(store, account.ID, callID, aLegID) {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.False(t, detail.Margin.Complete, "a superseded source edge cannot produce a complete margin")
		require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
	}
}

// TestQueryEconomicDetailCostCoverageValidInclusiveChainComplete is the durable
// control: a valid acyclic exact-identity inclusive chain stays complete for
// both the call and A-leg scopes.
func TestQueryEconomicDetailCostCoverageValidInclusiveChainComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-chain", "USD")
	aLegID := "a-ed-cc-chain"

	callID, _ := edSetupCostCoverageGraphCase(t, store, account.ID, aLegID, "edCCCh",
		func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
			endSubject := subject.Clone()
			endSubject.Kind = metering.SubjectProviderCharge
			endSubject.ProviderChargeID = "pc-edCCCh-end"
			endSubject.ProviderAccountKey = "acct-provider"
			end := edCostCoverageChargeObservation(t, "obs-edCCCh-end", "stream-edCCCh-end", endSubject, "0.20", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"})
			mid := edCostCoverageChargeObservation(t, "obs-edCCCh-mid", "stream-edCCCh-mid", subject, "0.10", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"})
			mid.Charges[0].Covers = []metering.ChargeCoverageRef{{
				Ref:      metering.ChargeRef{StoreID: store.StoreID(), ObservationID: end.ID, Revision: end.Revision, ChargeItemID: end.Charges[0].ChargeItemID},
				Relation: metering.CoverageInclusive,
			}}
			return []metering.Observation{mid, end}
		},
		func(_ metering.SubjectRef, extra []metering.Observation) []metering.ChargeCoverageRef {
			require.Len(t, extra, 2)
			return []metering.ChargeCoverageRef{{
				Ref:      metering.ChargeRef{StoreID: store.StoreID(), ObservationID: extra[0].ID, Revision: extra[0].Revision, ChargeItemID: extra[0].Charges[0].ChargeItemID},
				Relation: metering.CoverageInclusive,
			}}
		})

	for _, query := range edCostCoverageScopeQueries(store, account.ID, callID, aLegID) {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.True(t, detail.Coverage.CostCoverage.Complete, "a valid inclusive chain proves coverage")
		require.True(t, detail.Margin.Complete)
		require.NotNil(t, detail.Margin.Amount)
	}
}

// Phase 16 latest Finding 3 durable public boundary proof: only an authoritative
// provider zero proves a known-zero cost. A local estimator's exact zero is a
// valid estimated claim and must reach QueryEconomicDetail as an unresolved
// subject for both the call and A-leg scopes.

// edCostCoverageLocalEstimatedZeroObservation builds one local estimator claim
// with an exact zero operator amount: a valid estimated claim, never an
// authoritative provider zero.
func edCostCoverageLocalEstimatedZeroObservation(t *testing.T, id, stream string, subject metering.SubjectRef) metering.Observation {
	t.Helper()
	zero := edTestDecimal(t, "0")
	charge := metering.ReportedCharge{
		ChargeItemID: id + "-charge",
		Component: &metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		},
		Amount: &zero, Currency: "USD", Kind: metering.ChargeKindComponent,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
	}
	observation := edTestObservation(t, id, metering.OriginLocal, stream, 1, subject, nil, []metering.ReportedCharge{charge})
	observation.Acquisition = metering.AcquisitionLocalEstimator
	observation.Authority = metering.AuthorityEstimatedClaim
	require.NoError(t, observation.Validate())
	return observation
}

// TestQueryEconomicDetailCostCoverageLocalEstimatedZeroIncomplete proves a
// durable local estimated zero cannot close completeness on either scope.
func TestQueryEconomicDetailCostCoverageLocalEstimatedZeroIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-cc-local-zero", "USD")
	aLegID := "a-ed-cc-local-zero"

	callID, _, _ := edSetupCostCoverageTwoLegCall(t, store, account.ID, aLegID, "edCCLZ", func(_ billing.BillingCallID, subject metering.SubjectRef) []metering.Observation {
		return []metering.Observation{edCostCoverageLocalEstimatedZeroObservation(t, "obs-edCCLZ-2", "stream-edCCLZ-2", subject)}
	}, false)

	for _, query := range edCostCoverageScopeQueries(store, account.ID, callID, aLegID) {
		detail, err := store.QueryEconomicDetail(ctx, query)
		require.NoError(t, err)
		require.NotNil(t, detail.Coverage.CostCoverage)
		require.False(t, detail.Margin.Complete, "a local estimated zero cannot close completeness")
		require.Equal(t, "cost_coverage_unresolved", detail.Margin.Reason)
	}
}
