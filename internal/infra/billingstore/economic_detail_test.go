package billingstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.1B RED contract: durable billingstore read adapter for the approved
// 16.1A economic-detail model. Tests persist exact durable rows (no mocks)
// and assert the adapter loads observations, E/Q/P/S/R valuations,
// reconciliation, heads, summaries and coverage through scope-enforcing,
// bounded, dual-dialect SQL, then assembles via the pure 16.1A contract.

func edTestAccount(t *testing.T, store *DurableStore, id, currency string) billing.Account {
	t.Helper()
	account := billing.Account{ID: id, Currency: currency, Mode: billing.AccountPrepaid, BalanceNano: 1_000_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(context.Background(), account))
	return account
}

func edTestCallID(t *testing.T) billing.BillingCallID {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	return callID
}

func edTestDecimal(t *testing.T, raw string) metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	require.NoError(t, err)
	normalized, err := value.Normalize()
	require.NoError(t, err)
	return normalized
}

func edTestMeasure(t *testing.T, component, raw string) metering.Measure {
	t.Helper()
	value := edTestDecimal(t, raw)
	return metering.Measure{
		Key: metering.ComponentKey{
			Direction: metering.DirectionInput, Component: component,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		},
		Value: &value, Quality: metering.QualityObserved,
	}
}

func edTestObservation(t *testing.T, id, origin, stream string, sequence uint64, subject metering.SubjectRef, measures []metering.Measure, charges []metering.ReportedCharge) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionLocalTokenizer
	authority := metering.AuthorityObservedClaim
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	if origin == metering.OriginStatement {
		acquisition = metering.AcquisitionStatementImporter
		authority = metering.AuthorityVerifiedStatement
	}
	now := time.Unix(1_700_020_000, 0).UTC()
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event",
		Revision: 1, StreamID: stream, Sequence: sequence,
		Origin: origin, Acquisition: acquisition, Authority: authority,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt,
		Subject:   subject,
		Correlation: metering.CorrelationV2{
			StoreID: subject.StoreID, TenantID: subject.TenantID,
			ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
			ProviderAccountKey: subject.ProviderAccountKey,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now, ReceivedAt: now, MappingRef: "economic-detail.test.v1",
		Measures: measures, Charges: charges,
	}
	require.NoError(t, observation.Validate())
	return observation
}

func edTestBLegSubject(storeID, tenantID, accountID, aLegID, callID, bLegID string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, TenantID: tenantID, AccountID: accountID,
		ALegID: aLegID, BillingCallID: callID, BLegID: bLegID,
	}
}

func edTestCharge(t *testing.T, itemID, amount, currency string, payer metering.PaymentParty, aggregate bool) metering.ReportedCharge {
	t.Helper()
	charge := metering.ReportedCharge{
		ChargeItemID: itemID, Currency: currency, Kind: metering.ChargeKindComponent, Payer: payer,
	}
	if aggregate {
		charge.Kind = metering.ChargeKindAggregate
	} else {
		charge.Component = &metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		}
	}
	if amount != "" {
		value := edTestDecimal(t, amount)
		charge.Amount = &value
	}
	return charge
}

func edTestCurrencyTotal(t *testing.T, currency, amount string) economics.CurrencyTotal {
	t.Helper()
	value := edTestDecimal(t, amount)
	nanos, err := value.ToNanoUnits()
	require.NoError(t, err)
	return economics.CurrencyTotal{
		Currency: currency, Amount: &value,
		RoundedAmount: economics.Money{NanoUnits: nanos, Currency: currency, Present: true},
	}
}

func edTestValuation(t *testing.T, id string, basis economics.ValuationBasis, subject metering.SubjectRef, refs []metering.ObservationRef, totals ...economics.CurrencyTotal) economics.Valuation {
	t.Helper()
	inputHash, err := economics.CanonicalInputSetHash(basis, refs)
	require.NoError(t, err)
	valuation := economics.Valuation{
		ID: id, Version: economics.ValuationVersionV2,
		Perspective: metering.PerspectiveOperator, Basis: basis, Subject: subject,
		InputObservations: refs, InputSetHash: inputHash,
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://economic-detail/qualifiers/v1", ContentHash: strings.Repeat("a", 64)},
		Completeness:         economics.CompletenessComplete,
		CreatedAt:            time.Unix(1_700_020_100, 0).UTC(),
		Totals:               totals,
	}
	switch basis {
	case economics.BasisLocalExpected, economics.BasisProviderQuantityLocal:
		valuation.Tariff = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-ed", Version: "v1"}}
		valuation.TariffContent = &economics.SnapshotContentRef{ContentRef: "catalog://economic-detail/tariff/v1", ContentHash: strings.Repeat("f", 64)}
		valuation.Rater = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater-ed", Version: "v1"}, RaterID: "reference-rater"}
		valuation.RaterContent = &economics.SnapshotContentRef{ContentRef: "catalog://economic-detail/rater/v1", ContentHash: strings.Repeat("e", 64)}
	case economics.BasisCustomerPolicy:
		valuation.Rater = economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater-ed", Version: "v1"}, RaterID: "reference-rater"}
		valuation.RaterContent = &economics.SnapshotContentRef{ContentRef: "catalog://economic-detail/rater/v1", ContentHash: strings.Repeat("e", 64)}
		valuation.Policy = economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-ed", Version: "v1"}, PolicyID: "retail-policy"}
		valuation.PolicyContent = &economics.SnapshotContentRef{ContentRef: "catalog://economic-detail/policy/v1", ContentHash: strings.Repeat("b", 64)}
	}
	require.NoError(t, valuation.Validate())
	return valuation
}

// edTestReportedLine builds one provider-reported payable line that names the
// exact charge observation and charge-item identity a selected valuation prices.
// The line/source identity is the canonical selected-ref evidence that proves
// monetary inclusion of that atom.
func edTestReportedLine(t *testing.T, id string, charge metering.ReportedCharge, observation metering.Observation, storeID string) economics.LineItem {
	t.Helper()
	ref, err := observation.Ref(storeID)
	require.NoError(t, err)
	line := economics.LineItem{
		ID: id, RuleID: "provider_reported", ItemID: charge.ChargeItemID,
		Status:                economics.RatingLineProviderReported,
		SourceObservationRefs: []metering.ObservationRef{ref},
	}
	if charge.Component != nil {
		component := charge.Component.Clone()
		line.Component = &component
		line.Unit = component.Unit
	} else {
		line.ReportedAggregate = true
		line.Unit = "reported"
	}
	if charge.Amount != nil {
		amount := *charge.Amount
		line.Amount = &amount
		nanos, err := amount.ToNanoUnits()
		require.NoError(t, err)
		line.RoundingScope = economics.RoundingScopeLine
		line.RoundingPolicy = economics.RoundingHalfEven
		line.RoundedAmount = &economics.Money{NanoUnits: nanos, Currency: charge.Currency, Present: true}
	}
	return line
}

// edTestValuationWithReportedLine is edTestValuation plus the exact payable
// selected line for one provider charge observation. Selected valuations that
// price a provider charge carry this line so coverage can be proven by exact
// identity instead of by subject ownership.
func edTestValuationWithReportedLine(t *testing.T, id string, basis economics.ValuationBasis, subject metering.SubjectRef, refs []metering.ObservationRef, storeID string, charge metering.ReportedCharge, observation metering.Observation, total economics.CurrencyTotal) economics.Valuation {
	t.Helper()
	valuation := edTestValuation(t, id, basis, subject, refs, total)
	valuation.Lines = append(valuation.Lines, edTestReportedLine(t, id+"-line", charge, observation, storeID))
	require.NoError(t, valuation.Validate())
	return valuation
}

func edSetupCall(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, aLegID string, legs ...billing.CallLegUsageRecord) {
	t.Helper()
	ctx := context.Background()
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: aLegID, SessionID: "sess-" + aLegID,
		StartedAt: time.Unix(1_700_020_000, 0).UTC(), FinishedAt: time.Unix(1_700_020_001, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
	}
	for _, leg := range legs {
		call.ExpectedBLegIDs = append(call.ExpectedBLegIDs, leg.BLegID)
	}
	require.NoError(t, store.AppendCallUsage(ctx, call))
	for _, leg := range legs {
		leg.CallID = callID
		leg.ALegID = aLegID
		require.NoError(t, store.AppendCallLegUsage(ctx, leg))
	}
}

func edTestLeg(t *testing.T, bLegID string, observations ...metering.Observation) billing.CallLegUsageRecord {
	t.Helper()
	return billing.CallLegUsageRecord{
		BLegID: bLegID, AttemptSeq: 1,
		BackendID: "backend-ed", ProviderID: "provider-ed", ModelID: "model-ed",
		StartedAt: time.Unix(1_700_020_000, 0).UTC(), FinishedAt: time.Unix(1_700_020_001, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens: billing.Quantity{Value: 7, Present: true},
			Source:      billing.EvidenceSourceProviderReported, Authority: billing.EvidenceAuthorityAuthoritative,
		},
		Observations: observations,
	}
}

func edObservationRef(t *testing.T, storeID string, observation metering.Observation) metering.ObservationRef {
	t.Helper()
	ref, err := observation.Ref(storeID)
	require.NoError(t, err)
	return ref
}

// edCompleteCall persists one call with local/provider observations and
// E/Q/P/R valuations, returning the query scope and valuation IDs. Prefix
// keeps valuation/observation identities store-unique across calls sharing
// one A-leg, since valuation IDs are store-wide immutable identities.
func edCompleteCall(t *testing.T, store *DurableStore, accountID, aLegID, prefix string) (billing.BillingCallID, []string) {
	t.Helper()
	ctx := context.Background()
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", accountID, aLegID, callID.String(), "b-"+prefix)
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
	valuations := []economics.Valuation{
		edTestValuation(t, "val-"+prefix+"-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, "val-"+prefix+"-q", economics.BasisProviderQuantityLocal, subject, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuationWithReportedLine(t, "val-"+prefix+"-p", economics.BasisProviderReported, subject, refs, store.StoreID(), moneyCharge, moneyObs, edTestCurrencyTotal(t, "USD", "1.32")),
		edTestValuation(t, "val-"+prefix+"-r", economics.BasisCustomerPolicy, subject, refs, edTestCurrencyTotal(t, "USD", "2.00")),
	}
	for _, valuation := range valuations {
		require.NoError(t, store.AppendValuation(ctx, valuation))
	}
	return callID, []string{"val-" + prefix + "-e", "val-" + prefix + "-q", "val-" + prefix + "-p", "val-" + prefix + "-r"}
}

// edCompleteCallVariant persists one call with the same E/Q/P/R bases as
// edCompleteCall but under an explicit native currency and trusted payer, so a
// multi-call A-leg probe can prove that basis-only collapsing cannot masquerade
// as correct behavior.
func edCompleteCallVariant(t *testing.T, store *DurableStore, accountID, aLegID, prefix, currency string, payer metering.PaymentParty) (billing.BillingCallID, []string) {
	t.Helper()
	ctx := context.Background()
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", accountID, aLegID, callID.String(), "b-"+prefix)
	local := edTestObservation(t, "obs-"+prefix+"-local", metering.OriginLocal, "stream-"+prefix+"-local", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	provider := edTestObservation(t, "obs-"+prefix+"-provider", metering.OriginProvider, "stream-"+prefix+"-provider", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	moneyCharge := edTestCharge(t, "charge-"+prefix+"-p", "1.32", currency, payer, false)
	moneyObs := edTestObservation(t, "obs-"+prefix+"-money", metering.OriginProvider, "stream-"+prefix+"-money", 2, subject, nil, []metering.ReportedCharge{moneyCharge})
	edSetupCall(t, store, accountID, callID, aLegID, edTestLeg(t, "b-"+prefix, local, provider, moneyObs))

	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), local),
		edObservationRef(t, store.StoreID(), provider),
		edObservationRef(t, store.StoreID(), moneyObs),
	}
	valuations := []economics.Valuation{
		edTestValuation(t, "val-"+prefix+"-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, currency, "1.00")),
		edTestValuation(t, "val-"+prefix+"-q", economics.BasisProviderQuantityLocal, subject, refs, edTestCurrencyTotal(t, currency, "1.10")),
		edTestValuationWithReportedLine(t, "val-"+prefix+"-p", economics.BasisProviderReported, subject, refs, store.StoreID(), moneyCharge, moneyObs, edTestCurrencyTotal(t, currency, "1.32")),
		edTestValuation(t, "val-"+prefix+"-r", economics.BasisCustomerPolicy, subject, refs, edTestCurrencyTotal(t, currency, "2.00")),
	}
	ids := make([]string, 0, len(valuations))
	for i := range valuations {
		valuations[i].Payer = payer
		require.NoError(t, valuations[i].Validate())
		require.NoError(t, store.AppendValuation(ctx, valuations[i]))
		ids = append(ids, valuations[i].ID)
	}
	return callID, ids
}

func TestQueryEconomicDetailCompleteCall(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-complete", "USD")
	callID, valuationIDs := edCompleteCall(t, store, account.ID, "a-ed-complete", "ed1")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-complete",
	})
	require.NoError(t, err)
	require.Len(t, got.Observations, 3)
	for _, id := range valuationIDs {
		found := false
		for _, valuation := range got.Valuations {
			if valuation.ID == id {
				found = true
			}
		}
		require.True(t, found, "valuation %q must be loaded from billing_valuations", id)
	}
	bases := map[economics.ValuationBasis]bool{}
	for _, valuation := range got.Valuations {
		bases[valuation.Basis] = true
	}
	for _, basis := range []economics.ValuationBasis{
		economics.BasisLocalExpected, economics.BasisProviderQuantityLocal,
		economics.BasisProviderReported, economics.BasisCustomerPolicy,
	} {
		require.True(t, bases[basis], "basis %q must survive durable round-trip", basis)
	}
	require.False(t, got.Truncated)
}

func TestQueryEconomicDetailALegMultiCall(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-aleg", "USD")
	first, _ := edCompleteCall(t, store, account.ID, "a-ed-multi", "edA")
	second, _ := edCompleteCall(t, store, account.ID, "a-ed-multi", "edB")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, ALegID: "a-ed-multi",
	})
	require.NoError(t, err)
	// Two calls with three observations each, gathered without double-counting.
	require.Len(t, got.Observations, 6)
	seen := map[string]int{}
	for _, observation := range got.Observations {
		seen[observation.ID]++
		require.Equal(t, "a-ed-multi", observation.Subject.ALegID)
	}
	for id, count := range seen {
		require.Equal(t, 1, count, "observation %q must appear exactly once", id)
	}
	callIDs := map[string]bool{}
	for _, observation := range got.Observations {
		callIDs[observation.Subject.BillingCallID] = true
	}
	require.True(t, callIDs[first.String()])
	require.True(t, callIDs[second.String()])
	// Deterministic order across calls.
	for i := 1; i < len(got.Observations); i++ {
		prev, curr := got.Observations[i-1], got.Observations[i]
		if prev.StreamID == curr.StreamID {
			require.LessOrEqual(t, prev.Sequence, curr.Sequence)
		} else {
			require.LessOrEqual(t, prev.StreamID, curr.StreamID)
		}
	}
}

// TestQueryEconomicDetailALegMultiSubjectValuationsSurvive is the Finding 3A
// durable regression: one A-leg with two calls, each carrying an independent
// E/Q/P/R stream under a different native currency and payer. Eight persisted
// valuations must survive; a basis-only collapse would keep four, all from the
// lexically later call.
func TestQueryEconomicDetailALegMultiSubjectValuationsSurvive(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-multisubject", "USD")
	operatorPayer := metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-subject-a"}
	secondPayer := metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-subject-b"}
	first, firstIDs := edCompleteCallVariant(t, store, account.ID, "a-ed-multisubject", "edMSA", "USD", operatorPayer)
	second, secondIDs := edCompleteCallVariant(t, store, account.ID, "a-ed-multisubject", "edMSB", "EUR", secondPayer)

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, ALegID: "a-ed-multisubject",
	})
	require.NoError(t, err)
	require.Len(t, got.Valuations, 8, "every subject/basis valuation must survive basis-only collapsing")
	byID := map[string]economics.Valuation{}
	for _, valuation := range got.Valuations {
		require.NotContains(t, byID, valuation.ID, "valuation %q must appear once", valuation.ID)
		byID[valuation.ID] = valuation
	}
	for _, id := range firstIDs {
		require.Contains(t, byID, id)
		require.Equal(t, first.String(), byID[id].Subject.BillingCallID)
		require.Equal(t, "USD", byID[id].Totals[0].Currency)
		require.Equal(t, operatorPayer, byID[id].Payer)
	}
	for _, id := range secondIDs {
		require.Contains(t, byID, id)
		require.Equal(t, second.String(), byID[id].Subject.BillingCallID)
		require.Equal(t, "EUR", byID[id].Totals[0].Currency)
		require.Equal(t, secondPayer, byID[id].Payer)
	}
	require.Len(t, got.PerBasisTotals, 8)
	currencies := map[string]bool{}
	for _, total := range got.PerBasisTotals {
		for _, currencyTotal := range total.CurrencyTotals {
			currencies[currencyTotal.Currency] = true
		}
	}
	require.True(t, currencies["USD"])
	require.True(t, currencies["EUR"])
}

// TestQueryEconomicDetailValuationRevisionWithinSubjectWins proves the other
// half of Finding 3A: revisions of one true logical valuation stream
// (subject/perspective/basis) must not all leak as separate current
// contributions; only the latest revision is current while the stream identity
// is unchanged.
func TestQueryEconomicDetailValuationRevisionWithinSubjectWins(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-revision", "USD")
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-revision", callID.String(), "b-edRevision")
	local := edTestObservation(t, "obs-edRevision-local", metering.OriginLocal, "stream-edRevision-local", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	provider := edTestObservation(t, "obs-edRevision-provider", metering.OriginProvider, "stream-edRevision-provider", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	edSetupCall(t, store, account.ID, callID, "a-ed-revision", edTestLeg(t, "b-edRevision", local, provider))

	refsFirst := []metering.ObservationRef{edObservationRef(t, store.StoreID(), local)}
	first := edTestValuation(t, "val-edRevision-e-v1", economics.BasisLocalExpected, subject, refsFirst, edTestCurrencyTotal(t, "USD", "1.00"))
	first.CreatedAt = time.Unix(1_700_020_100, 0).UTC()
	require.NoError(t, store.AppendValuation(ctx, first))

	refsSecond := []metering.ObservationRef{edObservationRef(t, store.StoreID(), local), edObservationRef(t, store.StoreID(), provider)}
	second := edTestValuation(t, "val-edRevision-e-v2", economics.BasisLocalExpected, subject, refsSecond, edTestCurrencyTotal(t, "USD", "1.10"))
	second.CreatedAt = time.Unix(1_700_020_200, 0).UTC()
	require.NoError(t, store.AppendValuation(ctx, second))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-revision",
	})
	require.NoError(t, err)
	require.Len(t, got.Valuations, 1, "one logical stream keeps only its latest revision")
	require.Equal(t, "val-edRevision-e-v2", got.Valuations[0].ID)
}

func TestQueryEconomicDetailPartialMissing(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-partial", "USD")
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-partial", callID.String(), "b-ed-partial")
	local := edTestObservation(t, "obs-ed-partial", metering.OriginLocal, "stream-ed-partial", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	edSetupCall(t, store, account.ID, callID, "a-ed-partial", edTestLeg(t, "b-ed-partial", local))
	refs := []metering.ObservationRef{edObservationRef(t, store.StoreID(), local)}
	require.NoError(t, store.AppendValuation(ctx, edTestValuation(t, "val-ed-partial-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, "USD", "1.00"))))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-partial",
	})
	require.NoError(t, err)
	require.NotNil(t, got.ValuationFor(economics.BasisLocalExpected))
	require.Nil(t, got.ValuationFor(economics.BasisProviderReported))
	require.Empty(t, got.Heads)
	require.False(t, got.Margin.Complete)
	require.Nil(t, got.Margin.Amount)
	require.NotEmpty(t, got.Margin.Reason)
}

func TestQueryEconomicDetailMultiCurrencyBYOKAggregate(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-mixed", "USD")
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-mixed", callID.String(), "b-ed-mixed")
	usdCharge := edTestCharge(t, "charge-ed-usd", "1.32", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	eurCharge := edTestCharge(t, "charge-ed-eur", "5.00", "EUR", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	byokCharge := edTestCharge(t, "charge-ed-byok", "9.99", "USD", metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-creds"}, false)
	aggCharge := edTestCharge(t, "charge-ed-agg", "12.00", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, true)
	moneyObs := edTestObservation(t, "obs-ed-mixed", metering.OriginProvider, "stream-ed-mixed", 1, subject, nil,
		[]metering.ReportedCharge{usdCharge, eurCharge, byokCharge, aggCharge})
	edSetupCall(t, store, account.ID, callID, "a-ed-mixed", edTestLeg(t, "b-ed-mixed", moneyObs))
	refs := []metering.ObservationRef{edObservationRef(t, store.StoreID(), moneyObs)}
	require.NoError(t, store.AppendValuation(ctx, edTestValuation(t, "val-ed-mixed-p", economics.BasisProviderReported, subject, refs,
		edTestCurrencyTotal(t, "USD", "1.32"), edTestCurrencyTotal(t, "EUR", "5.00"))))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-mixed",
	})
	require.NoError(t, err)
	require.True(t, got.Payers.HasCustomerBYOK)
	require.True(t, got.Payers.HasOperatorPayable)
	require.True(t, got.Coverage.AggregateOnly)
	foundAgg := false
	for _, charge := range got.Coverage.AggregateCharges {
		if charge.ChargeItemID == "charge-ed-agg" {
			foundAgg = true
			require.Nil(t, charge.Component)
		}
	}
	require.True(t, foundAgg, "aggregate charge must be surfaced without invented component")
	currencies := map[string]bool{}
	for _, total := range got.PerBasisTotals {
		for _, currencyTotal := range total.CurrencyTotals {
			currencies[currencyTotal.Currency] = true
		}
	}
	require.True(t, currencies["USD"])
	require.True(t, currencies["EUR"])
	require.False(t, got.Margin.Complete, "multi-currency detail without frozen FX cannot report a complete single-currency margin")
}

// TestQueryEconomicDetailUnresolvedPayerSurvivesRetailCustomer proves the
// durable SQLite read path (Phase 16 second-pass Finding 3) does not let a
// resolved retail customer-policy valuation erase an earlier unresolved payer:
// the persisted valuation overview order is E/Q/P/S/R, so the unknown provider
// payer is marked before the customer retail payer, and HasUnallocated must stay
// true while the distinct payer set retains both.
func TestQueryEconomicDetailUnresolvedPayerSurvivesRetailCustomer(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-payer-monotonic", "USD")
	aLegID := "a-ed-payer-monotonic"
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, aLegID, callID.String(), "b-payer-monotonic")
	local := edTestObservation(t, "obs-ed-payer-local", metering.OriginLocal, "stream-ed-payer-local", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	provider := edTestObservation(t, "obs-ed-payer-provider", metering.OriginProvider, "stream-ed-payer-provider", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	moneyCharge := edTestCharge(t, "charge-ed-payer-p", "1.32", "USD", metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}, false)
	moneyObs := edTestObservation(t, "obs-ed-payer-money", metering.OriginProvider, "stream-ed-payer-money", 2, subject, nil, []metering.ReportedCharge{moneyCharge})
	edSetupCall(t, store, account.ID, callID, aLegID, edTestLeg(t, "b-payer-monotonic", local, provider, moneyObs))

	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), local),
		edObservationRef(t, store.StoreID(), provider),
		edObservationRef(t, store.StoreID(), moneyObs),
	}
	operatorPayer := metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}
	unknownPayer := metering.PaymentParty{Kind: metering.PaymentPartyUnknown}
	customerPayer := metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-creds"}
	valuations := []economics.Valuation{
		edTestValuation(t, "val-ed-payer-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, "val-ed-payer-q", economics.BasisProviderQuantityLocal, subject, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuation(t, "val-ed-payer-p", economics.BasisProviderReported, subject, refs, edTestCurrencyTotal(t, "USD", "1.32")),
		edTestValuation(t, "val-ed-payer-r", economics.BasisCustomerPolicy, subject, refs, edTestCurrencyTotal(t, "USD", "2.00")),
	}
	for i := range valuations {
		switch valuations[i].Basis {
		case economics.BasisProviderReported:
			valuations[i].Payer = unknownPayer
		case economics.BasisCustomerPolicy:
			valuations[i].Payer = customerPayer
		default:
			valuations[i].Payer = operatorPayer
		}
		require.NoError(t, valuations[i].Validate())
		require.NoError(t, store.AppendValuation(ctx, valuations[i]))
	}

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.True(t, got.Payers.HasUnallocated, "durable read must retain unresolved payer presence after a resolved retail payer")
	require.True(t, got.Payers.HasOperatorPayable)
	require.False(t, got.Payers.HasCustomerBYOK, "a retail customer-policy payer is not BYOK")
	require.Contains(t, got.Payers.Distinct, unknownPayer)
	require.Contains(t, got.Payers.Distinct, customerPayer)
}

func TestQueryEconomicDetailScopeRejection(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-scope", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-scope", "edS")

	t.Run("wrong store", func(t *testing.T) {
		t.Parallel()
		_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
			StoreID: "other-store", AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-scope",
		})
		require.ErrorIs(t, err, billing.ErrEconomicDetailScopeMismatch)
	})

	t.Run("cross-account call is not found, never leaked", func(t *testing.T) {
		t.Parallel()
		_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
			StoreID: store.StoreID(), AccountID: "other-account", BillingCallID: callID.String(), ALegID: "a-ed-scope",
		})
		require.ErrorIs(t, err, billing.ErrReportNotFound)
	})

	t.Run("tenant mismatch fails closed", func(t *testing.T) {
		t.Parallel()
		_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
			StoreID: store.StoreID(), TenantID: "tenant-other", AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-scope",
		})
		require.ErrorIs(t, err, billing.ErrEconomicDetailScopeMismatch)
	})

	t.Run("cross-aleg call fails closed", func(t *testing.T) {
		t.Parallel()
		_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
			StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-other",
		})
		require.ErrorIs(t, err, billing.ErrEconomicDetailScopeMismatch)
	})
}

func TestQueryEconomicDetailNotFound(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	edTestAccount(t, store, "ed-missing", "USD")

	t.Run("unknown call", func(t *testing.T) {
		t.Parallel()
		_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
			StoreID: store.StoreID(), AccountID: "ed-missing", BillingCallID: edTestCallID(t).String(),
		})
		require.ErrorIs(t, err, billing.ErrReportNotFound)
	})

	t.Run("empty aleg", func(t *testing.T) {
		t.Parallel()
		_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
			StoreID: store.StoreID(), AccountID: "ed-missing", ALegID: "a-absent",
		})
		require.ErrorIs(t, err, billing.ErrReportNotFound)
	})
}

func TestQueryEconomicDetailPagination(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-page", "USD")
	callID := edTestCallID(t)
	var observations []metering.Observation
	for i := range 5 {
		subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-page", callID.String(), "b-ed-page")
		observations = append(observations, edTestObservation(t,
			"obs-ed-page-"+string(rune('a'+i)), metering.OriginLocal, "stream-ed-page", uint64(i+1), subject,
			[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "10")}, nil))
	}
	edSetupCall(t, store, account.ID, callID, "a-ed-page", edTestLeg(t, "b-ed-page", observations...))

	full, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-page",
	})
	require.NoError(t, err)
	require.Len(t, full.Observations, 5)
	require.False(t, full.Truncated)

	paged, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-page", Limit: 2,
	})
	require.NoError(t, err)
	require.Len(t, paged.Observations, 2)
	require.True(t, paged.Truncated)
	// Page preserves semantic sequence order and matches the full prefix.
	for i := range paged.Observations {
		require.Equal(t, full.Observations[i].ID, paged.Observations[i].ID)
	}

	again, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-page", Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, paged.Observations, again.Observations, "paged reads must be deterministic")
}

func TestQueryEconomicDetailSummaryCompat(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "ed-summary", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := edTestCallID(t)
	settleIndependentCall(t, store, account.ID, callID, "a-ed-summary", "sess-ed-summary", 10, 3)

	explanation, err := store.CallExplanation(ctx, callID.String())
	require.NoError(t, err)

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-summary",
	})
	require.NoError(t, err)
	require.NotNil(t, got.Summary.Turn)
	require.Equal(t, explanation.Result, *got.Summary.Turn)
	// Legacy V1 legs carry no V2 observations: the adapter must not invent any.
	require.Empty(t, got.Observations)
	require.Empty(t, got.Valuations)
	require.False(t, got.Margin.Complete)

	// Existing explanation response is unchanged by the additive detail query.
	again, err := store.CallExplanation(ctx, callID.String())
	require.NoError(t, err)
	require.Equal(t, explanation, again)
}

// edTestPostSelectedHead persists one frozen selected/posted head through the
// canonical durable writer so the detail adapter reads exact durable state. The
// head is bound to the matching frozen valuation identity (valuation id,
// revision, input-set hash) when one exists for the subject/basis, mirroring
// the production writer; otherwise the synthetic identity is retained.
func edTestPostSelectedHead(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, subject metering.SubjectRef, headKey string, basis billing.OperatorCostSelectionBasis, status billing.OperatorCostSelectionStatus, provenance billing.OperatorCostProvenance, currency, amount string) {
	t.Helper()
	selection := billing.OperatorCostSelectionResult{
		Status: status, Basis: basis, Provenance: provenance, Currency: currency,
	}
	if amount != "" {
		decimal := edTestDecimal(t, amount)
		selection.Amount = &billing.MonetaryExactAmount{Currency: currency, Decimal: &decimal}
	}
	ref := billing.SelectedCostValuationRef{ValuationID: "val-" + headKey, Revision: 1, InputSetHash: strings.Repeat("d", 64)}
	if valuationID, revision, inputSetHash, ok := edTestSelectedValuationIdentity(t, store, subject, basis); ok {
		ref = billing.SelectedCostValuationRef{ValuationID: valuationID, Revision: revision, InputSetHash: inputSetHash}
	}
	selected, err := billing.NewSelectedCostValuation(
		ref,
		selection,
	)
	require.NoError(t, err)
	_, err = store.ApplySelectedCostAdjustment(context.Background(), billing.SelectedCostAdjustmentInput{
		AccountID: accountID, CallID: callID, HeadKey: headKey, Subject: subject,
		Expected: billing.SelectedCostHeadExpectation{}, Selected: selected,
	})
	require.NoError(t, err)
}

// edTestValuationBasisFor maps one operator selection basis onto its canonical
// valuation basis for identity binding and fixture lookups.
func edTestValuationBasisFor(basis billing.OperatorCostSelectionBasis) (economics.ValuationBasis, bool) {
	switch basis {
	case billing.OperatorCostBasisE:
		return economics.BasisLocalExpected, true
	case billing.OperatorCostBasisQ:
		return economics.BasisProviderQuantityLocal, true
	case billing.OperatorCostBasisP:
		return economics.BasisProviderReported, true
	case billing.OperatorCostBasisS:
		return economics.BasisStatementReported, true
	default:
		return "", false
	}
}

// edTestSelectedValuationIdentity resolves the frozen valuation identity a
// durable selected head must bind to for the given subject and selection basis.
// The lookup mirrors the production writer, which references the candidate's
// exact valuation id, revision and input-set hash.
func edTestSelectedValuationIdentity(t *testing.T, store *DurableStore, subject metering.SubjectRef, basis billing.OperatorCostSelectionBasis) (string, uint64, string, bool) {
	t.Helper()
	economicsBasis, ok := edTestValuationBasisFor(basis)
	if !ok {
		return "", 0, "", false
	}
	var rows []struct {
		ValuationID  string `bun:"valuation_id"`
		Version      int64  `bun:"valuation_version"`
		InputSetHash string `bun:"input_set_hash"`
	}
	err := store.db.NewRaw(
		`SELECT valuation_id, valuation_version, input_set_hash FROM billing_valuations WHERE store_id = ? AND subject_id = ? AND basis = ? ORDER BY valuation_version DESC, valuation_id LIMIT 1`,
		store.StoreID(), subjectIDForEconomics(subject), string(economicsBasis),
	).Scan(context.Background(), &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		require.NoError(t, err)
	}
	if len(rows) == 0 {
		return "", 0, "", false
	}
	revision := uint64(1)
	if rows[0].Version > 0 {
		revision = uint64(rows[0].Version)
	}
	return rows[0].ValuationID, revision, rows[0].InputSetHash, true
}

func TestQueryEconomicDetailPostedHead(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-head", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-head", "edH")

	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-head", callID.String(), "b-edH")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-p",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-head",
	})
	require.NoError(t, err)
	require.Len(t, got.Heads, 1)
	require.Equal(t, "head-ed-p", got.Heads[0].HeadKey)
	require.Equal(t, uint64(1), got.Heads[0].Version)
	require.NotNil(t, got.Heads[0].Selected)
	require.Equal(t, "val-edH-p", got.Heads[0].Selected.Ref.ValuationID)
	require.Equal(t, billing.OperatorCostBasisP, got.Heads[0].Selected.Basis)
	require.Equal(t, billing.OperatorCostProvenanceAttempted, got.Heads[0].Selected.Provenance)
	require.NotEmpty(t, got.Heads[0].LastOperationKey, "correction/operation lineage must be available in durable state")

	// The full candidate set is not retained, so the singular convenience
	// selection carries no candidates; it must not invent any.
	require.NotNil(t, got.Selection)
	require.Empty(t, got.Selection.Candidates)
	require.Equal(t, billing.OperatorCostSelectionStatusFinal, got.Selection.Status)

	require.Equal(t, billing.OperatorCostSelectionStatusFinal, got.Totals.SelectedStatus)
	require.Equal(t, billing.OperatorCostBasisP, got.Totals.SelectedBasis)
	require.Equal(t, "USD", got.Totals.SelectedCurrency)
	require.NotNil(t, got.Totals.SelectedAmount)
	require.Equal(t, edTestDecimal(t, "1.32").CanonicalString(), got.Totals.SelectedAmount.Decimal.CanonicalString())
	require.Equal(t, economics.CompletenessComplete, got.Totals.SelectedCompleteness)
	require.Equal(t, 1, got.Totals.SelectedHeadCount)
	require.False(t, got.Totals.SelectedAmbiguous)

	// One frozen final posted head plus complete retail revenue makes the
	// margin computable: 2.00 - 1.32 = 0.68. It must not report
	// missing_selected.
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
	require.Equal(t, "USD", got.Margin.Currency)
	require.Equal(t, edTestDecimal(t, "0.68").CanonicalString(), got.Margin.Amount.Decimal.CanonicalString())
	require.Equal(t, billing.SelectedCostPostingApplied, got.Heads[0].PostingState)
}

// edTestSetHeadPostingState overwrites the durable posting_state column
// directly, so the reader can be proven to read the exact stored value rather
// than infer it from selection status or transaction lineage.
func edTestSetHeadPostingState(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, headKey, state string) {
	t.Helper()
	_, err := store.db.NewRaw(`UPDATE billing_provider_cost_heads SET posting_state = ? WHERE store_id = ? AND account_id = ? AND call_id = ? AND head_key = ?`,
		state, store.StoreID(), accountID, callID.String(), headKey).Exec(context.Background())
	require.NoError(t, err)
}

// TestQueryEconomicDetailHeadPostingStates proves every economic-detail head
// exposes its own exact persisted posting state (applied, pending, replayed)
// with deterministic multi-head ordering and no cross-head inference.
func TestQueryEconomicDetailHeadPostingStates(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-posting", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-posting", "edPS")
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-posting", callID.String(), "b-edPS")
	for _, headKey := range []string{"head-ps-a", "head-ps-p", "head-ps-r"} {
		edTestPostSelectedHead(t, store, account.ID, callID, subject, headKey,
			billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	}
	edTestSetHeadPostingState(t, store, account.ID, callID, "head-ps-p", string(billing.SelectedCostPostingPending))
	edTestSetHeadPostingState(t, store, account.ID, callID, "head-ps-r", string(billing.SelectedCostPostingReplayed))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-posting",
	})
	require.NoError(t, err)
	require.Len(t, got.Heads, 3)
	require.Equal(t, "head-ps-a", got.Heads[0].HeadKey)
	require.Equal(t, "head-ps-p", got.Heads[1].HeadKey)
	require.Equal(t, "head-ps-r", got.Heads[2].HeadKey)
	require.Equal(t, billing.SelectedCostPostingApplied, got.Heads[0].PostingState)
	require.Equal(t, billing.SelectedCostPostingPending, got.Heads[1].PostingState)
	require.Equal(t, billing.SelectedCostPostingReplayed, got.Heads[2].PostingState)
}

// TestQueryEconomicDetailHeadPostingStateLegacyDefault locks the explicit
// legacy/unset semantics: the additive migration is NOT NULL DEFAULT 'applied',
// so an unset value reads as applied rather than as an inferred state.
func TestQueryEconomicDetailHeadPostingStateLegacyDefault(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-posting-legacy", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-posting-legacy", "edPSL")
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-posting-legacy", callID.String(), "b-edPSL")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ps-legacy",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	edTestSetHeadPostingState(t, store, account.ID, callID, "head-ps-legacy", "")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-posting-legacy",
	})
	require.NoError(t, err)
	require.Len(t, got.Heads, 1)
	require.Equal(t, billing.SelectedCostPostingApplied, got.Heads[0].PostingState)
}

// TestQueryEconomicDetailHeadPostingStateCorruptFailsClosed proves a
// non-empty unknown durable posting state fails closed instead of being
// silently coerced or inferred.
func TestQueryEconomicDetailHeadPostingStateCorruptFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-posting-corrupt", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-posting-corrupt", "edPSC")
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-posting-corrupt", callID.String(), "b-edPSC")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ps-corrupt",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	edTestSetHeadPostingState(t, store, account.ID, callID, "head-ps-corrupt", "not_a_posting_state")

	_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-posting-corrupt",
	})
	require.Error(t, err)
}

// TestQueryEconomicDetailKnownZeroSelectedHead proves a persisted known-zero
// selection stays distinct from missing evidence and supports a complete
// margin instead of being reported as missing_selected.
func TestQueryEconomicDetailKnownZeroSelectedHead(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-known-zero", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-known-zero", "edKZ")
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-known-zero", callID.String(), "b-edKZ")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-kz",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusKnownZero, billing.OperatorCostProvenanceNeverStarted, "USD", "0")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-known-zero",
	})
	require.NoError(t, err)
	require.Equal(t, billing.OperatorCostSelectionStatusKnownZero, got.Totals.SelectedStatus)
	require.Equal(t, economics.CompletenessComplete, got.Totals.SelectedCompleteness)
	require.False(t, got.Totals.SelectedAmbiguous)
	require.NotNil(t, got.Totals.SelectedAmount)
	require.Equal(t, edTestDecimal(t, "0").CanonicalString(), got.Totals.SelectedAmount.Decimal.CanonicalString())
	require.NotEqual(t, "missing_selected", got.Margin.Reason)
	require.True(t, got.Margin.Complete)
	require.NotNil(t, got.Margin.Amount)
	require.Equal(t, edTestDecimal(t, "2.00").CanonicalString(), got.Margin.Amount.Decimal.CanonicalString())
}

// TestQueryEconomicDetailMultipleSelectedHeadsAmbiguous proves multiple
// independent persisted heads are never summed or collapsed; the scope reports
// an explicit ambiguous status and no singular amount.
func TestQueryEconomicDetailMultipleSelectedHeadsAmbiguous(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-multi-head", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-multi-head", "edMH")
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-multi-head", callID.String(), "b-edMH")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-mh-1",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-mh-2",
		billing.OperatorCostBasisQ, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.10")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-multi-head",
	})
	require.NoError(t, err)
	require.Len(t, got.Heads, 2, "both independent heads survive")
	require.Nil(t, got.Selection)
	require.Equal(t, 2, got.Totals.SelectedHeadCount)
	require.True(t, got.Totals.SelectedAmbiguous)
	require.Nil(t, got.Totals.SelectedAmount, "independent heads must never be summed")
	require.False(t, got.Margin.Complete)
	require.Equal(t, "ambiguous_selected", got.Margin.Reason)
	require.Nil(t, got.Margin.Amount)
}

// TestQueryEconomicDetailMultipleSelectedHeadsDifferentCurrency proves distinct
// native currencies are never FX-converted or summed implicitly.
func TestQueryEconomicDetailMultipleSelectedHeadsDifferentCurrency(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-multi-currency-head", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-multi-currency", "edMC")
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-multi-currency", callID.String(), "b-edMC")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-mc-usd",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-mc-eur",
		billing.OperatorCostBasisQ, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "EUR", "5.00")

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-multi-currency",
	})
	require.NoError(t, err)
	require.Len(t, got.Heads, 2)
	require.True(t, got.Totals.SelectedAmbiguous)
	require.Nil(t, got.Totals.SelectedAmount)
	require.False(t, got.Margin.Complete)
	require.Nil(t, got.Margin.Amount)
	headCurrencies := map[string]bool{}
	for _, head := range got.Heads {
		require.NotNil(t, head.Selected)
		headCurrencies[head.Selected.Currency] = true
	}
	require.True(t, headCurrencies["USD"])
	require.True(t, headCurrencies["EUR"])
}

func TestQueryEconomicDetailReconciliation(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-recon", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-recon", "edR")

	legSubject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-recon", callID.String(), "b-edR")
	localObs := edTestObservation(t, "obs-edR-local", metering.OriginLocal, "stream-edR", 1, legSubject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "100")}, nil)
	providerObs := edTestObservation(t, "obs-edR-provider", metering.OriginProvider, "stream-edR", 1, legSubject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, "110")}, nil)
	quantity, err := billing.CompareComponentQuantities(
		billing.ReconciliationEvidenceSet{Subject: legSubject, Tokenizer: "ed-tokenizer-v1", Observations: []metering.Observation{localObs}},
		billing.ReconciliationEvidenceSet{Subject: legSubject, Tokenizer: "ed-tokenizer-v1", Observations: []metering.Observation{providerObs}},
	)
	require.NoError(t, err)
	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), localObs),
		edObservationRef(t, store.StoreID(), providerObs),
	}
	monetary, err := billing.DecomposeMonetaryDiscrepancies(billing.MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
		edTestValuation(t, "val-edR-e", economics.BasisLocalExpected, legSubject, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, "val-edR-q", economics.BasisProviderQuantityLocal, legSubject, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuation(t, "val-edR-p", economics.BasisProviderReported, legSubject, refs, edTestCurrencyTotal(t, "USD", "1.32")),
	}})
	require.NoError(t, err)
	result := billing.ReconciliationRetentionResult{
		SchemaVersion: billing.ReconciliationRetentionSchemaVersionV1,
		ID:            "recon-ed-call", ResultRevision: 1, Subject: legSubject, Scope: "call:" + callID.String(),
		Policy:       billing.VersionRef{ID: "policy-ed", Version: "v1"},
		InputSetHash: strings.Repeat("a", 64),
		ValuationIDs: []string{"val-edR-e", "val-edR-q", "val-edR-p"},
		Quantity:     &quantity, Monetary: &monetary,
		CreatedAt: time.Unix(1_700_020_200, 0).UTC(),
	}
	require.NoError(t, store.AppendReconciliationRetention(ctx, result))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-recon",
	})
	require.NoError(t, err)
	require.NotNil(t, got.Quantity)
	require.Equal(t, billing.ReconciliationStatusDiscrepant, got.Quantity.Status)
	require.NotNil(t, got.Monetary)
	require.Equal(t, billing.MonetaryDiscrepancyComplete, got.Monetary.Status)
}

func TestQueryEconomicDetailAllocation(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-alloc", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-alloc", "edL")

	amount, err := metering.ParseDecimal("10")
	require.NoError(t, err)
	record := economics.AllocationRecord{
		ID: "alloc-ed-1", Version: 1,
		SourceSubject: metering.SubjectRef{Kind: metering.SubjectResource, StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, ResourceID: "shared", PeriodID: "2026-09", StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC()},
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets: []economics.AllocationTarget{
			{TargetID: "b-edL", Target: edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-alloc", callID.String(), "b-edL"), Weight: economics.AllocationFraction{Numerator: "3", Denominator: "5"}},
			{TargetID: "unallocated", Unallocated: true, Weight: economics.AllocationFraction{Numerator: "2", Denominator: "5"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, record))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-alloc",
	})
	require.NoError(t, err)
	found := false
	for _, line := range got.Coverage.Allocations {
		if line.AllocationID == "alloc-ed-1" && !line.Unallocated {
			found = true
			require.Equal(t, "b-edL", line.TargetID)
			require.NotNil(t, line.SourceAmount, "allocation must preserve its source amount, never provider money")
		}
	}
	require.True(t, found, "allocation line targeting the call B-leg must be joined, got %+v", got.Coverage.Allocations)
}

func TestQueryEconomicDetailReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ed-reopen.db")
	openFileStore := func(t *testing.T) *DurableStore {
		t.Helper()
		sqlDB, err := sql.Open("sqlite", "file:"+path)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(4)
		bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
		require.NoError(t, err)
		seedTestSchemaIfEmpty(t, bunDB)
		store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
		require.NoError(t, err)
		return store
	}

	store := openFileStore(t)
	account := edTestAccount(t, store, "ed-reopen", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-reopen", "edO")
	before, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-reopen",
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	reopened := openFileStore(t)
	t.Cleanup(func() { _ = reopened.Close() })
	after, err := reopened.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: reopened.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-reopen",
	})
	require.NoError(t, err)
	require.Equal(t, before, after, "durable detail must rebuild identically after reopen")
}

// TestQueryEconomicDetailAllocationSharedEnvelopeIsolation is the Finding 2
// regression: an allocation envelope discovered through one in-scope target
// must contribute only targets that authoritatively belong to the requested
// call/leg traversal. A same-account foreign call and a foreign-account call
// share the envelope, so filtering that relies on account alone cannot fake
// correctness.
func TestQueryEconomicDetailAllocationSharedEnvelopeIsolation(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountA := edTestAccount(t, store, "ed-alloc-iso-a", "USD")
	accountB := edTestAccount(t, store, "ed-alloc-iso-b", "USD")
	callA, _ := edCompleteCall(t, store, accountA.ID, "a-ed-alloc-iso", "edIsoA")
	callA2, _ := edCompleteCall(t, store, accountA.ID, "a-ed-alloc-iso", "edIsoA2")
	callB, _ := edCompleteCall(t, store, accountB.ID, "a-ed-alloc-iso-b", "edIsoB")

	amount, err := metering.ParseDecimal("10")
	require.NoError(t, err)
	record := economics.AllocationRecord{
		ID: "alloc-ed-iso", Version: 1,
		SourceSubject: metering.SubjectRef{Kind: metering.SubjectResource, StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: accountA.ID, ResourceID: "shared-res", PeriodID: "2026-09"},
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-in-scope", Target: edTestBLegSubject(store.StoreID(), "tenant-ed", accountA.ID, "a-ed-alloc-iso", callA.String(), "b-edIsoA"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "4"}},
			{TargetID: "t-same-account-foreign-call", Target: edTestBLegSubject(store.StoreID(), "tenant-ed", accountA.ID, "a-ed-alloc-iso", callA2.String(), "b-edIsoA2"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "4"}},
			{TargetID: "t-foreign-account", Target: edTestBLegSubject(store.StoreID(), "tenant-ed", accountA.ID, "a-ed-alloc-iso-b", callB.String(), "b-edIsoB"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "4"}},
			{TargetID: "unallocated", Unallocated: true, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "4"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, record))

	foreignTargetIDs := map[string]bool{"t-same-account-foreign-call": true, "t-foreign-account": true}
	foreignCalls := map[string]bool{callA2.String(): true, callB.String(): true}

	t.Run("account A call scope sees only its own contribution", func(t *testing.T) {
		t.Parallel()
		got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
			StoreID: store.StoreID(), AccountID: accountA.ID, BillingCallID: callA.String(), ALegID: "a-ed-alloc-iso",
		})
		require.NoError(t, err)
		require.Len(t, got.Coverage.Allocations, 2, "only the in-scope contribution and the conserved remainder survive")
		sawInScope, sawRemainder := false, false
		for _, line := range got.Coverage.Allocations {
			require.False(t, foreignTargetIDs[line.TargetID], "foreign target %q leaked into account A detail", line.TargetID)
			require.False(t, foreignCalls[line.Target.BillingCallID], "foreign call %q leaked into account A detail", line.Target.BillingCallID)
			if line.Unallocated {
				sawRemainder = true
				continue
			}
			sawInScope = true
			require.Equal(t, "alloc-ed-iso", line.AllocationID)
			require.Equal(t, uint64(1), line.AllocationVersion)
			require.Equal(t, "t-in-scope", line.TargetID)
			require.Equal(t, "b-edIsoA", line.Target.BLegID)
			require.Equal(t, callA.String(), line.Target.BillingCallID)
			require.Equal(t, economics.BasisAllocatedCost, line.SourceBasis)
			require.NotNil(t, line.SourceAmount, "in-scope contribution must preserve the exact source amount")
			require.Equal(t, "10/0", line.SourceAmount.String())
		}
		require.True(t, sawInScope, "the in-scope contribution must survive")
		require.True(t, sawRemainder, "the allocation's unallocated remainder is its conservation audit reference")
	})

	t.Run("account B call scope cannot read account A's allocation", func(t *testing.T) {
		t.Parallel()
		// The envelope is owned by account A and only references B's B-leg. It is
		// discovered for account B because a target subject matches, but every
		// line is foreign-owned and must fail closed: account B sees nothing.
		got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
			StoreID: store.StoreID(), AccountID: accountB.ID, BillingCallID: callB.String(), ALegID: "a-ed-alloc-iso-b",
		})
		require.NoError(t, err)
		require.Empty(t, got.Coverage.Allocations)
	})
}

// TestQueryEconomicDetailAllocationRetainsInScopeMultiTarget proves the
// positive conservation case: every target that genuinely belongs to the
// requested scope survives with exact identity, including a call target and
// multiple B-leg targets of the same allocation envelope.
func TestQueryEconomicDetailAllocationRetainsInScopeMultiTarget(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-alloc-multi", "USD")
	callID := edTestCallID(t)
	legOne, legTwo := edTestLeg(t, "b-multi-1"), edTestLeg(t, "b-multi-2")
	legTwo.AttemptSeq = 2
	edSetupCall(t, store, account.ID, callID, "a-ed-alloc-multi", legOne, legTwo)

	amount, err := metering.ParseDecimal("9")
	require.NoError(t, err)
	legSubject := func(bLegID string) metering.SubjectRef {
		return edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, "a-ed-alloc-multi", callID.String(), bLegID)
	}
	record := economics.AllocationRecord{
		ID: "alloc-ed-multi", Version: 1,
		SourceSubject: metering.SubjectRef{Kind: metering.SubjectResource, StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, ResourceID: "shared-multi", PeriodID: "2026-09"},
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-call", Target: metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, ALegID: "a-ed-alloc-multi", BillingCallID: callID.String()}, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "3"}},
			{TargetID: "t-leg-1", Target: legSubject("b-multi-1"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "3"}},
			{TargetID: "t-leg-2", Target: legSubject("b-multi-2"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "3"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, record))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-alloc-multi",
	})
	require.NoError(t, err)
	require.Len(t, got.Coverage.Allocations, 3)
	targetIDs := make([]string, 0, len(got.Coverage.Allocations))
	for _, line := range got.Coverage.Allocations {
		require.Equal(t, "alloc-ed-multi", line.AllocationID)
		require.False(t, line.Unallocated)
		require.NotNil(t, line.SourceAmount)
		targetIDs = append(targetIDs, line.TargetID)
	}
	require.ElementsMatch(t, []string{"t-call", "t-leg-1", "t-leg-2"}, targetIDs)
}

// edAppendRetention persists one full-result reconciliation retention keyed by
// the given B-leg subject identity, with caller-controlled component amounts so
// callers can distinguish revisions of one true subject stream.
func edAppendRetention(t *testing.T, store *DurableStore, id, accountID, aLegID, callID, bLegID, localValue, providerValue string, revision uint64, createdAt time.Time) billing.ReconciliationRetentionResult {
	t.Helper()
	ctx := context.Background()
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", accountID, aLegID, callID, bLegID)
	local := edTestObservation(t, "obs-"+id+"-local", metering.OriginLocal, "stream-"+id+"-local", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, localValue)}, nil)
	provider := edTestObservation(t, "obs-"+id+"-provider", metering.OriginProvider, "stream-"+id+"-provider", 1, subject,
		[]metering.Measure{edTestMeasure(t, metering.ComponentInputToken, providerValue)}, nil)
	quantity, err := billing.CompareComponentQuantities(
		billing.ReconciliationEvidenceSet{Subject: subject, Tokenizer: "ed-tokenizer-v1", Observations: []metering.Observation{local}},
		billing.ReconciliationEvidenceSet{Subject: subject, Tokenizer: "ed-tokenizer-v1", Observations: []metering.Observation{provider}},
	)
	require.NoError(t, err)
	refs := []metering.ObservationRef{
		edObservationRef(t, store.StoreID(), local),
		edObservationRef(t, store.StoreID(), provider),
	}
	monetary, err := billing.DecomposeMonetaryDiscrepancies(billing.MonetaryDiscrepancyInput{Valuations: []economics.Valuation{
		edTestValuation(t, id+"-e", economics.BasisLocalExpected, subject, refs, edTestCurrencyTotal(t, "USD", "1.00")),
		edTestValuation(t, id+"-q", economics.BasisProviderQuantityLocal, subject, refs, edTestCurrencyTotal(t, "USD", "1.10")),
		edTestValuation(t, id+"-p", economics.BasisProviderReported, subject, refs, edTestCurrencyTotal(t, "USD", "1.32")),
	}})
	require.NoError(t, err)
	result := billing.ReconciliationRetentionResult{
		SchemaVersion: billing.ReconciliationRetentionSchemaVersionV1,
		ID:            id, ResultRevision: revision, Subject: subject, Scope: "call:" + callID,
		Policy:       billing.VersionRef{ID: "policy-ed", Version: "v1"},
		InputSetHash: strings.Repeat("a", 64),
		ValuationIDs: []string{id + "-e", id + "-q", id + "-p"},
		Quantity:     &quantity, Monetary: &monetary,
		CreatedAt: createdAt,
	}
	require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	return result
}

// TestQueryEconomicDetailReconciliationMultipleBLegsSurvive is the Finding 3B
// durable contract: reconciliation results for two independent B-leg subjects
// of one call must both survive instead of only the first available result.
func TestQueryEconomicDetailReconciliationMultipleBLegsSurvive(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-recon-multi", "USD")
	callID := edTestCallID(t)
	legOne, legTwo := edTestLeg(t, "b-recon-1"), edTestLeg(t, "b-recon-2")
	legTwo.AttemptSeq = 2
	edSetupCall(t, store, account.ID, callID, "a-ed-recon-multi", legOne, legTwo)
	edAppendRetention(t, store, "recon-multi-1", account.ID, "a-ed-recon-multi", callID.String(), "b-recon-1", "100", "110", 1, time.Unix(1_700_020_200, 0).UTC())
	edAppendRetention(t, store, "recon-multi-2", account.ID, "a-ed-recon-multi", callID.String(), "b-recon-2", "100", "120", 1, time.Unix(1_700_020_300, 0).UTC())

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-recon-multi",
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 2, "both B-leg reconciliations must survive")
	require.Equal(t, "b-recon-1", got.Reconciliations[0].Subject.BLegID)
	require.Equal(t, "b-recon-2", got.Reconciliations[1].Subject.BLegID)
	for _, entry := range got.Reconciliations {
		require.Equal(t, callID.String(), entry.Subject.BillingCallID)
		require.NotNil(t, entry.Quantity)
		require.NotNil(t, entry.Monetary)
		require.Equal(t, billing.ReconciliationStatusDiscrepant, entry.Quantity.Status)
	}
	require.NotNil(t, got.Quantity, "singular convenience fields project the deterministic first entry")
	require.NotNil(t, got.Monetary)
}

// TestQueryEconomicDetailReconciliationLaterChildSurvives proves a
// reconciliation present only on a later child is not silently omitted after
// the former 32-child probe cap.
func TestQueryEconomicDetailReconciliationLaterChildSurvives(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-recon-late", "USD")
	callID := edTestCallID(t)
	legs := make([]billing.CallLegUsageRecord, 0, 40)
	for i := range 40 {
		leg := edTestLeg(t, fmt.Sprintf("b-late-%02d", i))
		leg.AttemptSeq = i + 1
		legs = append(legs, leg)
	}
	edSetupCall(t, store, account.ID, callID, "a-ed-recon-late", legs...)
	edAppendRetention(t, store, "recon-late-only", account.ID, "a-ed-recon-late", callID.String(), "b-late-39", "100", "110", 1, time.Unix(1_700_020_200, 0).UTC())

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-recon-late",
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 1, "a reconciliation beyond the former 32-child cap must not be silently lost")
	require.Equal(t, "b-late-39", got.Reconciliations[0].Subject.BLegID)
	require.NotNil(t, got.Quantity)
}

// TestQueryEconomicDetailALegChildReconciliation proves A-leg scope traverses
// authoritative child call/B-leg subjects and preserves their distinct
// reconciliation results instead of probing only the A-leg subject.
func TestQueryEconomicDetailALegChildReconciliation(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-recon-aleg", "USD")
	aLegID := "a-ed-recon-aleg"
	first, _ := edCompleteCall(t, store, account.ID, aLegID, "edRA1")
	second, _ := edCompleteCall(t, store, account.ID, aLegID, "edRA2")
	edAppendRetention(t, store, "recon-aleg-1", account.ID, aLegID, first.String(), "b-edRA1", "100", "110", 1, time.Unix(1_700_020_200, 0).UTC())
	edAppendRetention(t, store, "recon-aleg-2", account.ID, aLegID, second.String(), "b-edRA2", "100", "120", 1, time.Unix(1_700_020_300, 0).UTC())

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID,
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 2, "A-leg scope must traverse child call/B-leg subjects")
	seen := map[string]bool{}
	for _, entry := range got.Reconciliations {
		require.Equal(t, aLegID, entry.Subject.ALegID)
		require.NotNil(t, entry.Quantity)
		seen[entry.Subject.BillingCallID] = true
	}
	require.True(t, seen[first.String()])
	require.True(t, seen[second.String()])
	require.NotNil(t, got.Quantity, "A-leg scope must project a child reconciliation into the singular convenience fields")
}

// TestQueryEconomicDetailReconciliationLatestRevisionWithinSubject proves
// revision selection applies only within one true reconciliation subject
// identity: two revisions of one B-leg resolve to the latest, not to two
// distinct subjects.
func TestQueryEconomicDetailReconciliationLatestRevisionWithinSubject(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-recon-rev", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-recon-rev", edTestLeg(t, "b-recon-rev"))
	edAppendRetention(t, store, "recon-rev", account.ID, "a-ed-recon-rev", callID.String(), "b-recon-rev", "100", "110", 1, time.Unix(1_700_020_200, 0).UTC())
	edAppendRetention(t, store, "recon-rev", account.ID, "a-ed-recon-rev", callID.String(), "b-recon-rev", "100", "100", 2, time.Unix(1_700_020_300, 0).UTC())

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-recon-rev",
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 1, "two revisions of one subject identity resolve to one current result")
	require.NotNil(t, got.Reconciliations[0].Quantity)
	require.Equal(t, billing.ReconciliationStatusMatched, got.Reconciliations[0].Quantity.Status, "the latest revision must win within one subject identity")
}

// TestQueryEconomicDetailReconciliationBoundFailsClosed proves the finite
// candidate-subject bound fails closed instead of silently omitting later
// children.
func TestQueryEconomicDetailReconciliationBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-recon-bound", "USD")
	aLegID := "a-ed-recon-bound"
	for i := 0; i <= billing.MaxEconomicDetailReconciliations; i++ {
		edSetupCall(t, store, account.ID, edTestCallID(t), aLegID, edTestLeg(t, fmt.Sprintf("b-bound-%04d", i)))
	}
	_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID,
	})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
}
