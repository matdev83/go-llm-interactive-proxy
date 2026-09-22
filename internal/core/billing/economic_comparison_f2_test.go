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

// F2: missing-then-known duplicate component panics and order-dependent merge.
// These runner-level tests prove the ComponentComparisonReconciler plane merge
// is total, nil-safe, and order-independent. Two local dependencies sharing
// one component must retain explicit missing/conflict evidence regardless of
// encounter order, never panic on nil.Cmp, and never let later known data
// silently erase missing evidence.

func f2ComponentKey() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		SchemaID:  metering.DefaultInclusionSchemaID,
	}
}

func f2Decimal(t *testing.T, value string) *metering.Decimal {
	t.Helper()
	decimal, err := metering.ParseDecimal(value)
	require.NoError(t, err)
	return &decimal
}

func f2LineKnown(t *testing.T, id string, key metering.ComponentKey, quantity string) economics.LineItem {
	t.Helper()
	component := key.Clone()
	return economics.LineItem{
		ID:        "line-" + id,
		RuleID:    "rule-f2",
		ItemID:    "item-" + id,
		Component: &component,
		Quantity:  f2Decimal(t, quantity),
		Unit:      component.Unit,
		Status:    economics.RatingLineRated,
	}
}

func f2LineMissing(t *testing.T, id string, key metering.ComponentKey) economics.LineItem {
	t.Helper()
	component := key.Clone()
	return economics.LineItem{
		ID:        "line-" + id,
		RuleID:    "rule-f2",
		ItemID:    "item-" + id,
		Component: &component,
		Quantity:  nil,
		Unit:      component.Unit,
		Status:    economics.RatingLineQuantityIncomplete,
	}
}

func f2RatingWork(t *testing.T, revision uint64, headKey, obsID string, basis economics.ValuationBasis) EconomicRevisionWork {
	t.Helper()
	key := f2ComponentKey()
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
		CreatedAt:        time.Unix(1_700_300_000+int64(revision), 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

func f2Dependency(t *testing.T, work EconomicRevisionWork) EconomicJobDependency {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	dependency, err := NewEconomicJobDependency(EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	return dependency
}

func f2SeedValuation(t *testing.T, store *economicJobRunnerTestStore, work EconomicRevisionWork, lines []economics.LineItem) {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	// Valuations with unavailable quantities are semantically partial; known
	// quantities are semantically complete. Seeding missing evidence as
	// complete would misrepresent the fixture.
	completeness := economics.CompletenessComplete
	for _, line := range lines {
		if line.Quantity == nil {
			completeness = economics.CompletenessPartial
			break
		}
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
	store.mu.Lock()
	store.valuations[identity.ValuationKey()] = valuation
	store.mu.Unlock()
}

func f2ReconWork(t *testing.T, deps ...EconomicJobDependency) EconomicRevisionWork {
	t.Helper()
	// Reuse the shared phase9 subject so every dependency valuation joins.
	// Basis is provider-reported for the envelope only; planes come from deps.
	probe := phase9Observation(t, "f2-recon-obs", metering.OriginProvider, f2ComponentKey(), "1")
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{probe})
	// Align scope/subject with rating works (phase9 uses call:phase9 + store/a/call/b).
	work := EconomicRevisionWork{
		Queue:            EconomicQueueProvider,
		Kind:             EconomicWorkKindReconciliation,
		HeadKey:          "f2-recon-head",
		Subject:          input.Subject,
		EvidenceRevision: 99,
		Input:            input,
		Dependencies:     deps,
		CreatedAt:        time.Unix(1_700_399_000, 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

type f2EnvelopeItem struct {
	Component        string  `json:"component"`
	Status           string  `json:"status"`
	Reason           string  `json:"reason"`
	LocalQuantity    *string `json:"local_quantity"`
	ProviderQuantity *string `json:"provider_quantity"`
}

type f2Envelope struct {
	Status   string           `json:"status"`
	Complete bool             `json:"complete"`
	Reason   string           `json:"reason"`
	Items    []f2EnvelopeItem `json:"items"`
}

func f2RunReconciliation(t *testing.T, store *economicJobRunnerTestStore, reconWork EconomicRevisionWork) (EconomicJobRunSummary, f2Envelope) {
	t.Helper()
	store.appendWork(t, reconWork)
	runner := newEconomicJobRunnerTest(t, EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: store, Dependencies: store, Reconciliations: store,
		Rater: &economicJobRunnerTestRater{}, Reconciler: ComponentComparisonReconciler{},
		Owner: "f2-test", Batch: 8,
	})
	ctx := context.Background()
	var summary EconomicJobRunSummary
	var runErr error
	require.NotPanics(t, func() {
		summary, runErr = runner.RunOnce(ctx, EconomicQueueProvider)
	}, "reconciliation must never panic on duplicate components")
	require.NoError(t, runErr)
	require.Equal(t, 1, summary.Completed, "reconciliation work must complete, not retry/fail")
	require.Zero(t, summary.Retried+summary.Failed)
	identity, err := reconWork.Identity()
	require.NoError(t, err)
	store.mu.Lock()
	record, ok := store.reconciliations[identity.Key()]
	store.mu.Unlock()
	require.True(t, ok, "reconciliation must be persisted")
	var envelope f2Envelope
	require.NoError(t, json.Unmarshal(record.ResultJSON, &envelope))
	return summary, envelope
}

func f2ItemByComponent(t *testing.T, envelope f2Envelope, key metering.ComponentKey) f2EnvelopeItem {
	t.Helper()
	want := key.CanonicalKey()
	for _, item := range envelope.Items {
		if item.Component == want {
			return item
		}
	}
	t.Fatalf("component %s absent from comparison", want)
	return f2EnvelopeItem{}
}

func TestPhase172F2MissingThenKnownIsPartialThroughRunner(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	key := f2ComponentKey()
	localMissing := f2RatingWork(t, 81, "f2-head-local-missing", "f2-obs-local-missing", economics.BasisLocalExpected)
	localKnown := f2RatingWork(t, 82, "f2-head-local-known", "f2-obs-local-known", economics.BasisLocalExpected)
	provider := f2RatingWork(t, 83, "f2-head-provider", "f2-obs-provider", economics.BasisProviderQuantityLocal)
	f2SeedValuation(t, store, localMissing, []economics.LineItem{f2LineMissing(t, "missing", key)})
	f2SeedValuation(t, store, localKnown, []economics.LineItem{f2LineKnown(t, "known", key, "100")})
	f2SeedValuation(t, store, provider, []economics.LineItem{f2LineKnown(t, "provider", key, "100")})
	recon := f2ReconWork(t, f2Dependency(t, localMissing), f2Dependency(t, localKnown), f2Dependency(t, provider))
	_, envelope := f2RunReconciliation(t, store, recon)
	require.Equal(t, "partial", envelope.Status)
	require.False(t, envelope.Complete)
	require.Len(t, envelope.Items, 1)
	item := f2ItemByComponent(t, envelope, key)
	require.Equal(t, "partial", item.Status)
	require.Nil(t, item.LocalQuantity, "missing local evidence must not expose a quantity")
	require.NotNil(t, item.ProviderQuantity, "provider quantity stays visible alongside missing local evidence")
	require.Equal(t, "100", *item.ProviderQuantity)
}

func TestPhase172F2KnownThenMissingIsPartialThroughRunner(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	key := f2ComponentKey()
	localKnown := f2RatingWork(t, 81, "f2-head-local-known", "f2-obs-local-known", economics.BasisLocalExpected)
	localMissing := f2RatingWork(t, 82, "f2-head-local-missing", "f2-obs-local-missing", economics.BasisLocalExpected)
	provider := f2RatingWork(t, 83, "f2-head-provider", "f2-obs-provider", economics.BasisProviderQuantityLocal)
	f2SeedValuation(t, store, localKnown, []economics.LineItem{f2LineKnown(t, "known", key, "100")})
	f2SeedValuation(t, store, localMissing, []economics.LineItem{f2LineMissing(t, "missing", key)})
	f2SeedValuation(t, store, provider, []economics.LineItem{f2LineKnown(t, "provider", key, "100")})
	recon := f2ReconWork(t, f2Dependency(t, localKnown), f2Dependency(t, localMissing), f2Dependency(t, provider))
	_, envelope := f2RunReconciliation(t, store, recon)
	require.Equal(t, "partial", envelope.Status)
	require.False(t, envelope.Complete)
	require.Len(t, envelope.Items, 1)
	item := f2ItemByComponent(t, envelope, key)
	require.Equal(t, "partial", item.Status)
	require.Nil(t, item.LocalQuantity)
	require.NotNil(t, item.ProviderQuantity)
	require.Equal(t, "100", *item.ProviderQuantity)
}

func TestPhase172F2MissingMissingIsPartialThroughRunner(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	key := f2ComponentKey()
	localMissingA := f2RatingWork(t, 81, "f2-head-local-missing-a", "f2-obs-local-missing-a", economics.BasisLocalExpected)
	localMissingB := f2RatingWork(t, 82, "f2-head-local-missing-b", "f2-obs-local-missing-b", economics.BasisLocalExpected)
	provider := f2RatingWork(t, 83, "f2-head-provider", "f2-obs-provider", economics.BasisProviderQuantityLocal)
	f2SeedValuation(t, store, localMissingA, []economics.LineItem{f2LineMissing(t, "missing-a", key)})
	f2SeedValuation(t, store, localMissingB, []economics.LineItem{f2LineMissing(t, "missing-b", key)})
	f2SeedValuation(t, store, provider, []economics.LineItem{f2LineKnown(t, "provider", key, "100")})
	recon := f2ReconWork(t, f2Dependency(t, localMissingA), f2Dependency(t, localMissingB), f2Dependency(t, provider))
	_, envelope := f2RunReconciliation(t, store, recon)
	require.Equal(t, "partial", envelope.Status)
	require.False(t, envelope.Complete)
	item := f2ItemByComponent(t, envelope, key)
	require.Equal(t, "partial", item.Status)
	require.Nil(t, item.LocalQuantity)
	require.NotNil(t, item.ProviderQuantity)
	require.Equal(t, "100", *item.ProviderQuantity)
}

func TestPhase172F2EqualKnownDuplicatesMatchThroughRunner(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	key := f2ComponentKey()
	localA := f2RatingWork(t, 81, "f2-head-local-a", "f2-obs-local-a", economics.BasisLocalExpected)
	localB := f2RatingWork(t, 82, "f2-head-local-b", "f2-obs-local-b", economics.BasisLocalExpected)
	provider := f2RatingWork(t, 83, "f2-head-provider", "f2-obs-provider", economics.BasisProviderQuantityLocal)
	f2SeedValuation(t, store, localA, []economics.LineItem{f2LineKnown(t, "a", key, "100")})
	f2SeedValuation(t, store, localB, []economics.LineItem{f2LineKnown(t, "b", key, "100")})
	f2SeedValuation(t, store, provider, []economics.LineItem{f2LineKnown(t, "provider", key, "100")})
	recon := f2ReconWork(t, f2Dependency(t, localA), f2Dependency(t, localB), f2Dependency(t, provider))
	_, envelope := f2RunReconciliation(t, store, recon)
	require.Equal(t, "matched", envelope.Status)
	require.True(t, envelope.Complete)
	require.Len(t, envelope.Items, 1)
	item := f2ItemByComponent(t, envelope, key)
	require.Equal(t, "matched", item.Status)
	require.NotNil(t, item.LocalQuantity)
	require.NotNil(t, item.ProviderQuantity)
	require.Equal(t, "100", *item.LocalQuantity)
	require.Equal(t, "100", *item.ProviderQuantity)
}

func TestPhase172F2ConflictingKnownDuplicatesConflictThroughRunner(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	key := f2ComponentKey()
	localA := f2RatingWork(t, 81, "f2-head-local-a", "f2-obs-local-a", economics.BasisLocalExpected)
	localB := f2RatingWork(t, 82, "f2-head-local-b", "f2-obs-local-b", economics.BasisLocalExpected)
	provider := f2RatingWork(t, 83, "f2-head-provider", "f2-obs-provider", economics.BasisProviderQuantityLocal)
	f2SeedValuation(t, store, localA, []economics.LineItem{f2LineKnown(t, "a", key, "100")})
	f2SeedValuation(t, store, localB, []economics.LineItem{f2LineKnown(t, "b", key, "110")})
	f2SeedValuation(t, store, provider, []economics.LineItem{f2LineKnown(t, "provider", key, "100")})
	recon := f2ReconWork(t, f2Dependency(t, localA), f2Dependency(t, localB), f2Dependency(t, provider))
	_, envelope := f2RunReconciliation(t, store, recon)
	require.Equal(t, "conflict", envelope.Status)
	require.False(t, envelope.Complete)
	require.Len(t, envelope.Items, 1)
	item := f2ItemByComponent(t, envelope, key)
	require.Equal(t, "conflict", item.Status)
}

func TestPhase172F2DuplicateMergeIsOrderIndependentThroughRunner(t *testing.T) {
	t.Parallel()
	key := f2ComponentKey()
	runScenario := func(t *testing.T, localFirstLines, localSecondLines []economics.LineItem, providerLines []economics.LineItem) f2Envelope {
		t.Helper()
		store := newEconomicJobRunnerTestStore()
		localFirst := f2RatingWork(t, 81, "f2-head-local-first", "f2-obs-local-first", economics.BasisLocalExpected)
		localSecond := f2RatingWork(t, 82, "f2-head-local-second", "f2-obs-local-second", economics.BasisLocalExpected)
		provider := f2RatingWork(t, 83, "f2-head-provider", "f2-obs-provider", economics.BasisProviderQuantityLocal)
		f2SeedValuation(t, store, localFirst, localFirstLines)
		f2SeedValuation(t, store, localSecond, localSecondLines)
		f2SeedValuation(t, store, provider, providerLines)
		recon := f2ReconWork(t, f2Dependency(t, localFirst), f2Dependency(t, localSecond), f2Dependency(t, provider))
		_, envelope := f2RunReconciliation(t, store, recon)
		return envelope
	}
	t.Run("missing-known order", func(t *testing.T) {
		t.Parallel()
		forward := runScenario(t,
			[]economics.LineItem{f2LineMissing(t, "missing", key)},
			[]economics.LineItem{f2LineKnown(t, "known", key, "100")},
			[]economics.LineItem{f2LineKnown(t, "provider", key, "100")})
		reverse := runScenario(t,
			[]economics.LineItem{f2LineKnown(t, "known", key, "100")},
			[]economics.LineItem{f2LineMissing(t, "missing", key)},
			[]economics.LineItem{f2LineKnown(t, "provider", key, "100")})
		// Same dependency identities carry swapped payloads; the canonical
		// envelope must be identical. Before the fix the forward order panics
		// on nil.Cmp while the reverse order returns partial.
		require.Equal(t, forward.Status, reverse.Status)
		require.Equal(t, forward.Complete, reverse.Complete)
		forwardJSON, err := json.Marshal(forward)
		require.NoError(t, err)
		reverseJSON, err := json.Marshal(reverse)
		require.NoError(t, err)
		require.JSONEq(t, string(forwardJSON), string(reverseJSON))
		require.Equal(t, "partial", forward.Status)
	})
	t.Run("equal-known order", func(t *testing.T) {
		t.Parallel()
		forward := runScenario(t,
			[]economics.LineItem{f2LineKnown(t, "a", key, "100")},
			[]economics.LineItem{f2LineKnown(t, "b", key, "100")},
			[]economics.LineItem{f2LineKnown(t, "provider", key, "100")})
		reverse := runScenario(t,
			[]economics.LineItem{f2LineKnown(t, "b", key, "100")},
			[]economics.LineItem{f2LineKnown(t, "a", key, "100")},
			[]economics.LineItem{f2LineKnown(t, "provider", key, "100")})
		require.Equal(t, "matched", forward.Status)
		forwardJSON, err := json.Marshal(forward)
		require.NoError(t, err)
		reverseJSON, err := json.Marshal(reverse)
		require.NoError(t, err)
		require.JSONEq(t, string(forwardJSON), string(reverseJSON))
	})
	t.Run("conflicting-known order", func(t *testing.T) {
		t.Parallel()
		forward := runScenario(t,
			[]economics.LineItem{f2LineKnown(t, "a", key, "100")},
			[]economics.LineItem{f2LineKnown(t, "b", key, "110")},
			[]economics.LineItem{f2LineKnown(t, "provider", key, "100")})
		reverse := runScenario(t,
			[]economics.LineItem{f2LineKnown(t, "b", key, "110")},
			[]economics.LineItem{f2LineKnown(t, "a", key, "100")},
			[]economics.LineItem{f2LineKnown(t, "provider", key, "100")})
		require.Equal(t, "conflict", forward.Status)
		forwardJSON, err := json.Marshal(forward)
		require.NoError(t, err)
		reverseJSON, err := json.Marshal(reverse)
		require.NoError(t, err)
		require.JSONEq(t, string(forwardJSON), string(reverseJSON))
	})
}
