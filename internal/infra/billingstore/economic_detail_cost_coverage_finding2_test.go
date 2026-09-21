package billingstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 fifth-pass Finding 2 durable boundary proof: QueryEconomicDetail must
// resolve the exact frozen selected valuation revision named by the durable
// selected head (even when a newer revision of the same logical stream exists in
// the latest-per-stream display set) and must verify every selected line source
// reference against the original persisted observation payload. A stale/wrong
// frozen identity fails closed.

// edTestPostSelectedHeadBound persists one frozen selected head bound to an
// explicit immutable valuation identity, mirroring the production writer. It is
// the exact-identity counterpart of edTestPostSelectedHead, whose canonical
// lookup always binds the latest stream revision.
func edTestPostSelectedHeadBound(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, subject metering.SubjectRef, headKey string, ref billing.SelectedCostValuationRef, currency, amount string) {
	t.Helper()
	selection := billing.OperatorCostSelectionResult{
		Status: billing.OperatorCostSelectionStatusFinal, Basis: billing.OperatorCostBasisP,
		Provenance: billing.OperatorCostProvenanceAttempted, Currency: currency,
	}
	if amount != "" {
		decimal := edTestDecimal(t, amount)
		selection.Amount = &billing.MonetaryExactAmount{Currency: currency, Decimal: &decimal}
	}
	selected, err := billing.NewSelectedCostValuation(ref, selection)
	require.NoError(t, err)
	_, err = store.ApplySelectedCostAdjustment(context.Background(), billing.SelectedCostAdjustmentInput{
		AccountID: accountID, CallID: callID, HeadKey: headKey, Subject: subject,
		Expected: billing.SelectedCostHeadExpectation{}, Selected: selected,
	})
	require.NoError(t, err)
}

// edFinding2Base persists one complete single-leg call with E/Q/R valuations and
// returns the call identity, subject, the provider money charge and the frozen
// observation references so a caller can append its own provider-reported P
// valuation.
func edFinding2Base(t *testing.T, store *DurableStore, accountID, aLegID, prefix string) (billing.BillingCallID, metering.SubjectRef, metering.ReportedCharge, metering.Observation, []metering.ObservationRef) {
	t.Helper()
	ctx := context.Background()
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, accountID, aLegID, callID.String(), "b-"+prefix)
	local := edTestObservation(t, "obs-"+prefix+"-local", metering.OriginLocal, "stream-"+prefix+"-local", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	provider := edTestObservation(t, "obs-"+prefix+"-provider", metering.OriginProvider, "stream-"+prefix+"-provider", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	moneyCharge := edTestCharge(t, "charge-"+prefix+"-p", "1.32", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	moneyObs := edTestObservation(t, "obs-"+prefix+"-money", metering.OriginProvider, "stream-"+prefix+"-money", 2, subject, nil, []metering.ReportedCharge{moneyCharge})
	edSetupCall(t, store, accountID, callID, aLegID, edTestLeg(t, "b-"+prefix, local, provider, moneyObs))

	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), local),
		edObservationRef(t, store.StoreID(), provider),
		edObservationRef(t, store.StoreID(), moneyObs),
	}
	for _, valuation := range []economics.Valuation{
		edTestValuation(t, "val-"+prefix+"-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, "val-"+prefix+"-q", economics.BasisProviderQuantityLocal, subject, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuation(t, "val-"+prefix+"-r", economics.BasisCustomerPolicy, subject, refs, edTestCurrencyTotal(t, "USD", "2.00")),
	} {
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}
	return callID, subject, moneyCharge, moneyObs, refs
}

// TestQueryEconomicDetailCostCoverageResolvesFrozenSelectedRevision proves the
// durable reader resolves the exact frozen selected valuation revision even
// though a newer revision of the same logical stream is the latest-per-stream
// display entry.
func TestQueryEconomicDetailCostCoverageResolvesFrozenSelectedRevision(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f2-rev", "USD")
	aLegID := "a-ed-f2-rev"

	callID, subject, moneyCharge, moneyObs, refs := edFinding2Base(t, store, account.ID, aLegID, "edF2Rev")
	frozen := edTestValuationWithReportedLine(t, "val-edF2Rev-p", economics.BasisProviderReported, subject, refs, store.StoreID(), moneyCharge, moneyObs, edTestCurrencyTotal(t, "USD", "1.32"))
	require.NoError(t, store.AppendValuation(ctx, frozen))

	// A newer revision of the same logical P stream (same subject/perspective/
	// basis) with a distinct immutable input set. The latest-per-stream display
	// read selects this row.
	newerRef := metering.ObservationRef{
		StoreID: store.StoreID(), ObservationID: "obs-edF2Rev-money-extra", Revision: 1, PayloadHash: strings.Repeat("c", 64),
	}
	newerRefs := append(append([]metering.ObservationRef{}, refs...), newerRef)
	newer := edTestValuation(t, "val-edF2Rev-p-newer", economics.BasisProviderReported, subject, newerRefs, edTestCurrencyTotal(t, "USD", "1.32"))
	newer.CreatedAt = frozen.CreatedAt.Add(time.Hour)
	require.NoError(t, store.AppendValuation(ctx, newer))

	edTestPostSelectedHeadBound(t, store, account.ID, callID, subject, "head-edF2Rev",
		billing.SelectedCostValuationRef{ValuationID: frozen.ID, Revision: 1, InputSetHash: frozen.InputSetHash}, "USD", "1.32")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.True(t, got.Margin.Complete, "the exact frozen selected revision must resolve, not the latest display revision")
	require.Equal(t, "complete", got.Margin.Reason)
	require.NotNil(t, got.Margin.Amount)
	require.Len(t, got.SelectedValuations, 1)
	require.Equal(t, frozen.ID, got.SelectedValuations[0].ID)
}

// TestQueryEconomicDetailCostCoverageMismatchedFrozenInputSetHashIncomplete
// proves a durable selected head naming a syntactically valid but non-matching
// input-set identity cannot resolve a frozen valuation, so coverage fails
// closed even when the named valuation id exists in the display set.
func TestQueryEconomicDetailCostCoverageMismatchedFrozenInputSetHashIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f2-mismatch", "USD")
	aLegID := "a-ed-f2-mismatch"

	callID, subject, moneyCharge, moneyObs, refs := edFinding2Base(t, store, account.ID, aLegID, "edF2Mismatch")
	frozen := edTestValuationWithReportedLine(t, "val-edF2Mismatch-p", economics.BasisProviderReported, subject, refs, store.StoreID(), moneyCharge, moneyObs, edTestCurrencyTotal(t, "USD", "1.32"))
	require.NoError(t, store.AppendValuation(ctx, frozen))

	edTestPostSelectedHeadBound(t, store, account.ID, callID, subject, "head-edF2Mismatch",
		billing.SelectedCostValuationRef{ValuationID: frozen.ID, Revision: 1, InputSetHash: strings.Repeat("e", 64)}, "USD", "1.32")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Margin.Complete, "a mismatched frozen input-set identity must fail closed")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.False(t, got.Coverage.CostCoverage.Complete)
}

// TestQueryEconomicDetailCostCoverageStaleSourcePayloadHashIncomplete proves a
// frozen selected valuation whose line source payload hash no longer matches the
// retained observation payload cannot prove coverage through the durable path.
func TestQueryEconomicDetailCostCoverageStaleSourcePayloadHashIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f2-hash", "USD")
	aLegID := "a-ed-f2-hash"

	callID, subject, moneyCharge, moneyObs, refs := edFinding2Base(t, store, account.ID, aLegID, "edF2Hash")
	// The frozen P valuation names a syntactically valid but wrong source
	// payload hash. A distinct input set keeps it appendable beside the base
	// evidence; the line source ref is still stale.
	staleRefs := append(append([]metering.ObservationRef{}, refs...), metering.ObservationRef{
		StoreID: store.StoreID(), ObservationID: "obs-edF2Hash-money-extra", Revision: 1, PayloadHash: strings.Repeat("c", 64),
	})
	stale := edTestValuationWithReportedLine(t, "val-edF2Hash-p", economics.BasisProviderReported, subject, staleRefs, store.StoreID(), moneyCharge, moneyObs, edTestCurrencyTotal(t, "USD", "1.32"))
	stale.Lines[0].SourceObservationRefs[0].PayloadHash = strings.Repeat("b", 64)
	require.NoError(t, stale.Validate())
	require.NoError(t, store.AppendValuation(ctx, stale))

	edTestPostSelectedHeadBound(t, store, account.ID, callID, subject, "head-edF2Hash",
		billing.SelectedCostValuationRef{ValuationID: stale.ID, Revision: 1, InputSetHash: stale.InputSetHash}, "USD", "1.32")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Margin.Complete, "a wrong frozen source payload hash must fail closed")
	require.Equal(t, "cost_coverage_unresolved", got.Margin.Reason)
	require.False(t, got.Coverage.CostCoverage.Complete)
}
