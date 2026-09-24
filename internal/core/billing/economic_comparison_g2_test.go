package billing

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// G2: equal-quantity duplicate lines with differing rounded amounts must merge
// deterministically. Three distinct same-component local lines, all quantity
// 100, with rounded amounts [1,1,2] in any order must produce byte-identical
// persisted ResultJSON. Amount conflicts must canonicalize like quantity
// conflicts: no order-dependent quantity/literal/amount remnants.

func g2ComponentKey() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		SchemaID:  metering.DefaultInclusionSchemaID,
	}
}

func g2Decimal(t *testing.T, value string) *metering.Decimal {
	t.Helper()
	decimal, err := metering.ParseDecimal(value)
	require.NoError(t, err)
	return &decimal
}

func g2Line(t *testing.T, id string, key metering.ComponentKey, quantity string, amountNano *int64) economics.LineItem {
	t.Helper()
	component := key.Clone()
	line := economics.LineItem{
		ID:        "line-g2-" + id,
		RuleID:    "rule-g2",
		ItemID:    "item-g2-" + id,
		Component: &component,
		Quantity:  g2Decimal(t, quantity),
		Unit:      component.Unit,
		Status:    economics.RatingLineRated,
	}
	if amountNano != nil {
		nano := *amountNano
		line.RoundedAmount = &economics.Money{NanoUnits: nano, Currency: "USD", Present: true}
	}
	return line
}

func g2RatingWork(t *testing.T, revision uint64, headKey, obsID string, basis economics.ValuationBasis) EconomicRevisionWork {
	t.Helper()
	key := g2ComponentKey()
	origin := metering.OriginLocal
	if basis == economics.BasisProviderQuantityLocal || basis == economics.BasisProviderReported {
		origin = metering.OriginProvider
	}
	observation := phase9Observation(t, obsID, origin, key, "1")
	input := phase9RatingInput(t, basis, []metering.Observation{observation})
	work := EconomicRevisionWork{
		Queue:            EconomicQueueProvider,
		HeadKey:          headKey,
		Subject:          input.Subject,
		EvidenceRevision: revision,
		Input:            input,
		CreatedAt:        time.Unix(1_700_500_000+int64(revision), 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

func g2Dependency(t *testing.T, work EconomicRevisionWork) EconomicJobDependency {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	dependency, err := NewEconomicJobDependency(EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	return dependency
}

func g2SeedValuation(t *testing.T, store *economicJobRunnerTestStore, work EconomicRevisionWork, lines []economics.LineItem) {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
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
		Completeness:      economics.CompletenessComplete,
		CreatedAt:         normalized.CreatedAt,
	}
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

func g2ReconWork(t *testing.T, headKey string, deps ...EconomicJobDependency) EconomicRevisionWork {
	t.Helper()
	probe := phase9Observation(t, "g2-recon-obs-"+headKey, metering.OriginProvider, g2ComponentKey(), "1")
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{probe})
	work := EconomicRevisionWork{
		Queue:            EconomicQueueProvider,
		Kind:             EconomicWorkKindReconciliation,
		HeadKey:          "g2-recon-head-" + headKey,
		Subject:          input.Subject,
		EvidenceRevision: 599,
		Input:            input,
		Dependencies:     deps,
		CreatedAt:        time.Unix(1_700_599_000, 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

type g2EnvelopeItem struct {
	Component        string  `json:"component"`
	Status           string  `json:"status"`
	Reason           string  `json:"reason"`
	LocalQuantity    *string `json:"local_quantity"`
	ProviderQuantity *string `json:"provider_quantity"`
	LocalAmountNano  *int64  `json:"local_amount_nano"`
	ProviderAmount   *int64  `json:"provider_amount_nano"`
	SignedDelta      *string `json:"signed_delta"`
	AbsoluteDelta    *string `json:"absolute_delta"`
}

type g2Envelope struct {
	Status   string           `json:"status"`
	Complete bool             `json:"complete"`
	Reason   string           `json:"reason"`
	Items    []g2EnvelopeItem `json:"items"`
}

func g2RunReconciliation(t *testing.T, store *economicJobRunnerTestStore, reconWork EconomicRevisionWork) (g2Envelope, string) {
	t.Helper()
	store.appendWork(t, reconWork)
	runner := newEconomicJobRunnerTest(t, EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: store, Dependencies: store, Reconciliations: store,
		Rater: &economicJobRunnerTestRater{}, Reconciler: ComponentComparisonReconciler{},
		Owner: "g2-test", Batch: 8,
	})
	ctx := context.Background()
	var summary EconomicJobRunSummary
	var runErr error
	require.NotPanics(t, func() {
		summary, runErr = runner.RunOnce(ctx, EconomicQueueProvider)
	})
	require.NoError(t, runErr)
	require.Equal(t, 1, summary.Completed)
	require.Zero(t, summary.Retried+summary.Failed)
	identity, err := reconWork.Identity()
	require.NoError(t, err)
	store.mu.Lock()
	record, ok := store.reconciliations[identity.Key()]
	store.mu.Unlock()
	require.True(t, ok, "reconciliation must be persisted")
	var envelope g2Envelope
	require.NoError(t, json.Unmarshal(record.ResultJSON, &envelope))
	return envelope, string(record.ResultJSON)
}

//go:fix inline
func g2Nano(v int64) *int64 { return new(v) }

// g2SingleValuationJSON runs one local valuation carrying three distinct lines
// (ids lineA/lineB/lineC) with the given rounded-amount order, plus one clean
// provider line. Line order inside the valuation is preserved by the merge, so
// permuting amounts directly permutes merge encounter order.
func g2SingleValuationJSON(t *testing.T, tag string, amounts []*int64, depOrderReversed bool) (g2Envelope, string) {
	t.Helper()
	key := g2ComponentKey()
	local := g2RatingWork(t, 601, "g2-head-local-three-"+tag, "g2-obs-local-three-"+tag, economics.BasisLocalExpected)
	provider := g2RatingWork(t, 604, "g2-head-provider-"+tag, "g2-obs-provider-"+tag, economics.BasisProviderQuantityLocal)
	store := newEconomicJobRunnerTestStore()
	lines := []economics.LineItem{
		g2Line(t, "three-a-"+tag, key, "100", amounts[0]),
		g2Line(t, "three-b-"+tag, key, "100", amounts[1]),
		g2Line(t, "three-c-"+tag, key, "100", amounts[2]),
	}
	g2SeedValuation(t, store, local, lines)
	g2SeedValuation(t, store, provider, []economics.LineItem{g2Line(t, "provider-"+tag, key, "100", nil)})
	localDep := g2Dependency(t, local)
	providerDep := g2Dependency(t, provider)
	var recon EconomicRevisionWork
	if depOrderReversed {
		recon = g2ReconWork(t, tag, providerDep, localDep)
	} else {
		recon = g2ReconWork(t, tag, localDep, providerDep)
	}
	return g2RunReconciliation(t, store, recon)
}

// g2ThreeDepJSON runs three local valuations (fixed works per tag, one line
// each) with amounts assigned to A/B/C. Dependency keys do not depend on line
// amounts, so the canonical sorted dependency order is fixed per tag and
// permuting amount assignment permutes merge encounter order.
func g2ThreeDepJSON(t *testing.T, tag string, amounts []*int64, depOrderReversed bool, localA, localB, localC, provider EconomicRevisionWork) (g2Envelope, string) {
	t.Helper()
	key := g2ComponentKey()
	store := newEconomicJobRunnerTestStore()
	locals := []EconomicRevisionWork{localA, localB, localC}
	ids := []string{"dep-a-" + tag, "dep-b-" + tag, "dep-c-" + tag}
	for i, work := range locals {
		g2SeedValuation(t, store, work, []economics.LineItem{g2Line(t, ids[i], key, "100", amounts[i])})
	}
	g2SeedValuation(t, store, provider, []economics.LineItem{g2Line(t, "dep-provider-"+tag, key, "100", nil)})
	deps := []EconomicJobDependency{g2Dependency(t, localA), g2Dependency(t, localB), g2Dependency(t, localC), g2Dependency(t, provider)}
	var recon EconomicRevisionWork
	if depOrderReversed {
		recon = g2ReconWork(t, tag, deps[3], deps[2], deps[1], deps[0])
	} else {
		recon = g2ReconWork(t, tag, deps...)
	}
	return g2RunReconciliation(t, store, recon)
}

func TestPhase172G2EqualQuantityAmountConflictIsOrderIndependentThroughRunner(t *testing.T) {
	t.Parallel()
	permutations := map[string][]*int64{
		"1-1-2": {g2Nano(1), g2Nano(1), g2Nano(2)},
		"1-2-1": {g2Nano(1), g2Nano(2), g2Nano(1)},
		"2-1-1": {g2Nano(2), g2Nano(1), g2Nano(1)},
	}
	t.Run("single-valuation-line-order", func(t *testing.T) {
		t.Parallel()
		payloads := map[string]string{}
		envelopes := map[string]g2Envelope{}
		for name, amounts := range permutations {
			envelope, raw := g2SingleValuationJSON(t, "single-"+name, amounts, false)
			require.Equal(t, "conflict", envelope.Status, "equal quantities with differing amounts must conflict (%s)", name)
			require.False(t, envelope.Complete)
			require.Len(t, envelope.Items, 1)
			require.Equal(t, "conflict", envelope.Items[0].Status)
			payloads[name] = raw
			envelopes[name] = envelope
			revEnvelope, revRaw := g2SingleValuationJSON(t, "single-"+name, amounts, true)
			require.Equal(t, "conflict", revEnvelope.Status)
			require.Equal(t, raw, revRaw, "reverse dependency order must not change persisted ResultJSON (%s)", name)
		}
		require.Equal(t, payloads["1-1-2"], payloads["1-2-1"], "amount order [1,1,2] vs [1,2,1] must persist identically")
		require.Equal(t, payloads["1-1-2"], payloads["2-1-1"], "amount order [1,1,2] vs [2,1,1] must persist identically")
		for name, envelope := range envelopes {
			require.Nil(t, envelope.Items[0].LocalQuantity, "conflicting local must not expose a quantity (%s)", name)
			require.Nil(t, envelope.Items[0].LocalAmountNano, "conflicting local must not expose an amount (%s)", name)
			require.NotNil(t, envelope.Items[0].ProviderQuantity, "provider quantity stays visible")
			require.Equal(t, "100", *envelope.Items[0].ProviderQuantity)
		}
	})
	t.Run("three-dependency-amount-assignment", func(t *testing.T) {
		t.Parallel()
		tag := "three-dep-shared"
		localA := g2RatingWork(t, 601, "g2-head-local-a-"+tag, "g2-obs-local-a-"+tag, economics.BasisLocalExpected)
		localB := g2RatingWork(t, 602, "g2-head-local-b-"+tag, "g2-obs-local-b-"+tag, economics.BasisLocalExpected)
		localC := g2RatingWork(t, 603, "g2-head-local-c-"+tag, "g2-obs-local-c-"+tag, economics.BasisLocalExpected)
		provider := g2RatingWork(t, 604, "g2-head-provider-"+tag, "g2-obs-provider-"+tag, economics.BasisProviderQuantityLocal)
		payloads := map[string]string{}
		envelopes := map[string]g2Envelope{}
		for name, amounts := range permutations {
			envelope, raw := g2ThreeDepJSON(t, tag+"-"+name, amounts, false, localA, localB, localC, provider)
			require.Equal(t, "conflict", envelope.Status, "equal quantities with differing amounts must conflict (%s)", name)
			require.False(t, envelope.Complete)
			payloads[name] = raw
			envelopes[name] = envelope
			revEnvelope, revRaw := g2ThreeDepJSON(t, tag+"-"+name, amounts, true, localA, localB, localC, provider)
			require.Equal(t, "conflict", revEnvelope.Status)
			require.Equal(t, raw, revRaw, "reverse dependency order must not change persisted ResultJSON (%s)", name)
		}
		require.Equal(t, payloads["1-1-2"], payloads["1-2-1"], "amount assignment [1,1,2] vs [1,2,1] must persist identically")
		require.Equal(t, payloads["1-1-2"], payloads["2-1-1"], "amount assignment [1,1,2] vs [2,1,1] must persist identically")
		for name, envelope := range envelopes {
			require.Nil(t, envelope.Items[0].LocalQuantity, "conflicting local must not expose a quantity (%s)", name)
			require.Nil(t, envelope.Items[0].LocalAmountNano, "conflicting local must not expose an amount (%s)", name)
		}
	})
}

func TestPhase172G2AllEqualAmountsMatchThroughRunner(t *testing.T) {
	t.Parallel()
	for _, amount := range []*int64{g2Nano(1), g2Nano(7), nil} {
		label := "nil"
		if amount != nil {
			label = "nano"
		}
		t.Run("all-equal-"+label, func(t *testing.T) {
			t.Parallel()
			var amounts []*int64
			if amount != nil {
				amounts = []*int64{new(*amount), new(*amount), new(*amount)}
			} else {
				amounts = []*int64{nil, nil, nil}
			}
			envelope, raw := g2SingleValuationJSON(t, "equal-"+label, amounts, false)
			revEnvelope, revRaw := g2SingleValuationJSON(t, "equal-"+label, amounts, true)
			require.Equal(t, "matched", envelope.Status, "equal quantities with all-equal amounts must match")
			require.True(t, envelope.Complete)
			require.Len(t, envelope.Items, 1)
			require.Equal(t, "matched", envelope.Items[0].Status)
			require.NotNil(t, envelope.Items[0].LocalQuantity)
			require.Equal(t, "100", *envelope.Items[0].LocalQuantity)
			require.Equal(t, raw, revRaw, "equal-amount control must be order-independent")
			require.Equal(t, envelope.Status, revEnvelope.Status)
		})
	}
}

func TestPhase172G2QuantityConflictStaysCanonicalThroughRunner(t *testing.T) {
	t.Parallel()
	key := g2ComponentKey()
	runQuantityConflict := func(t *testing.T, tag string, quantities []string, depReversed bool) (g2Envelope, string) {
		t.Helper()
		local := g2RatingWork(t, 611, "g2-head-q-"+tag, "g2-obs-q-"+tag, economics.BasisLocalExpected)
		provider := g2RatingWork(t, 614, "g2-head-q-p-"+tag, "g2-obs-q-p-"+tag, economics.BasisProviderQuantityLocal)
		store := newEconomicJobRunnerTestStore()
		lines := []economics.LineItem{
			g2Line(t, "q-a-"+tag, key, quantities[0], g2Nano(1)),
			g2Line(t, "q-b-"+tag, key, quantities[1], g2Nano(1)),
			g2Line(t, "q-c-"+tag, key, quantities[2], g2Nano(1)),
		}
		g2SeedValuation(t, store, local, lines)
		g2SeedValuation(t, store, provider, []economics.LineItem{g2Line(t, "q-provider-"+tag, key, "100", nil)})
		var recon EconomicRevisionWork
		if depReversed {
			recon = g2ReconWork(t, "q-"+tag, g2Dependency(t, provider), g2Dependency(t, local))
		} else {
			recon = g2ReconWork(t, "q-"+tag, g2Dependency(t, local), g2Dependency(t, provider))
		}
		return g2RunReconciliation(t, store, recon)
	}
	forward, forwardRaw := runQuantityConflict(t, "conf", []string{"100", "100", "110"}, false)
	reverse, reverseRaw := runQuantityConflict(t, "conf", []string{"100", "100", "110"}, true)
	require.Equal(t, "conflict", forward.Status)
	require.False(t, forward.Complete)
	require.Equal(t, "conflict", forward.Items[0].Status)
	require.Nil(t, forward.Items[0].LocalQuantity, "quantity conflict must not expose a local quantity")
	require.Nil(t, forward.Items[0].LocalAmountNano, "quantity conflict must not expose a local amount")
	require.Equal(t, forwardRaw, reverseRaw, "quantity conflict must stay order-independent")
	require.Equal(t, forward.Status, reverse.Status)
}

func TestPhase172G2AmountNilVsPresentFollowsMergeSemanticsThroughRunner(t *testing.T) {
	t.Parallel()
	matched, matchedRaw := g2SingleValuationJSON(t, "nil-present-match", []*int64{nil, g2Nano(1), g2Nano(1)}, false)
	matchedRev, matchedRevRaw := g2SingleValuationJSON(t, "nil-present-match", []*int64{nil, g2Nano(1), g2Nano(1)}, true)
	require.Equal(t, "matched", matched.Status, "nil amount with equal present amounts must not conflict")
	require.True(t, matched.Complete)
	require.Len(t, matched.Items, 1)
	require.Equal(t, "matched", matched.Items[0].Status)
	require.NotNil(t, matched.Items[0].LocalQuantity)
	require.Equal(t, "100", *matched.Items[0].LocalQuantity)
	require.Equal(t, matchedRaw, matchedRevRaw, "nil-vs-present match must be order-independent")
	require.Equal(t, matched.Status, matchedRev.Status)

	conflicted, conflictedRaw := g2SingleValuationJSON(t, "nil-present-conflict", []*int64{nil, g2Nano(1), g2Nano(2)}, false)
	conflictedRev, conflictedRevRaw := g2SingleValuationJSON(t, "nil-present-conflict", []*int64{nil, g2Nano(1), g2Nano(2)}, true)
	require.Equal(t, "conflict", conflicted.Status, "nil must not mask a conflict between two present differing amounts")
	require.False(t, conflicted.Complete)
	require.Nil(t, conflicted.Items[0].LocalQuantity, "nil-mixed amount conflict must canonicalize like every other amount conflict")
	require.Nil(t, conflicted.Items[0].LocalAmountNano)
	require.Equal(t, conflictedRaw, conflictedRevRaw, "nil-mixed amount conflict must be order-independent")
	require.Equal(t, conflicted.Status, conflictedRev.Status)
}
