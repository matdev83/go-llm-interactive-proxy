package billing

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// detailTestScope returns the canonical scoped identities used by 16.1A
// fixtures. Store/account/call/A-leg are explicit trusted scope; no test
// invents raw prompts, responses or provider payloads.
func detailTestScope() (storeID, accountID, aLegID, billingCallID string) {
	return "store-detail", "acct-detail", "a-detail", "bc_" + strings.Repeat("a", 32)
}

func detailTestSubject(storeID, aLegID, billingCallID, bLegID string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID,
		ALegID: aLegID, BillingCallID: billingCallID, BLegID: bLegID,
	}
}

func detailTestDecimal(t *testing.T, raw string) metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	require.NoError(t, err)
	normalized, err := value.Normalize()
	require.NoError(t, err)
	return normalized
}

func detailTestObservation(t *testing.T, id, origin, streamID string, sequence uint64, subject metering.SubjectRef, measures []metering.Measure, charges []metering.ReportedCharge) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionLocalTokenizer
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	if origin == metering.OriginStatement {
		acquisition = metering.AcquisitionStatementImporter
	}
	now := time.Unix(1_700_010_000, 0).UTC()
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: streamID, Sequence: sequence,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject:   subject,
		Correlation: metering.CorrelationV2{
			StoreID: subject.StoreID, ALegID: subject.ALegID,
			BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now, ReceivedAt: now, MappingRef: "detail.test.v1",
		Measures: measures, Charges: charges,
	}
	if origin == metering.OriginStatement {
		observation.Authority = metering.AuthorityVerifiedStatement
	}
	require.NoError(t, observation.Validate())
	return observation
}

func detailTestMeasure(t *testing.T, component, raw string) metering.Measure {
	t.Helper()
	key := metering.ComponentKey{
		Direction: metering.DirectionInput, Component: component,
		Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
	}
	return metering.Measure{Key: key, Value: func() *metering.Decimal { v := detailTestDecimal(t, raw); return &v }(), Quality: metering.QualityObserved}
}

func detailTestValuation(t *testing.T, id string, basis economics.ValuationBasis, subject metering.SubjectRef, storeID string, totals ...economics.CurrencyTotal) economics.Valuation {
	t.Helper()
	observationID := id + "-observation"
	valuation := economics.Valuation{
		ID: id, Version: economics.ValuationVersionV2,
		Perspective: metering.PerspectiveOperator,
		Basis:       basis, Subject: subject,
		InputObservations: []metering.ObservationRef{{
			StoreID: storeID, ObservationID: observationID, Revision: 1, PayloadHash: strings.Repeat("c", 64),
		}},
		InputSetHash:         strings.Repeat("d", 64),
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://detail/qualifiers/v1", ContentHash: strings.Repeat("a", 64)},
		Completeness:         economics.CompletenessComplete,
		CreatedAt:            time.Unix(1_700_010_100, 0).UTC(),
		Totals:               totals,
	}
	switch basis {
	case economics.BasisLocalExpected, economics.BasisProviderQuantityLocal:
		valuation.Tariff = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-detail", Version: "v1"}}
		valuation.TariffContent = &economics.SnapshotContentRef{ContentRef: "catalog://detail/tariff/v1", ContentHash: strings.Repeat("f", 64)}
		valuation.Rater = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater-detail", Version: "v1"}, RaterID: "reference-rater"}
		valuation.RaterContent = &economics.SnapshotContentRef{ContentRef: "catalog://detail/rater/v1", ContentHash: strings.Repeat("e", 64)}
	case economics.BasisCustomerPolicy:
		valuation.Rater = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater-detail", Version: "v1"}, RaterID: "reference-rater"}
		valuation.RaterContent = &economics.SnapshotContentRef{ContentRef: "catalog://detail/rater/v1", ContentHash: strings.Repeat("e", 64)}
		valuation.Policy = economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-detail", Version: "v1"}, PolicyID: "retail-policy"}
		valuation.PolicyContent = &economics.SnapshotContentRef{ContentRef: "catalog://detail/policy/v1", ContentHash: strings.Repeat("b", 64)}
	}
	require.NoError(t, valuation.Validate())
	return valuation
}

func detailTestCurrencyTotal(t *testing.T, currency, amount string) economics.CurrencyTotal {
	t.Helper()
	value := detailTestDecimal(t, amount)
	nanos, err := value.ToNanoUnits()
	require.NoError(t, err)
	return economics.CurrencyTotal{
		Currency: currency, Amount: &value,
		RoundedAmount: economics.Money{NanoUnits: nanos, Currency: currency, Present: true},
	}
}

func detailTestCompleteInput(t *testing.T) EconomicDetailInput {
	t.Helper()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")
	localObs := detailTestObservation(t, "obs-local-1", metering.OriginLocal, "stream-local", 1, subject,
		[]metering.Measure{detailTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	providerObs := detailTestObservation(t, "obs-provider-1", metering.OriginProvider, "stream-provider", 1, subject,
		[]metering.Measure{detailTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	providerCharge := metering.ReportedCharge{
		ChargeItemID: "charge-p-1",
		Component: &metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		},
		Amount:   func() *metering.Decimal { v := detailTestDecimal(t, "1.32"); return &v }(),
		Currency: "USD", Kind: metering.ChargeKindComponent,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
	}
	providerMoneyObs := detailTestObservation(t, "obs-provider-money-1", metering.OriginProvider, "stream-provider-money", 2, subject, nil, []metering.ReportedCharge{providerCharge})
	statementObs := detailTestObservation(t, "obs-statement-1", metering.OriginStatement, "stream-statement", 1,
		metering.SubjectRef{Kind: metering.SubjectStatementLine, StoreID: storeID, ProviderAccountKey: "acct-provider", StatementID: "stmt-1", StatementLineID: "line-1"},
		nil, []metering.ReportedCharge{{
			ChargeItemID: "charge-s-1",
			Component: &metering.ComponentKey{
				Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
				Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
			},
			Amount:   func() *metering.Decimal { v := detailTestDecimal(t, "1.30"); return &v }(),
			Currency: "USD", Kind: metering.ChargeKindComponent,
			Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
		}})

	eVal := detailTestValuation(t, "valuation-e", economics.BasisLocalExpected, subject, storeID, detailTestCurrencyTotal(t, "USD", "1.00"))
	qVal := detailTestValuation(t, "valuation-q", economics.BasisProviderQuantityLocal, subject, storeID, detailTestCurrencyTotal(t, "USD", "1.10"))
	pVal := detailTestValuation(t, "valuation-p", economics.BasisProviderReported, subject, storeID, detailTestCurrencyTotal(t, "USD", "1.32"))
	// The selected provider-reported valuation carries the exact payable line
	// that names the request charge it prices. Monetary inclusion is proven by
	// that selected line/source identity, never by the selected subject merely
	// owning the charge.
	moneyRef, err := providerMoneyObs.Ref(storeID)
	require.NoError(t, err)
	lineAmount := detailTestDecimal(t, "1.32")
	lineNanos, err := lineAmount.ToNanoUnits()
	require.NoError(t, err)
	pVal.Lines = append(pVal.Lines, economics.LineItem{
		ID: "line-p-1", RuleID: "provider_reported", ItemID: "charge-p-1",
		Component: &metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		},
		Unit:                  metering.UnitToken,
		Amount:                &lineAmount,
		RoundingScope:         economics.RoundingScopeLine,
		RoundingPolicy:        economics.RoundingHalfEven,
		RoundedAmount:         &economics.Money{NanoUnits: lineNanos, Currency: "USD", Present: true},
		Status:                economics.RatingLineProviderReported,
		SourceObservationRefs: []metering.ObservationRef{moneyRef},
	})
	require.NoError(t, pVal.Validate())
	sVal := detailTestValuation(t, "valuation-s", economics.BasisStatementReported, subject, storeID, detailTestCurrencyTotal(t, "USD", "1.30"))
	rVal := detailTestValuation(t, "valuation-r", economics.BasisCustomerPolicy, subject, storeID, detailTestCurrencyTotal(t, "USD", "2.00"))

	quantity := ComponentQuantityComparison{Status: ReconciliationStatusDiscrepant, Complete: true}
	monetaryE := eVal
	monetaryQ := qVal
	monetaryP := pVal
	monetary, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{monetaryE, monetaryQ, monetaryP}})
	require.NoError(t, err)

	policy := OperatorCostSelectionPolicy{
		Version: OperatorCostSelectionPolicyV1,
		Ref:     VersionRef{ID: "policy-detail", Version: "v1"},
		Rules: []OperatorCostSelectionRule{
			{ID: "p-final", Basis: OperatorCostBasisP, Status: OperatorCostSelectionStatusFinal, RequireOperatorPayer: true},
		},
	}
	selectionInput := OperatorCostSelectionInput{
		Subject: subject, Currency: "USD", PayerClass: OperatorCostPayerOperator, Provenance: OperatorCostProvenanceAttempted,
		Reconciliation: OperatorCostReconciliationState{
			Ref:    OperatorCostReconciliationRef{ID: "recon-1", Version: 1, Fingerprint: strings.Repeat("f", 64)},
			Status: ReconciliationStatusDiscrepant, Complete: true,
		},
		Candidates: []OperatorCostCandidate{{
			Basis: OperatorCostBasisP, ValuationID: "valuation-p", ValuationVersion: 2,
			Currency: "USD", Amount: func() *MonetaryExactAmount { a := toleranceAmount(t, "USD", "1.32"); return &a }(),
			Completeness: economics.CompletenessComplete,
			Payer:        metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
			SourceRefs:   []metering.ObservationRef{{StoreID: storeID, ObservationID: "obs-provider-money-1", Revision: 1, PayloadHash: strings.Repeat("a", 64)}},
		}},
	}
	selection, err := SelectOperatorCost(policy, selectionInput)
	require.NoError(t, err)

	head := SelectedCostHead{
		AccountID: accountID, CallID: BillingCallID(billingCallID), HeadKey: "head-p",
		Subject: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, ALegID: aLegID, BillingCallID: billingCallID, BLegID: "b-detail-1"},
		Version: 1,
	}
	selectedRef := SelectedCostValuationRef{ValuationID: "valuation-p", Revision: 1, InputSetHash: strings.Repeat("d", 64)}
	selectedVal, err := NewSelectedCostValuation(selectedRef, selection)
	require.NoError(t, err)
	head.Selected = &selectedVal
	require.NoError(t, head.Validate())

	turnSummary := TurnResultSummary{
		CustomerCharge: Money{Nano: 2000000000, Currency: "USD"},
		ProviderCost:   Money{Nano: 1320000000, Currency: "USD"},
		GrossMargin:    Money{Nano: 680000000, Currency: "USD"},
		Processed:      true,
	}

	return EconomicDetailInput{
		Query:        EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID},
		Observations: []metering.Observation{localObs, providerObs, providerMoneyObs, statementObs},
		Valuations:   []economics.Valuation{eVal, qVal, pVal, sVal, rVal},
		Quantity:     &quantity,
		Monetary:     &monetary,
		Selection:    &selection,
		Heads:        []SelectedCostHead{head},
		TurnSummary:  &turnSummary,
	}
}

func TestEconomicDetailQueryNormalize(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	callQuery, err := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, BillingCallID: billingCallID, ALegID: aLegID}.Normalize()
	require.NoError(t, err)
	require.Equal(t, EconomicDetailDefaultLimit, callQuery.Limit)

	aLegQuery, err := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID}.Normalize()
	require.NoError(t, err)
	require.Equal(t, EconomicDetailDefaultLimit, aLegQuery.Limit)

	for _, tc := range []struct {
		name  string
		query EconomicDetailQuery
	}{
		{name: "missing store", query: EconomicDetailQuery{AccountID: accountID, ALegID: aLegID}},
		{name: "missing account", query: EconomicDetailQuery{StoreID: storeID, ALegID: aLegID}},
		{name: "missing call and aleg", query: EconomicDetailQuery{StoreID: storeID, AccountID: accountID}},
		{name: "call without aleg binding is allowed only when billing call present", query: EconomicDetailQuery{StoreID: storeID, AccountID: accountID, BillingCallID: "not-a-call-id"}},
		{name: "malformed billing call", query: EconomicDetailQuery{StoreID: storeID, AccountID: accountID, BillingCallID: "bc_short", ALegID: aLegID}},
		{name: "zero limit", query: EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, Limit: -1}},
		{name: "unbounded limit", query: EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, Limit: EconomicDetailMaxLimit + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := tc.query.Normalize()
			require.Error(t, err)
		})
	}
}

func TestAssembleEconomicDetailCompleteEQPSR(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)

	// Source-separated planes are never collapsed.
	bases := map[economics.ValuationBasis]bool{}
	for _, valuation := range got.Valuations {
		bases[valuation.Basis] = true
	}
	for _, basis := range []economics.ValuationBasis{
		economics.BasisLocalExpected, economics.BasisProviderQuantityLocal,
		economics.BasisProviderReported, economics.BasisStatementReported, economics.BasisCustomerPolicy,
	} {
		require.True(t, bases[basis], "basis %q must survive assembly", basis)
	}
	eVal := got.ValuationFor(economics.BasisLocalExpected)
	qVal := got.ValuationFor(economics.BasisProviderQuantityLocal)
	pVal := got.ValuationFor(economics.BasisProviderReported)
	require.NotNil(t, eVal)
	require.NotNil(t, qVal)
	require.NotNil(t, pVal)
	require.NotEqual(t, eVal.Totals[0].RoundedAmount, qVal.Totals[0].RoundedAmount)
	require.NotEqual(t, qVal.Totals[0].RoundedAmount, pVal.Totals[0].RoundedAmount)

	// Known subtotal follows the selected operator basis, margin is explicit.
	require.NotNil(t, got.Totals.SelectedAmount)
	require.Equal(t, "USD", got.Totals.SelectedCurrency)
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)

	// Summary projection is preserved verbatim.
	require.NotNil(t, got.Summary.Turn)
	require.Equal(t, *in.TurnSummary, *got.Summary.Turn)
}

func TestAssembleEconomicDetailPartialMissing(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	// Drop provider money: P valuation absent, provider charge observation removed.
	filteredVals := in.Valuations[:0]
	for _, valuation := range in.Valuations {
		if valuation.Basis != economics.BasisProviderReported {
			filteredVals = append(filteredVals, valuation)
		}
	}
	in.Valuations = filteredVals
	keptObs := in.Observations[:0]
	for _, observation := range in.Observations {
		if observation.ID != "obs-provider-money-1" {
			keptObs = append(keptObs, observation)
		}
	}
	in.Observations = keptObs
	in.Selection = nil
	in.Heads = nil
	in.Monetary = nil

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Nil(t, got.ValuationFor(economics.BasisProviderReported))
	require.False(t, got.Margin.Complete)
	require.Nil(t, got.Margin.Amount)
	require.NotEmpty(t, got.Margin.Reason)
}

func TestAssembleEconomicDetailIncomparable(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.Quantity = &ComponentQuantityComparison{Status: ReconciliationStatusIncomparable, Reason: ReconciliationReasonTokenizerMismatch, Complete: false}
	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Equal(t, ReconciliationStatusIncomparable, got.Quantity.Status)
	require.False(t, got.Quantity.Complete)
	require.False(t, got.Margin.Complete)
}

func TestAssembleEconomicDetailMultiCurrencyNoFX(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")
	// Replace the E plane with a multi-currency E carrying both USD and EUR
	// native totals. No FX conversion exists, so margin must stay incomplete.
	eVal := detailTestValuation(t, "valuation-e", economics.BasisLocalExpected, subject, storeID,
		detailTestCurrencyTotal(t, "USD", "1.00"), detailTestCurrencyTotal(t, "EUR", "5.00"))
	for i, valuation := range in.Valuations {
		if valuation.ID == "valuation-e" {
			in.Valuations[i] = eVal
		}
	}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	currencies := map[string]bool{}
	for _, total := range got.PerBasisTotals {
		for _, currencyTotal := range total.CurrencyTotals {
			currencies[currencyTotal.Currency] = true
		}
	}
	require.True(t, currencies["USD"])
	require.True(t, currencies["EUR"])
	// No implicit FX: multi-currency detail cannot report a complete single-currency margin.
	require.False(t, got.Margin.Complete)
	require.Nil(t, got.Margin.Amount)
}

func TestAssembleEconomicDetailBYOKExcludedFromOperator(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")
	byokCharge := metering.ReportedCharge{
		ChargeItemID: "charge-byok-1",
		Component: &metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		},
		Amount:   func() *metering.Decimal { v := detailTestDecimal(t, "9.99"); return &v }(),
		Currency: "USD", Kind: metering.ChargeKindComponent,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-creds"},
	}
	byokObs := detailTestObservation(t, "obs-byok-1", metering.OriginProvider, "stream-byok", 3, subject, nil, []metering.ReportedCharge{byokCharge})
	in.Observations = append(in.Observations, byokObs)

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Payers.HasCustomerBYOK)
	require.True(t, got.Payers.HasOperatorPayable)
	// Operator selected subtotal must not silently include the BYOK charge:
	// the selected amount remains the frozen P selection, and BYOK is visible separately.
	require.NotNil(t, got.Totals.SelectedAmount)
	for _, charge := range got.Coverage.AggregateCharges {
		require.NotEqual(t, "charge-byok-1", charge.ChargeItemID, "BYOK component charge must not be reclassified as aggregate")
	}
	foundBYOK := false
	for _, observation := range got.Observations {
		for _, charge := range observation.Charges {
			if charge.ChargeItemID == "charge-byok-1" {
				foundBYOK = true
				require.Equal(t, metering.PaymentPartyCustomer, charge.Payer.Kind)
			}
		}
	}
	require.True(t, foundBYOK, "BYOK charge must remain visible with customer payer")
}

func TestAssembleEconomicDetailAggregateOnly(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-agg")
	aggCharge := metering.ReportedCharge{
		ChargeItemID: "charge-agg-1",
		Component:    nil,
		Amount:       func() *metering.Decimal { v := detailTestDecimal(t, "12.00"); return &v }(),
		Currency:     "USD", Kind: metering.ChargeKindAggregate,
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
	}
	aggObs := detailTestObservation(t, "obs-agg-1", metering.OriginProvider, "stream-agg", 1, subject, nil, []metering.ReportedCharge{aggCharge})
	in.Observations = append(in.Observations, aggObs)

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Coverage.AggregateOnly)
	found := false
	for _, charge := range got.Coverage.AggregateCharges {
		if charge.ChargeItemID == "charge-agg-1" {
			found = true
			require.Nil(t, charge.Component, "aggregate charge must not invent a component key")
		}
	}
	require.True(t, found, "aggregate charge must be surfaced")
}

func TestAssembleEconomicDetailAccountPeriodCoverage(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	storeID := "store-detail"
	windowSubject := metering.SubjectRef{
		Kind: metering.SubjectAccountWindow, StoreID: storeID,
		ProviderAccountKey: "provider-acct", PoolID: "pool-1", WindowID: "window-1",
		ResetAt: time.Unix(1_700_010_000, 0).UTC(),
	}
	gaugeMeasure := metering.Measure{
		Key: metering.ComponentKey{
			Direction: metering.DirectionNone, Component: "provider:account_utilization",
			Unit: metering.UnitPercent, SchemaID: "provider:account:v1",
		},
		Value:   func() *metering.Decimal { v := detailTestDecimal(t, "12.5"); return &v }(),
		Quality: metering.QualityObserved,
	}
	now := time.Unix(1_700_010_000, 0).UTC()
	gaugeObs := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "obs-gauge-1", SourceEventKey: "obs-gauge-1-event",
		Revision: 1, StreamID: "stream-gauge", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject:   windowSubject,
		Correlation: metering.CorrelationV2{
			StoreID: windowSubject.StoreID, ProviderAccountKey: windowSubject.ProviderAccountKey,
		},
		Semantics:  metering.SemanticsGauge,
		ObservedAt: now, ReceivedAt: now, MappingRef: "detail.test.v1",
		Measures: []metering.Measure{gaugeMeasure},
	}
	require.NoError(t, gaugeObs.Validate())
	canonical, err := gaugeObs.Canonical()
	require.NoError(t, err)
	in.Observations = append(in.Observations, canonical)

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotEmpty(t, got.Coverage.NonRequestSubjects)
	found := false
	for _, subject := range got.Coverage.NonRequestSubjects {
		if subject.Kind == metering.SubjectAccountWindow {
			found = true
		}
	}
	require.True(t, found, "account-window subject must remain non-request scoped")
	// Gauge must never appear as a B-leg inference measure.
	for _, observation := range got.Observations {
		if observation.ID == "obs-gauge-1" {
			require.Equal(t, metering.SubjectAccountWindow, observation.Subject.Kind)
		}
	}
}

func TestAssembleEconomicDetailALegScope(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, _ := detailTestScope()
	subjectOne := detailTestSubject(storeID, aLegID, "bc_"+strings.Repeat("a", 32), "b-aleg-1")
	subjectTwo := detailTestSubject(storeID, aLegID, "bc_"+strings.Repeat("b", 32), "b-aleg-2")
	localOne := detailTestObservation(t, "obs-aleg-1", metering.OriginLocal, "stream-aleg-1", 1, subjectOne,
		[]metering.Measure{detailTestMeasure(t, metering.ComponentInputToken, "50")}, nil)
	localTwo := detailTestObservation(t, "obs-aleg-2", metering.OriginLocal, "stream-aleg-2", 1, subjectTwo,
		[]metering.Measure{detailTestMeasure(t, metering.ComponentInputToken, "70")}, nil)
	eVal := detailTestValuation(t, "valuation-aleg-e", economics.BasisLocalExpected, subjectOne, storeID, detailTestCurrencyTotal(t, "USD", "1.20"))
	in := EconomicDetailInput{
		Query:        EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID},
		Observations: []metering.Observation{localOne, localTwo},
		Valuations:   []economics.Valuation{eVal},
	}
	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Equal(t, aLegID, got.Scope.ALegID)
	require.Empty(t, got.Scope.BillingCallID)
	require.Len(t, got.Observations, 2)
	require.NotNil(t, got.ValuationFor(economics.BasisLocalExpected))
	// Two billing calls under one A-leg remain distinct B-leg evidence.
	seen := map[string]bool{}
	for _, observation := range got.Observations {
		seen[observation.Subject.BillingCallID] = true
	}
	require.True(t, seen["bc_"+strings.Repeat("a", 32)])
	require.True(t, seen["bc_"+strings.Repeat("b", 32)])
}

// TestAssembleEconomicDetailDistinctSubjectSameBasis is the Finding 3A core
// contract: two valuations that share a basis but belong to distinct
// authoritative subjects are independent contributions and must both survive.
// A repeated valuation of the same logical stream identity (subject,
// perspective, basis) is a genuine duplicate and remains rejected.
func TestAssembleEconomicDetailDistinctSubjectSameBasis(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subjectOne := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")
	subjectTwo := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-2")

	first := detailTestValuation(t, "valuation-subject-1-e", economics.BasisLocalExpected, subjectOne, storeID, detailTestCurrencyTotal(t, "USD", "1.00"))
	first.Payer = metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-subject-1"}
	require.NoError(t, first.Validate())
	second := detailTestValuation(t, "valuation-subject-2-e", economics.BasisLocalExpected, subjectTwo, storeID, detailTestCurrencyTotal(t, "EUR", "2.00"))
	second.Payer = metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-subject-2"}
	require.NoError(t, second.Validate())

	query := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}
	got, err := AssembleEconomicDetail(EconomicDetailInput{Query: query, Valuations: []economics.Valuation{first, second}})
	require.NoError(t, err)
	require.Len(t, got.Valuations, 2, "distinct subjects sharing a basis must both survive")

	byID := map[string]economics.Valuation{}
	for _, valuation := range got.Valuations {
		byID[valuation.ID] = valuation
	}
	require.Equal(t, "USD", byID["valuation-subject-1-e"].Totals[0].Currency)
	require.Equal(t, metering.PaymentPartyOperator, byID["valuation-subject-1-e"].Payer.Kind)
	require.Equal(t, "EUR", byID["valuation-subject-2-e"].Totals[0].Currency)
	require.Equal(t, metering.PaymentPartyOperator, byID["valuation-subject-2-e"].Payer.Kind)
	require.Equal(t, "op-subject-2", byID["valuation-subject-2-e"].Payer.ID)
	require.Len(t, got.PerBasisTotals, 2, "each subject's plane keeps its own native totals")
	for _, total := range got.PerBasisTotals {
		require.Equal(t, economics.BasisLocalExpected, total.Basis)
	}

	t.Run("same logical stream identity remains rejected", func(t *testing.T) {
		t.Parallel()
		duplicate := first.Clone()
		duplicate.ID = "valuation-subject-1-e-revision"
		require.NoError(t, duplicate.Validate())
		_, err := AssembleEconomicDetail(EconomicDetailInput{Query: query, Valuations: []economics.Valuation{first, duplicate}})
		require.ErrorIs(t, err, ErrEconomicDetailInvalid)
	})
}

func TestAssembleEconomicDetailPreservesSummary(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	retail := ALegRetailTotals{Currency: "USD", KnownSubtotal: Money{Nano: 2000000000, Currency: "USD"}, SettledCalls: 1}
	provider := ALegProviderTotals{Currency: "USD", KnownSubtotal: Money{Nano: 1320000000, Currency: "USD"}, KnownLegs: 1}
	in.RetailTotals = &retail
	in.ProviderTotals = &provider
	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Summary.Turn)
	require.Equal(t, *in.TurnSummary, *got.Summary.Turn)
	require.NotNil(t, got.Summary.Retail)
	require.Equal(t, retail, *got.Summary.Retail)
	require.NotNil(t, got.Summary.Provider)
	require.Equal(t, provider, *got.Summary.Provider)
}

func TestAssembleEconomicDetailRejectsCrossScope(t *testing.T) {
	t.Parallel()
	base := detailTestCompleteInput(t)

	t.Run("cross-store observation", func(t *testing.T) {
		t.Parallel()
		in := base
		other := in.Observations[0].Clone()
		other.Subject.StoreID = "store-other"
		other.Correlation.StoreID = "store-other"
		canonical, err := other.Canonical()
		require.NoError(t, err)
		in.Observations = []metering.Observation{canonical}
		_, err = AssembleEconomicDetail(in)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})

	t.Run("cross-call observation", func(t *testing.T) {
		t.Parallel()
		in := base
		other := in.Observations[0].Clone()
		other.Subject.BillingCallID = "bc_" + strings.Repeat("b", 32)
		other.Correlation.BillingCallID = "bc_" + strings.Repeat("b", 32)
		canonical, err := other.Canonical()
		require.NoError(t, err)
		in.Observations = []metering.Observation{canonical}
		_, err = AssembleEconomicDetail(in)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})

	t.Run("cross-aleg observation", func(t *testing.T) {
		t.Parallel()
		in := base
		other := in.Observations[0].Clone()
		other.Subject.ALegID = "a-other"
		other.Correlation.ALegID = "a-other"
		canonical, err := other.Canonical()
		require.NoError(t, err)
		in.Observations = []metering.Observation{canonical}
		_, err = AssembleEconomicDetail(in)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})

	t.Run("ambiguous query both empty", func(t *testing.T) {
		t.Parallel()
		in := base
		in.Query.BillingCallID = ""
		in.Query.ALegID = ""
		_, err := AssembleEconomicDetail(in)
		require.Error(t, err)
	})

	t.Run("valuation store mismatch", func(t *testing.T) {
		t.Parallel()
		in := base
		other := in.Valuations[0].Clone()
		other.Subject.StoreID = "store-other"
		for i := range other.InputObservations {
			other.InputObservations[i].StoreID = "store-other"
		}
		for i := range other.MissingObservations {
			other.MissingObservations[i].StoreID = "store-other"
		}
		for _, line := range other.Lines {
			for i := range line.SourceObservationRefs {
				line.SourceObservationRefs[i].StoreID = "store-other"
			}
		}
		in.Valuations = []economics.Valuation{other}
		_, err := AssembleEconomicDetail(in)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})
}

func TestAssembleEconomicDetailBoundsAndOrder(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	// Reverse input order; output must be deterministic sequence order.
	for i, j := 0, len(in.Observations)-1; i < j; i, j = i+1, j-1 {
		in.Observations[i], in.Observations[j] = in.Observations[j], in.Observations[i]
	}
	in.Query.Limit = 2
	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.LessOrEqual(t, len(got.Observations), 2)
	require.True(t, got.Truncated)
	// Sequence order preserved within returned page.
	for i := 1; i < len(got.Observations); i++ {
		prev, curr := got.Observations[i-1], got.Observations[i]
		if prev.StreamID == curr.StreamID {
			require.LessOrEqual(t, prev.Sequence, curr.Sequence)
		} else {
			require.LessOrEqual(t, prev.StreamID, curr.StreamID)
		}
	}
	// Valuations sorted by basis rank deterministically.
	for i := 1; i < len(got.Valuations); i++ {
		require.LessOrEqual(t, basisRankForTest(got.Valuations[i-1].Basis), basisRankForTest(got.Valuations[i].Basis))
	}

	t.Run("observation bound exceeded", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		in.Observations = make([]metering.Observation, MaxEconomicDetailObservations+1)
		_, err := AssembleEconomicDetail(in)
		require.ErrorIs(t, err, ErrEconomicDetailBoundExceeded)
	})
}

func TestEconomicDetailExposesNoRawContent(t *testing.T) {
	t.Parallel()
	// PayloadHash is an explicit allowlist exception: it is a SHA-256 content
	// hash used for immutable revision identity, never raw provider content.
	// All other raw-content carriers (prompts, responses, bodies, headers,
	// cookies, ciphertext, tool args, secrets) must not appear as DTO fields.
	forbidden := []string{"prompt", "response", "raw", "header", "cookie", "ciphertext", "output_text", "tool_arg", "secret", "authorization"}
	seen := map[string]bool{}
	var walk func(prefix string, typ reflect.Type)
	walk = func(prefix string, typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		key := typ.PkgPath() + "." + typ.Name()
		if seen[key] {
			return
		}
		seen[key] = true
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			lower := strings.ToLower(field.Name + " " + string(field.Tag))
			for _, word := range forbidden {
				require.NotContains(t, lower, word, "type %s field %s must not expose raw content", prefix+typ.Name(), field.Name)
			}
			walk(prefix+typ.Name()+".", field.Type)
		}
	}
	walk("", reflect.TypeOf(EconomicDetail{}))
	walk("", reflect.TypeOf(EconomicDetailQuery{}))
	walk("", reflect.TypeOf(EconomicDetailInput{}))
}

// detailTestAllocationLine builds one source-preserving allocation
// contribution for the canonical detail scope. Callers may mutate the returned
// Target to probe scope enforcement.
func detailTestAllocationLine(storeID, accountID, aLegID, billingCallID, bLegID string) AllocatedCostLine {
	return AllocatedCostLine{
		AllocationID: "alloc-detail-1", AllocationVersion: 1, AllocationRevision: 1,
		Operation: economics.AllocationOperationAllocate,
		Policy:    economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)},
		TargetID:  "t-detail-leg",
		SourceSubject: metering.SubjectRef{
			Kind: metering.SubjectResource, StoreID: storeID, AccountID: accountID,
			ResourceID: "res-detail", PeriodID: "2026-09",
		},
		SourceBasis: economics.BasisAllocatedCost,
		Currency:    "USD",
		Target: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: accountID,
			ALegID: aLegID, BillingCallID: billingCallID, BLegID: bLegID,
		},
		Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"},
		Share:  economics.AllocationFraction{Numerator: "1", Denominator: "1"},
	}
}

// TestAssembleEconomicDetailEnforcesAllocationTargetScope proves the assembly
// contract fails closed on a contribution whose target ownership is outside
// the requested account/tenant/call/A-leg scope, while genuinely in-scope call
// and B-leg targets survive unchanged. This is defense in depth over the
// durable reader's authoritative membership filter.
func TestAssembleEconomicDetailEnforcesAllocationTargetScope(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	t.Run("in-scope call and leg targets survive", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		legLine := detailTestAllocationLine(storeID, accountID, aLegID, billingCallID, "b-detail-1")
		callLine := detailTestAllocationLine(storeID, accountID, aLegID, billingCallID, "b-detail-1")
		callLine.TargetID = "t-detail-call"
		callLine.Target = metering.SubjectRef{
			Kind: metering.SubjectBillingCall, StoreID: storeID, AccountID: accountID,
			ALegID: aLegID, BillingCallID: billingCallID,
		}
		in.Allocations = []AllocatedCostLine{legLine, callLine}
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.Len(t, got.Coverage.Allocations, 2)
	})

	t.Run("foreign account target fails closed", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		line := detailTestAllocationLine(storeID, accountID, aLegID, billingCallID, "b-detail-1")
		line.Target.AccountID = "acct-other"
		in.Allocations = []AllocatedCostLine{line}
		_, err := AssembleEconomicDetail(in)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})

	t.Run("foreign call target fails closed", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		line := detailTestAllocationLine(storeID, accountID, aLegID, billingCallID, "b-detail-1")
		line.Target.BillingCallID = "bc_" + strings.Repeat("b", 32)
		in.Allocations = []AllocatedCostLine{line}
		_, err := AssembleEconomicDetail(in)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})

	t.Run("foreign aleg target fails closed", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		line := detailTestAllocationLine(storeID, accountID, aLegID, billingCallID, "b-detail-1")
		line.Target.ALegID = "a-other"
		in.Allocations = []AllocatedCostLine{line}
		_, err := AssembleEconomicDetail(in)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})

	t.Run("foreign tenant target fails closed under tenant scope", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		in.Query.TenantID = "tenant-detail"
		line := detailTestAllocationLine(storeID, accountID, aLegID, billingCallID, "b-detail-1")
		line.Target.TenantID = "tenant-other"
		in.Allocations = []AllocatedCostLine{line}
		_, err := AssembleEconomicDetail(in)
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})
}

func basisRankForTest(basis economics.ValuationBasis) int {
	switch basis {
	case economics.BasisLocalExpected:
		return 0
	case economics.BasisProviderQuantityLocal:
		return 1
	case economics.BasisProviderReported:
		return 2
	case economics.BasisStatementReported:
		return 3
	case economics.BasisCustomerPolicy:
		return 4
	default:
		return 5
	}
}

// detailTestReconciliation builds one independently identified reconciliation
// comparison for one subject, carrying both an exact quantity comparison and a
// monetary decomposition derived from the same subject's E/Q/P valuations.
func detailTestReconciliation(t *testing.T, storeID string, subject metering.SubjectRef, status ReconciliationComparisonStatus) EconomicDetailReconciliation {
	t.Helper()
	quantity := ComponentQuantityComparison{Status: status, Complete: true}
	prefix := "recon-" + subject.BLegID
	refs := []metering.ObservationRef{{
		StoreID: storeID, ObservationID: prefix + "-observation", Revision: 1, PayloadHash: strings.Repeat("c", 64),
	}}
	eVal := detailTestValuation(t, prefix+"-e", economics.BasisLocalExpected, subject, storeID, detailTestCurrencyTotal(t, "USD", "1.00"))
	qVal := detailTestValuation(t, prefix+"-q", economics.BasisProviderQuantityLocal, subject, storeID, detailTestCurrencyTotal(t, "USD", "1.10"))
	pVal := detailTestValuation(t, prefix+"-p", economics.BasisProviderReported, subject, storeID, detailTestCurrencyTotal(t, "USD", "1.32"))
	for _, valuation := range []*economics.Valuation{&eVal, &qVal, &pVal} {
		valuation.InputObservations = refs
	}
	monetary, err := DecomposeMonetaryDiscrepancies(MonetaryDiscrepancyInput{Valuations: []economics.Valuation{eVal, qVal, pVal}})
	require.NoError(t, err)
	return EconomicDetailReconciliation{Subject: subject, Quantity: &quantity, Monetary: &monetary}
}

// TestAssembleEconomicDetailReconciliationSet is the Finding 3B core contract:
// every independently identified in-scope reconciliation subject survives as
// its own comparison; distinct subjects are never collapsed to the first found
// result, a genuine duplicate subject identity is rejected, ordering is
// deterministic, and the explicit bound fails closed.
func TestAssembleEconomicDetailReconciliationSet(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	subjectOne := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1")
	subjectTwo := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-2")
	first := detailTestReconciliation(t, storeID, subjectOne, ReconciliationStatusDiscrepant)
	second := detailTestReconciliation(t, storeID, subjectTwo, ReconciliationStatusMatched)
	query := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}

	// Input order is deliberately reversed from the deterministic subject order.
	got, err := AssembleEconomicDetail(EconomicDetailInput{
		Query:           query,
		Reconciliations: []EconomicDetailReconciliation{second, first},
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 2, "distinct subject reconciliations must all survive")
	require.Equal(t, "b-detail-1", got.Reconciliations[0].Subject.BLegID)
	require.Equal(t, "b-detail-2", got.Reconciliations[1].Subject.BLegID)
	require.Equal(t, ReconciliationStatusDiscrepant, got.Reconciliations[0].Quantity.Status)
	require.Equal(t, ReconciliationStatusMatched, got.Reconciliations[1].Quantity.Status)
	require.NotNil(t, got.Reconciliations[0].Monetary)
	require.NotNil(t, got.Reconciliations[1].Monetary)

	// Singular convenience fields project the deterministic first entry.
	require.NotNil(t, got.Quantity)
	require.Equal(t, ReconciliationStatusDiscrepant, got.Quantity.Status)
	require.NotNil(t, got.Monetary)

	t.Run("true duplicate subject identity rejected", func(t *testing.T) {
		t.Parallel()
		duplicate := detailTestReconciliation(t, storeID, subjectOne, ReconciliationStatusMatched)
		_, err := AssembleEconomicDetail(EconomicDetailInput{
			Query:           query,
			Reconciliations: []EconomicDetailReconciliation{first, duplicate},
		})
		require.ErrorIs(t, err, ErrEconomicDetailInvalid)
	})

	t.Run("cross-scope subject fails closed", func(t *testing.T) {
		t.Parallel()
		foreign := detailTestSubject(storeID, aLegID, "bc_"+strings.Repeat("b", 32), "b-foreign")
		entry := detailTestReconciliation(t, storeID, foreign, ReconciliationStatusMatched)
		_, err := AssembleEconomicDetail(EconomicDetailInput{
			Query:           query,
			Reconciliations: []EconomicDetailReconciliation{entry},
		})
		require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
	})

	t.Run("bound exceeded fails closed", func(t *testing.T) {
		t.Parallel()
		entries := make([]EconomicDetailReconciliation, 0, MaxEconomicDetailReconciliations+1)
		for i := 0; i <= MaxEconomicDetailReconciliations; i++ {
			subject := detailTestSubject(storeID, aLegID, billingCallID, fmt.Sprintf("b-bound-%04d", i))
			entries = append(entries, EconomicDetailReconciliation{
				Subject:  subject,
				Quantity: &ComponentQuantityComparison{Status: ReconciliationStatusMatched, Complete: true},
			})
		}
		_, err := AssembleEconomicDetail(EconomicDetailInput{Query: query, Reconciliations: entries})
		require.ErrorIs(t, err, ErrEconomicDetailBoundExceeded)
	})
}

// detailTestPersistedHead freezes one persisted selected-cost head for the
// canonical detail scope. amount is optional so known-zero and
// not-operator-payable selections can carry no posted value.
func detailTestPersistedHead(t *testing.T, headKey string, basis OperatorCostSelectionBasis, status OperatorCostSelectionStatus, provenance OperatorCostProvenance, currency, amount string) SelectedCostHead {
	t.Helper()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	head := detailTestPersistedHeadFor(t, accountID, billingCallID,
		detailTestSubject(storeID, aLegID, billingCallID, "b-detail-1"),
		headKey, basis, status, provenance, currency, amount)
	// Bind the persisted selection to the matching frozen fixture valuation so
	// its exact payable line can prove the request charge, mirroring the
	// production identity carried by a durable head.
	if head.Selected != nil {
		head.Selected.Ref.ValuationID = "valuation-" + string(basis)
	}
	return head
}

// detailTestPersistedHeadFor freezes one persisted selected-cost head for an
// explicit account/call/subject so A-leg scope can hold independent calls.
func detailTestPersistedHeadFor(t *testing.T, accountID, callID string, subject metering.SubjectRef, headKey string, basis OperatorCostSelectionBasis, status OperatorCostSelectionStatus, provenance OperatorCostProvenance, currency, amount string) SelectedCostHead {
	t.Helper()
	selection := OperatorCostSelectionResult{
		Status: status, Basis: basis, Provenance: provenance, Currency: currency,
	}
	if amount != "" {
		exact := toleranceAmount(t, currency, amount)
		selection.Amount = &exact
	}
	selected, err := NewSelectedCostValuation(
		SelectedCostValuationRef{ValuationID: "valuation-" + headKey, Revision: 1, InputSetHash: strings.Repeat("d", 64)},
		selection,
	)
	require.NoError(t, err)
	head := SelectedCostHead{
		AccountID: accountID, CallID: BillingCallID(callID), HeadKey: headKey,
		Subject: subject, Version: 1, Selected: &selected,
	}
	require.NoError(t, head.Validate())
	return head
}

// TestAssembleEconomicDetailDerivesPersistedSelectionFromSingleHead is the
// Finding 4A core contract: one authoritative persisted selected head is
// projected into the economic detail as the truthful singular selection even
// when the full Phase 12 candidate set is not retained. Totals, completeness
// and margin follow the persisted fact and must not report missing_selected.
func TestAssembleEconomicDetailDerivesPersistedSelectionFromSingleHead(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	// The durable adapter cannot retain the whole candidate set; drop it.
	in.Selection = nil

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Selection, "the single persisted head is the truthful singular selection")
	require.Empty(t, got.Selection.Candidates, "a candidate set that was never persisted must not be invented")
	require.Equal(t, OperatorCostSelectionStatusFinal, got.Selection.Status)
	require.Equal(t, OperatorCostBasisP, got.Selection.Basis)

	require.Equal(t, OperatorCostSelectionStatusFinal, got.Totals.SelectedStatus)
	require.Equal(t, OperatorCostBasisP, got.Totals.SelectedBasis)
	require.Equal(t, "USD", got.Totals.SelectedCurrency)
	require.NotNil(t, got.Totals.SelectedAmount)
	require.Equal(t, economics.CompletenessComplete, got.Totals.SelectedCompleteness)
	require.Equal(t, 1, got.Totals.SelectedHeadCount)
	require.False(t, got.Totals.SelectedAmbiguous)

	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
	require.Equal(t, "complete", got.Margin.Reason)
}

// TestAssembleEconomicDetailMultiplePersistedHeadsAreAmbiguous proves distinct
// independent heads are never summed, collapsed or FX-converted merely because
// a basis matches: the scope reports an explicit ambiguous status instead.
func TestAssembleEconomicDetailMultiplePersistedHeadsAreAmbiguous(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.Selection = nil
	second := in.Heads[0].Clone()
	second.HeadKey = "head-p-2"
	second.Subject.BLegID = "b-detail-2"
	require.NoError(t, second.Validate())
	// Deliberately reversed input order: output order stays deterministic.
	in.Heads = []SelectedCostHead{second, in.Heads[0]}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Len(t, got.Heads, 2, "both independent heads survive")
	require.Equal(t, "head-p", got.Heads[0].HeadKey)
	require.Equal(t, "head-p-2", got.Heads[1].HeadKey)
	require.Nil(t, got.Selection, "no singular selection can represent two independent heads")
	require.Equal(t, 2, got.Totals.SelectedHeadCount)
	require.True(t, got.Totals.SelectedAmbiguous)
	require.Nil(t, got.Totals.SelectedAmount, "independent heads must never be summed")
	require.False(t, got.Margin.Complete)
	require.Equal(t, "ambiguous_selected", got.Margin.Reason)
	require.Nil(t, got.Margin.Amount)
}

// TestAssembleEconomicDetailALegMultipleHeadsNeverCollapse proves an A-leg
// scope with heads on independent calls keeps both, orders them deterministically
// by call then head key, and never collapses or sums them by shared basis.
func TestAssembleEconomicDetailALegMultipleHeadsNeverCollapse(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, _ := detailTestScope()
	callOne := "bc_" + strings.Repeat("1", 32)
	callTwo := "bc_" + strings.Repeat("2", 32)
	subjectOne := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, ALegID: aLegID, BillingCallID: callOne, BLegID: "b-one"}
	subjectTwo := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, ALegID: aLegID, BillingCallID: callTwo, BLegID: "b-two"}
	// The lexically later call deliberately carries the lexically earlier head
	// key so call-then-head ordering is observable.
	headOne := detailTestPersistedHeadFor(t, accountID, callOne, subjectOne, "head-z", OperatorCostBasisP, OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "1.00")
	headTwo := detailTestPersistedHeadFor(t, accountID, callTwo, subjectTwo, "head-a", OperatorCostBasisP, OperatorCostSelectionStatusFinal, OperatorCostProvenanceAttempted, "USD", "2.00")

	got, err := AssembleEconomicDetail(EconomicDetailInput{
		Query: EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID},
		Heads: []SelectedCostHead{headOne, headTwo},
	})
	require.NoError(t, err)
	require.Len(t, got.Heads, 2, "distinct calls are independent contributions")
	require.Equal(t, callOne, got.Heads[0].CallID.String())
	require.Equal(t, "head-z", got.Heads[0].HeadKey)
	require.Equal(t, callTwo, got.Heads[1].CallID.String())
	require.Equal(t, "head-a", got.Heads[1].HeadKey)
	require.Nil(t, got.Selection)
	require.True(t, got.Totals.SelectedAmbiguous)
	require.Nil(t, got.Totals.SelectedAmount)
	require.Equal(t, "ambiguous_selected", got.Margin.Reason)
}

// TestAssembleEconomicDetailUnselectedHeadIsExplicitlyMissing proves an
// authoritative but unselected head stays explicitly missing instead of being
// treated as a known zero or an ambiguous selection.
func TestAssembleEconomicDetailUnselectedHeadIsExplicitlyMissing(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.Selection = nil
	in.Heads = []SelectedCostHead{{
		AccountID: in.Heads[0].AccountID, CallID: in.Heads[0].CallID,
		HeadKey: "head-empty", Subject: in.Heads[0].Subject,
	}}
	require.NoError(t, in.Heads[0].Validate())

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Len(t, got.Heads, 1)
	require.Nil(t, got.Heads[0].Selected)
	require.Nil(t, got.Selection)
	require.Equal(t, 0, got.Totals.SelectedHeadCount)
	require.False(t, got.Totals.SelectedAmbiguous)
	require.Nil(t, got.Totals.SelectedAmount)
	require.False(t, got.Margin.Complete)
	require.Equal(t, "missing_selected", got.Margin.Reason)
}

// TestAssembleEconomicDetailPersistedHeadStatusesStayDistinct proves a single
// persisted head keeps its exact selection status and completeness, and that
// provisional/conflict/incomparable/not-operator-payable/known-zero are not
// conflated with missing or with each other.
func TestAssembleEconomicDetailPersistedHeadStatusesStayDistinct(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		status       OperatorCostSelectionStatus
		provenance   OperatorCostProvenance
		amount       string
		completeness economics.Completeness
		margin       string
		complete     bool
	}{
		{"provisional", OperatorCostSelectionStatusProvisional, OperatorCostProvenanceAttempted, "1.32", economics.CompletenessPartial, "selected_incomplete", false},
		{"known zero", OperatorCostSelectionStatusKnownZero, OperatorCostProvenanceNeverStarted, "0", economics.CompletenessComplete, "complete", true},
		{"conflict", OperatorCostSelectionStatusConflict, OperatorCostProvenanceAttempted, "1.32", economics.CompletenessConflict, "selected_incomplete", false},
		{"incomparable", OperatorCostSelectionStatusIncomparable, OperatorCostProvenanceAttempted, "1.32", economics.CompletenessUnavailable, "selected_incomplete", false},
		{"not operator payable", OperatorCostSelectionStatusNotOperatorPayable, OperatorCostProvenanceAttempted, "", economics.CompletenessUnavailable, "not_operator_payable", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := detailTestCompleteInput(t)
			in.Selection = nil
			in.Heads = []SelectedCostHead{detailTestPersistedHead(t, "head-status", OperatorCostBasisP, tc.status, tc.provenance, "USD", tc.amount)}
			got, err := AssembleEconomicDetail(in)
			require.NoError(t, err)
			require.Equal(t, tc.status, got.Totals.SelectedStatus)
			require.Equal(t, tc.completeness, got.Totals.SelectedCompleteness)
			require.False(t, got.Totals.SelectedAmbiguous)
			require.Equal(t, 1, got.Totals.SelectedHeadCount)
			require.Equal(t, tc.margin, got.Margin.Reason)
			require.Equal(t, tc.complete, got.Margin.Complete)
		})
	}
}

// TestAssembleEconomicDetailRejectsDuplicateHeadIdentity proves a repeated
// authoritative head identity fails closed instead of silently overwriting or
// double-counting one economic charge.
func TestAssembleEconomicDetailRejectsDuplicateHeadIdentity(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.Selection = nil
	duplicate := in.Heads[0].Clone()
	in.Heads = []SelectedCostHead{in.Heads[0], duplicate}
	_, err := AssembleEconomicDetail(in)
	require.ErrorIs(t, err, ErrEconomicDetailInvalid)
}

// TestAssembleEconomicDetailHeadScopeMismatch proves account, call, A-leg and
// tenant scope are enforced on persisted heads; foreign evidence never leaks
// into another scope.
func TestAssembleEconomicDetailHeadScopeMismatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(head *SelectedCostHead, query *EconomicDetailQuery)
	}{
		{"foreign account", func(head *SelectedCostHead, _ *EconomicDetailQuery) { head.AccountID = "acct-other" }},
		{"foreign call", func(head *SelectedCostHead, _ *EconomicDetailQuery) {
			foreign := "bc_" + strings.Repeat("b", 32)
			head.CallID = BillingCallID(foreign)
			head.Subject.BillingCallID = foreign
		}},
		{"foreign aleg", func(head *SelectedCostHead, _ *EconomicDetailQuery) { head.Subject.ALegID = "a-other" }},
		{"foreign tenant", func(head *SelectedCostHead, query *EconomicDetailQuery) {
			query.TenantID = "tenant-detail"
			head.Subject.TenantID = "tenant-other"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := detailTestCompleteInput(t)
			in.Selection = nil
			head := in.Heads[0].Clone()
			query := in.Query
			tc.mutate(&head, &query)
			in.Query = query
			in.Heads = []SelectedCostHead{head}
			_, err := AssembleEconomicDetail(in)
			require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
		})
	}
}

// TestAssembleEconomicDetailHeadDeepCopy proves assembly deep-copies persisted
// heads so neither caller nor result mutation can corrupt the other.
func TestAssembleEconomicDetailHeadDeepCopy(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.Selection = nil
	require.NotNil(t, in.Heads[0].Selected)
	require.NotNil(t, in.Heads[0].Selected.Amount)
	originalAmount := *in.Heads[0].Selected.Amount

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Heads[0].Selected)
	require.NotNil(t, got.Heads[0].Selected.Amount)
	require.True(t, monetaryExactAmountsEqual(&originalAmount, got.Heads[0].Selected.Amount))

	// Mutating the caller's head must not change the assembled result.
	*in.Heads[0].Selected = SelectedCostValuation{Status: OperatorCostSelectionStatusUnknown}
	require.Equal(t, OperatorCostSelectionStatusFinal, got.Heads[0].Selected.Status)
	require.True(t, monetaryExactAmountsEqual(&originalAmount, got.Heads[0].Selected.Amount))

	// Mutating the assembled result must not change the caller's head.
	*in.Heads[0].Selected = SelectedCostValuation{Status: OperatorCostSelectionStatusFinal, Amount: &originalAmount}
	before := *got.Heads[0].Selected.Amount
	*got.Heads[0].Selected = SelectedCostValuation{Status: OperatorCostSelectionStatusUnknown}
	require.Equal(t, OperatorCostSelectionStatusFinal, in.Heads[0].Selected.Status)
	require.True(t, monetaryExactAmountsEqual(&before, in.Heads[0].Selected.Amount))
}

// TestAssembleEconomicDetailPreservesHeadPostingState proves every persisted
// head keeps its own exact posting state through assembly and deep copy, and
// that a non-empty unknown posting state fails closed.
func TestAssembleEconomicDetailPreservesHeadPostingState(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.Selection = nil
	applied := in.Heads[0].Clone()
	applied.HeadKey = "head-ps-applied"
	applied.PostingState = SelectedCostPostingApplied
	pending := in.Heads[0].Clone()
	pending.HeadKey = "head-ps-pending"
	pending.PostingState = SelectedCostPostingPending
	require.NoError(t, applied.Validate())
	require.NoError(t, pending.Validate())
	in.Heads = []SelectedCostHead{pending, applied}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Len(t, got.Heads, 2)
	require.Equal(t, SelectedCostPostingApplied, got.Heads[0].PostingState)
	require.Equal(t, SelectedCostPostingPending, got.Heads[1].PostingState)

	// Deep copy: mutating the caller's head must not change the result.
	in.Heads[0].PostingState = SelectedCostPostingReplayed
	pending.PostingState = SelectedCostPostingUnposted
	require.Equal(t, SelectedCostPostingApplied, got.Heads[0].PostingState)

	t.Run("unknown posting state fails closed", func(t *testing.T) {
		t.Parallel()
		corrupt := in.Heads[1].Clone()
		corrupt.HeadKey = "head-ps-corrupt"
		corrupt.PostingState = SelectedCostPostingStatus("forged")
		_, err := AssembleEconomicDetail(EconomicDetailInput{Query: in.Query, Heads: []SelectedCostHead{corrupt}})
		require.ErrorIs(t, err, ErrEconomicDetailInvalid)
	})

	t.Run("posting state is exposed in operator JSON", func(t *testing.T) {
		t.Parallel()
		jsonInput := detailTestCompleteInput(t)
		jsonInput.Selection = nil
		head := jsonInput.Heads[0].Clone()
		head.PostingState = SelectedCostPostingPending
		jsonInput.Heads = []SelectedCostHead{head}
		detail, err := AssembleEconomicDetail(jsonInput)
		require.NoError(t, err)
		payload, err := json.Marshal(detail)
		require.NoError(t, err)
		require.Contains(t, string(payload), `"PostingState":"pending"`)
	})
}

// TestAssembleEconomicDetailHeadBoundFailsClosed proves the persisted-head set
// is bounded and an over-bound scope fails closed rather than truncating.
func TestAssembleEconomicDetailHeadBoundFailsClosed(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.Selection = nil
	base := in.Heads[0]
	heads := make([]SelectedCostHead, 0, MaxEconomicDetailHeads+1)
	for i := 0; i <= MaxEconomicDetailHeads; i++ {
		head := base.Clone()
		head.HeadKey = fmt.Sprintf("head-bound-%04d", i)
		heads = append(heads, head)
	}
	in.Heads = heads
	_, err := AssembleEconomicDetail(in)
	require.ErrorIs(t, err, ErrEconomicDetailBoundExceeded)
}

// detailCursorObservations builds a deterministic multi-stream observation set
// whose canonical order is (stream, sequence): stream-a 1..3, stream-b 1..3,
// stream-c 1. The returned IDs mirror that order so tests can assert exact
// traversal without depending on unrelated helper fixtures.
func detailCursorObservations(t *testing.T) ([]metering.Observation, []string) {
	t.Helper()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-cursor")
	var observations []metering.Observation
	var ids []string
	for _, spec := range []struct {
		stream string
		count  int
	}{{"stream-a", 3}, {"stream-b", 3}, {"stream-c", 1}} {
		for seq := 1; seq <= spec.count; seq++ {
			id := fmt.Sprintf("obs-%s-%d", spec.stream, seq)
			observations = append(observations, detailTestObservation(t, id, metering.OriginLocal, spec.stream, uint64(seq), subject,
				[]metering.Measure{detailTestMeasure(t, metering.ComponentInputToken, fmt.Sprint(100+seq))}, nil))
			ids = append(ids, id)
		}
	}
	return observations, ids
}

// TestAssembleEconomicDetailCursorTraversalExactlyOnce proves the observations
// of a scope above one page can be fully traversed with the continuation
// position: page N+1 strictly continues after page N, every observation is
// returned exactly once in the canonical order, and the final page carries no
// next position.
func TestAssembleEconomicDetailCursorTraversalExactlyOnce(t *testing.T) {
	t.Parallel()
	observations, expected := detailCursorObservations(t)
	in := detailTestCompleteInput(t)
	in.Observations = observations
	in.Query.Limit = 2

	var collected []string
	var after *EconomicObservationPosition
	pages := 0
	for {
		input := in
		input.After = after
		got, err := AssembleEconomicDetail(input)
		require.NoError(t, err)
		require.Equal(t, got.NextPosition != nil, got.Truncated, "Truncated must agree with NextPosition")
		for _, observation := range got.Observations {
			collected = append(collected, observation.ID)
		}
		if got.NextPosition == nil {
			break
		}
		after = got.NextPosition
		pages++
		require.Less(t, pages, 20, "pagination must terminate instead of repeating the first page")
	}
	require.Equal(t, expected, collected, "every in-scope observation must be traversed exactly once in stable order")
}

// TestAssembleEconomicDetailCursorFinalPageIsEmpty proves an exhausted scope
// returns an empty page with no continuation, never a perpetual first page.
func TestAssembleEconomicDetailCursorFinalPageIsEmpty(t *testing.T) {
	t.Parallel()
	observations, _ := detailCursorObservations(t)
	in := detailTestCompleteInput(t)
	in.Observations = observations
	in.Query.Limit = len(observations)

	all, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Len(t, all.Observations, len(observations))
	require.False(t, all.Truncated)
	require.Nil(t, all.NextPosition)

	last := EconomicObservationPositionOf(all.Observations[len(all.Observations)-1])
	exhausted := in
	exhausted.After = &last
	empty, err := AssembleEconomicDetail(exhausted)
	require.NoError(t, err)
	require.Empty(t, empty.Observations)
	require.False(t, empty.Truncated)
	require.Nil(t, empty.NextPosition)
}

// TestAssembleEconomicDetailCursorKeepsFullScopeFacts proves every page carries
// the same full-scope detail snapshot; only the bounded observation page
// differs. Page-local observation coverage is never silently presented as the
// full scope and monetary aggregation is never duplicated per page.
func TestAssembleEconomicDetailCursorKeepsFullScopeFacts(t *testing.T) {
	t.Parallel()
	observations, _ := detailCursorObservations(t)
	in := detailTestCompleteInput(t)
	in.Observations = observations
	in.Query.Limit = 2

	first, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Len(t, first.Observations, 2)
	require.NotNil(t, first.NextPosition)

	secondInput := in
	secondInput.After = first.NextPosition
	second, err := AssembleEconomicDetail(secondInput)
	require.NoError(t, err)
	require.Len(t, second.Observations, 2)

	require.NotEqual(t, first.Observations, second.Observations)
	require.Equal(t, first.Scope, second.Scope)
	require.Equal(t, first.Valuations, second.Valuations)
	require.Equal(t, first.Heads, second.Heads)
	require.Equal(t, first.PerBasisTotals, second.PerBasisTotals)
	require.Equal(t, first.Totals, second.Totals)
	require.Equal(t, first.Margin, second.Margin)
	require.Equal(t, first.Coverage, second.Coverage)
	require.Equal(t, first.Payers, second.Payers)
	require.Equal(t, first.Summary, second.Summary)
	require.Equal(t, first.Reconciliations, second.Reconciliations)
	require.Equal(t, first.Selection, second.Selection)
}

// TestAssembleEconomicDetailCursorRejectsInvalidPosition proves core
// independently rejects a malformed decoded position rather than trusting a
// caller-supplied keyset fragment.
func TestAssembleEconomicDetailCursorRejectsInvalidPosition(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	_, err := AssembleEconomicDetail(EconomicDetailInput{Query: in.Query, After: &EconomicObservationPosition{}})
	require.ErrorIs(t, err, ErrEconomicDetailInvalid)
}

// TestAssembleEconomicDetailKeepsTemporalDistinctValuationStreams is the
// Finding 2 domain boundary: two accepted SubjectRefs that differ only by an
// accepted temporal field are independent valuation streams, not revisions of
// one stream. Collapsing them here would silently drop an economic subject.
func TestAssembleEconomicDetailKeepsTemporalDistinctValuationStreams(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()
	firstSubject := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-temporal")
	firstSubject.StartAt = time.Unix(1_700_010_000, 0).UTC()
	firstSubject.EndAt = time.Unix(1_700_010_100, 0).UTC()
	secondSubject := detailTestSubject(storeID, aLegID, billingCallID, "b-detail-temporal")
	secondSubject.StartAt = time.Unix(1_700_020_000, 0).UTC()
	secondSubject.EndAt = time.Unix(1_700_020_100, 0).UTC()

	first := detailTestValuation(t, "valuation-temporal-1-e", economics.BasisLocalExpected, firstSubject, storeID, detailTestCurrencyTotal(t, "USD", "1.00"))
	second := detailTestValuation(t, "valuation-temporal-2-e", economics.BasisLocalExpected, secondSubject, storeID, detailTestCurrencyTotal(t, "USD", "2.00"))

	query := EconomicDetailQuery{StoreID: storeID, AccountID: accountID, ALegID: aLegID, BillingCallID: billingCallID}
	got, err := AssembleEconomicDetail(EconomicDetailInput{Query: query, Valuations: []economics.Valuation{first, second}})
	require.NoError(t, err)
	require.Len(t, got.Valuations, 2, "an accepted temporal subject field is part of valuation stream identity")
}

// TestEconomicValuationStreamKeyCoversEverySubjectIdentityField proves the
// in-memory stream key folds in every SubjectRef field, so it can never be
// coarser than the database partition and silently merge distinct identities.
func TestEconomicValuationStreamKeyCoversEverySubjectIdentityField(t *testing.T) {
	t.Parallel()
	base := economics.Valuation{
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "store-identity", TenantID: "tenant-identity",
			AccountID: "account-identity", ALegID: "a-identity", RequestID: "req-identity",
			BillingCallID: "call-identity", CallID: "call-identity", BLegID: "b-identity",
			AttemptID: "attempt-identity", AttemptSeq: 3, SubmissionID: "submission-identity",
			ProviderAccountKey: "provider-account-identity", ProviderRequestID: "provider-request-identity",
			ProviderChargeID: "provider-charge-identity", ResourceID: "resource-identity",
			PeriodID: "period-identity", PoolID: "pool-identity", WindowID: "window-identity",
			StatementID: "statement-identity", StatementLineID: "statement-line-identity",
			ResetAt: time.Unix(1_000, 0).UTC(), StartAt: time.Unix(2_000, 0).UTC(), EndAt: time.Unix(3_000, 0).UTC(),
		},
		Perspective: metering.PerspectiveOperator, Basis: economics.BasisLocalExpected,
	}
	baseKey := EconomicValuationStreamKey(base)
	typ := reflect.TypeOf(metering.SubjectRef{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		variant := base
		value := reflect.ValueOf(&variant.Subject).Elem().Field(i)
		switch value.Kind() {
		case reflect.String:
			value.SetString(value.String() + "-variant")
		case reflect.Uint64:
			value.SetUint(value.Uint() + 1)
		case reflect.Struct:
			if moment, ok := value.Interface().(time.Time); ok {
				value.Set(reflect.ValueOf(moment.Add(time.Second)))
			}
		}
		require.NotEqual(t, baseKey, EconomicValuationStreamKey(variant), "SubjectRef field %q must participate in the valuation stream key", tag)
	}
}
