package billing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// F4: comparison discards completeness and independent-source semantics.
// These runner-level tests prove ComponentComparisonReconciler preserves
// dependency Completeness/MissingObservations/coverage/effective measurement
// context/payer provenance, restricts comparable basis pairs to genuinely
// independent evidence, and gates comparability on material context.
// Equal known subtotals with missing coverage must stay partial/incomplete;
// retail/customer-policy output must never masquerade as independent local E;
// mismatched payer/coverage/context with equal quantity must be
// incomparable/partial per C4 (never matched/complete).

func f4InputKey() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		SchemaID:  metering.DefaultInclusionSchemaID,
	}
}

func f4Decimal(t *testing.T, value string) *metering.Decimal {
	t.Helper()
	decimal, err := metering.ParseDecimal(value)
	require.NoError(t, err)
	return &decimal
}

func f4LineKnown(t *testing.T, id string, key metering.ComponentKey, quantity string) economics.LineItem {
	t.Helper()
	component := key.Clone()
	return economics.LineItem{
		ID:        "line-f4-" + id,
		RuleID:    "rule-f4",
		ItemID:    "item-f4-" + id,
		Component: &component,
		Quantity:  f4Decimal(t, quantity),
		Unit:      component.Unit,
		Status:    economics.RatingLineRated,
	}
}

func f4RatingWork(t *testing.T, revision uint64, headKey, obsID string, basis economics.ValuationBasis) EconomicRevisionWork {
	t.Helper()
	key := f4InputKey()
	origin := metering.OriginLocal
	if basis == economics.BasisProviderQuantityLocal || basis == economics.BasisProviderReported {
		origin = metering.OriginProvider
	}
	observation := phase9Observation(t, obsID, origin, key, "1")
	input := phase9RatingInput(t, basis, []metering.Observation{observation})
	if basis == economics.BasisCustomerPolicy {
		input.Policy = economics.PolicySnapshotRef{
			VersionRef: economics.VersionRef{ID: "f4-policy", Version: "v1"},
			PolicyID:   "customer",
		}
		input.PolicyContent = &economics.SnapshotContentRef{
			ContentRef:  "catalog://policy/v1",
			ContentHash: strings.Repeat("3", 64),
		}
	}
	work := EconomicRevisionWork{
		Queue:            EconomicQueueProvider,
		HeadKey:          headKey,
		Subject:          input.Subject,
		EvidenceRevision: revision,
		Input:            input,
		CreatedAt:        time.Unix(1_700_400_000+int64(revision), 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

func f4Dependency(t *testing.T, work EconomicRevisionWork) EconomicJobDependency {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	dependency, err := NewEconomicJobDependency(EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	return dependency
}

type f4ValuationOverride struct {
	Completeness        economics.Completeness
	MissingObservations []metering.ObservationRef
	Payer               *metering.PaymentParty
	CoverageRefs        []metering.ChargeCoverageRef
	EffectiveQualifiers []metering.Dimension
}

func f4SeedValuation(t *testing.T, store *economicJobRunnerTestStore, work EconomicRevisionWork, lines []economics.LineItem, override *f4ValuationOverride) {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	completeness := economics.CompletenessComplete
	if override != nil && override.Completeness.IsKnown() {
		completeness = override.Completeness
	}
	valuation := economics.Valuation{
		ID:                identity.ValuationKey(),
		Version:           economics.ValuationVersionV2,
		Perspective:       normalized.Input.Perspective,
		Basis:             normalized.Input.Basis,
		Subject:           normalized.Subject,
		Scope:             normalized.Input.Scope,
		InputObservations: append([]metering.ObservationRef(nil), normalized.Input.ObservationRefs...),
		InputSetHash:      normalized.InputSetHash,
		Lines:             append([]economics.LineItem(nil), lines...),
		Completeness:      completeness,
		CreatedAt:         normalized.CreatedAt,
	}
	if override != nil {
		if override.MissingObservations != nil {
			valuation.MissingObservations = append([]metering.ObservationRef(nil), override.MissingObservations...)
		}
		if override.Payer != nil {
			valuation.Payer = *override.Payer
		}
		if override.CoverageRefs != nil {
			valuation.CoverageRefs = append([]metering.ChargeCoverageRef(nil), override.CoverageRefs...)
		}
		if override.EffectiveQualifiers != nil {
			valuation.EffectiveQualifiers = append([]metering.Dimension(nil), override.EffectiveQualifiers...)
			if len(override.EffectiveQualifiers) != 0 {
				valuation.QualifierSnapshotRef = &economics.SnapshotContentRef{
					ContentRef:  "catalog://qualifiers/v1",
					ContentHash: strings.Repeat("4", 64),
				}
			}
		}
	}
	// Carry rater/tariff/policy snapshots from the rating input so seeded
	// valuations retain the frozen measurement/price context identity used by
	// approved comparability gates.
	valuation.Rater = normalized.Input.Rater
	valuation.RaterContent = normalized.Input.RaterContent
	valuation.Tariff = normalized.Input.Tariff
	valuation.TariffContent = normalized.Input.TariffContent
	valuation.Policy = normalized.Input.Policy
	valuation.PolicyContent = normalized.Input.PolicyContent
	valuation.EffectiveQualifiers = append(valuation.EffectiveQualifiers, normalized.Input.EffectiveQualifiers...)
	store.mu.Lock()
	store.valuations[identity.ValuationKey()] = valuation
	store.mu.Unlock()
}

func f4ReconWork(t *testing.T, deps ...EconomicJobDependency) EconomicRevisionWork {
	t.Helper()
	probe := phase9Observation(t, "f4-recon-obs", metering.OriginProvider, f4InputKey(), "1")
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{probe})
	work := EconomicRevisionWork{
		Queue:            EconomicQueueProvider,
		Kind:             EconomicWorkKindReconciliation,
		HeadKey:          "f4-recon-head",
		Subject:          input.Subject,
		EvidenceRevision: 299,
		Input:            input,
		Dependencies:     deps,
		CreatedAt:        time.Unix(1_700_499_000, 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

type f4EnvelopeItem struct {
	Component        string  `json:"component"`
	Status           string  `json:"status"`
	Reason           string  `json:"reason"`
	LocalQuantity    *string `json:"local_quantity"`
	ProviderQuantity *string `json:"provider_quantity"`
}

type f4Envelope struct {
	Status   string           `json:"status"`
	Complete bool             `json:"complete"`
	Reason   string           `json:"reason"`
	Items    []f4EnvelopeItem `json:"items"`
}

func f4RunReconciliation(t *testing.T, store *economicJobRunnerTestStore, reconWork EconomicRevisionWork) (EconomicJobRunSummary, f4Envelope, EconomicReconciliation) {
	t.Helper()
	store.appendWork(t, reconWork)
	runner := newEconomicJobRunnerTest(t, EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: store, Dependencies: store, Reconciliations: store,
		Rater: &economicJobRunnerTestRater{}, Reconciler: ComponentComparisonReconciler{},
		Owner: "f4-test", Batch: 8,
	})
	ctx := context.Background()
	var summary EconomicJobRunSummary
	var runErr error
	require.NotPanics(t, func() {
		summary, runErr = runner.RunOnce(ctx, EconomicQueueProvider)
	})
	require.NoError(t, runErr)
	require.Equal(t, 1, summary.Completed, "reconciliation work must complete with an explicit envelope, not retry/fail")
	require.Zero(t, summary.Retried+summary.Failed)
	identity, err := reconWork.Identity()
	require.NoError(t, err)
	store.mu.Lock()
	record, ok := store.reconciliations[identity.Key()]
	store.mu.Unlock()
	require.True(t, ok, "reconciliation must be persisted")
	var envelope f4Envelope
	require.NoError(t, json.Unmarshal(record.ResultJSON, &envelope))
	return summary, envelope, record
}

func f4MissingOutputRef() metering.ObservationRef {
	return metering.ObservationRef{
		StoreID: "store", ObservationID: "f4-missing-output", Revision: 1,
		PayloadHash: strings.Repeat("a", 64),
	}
}

func f4CoverageEdge(obsID, chargeID string, relation metering.CoverageRelation) metering.ChargeCoverageRef {
	return metering.ChargeCoverageRef{
		Ref: metering.ChargeRef{
			StoreID: "store", ObservationID: obsID, Revision: 1, ChargeItemID: chargeID,
		},
		Relation: relation,
	}
}

func f4RunScenario(t *testing.T, localWork, providerWork EconomicRevisionWork, localLines []economics.LineItem, localOverride *f4ValuationOverride, providerLines []economics.LineItem, providerOverride *f4ValuationOverride) f4Envelope {
	t.Helper()
	store := newEconomicJobRunnerTestStore()
	f4SeedValuation(t, store, localWork, localLines, localOverride)
	f4SeedValuation(t, store, providerWork, providerLines, providerOverride)
	recon := f4ReconWork(t, f4Dependency(t, localWork), f4Dependency(t, providerWork))
	_, envelope, _ := f4RunReconciliation(t, store, recon)
	return envelope
}

func f4RunScenarioOrdered(t *testing.T, firstWork, secondWork EconomicRevisionWork, firstLines []economics.LineItem, firstOverride *f4ValuationOverride, secondLines []economics.LineItem, secondOverride *f4ValuationOverride) (f4Envelope, f4Envelope) {
	t.Helper()
	// Forward: first then second. Reverse: same works seeded identically but
	// dependencies declared in opposite order. Envelopes must be identical.
	forwardStore := newEconomicJobRunnerTestStore()
	f4SeedValuation(t, forwardStore, firstWork, firstLines, firstOverride)
	f4SeedValuation(t, forwardStore, secondWork, secondLines, secondOverride)
	forwardRecon := f4ReconWork(t, f4Dependency(t, firstWork), f4Dependency(t, secondWork))
	_, forward, _ := f4RunReconciliation(t, forwardStore, forwardRecon)

	reverseStore := newEconomicJobRunnerTestStore()
	f4SeedValuation(t, reverseStore, firstWork, firstLines, firstOverride)
	f4SeedValuation(t, reverseStore, secondWork, secondLines, secondOverride)
	reverseRecon := f4ReconWork(t, f4Dependency(t, secondWork), f4Dependency(t, firstWork))
	_, reverse, _ := f4RunReconciliation(t, reverseStore, reverseRecon)
	return forward, reverse
}

func f4RequireDeterministic(t *testing.T, forward, reverse f4Envelope) {
	t.Helper()
	require.Equal(t, forward.Status, reverse.Status, "dependency order must not change aggregate status")
	require.Equal(t, forward.Complete, reverse.Complete, "dependency order must not change completeness")
	forwardJSON, err := json.Marshal(forward)
	require.NoError(t, err)
	reverseJSON, err := json.Marshal(reverse)
	require.NoError(t, err)
	require.JSONEq(t, string(forwardJSON), string(reverseJSON), "comparison envelope must be order-independent")
}

func TestPhase172F4PartialWithMissingOutputIsNotCompleteMatchThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	local := f4RatingWork(t, 301, "f4-head-local-partial", "f4-obs-local-partial", economics.BasisLocalExpected)
	provider := f4RatingWork(t, 302, "f4-head-provider", "f4-obs-provider", economics.BasisProviderReported)
	localOverride := &f4ValuationOverride{
		Completeness:        economics.CompletenessPartial,
		MissingObservations: []metering.ObservationRef{f4MissingOutputRef()},
	}
	providerOverride := &f4ValuationOverride{Completeness: economics.CompletenessComplete}
	envelope := f4RunScenario(t, local, provider,
		[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")}, localOverride,
		[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")}, providerOverride)
	require.NotEqual(t, "matched", envelope.Status, "equal known subtotal with partial completeness + missing output must not be matched")
	require.False(t, envelope.Complete, "equal known subtotal with missing coverage must remain incomplete")
	require.NotEmpty(t, envelope.Items, "partial evidence must retain comparison items, not empty match")
}

func TestPhase172F4CustomerPolicyVsProviderReportIsNotIndependentMatchThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	retail := f4RatingWork(t, 311, "f4-head-retail", "f4-obs-retail", economics.BasisCustomerPolicy)
	provider := f4RatingWork(t, 312, "f4-head-provider-r", "f4-obs-provider-r", economics.BasisProviderReported)
	retailOverride := &f4ValuationOverride{Completeness: economics.CompletenessComplete}
	providerOverride := &f4ValuationOverride{Completeness: economics.CompletenessComplete}
	envelope := f4RunScenario(t, retail, provider,
		[]economics.LineItem{f4LineKnown(t, "retail-input", key, "100")}, retailOverride,
		[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")}, providerOverride)
	require.NotEqual(t, "matched", envelope.Status, "provider-derived customer-policy vs provider-reported must not be an independent match")
	require.False(t, envelope.Complete, "retail vs provider without independent local evidence must remain incomplete")
}

func TestPhase172F4PayerMismatchIsIncomparableThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	local := f4RatingWork(t, 321, "f4-head-local-payer", "f4-obs-local-payer", economics.BasisLocalExpected)
	provider := f4RatingWork(t, 322, "f4-head-provider-payer", "f4-obs-provider-payer", economics.BasisProviderReported)
	operatorPayer := metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}
	customerPayer := metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-creds"}
	envelope := f4RunScenario(t, local, provider,
		[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
		&f4ValuationOverride{Completeness: economics.CompletenessComplete, Payer: &operatorPayer},
		[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")},
		&f4ValuationOverride{Completeness: economics.CompletenessComplete, Payer: &customerPayer})
	require.Equal(t, "incomparable", envelope.Status, "mismatched payer with equal quantity must be incomparable per C4")
	require.False(t, envelope.Complete)
	require.Equal(t, "payer_mismatch", envelope.Reason)
}

func TestPhase172F4CoverageMismatchIsIncomparableThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	local := f4RatingWork(t, 331, "f4-head-local-coverage", "f4-obs-local-coverage", economics.BasisLocalExpected)
	provider := f4RatingWork(t, 332, "f4-head-provider-coverage", "f4-obs-provider-coverage", economics.BasisProviderReported)
	localCoverage := []metering.ChargeCoverageRef{f4CoverageEdge("obs-cov-a", "charge-a", metering.CoverageInclusive)}
	providerCoverage := []metering.ChargeCoverageRef{f4CoverageEdge("obs-cov-b", "charge-b", metering.CoverageInclusive)}
	envelope := f4RunScenario(t, local, provider,
		[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
		&f4ValuationOverride{Completeness: economics.CompletenessComplete, CoverageRefs: localCoverage},
		[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")},
		&f4ValuationOverride{Completeness: economics.CompletenessComplete, CoverageRefs: providerCoverage})
	require.Equal(t, "incomparable", envelope.Status, "mismatched coverage with equal quantity must be incomparable per C4")
	require.False(t, envelope.Complete)
	require.Equal(t, "coverage_mismatch", envelope.Reason)
}

func TestPhase172F4MeasurementContextMismatchIsIncomparableThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	local := f4RatingWork(t, 341, "f4-head-local-ctx", "f4-obs-local-ctx", economics.BasisLocalExpected)
	provider := f4RatingWork(t, 342, "f4-head-provider-ctx", "f4-obs-provider-ctx", economics.BasisProviderReported)
	localQualifiers := []metering.Dimension{{Name: "model", Value: "model-a"}}
	providerQualifiers := []metering.Dimension{{Name: "model", Value: "model-b"}}
	envelope := f4RunScenario(t, local, provider,
		[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
		&f4ValuationOverride{Completeness: economics.CompletenessComplete, EffectiveQualifiers: localQualifiers},
		[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")},
		&f4ValuationOverride{Completeness: economics.CompletenessComplete, EffectiveQualifiers: providerQualifiers})
	require.Equal(t, "incomparable", envelope.Status, "mismatched effective measurement context with equal quantity must be incomparable per C4")
	require.False(t, envelope.Complete)
	require.Equal(t, "measurement_context_mismatch", envelope.Reason)
}

func TestPhase172F4IndependentLocalVsProviderReportMatchControlThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	local := f4RatingWork(t, 351, "f4-head-local-control", "f4-obs-local-control", economics.BasisLocalExpected)
	provider := f4RatingWork(t, 352, "f4-head-provider-control", "f4-obs-provider-control", economics.BasisProviderReported)
	envelope := f4RunScenario(t, local, provider,
		[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
		&f4ValuationOverride{Completeness: economics.CompletenessComplete},
		[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")},
		&f4ValuationOverride{Completeness: economics.CompletenessComplete})
	require.Equal(t, "matched", envelope.Status, "supported independent local vs provider-report exact match must stay matched")
	require.True(t, envelope.Complete)
	require.Len(t, envelope.Items, 1)
	require.Equal(t, "matched", envelope.Items[0].Status)
	require.NotNil(t, envelope.Items[0].LocalQuantity)
	require.NotNil(t, envelope.Items[0].ProviderQuantity)
	require.Equal(t, "100", *envelope.Items[0].LocalQuantity)
	require.Equal(t, "100", *envelope.Items[0].ProviderQuantity)
}

func TestPhase172F4IndependentLocalVsProviderReportDiscrepancyControlThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	local := f4RatingWork(t, 361, "f4-head-local-disc", "f4-obs-local-disc", economics.BasisLocalExpected)
	provider := f4RatingWork(t, 362, "f4-head-provider-disc", "f4-obs-provider-disc", economics.BasisProviderReported)
	envelope := f4RunScenario(t, local, provider,
		[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
		&f4ValuationOverride{Completeness: economics.CompletenessComplete},
		[]economics.LineItem{f4LineKnown(t, "provider-input", key, "110")},
		&f4ValuationOverride{Completeness: economics.CompletenessComplete})
	require.Equal(t, "discrepant", envelope.Status, "supported quantity discrepancy must stay discrepant")
	require.True(t, envelope.Complete)
	require.Len(t, envelope.Items, 1)
	require.Equal(t, "discrepant", envelope.Items[0].Status)
}

func TestPhase172F4OrderIndependentAndCombinationsThroughRunner(t *testing.T) {
	t.Parallel()
	key := f4InputKey()
	t.Run("partial-missing reverse order deterministic", func(t *testing.T) {
		t.Parallel()
		local := f4RatingWork(t, 371, "f4-head-local-ord", "f4-obs-local-ord", economics.BasisLocalExpected)
		provider := f4RatingWork(t, 372, "f4-head-provider-ord", "f4-obs-provider-ord", economics.BasisProviderReported)
		localLines := []economics.LineItem{f4LineKnown(t, "local-input", key, "100")}
		localOverride := &f4ValuationOverride{Completeness: economics.CompletenessPartial, MissingObservations: []metering.ObservationRef{f4MissingOutputRef()}}
		providerLines := []economics.LineItem{f4LineKnown(t, "provider-input", key, "100")}
		providerOverride := &f4ValuationOverride{Completeness: economics.CompletenessComplete}
		forward, reverse := f4RunScenarioOrdered(t, local, provider, localLines, localOverride, providerLines, providerOverride)
		require.NotEqual(t, "matched", forward.Status)
		require.False(t, forward.Complete)
		f4RequireDeterministic(t, forward, reverse)
	})
	t.Run("retail reverse order deterministic", func(t *testing.T) {
		t.Parallel()
		retail := f4RatingWork(t, 373, "f4-head-retail-ord", "f4-obs-retail-ord", economics.BasisCustomerPolicy)
		provider := f4RatingWork(t, 374, "f4-head-provider-ord2", "f4-obs-provider-ord2", economics.BasisProviderReported)
		retailLines := []economics.LineItem{f4LineKnown(t, "retail-input", key, "100")}
		providerLines := []economics.LineItem{f4LineKnown(t, "provider-input", key, "100")}
		forward, reverse := f4RunScenarioOrdered(t, retail, provider, retailLines,
			&f4ValuationOverride{Completeness: economics.CompletenessComplete}, providerLines,
			&f4ValuationOverride{Completeness: economics.CompletenessComplete})
		require.NotEqual(t, "matched", forward.Status)
		require.False(t, forward.Complete)
		f4RequireDeterministic(t, forward, reverse)
	})
	t.Run("payer mismatch reverse order deterministic", func(t *testing.T) {
		t.Parallel()
		local := f4RatingWork(t, 375, "f4-head-local-payer-ord", "f4-obs-local-payer-ord", economics.BasisLocalExpected)
		provider := f4RatingWork(t, 376, "f4-head-provider-payer-ord", "f4-obs-provider-payer-ord", economics.BasisProviderReported)
		operatorPayer := metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}
		customerPayer := metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-creds"}
		forward, reverse := f4RunScenarioOrdered(t, local, provider,
			[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
			&f4ValuationOverride{Completeness: economics.CompletenessComplete, Payer: &operatorPayer},
			[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")},
			&f4ValuationOverride{Completeness: economics.CompletenessComplete, Payer: &customerPayer})
		require.Equal(t, "incomparable", forward.Status)
		f4RequireDeterministic(t, forward, reverse)
	})
	t.Run("coverage and context combination stays incomparable deterministic", func(t *testing.T) {
		t.Parallel()
		local := f4RatingWork(t, 377, "f4-head-local-combo", "f4-obs-local-combo", economics.BasisLocalExpected)
		provider := f4RatingWork(t, 378, "f4-head-provider-combo", "f4-obs-provider-combo", economics.BasisProviderReported)
		operatorPayer := metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"}
		customerPayer := metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer-creds"}
		localCoverage := []metering.ChargeCoverageRef{f4CoverageEdge("obs-cov-a", "charge-a", metering.CoverageInclusive)}
		providerCoverage := []metering.ChargeCoverageRef{f4CoverageEdge("obs-cov-b", "charge-b", metering.CoverageInclusive)}
		localQualifiers := []metering.Dimension{{Name: "model", Value: "model-a"}}
		providerQualifiers := []metering.Dimension{{Name: "model", Value: "model-b"}}
		forward, reverse := f4RunScenarioOrdered(t, local, provider,
			[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
			&f4ValuationOverride{Completeness: economics.CompletenessPartial, MissingObservations: []metering.ObservationRef{f4MissingOutputRef()}, Payer: &operatorPayer, CoverageRefs: localCoverage, EffectiveQualifiers: localQualifiers},
			[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")},
			&f4ValuationOverride{Completeness: economics.CompletenessComplete, Payer: &customerPayer, CoverageRefs: providerCoverage, EffectiveQualifiers: providerQualifiers})
		require.NotEqual(t, "matched", forward.Status)
		require.False(t, forward.Complete)
		f4RequireDeterministic(t, forward, reverse)
	})
	t.Run("supported match reverse order deterministic", func(t *testing.T) {
		t.Parallel()
		local := f4RatingWork(t, 379, "f4-head-local-match-ord", "f4-obs-local-match-ord", economics.BasisLocalExpected)
		provider := f4RatingWork(t, 380, "f4-head-provider-match-ord", "f4-obs-provider-match-ord", economics.BasisProviderReported)
		forward, reverse := f4RunScenarioOrdered(t, local, provider,
			[]economics.LineItem{f4LineKnown(t, "local-input", key, "100")},
			&f4ValuationOverride{Completeness: economics.CompletenessComplete},
			[]economics.LineItem{f4LineKnown(t, "provider-input", key, "100")},
			&f4ValuationOverride{Completeness: economics.CompletenessComplete})
		require.Equal(t, "matched", forward.Status)
		require.True(t, forward.Complete)
		f4RequireDeterministic(t, forward, reverse)
	})
}
