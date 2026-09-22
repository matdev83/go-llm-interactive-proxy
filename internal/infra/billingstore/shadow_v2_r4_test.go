package billingstore

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

type r4ErrRater struct{}

func (r4ErrRater) Rate(context.Context, economics.PostUsageRatingInput) (economics.Valuation, error) {
	return economics.Valuation{}, errors.New("r4: rater must not run for already-persisted revisions")
}

type r4CountingReconciler struct {
	billing.ComponentComparisonReconciler
	calls *atomic.Int64
}

func (r r4CountingReconciler) ReconcileJob(ctx context.Context, work billing.EconomicRevisionWork, outputs []billing.EconomicJobDependencyOutput) (*billing.EconomicReconciliation, error) {
	r.calls.Add(1)
	return r.ComponentComparisonReconciler.ReconcileJob(ctx, work, outputs)
}

func r4TokenKey() metering.ComponentKey {
	return metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
}

func r4OutputKey() metering.ComponentKey {
	return metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
}

func r4Observation(t *testing.T, obsID string, revision uint64, origin, acquisition, tokens string) metering.Observation {
	t.Helper()
	qty, err := metering.ParseDecimal(tokens)
	require.NoError(t, err)
	now := time.Unix(1_700_200_000, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: obsID, SourceEventKey: obsID, Revision: revision,
		StreamID: "r4-stream", Sequence: revision,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "test", TenantID: "tenant-r4",
			AccountID: "r4-acct", ALegID: "a-r4", BillingCallID: "call-r4",
			BLegID: "b-r4", AttemptID: "attempt-r4", AttemptSeq: revision,
		},
		Correlation: metering.CorrelationV2{
			StoreID: "test", TenantID: "tenant-r4", CallID: "call-r4",
			BillingCallID: "call-r4", ALegID: "a-r4", BLegID: "b-r4",
			AttemptID: "attempt-r4", AttemptSeq: revision,
		},
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now,
		MappingRef: "r4:v1",
		Measures: []metering.Measure{{
			Key:     r4TokenKey(),
			Value:   &qty,
			Quality: metering.QualityObserved,
		}},
	}
}

func r4Work(t *testing.T, observation metering.Observation, basis economics.ValuationBasis, headKey string, revision uint64) billing.EconomicRevisionWork {
	t.Helper()
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: basis,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r4-rater", Version: "v1"}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "catalog://r4-rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff:               economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r4-tariff", Version: "v1"}, RaterID: "reference"},
		TariffContent:        &economics.SnapshotContentRef{ContentRef: "catalog://r4-tariff/v1", ContentHash: strings.Repeat("2", 64)},
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://r4-qualifiers/v1", ContentHash: strings.Repeat("4", 64)},
		AsOf:                 time.Unix(100, 0).UTC(),
	}
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: headKey,
		Subject: observation.Subject, EvidenceRevision: revision, Input: input,
		CreatedAt: time.Unix(1_700_201_000+int64(revision), 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

func r4Line(t *testing.T, id, tokens string, key metering.ComponentKey) economics.LineItem {
	t.Helper()
	quantity, err := metering.ParseDecimal(tokens)
	require.NoError(t, err)
	component := key.Clone()
	qty, err := strconv.ParseInt(tokens, 10, 64)
	require.NoError(t, err)
	amountNano := qty * 1_000_000
	amount := metering.Decimal{Coefficient: strconv.FormatInt(amountNano, 10), Scale: 9}
	unitPrice := metering.Decimal{Coefficient: "1", Scale: 3}
	rounded := economics.Money{NanoUnits: amountNano, Currency: "USD", Present: true}
	return economics.LineItem{
		ID: "line-" + id, RuleID: "rule-r4", ItemID: "item-" + id,
		Component: &component, Quantity: &quantity, Unit: component.Unit,
		UnitPrice: &unitPrice, Amount: &amount, RoundedAmount: &rounded,
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingTowardZero,
		Status: economics.RatingLineRated,
	}
}

func r4SeedValuation(t *testing.T, ctx context.Context, store *DurableStore, work billing.EconomicRevisionWork, lines []economics.LineItem) economics.Valuation {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	var totalNano int64
	for _, line := range lines {
		require.True(t, line.RoundedAmount != nil && line.RoundedAmount.Present)
		totalNano += line.RoundedAmount.NanoUnits
	}
	totalAmount := metering.Decimal{Coefficient: strconv.FormatInt(totalNano, 10), Scale: 9}
	valuation := economics.Valuation{
		ID: identity.ValuationKey(), Version: economics.ValuationVersionV2,
		Perspective: normalized.Input.Perspective, Basis: normalized.Input.Basis,
		Subject: normalized.Subject, Scope: normalized.Input.Scope,
		InputObservations:    append([]metering.ObservationRef(nil), normalized.Input.ObservationRefs...),
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r4-rater", Version: "v1"}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "catalog://r4-rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff:               economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "r4-tariff", Version: "v1"}, RaterID: "reference"},
		TariffContent:        &economics.SnapshotContentRef{ContentRef: "catalog://r4-tariff/v1", ContentHash: strings.Repeat("2", 64)},
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://r4-qualifiers/v1", ContentHash: strings.Repeat("4", 64)},
		Lines:                append([]economics.LineItem(nil), lines...),
		Totals: []economics.CurrencyTotal{{
			Currency:      "USD",
			Amount:        &totalAmount,
			RoundedAmount: economics.Money{NanoUnits: totalNano, Currency: "USD", Present: true},
		}},
		Completeness: economics.CompletenessComplete,
		CreatedAt:    normalized.CreatedAt,
	}
	require.NoError(t, store.AppendEconomicRevisionResult(ctx, normalized, billing.EconomicRevisionResult{Valuation: valuation}))
	loaded, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	return loaded
}

func r4SeedEQ(t *testing.T, ctx context.Context, store *DurableStore) (eWork, qWork billing.EconomicRevisionWork, eID, qID string) {
	t.Helper()
	// Both planes describe the same B-leg attempt so their subjects join;
	// only source identity and measures differ between E and Q.
	eObs := r4Observation(t, "r4-e-obs", 51, metering.OriginLocal, metering.AcquisitionLocalTransport, "100")
	eWork = r4Work(t, eObs, economics.BasisLocalExpected, "r4-head-e", 51)
	qObs := r4Observation(t, "r4-q-obs", 51, metering.OriginProvider, metering.AcquisitionProviderResponse, "110")
	qWork = r4Work(t, qObs, economics.BasisProviderQuantityLocal, "r4-head-q", 52)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, eWork))
	eVal := r4SeedValuation(t, ctx, store, eWork, []economics.LineItem{r4Line(t, "e-input", "100", r4TokenKey())})
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, qWork))
	qVal := r4SeedValuation(t, ctx, store, qWork, []economics.LineItem{
		r4Line(t, "q-input", "110", r4TokenKey()),
	})
	return eWork, qWork, eVal.ID, qVal.ID
}

func mustR4Decimal(t *testing.T, value string) *metering.Decimal {
	t.Helper()
	decimal, err := metering.ParseDecimal(value)
	require.NoError(t, err)
	return &decimal
}

func r4ReconWork(t *testing.T, deps ...billing.EconomicJobDependency) billing.EconomicRevisionWork {
	t.Helper()
	observation := r4Observation(t, "r4-recon-evidence", 53, metering.OriginProvider, metering.AcquisitionProviderResponse, "1")
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, Kind: billing.EconomicWorkKindReconciliation,
		HeadKey: "r4-recon-head", Subject: observation.Subject,
		EvidenceRevision: 53, Input: input, Dependencies: deps,
		CreatedAt: time.Unix(1_700_202_000, 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

func r4Runner(t *testing.T, store *DurableStore) (*billing.EconomicJobRunner, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	counting := r4CountingReconciler{ComponentComparisonReconciler: billing.ComponentComparisonReconciler{}, calls: &calls}
	runner, err := billing.NewEconomicJobRunner(billing.EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: store, Dependencies: store, Reconciliations: store,
		Rater: r4ErrRater{}, Reconciler: counting,
		Owner: "r4-test", Batch: 8,
	})
	require.NoError(t, err)
	return runner, &calls
}

func r4Dependency(t *testing.T, work billing.EconomicRevisionWork) billing.EconomicJobDependency {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	dependency, err := billing.NewEconomicJobDependency(billing.EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	return dependency
}

type r4ComparisonEnvelope struct {
	Status   string `json:"status"`
	Complete bool   `json:"complete"`
	Reason   string `json:"reason"`
	Items    []struct {
		Component        string  `json:"component"`
		Status           string  `json:"status"`
		Reason           string  `json:"reason"`
		LocalQuantity    *string `json:"local_quantity"`
		ProviderQuantity *string `json:"provider_quantity"`
		SignedDelta      *string `json:"signed_delta"`
		AbsoluteDelta    *string `json:"absolute_delta"`
	} `json:"items"`
}

func r4ReadEnvelope(t *testing.T, ctx context.Context, store *DurableStore, work billing.EconomicRevisionWork) r4ComparisonEnvelope {
	t.Helper()
	identity, err := work.Identity()
	require.NoError(t, err)
	record, err := store.GetReconciliation(ctx, identity.ReconciliationKey(), identity.EvidenceRevision)
	require.NoError(t, err)
	var envelope r4ComparisonEnvelope
	require.NoError(t, json.Unmarshal(record.ResultJSON, &envelope))
	return envelope
}

func r4ItemByComponent(t *testing.T, envelope r4ComparisonEnvelope, component metering.ComponentKey) struct {
	Component        string  `json:"component"`
	Status           string  `json:"status"`
	Reason           string  `json:"reason"`
	LocalQuantity    *string `json:"local_quantity"`
	ProviderQuantity *string `json:"provider_quantity"`
	SignedDelta      *string `json:"signed_delta"`
	AbsoluteDelta    *string `json:"absolute_delta"`
} {
	t.Helper()
	for _, item := range envelope.Items {
		if item.Component == component.CanonicalKey() {
			return item
		}
	}
	t.Fatalf("component %s absent from comparison", component.CanonicalKey())
	return envelope.Items[0]
}

func TestPhase172R4RunnerComputesDiscrepancy(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	eWork, qWork, _, _ := r4SeedEQ(t, ctx, store)
	reconWork := r4ReconWork(t, r4Dependency(t, eWork), r4Dependency(t, qWork))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconWork))

	runner, calls := r4Runner(t, store)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 3, summary.Completed)
	require.Equal(t, 0, summary.Failed+summary.Retried)
	require.Equal(t, int64(1), calls.Load(), "exactly one reconciliation computation for one recon claim")

	envelope := r4ReadEnvelope(t, ctx, store, reconWork)
	require.Equal(t, "discrepant", envelope.Status)
	require.True(t, envelope.Complete, "a proven discrepancy is complete evidence")
	require.Len(t, envelope.Items, 1)
	inputItem := r4ItemByComponent(t, envelope, r4TokenKey())
	require.Equal(t, "discrepant", inputItem.Status)
	require.Equal(t, "100", stringValue(t, inputItem.LocalQuantity))
	require.Equal(t, "110", stringValue(t, inputItem.ProviderQuantity))
	require.Equal(t, "10", stringValue(t, inputItem.SignedDelta))
	require.Equal(t, "10", stringValue(t, inputItem.AbsoluteDelta))
}

func TestPhase172R4ComparisonUsesEveryDependency(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	eObs := r4Observation(t, "r4-multi-e-obs", 61, metering.OriginLocal, metering.AcquisitionLocalTransport, "100")
	eWork := r4Work(t, eObs, economics.BasisLocalExpected, "r4-head-multi-e", 61)
	qObs := r4Observation(t, "r4-multi-q-obs", 61, metering.OriginProvider, metering.AcquisitionProviderResponse, "110")
	qObs.Measures = append(qObs.Measures, metering.Measure{
		Key:   r4OutputKey(),
		Value: mustR4Decimal(t, "5"), Quality: metering.QualityObserved,
	})
	qWork := r4Work(t, qObs, economics.BasisProviderQuantityLocal, "r4-head-multi-q", 62)
	for _, seeded := range []struct {
		work  billing.EconomicRevisionWork
		lines []economics.LineItem
	}{
		{eWork, []economics.LineItem{r4Line(t, "multi-e-input", "100", r4TokenKey())}},
		{qWork, []economics.LineItem{r4Line(t, "multi-q-input", "110", r4TokenKey()), r4Line(t, "multi-q-output", "5", r4OutputKey())}},
	} {
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, seeded.work))
		r4SeedValuation(t, ctx, store, seeded.work, seeded.lines)
	}
	reconWork := r4ReconWork(t, r4Dependency(t, eWork), r4Dependency(t, qWork))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconWork))

	runner, _ := r4Runner(t, store)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 3, summary.Completed)

	// A last-write-wins marshal would keep only the second dependency: the
	// joined input-token item and the Q-only output item prove both planes
	// were consumed. Production precedence reports the incomplete picture as
	// partial rather than masking it behind the proven discrepancy.
	envelope := r4ReadEnvelope(t, ctx, store, reconWork)
	require.Equal(t, "partial", envelope.Status)
	require.False(t, envelope.Complete)
	require.Len(t, envelope.Items, 2)
	inputItem := r4ItemByComponent(t, envelope, r4TokenKey())
	require.Equal(t, "discrepant", inputItem.Status)
	require.Equal(t, "100", stringValue(t, inputItem.LocalQuantity))
	require.Equal(t, "110", stringValue(t, inputItem.ProviderQuantity))
	outputItem := r4ItemByComponent(t, envelope, r4OutputKey())
	require.Equal(t, "missing_local", outputItem.Status)
	require.Nil(t, outputItem.LocalQuantity)
	require.Equal(t, "5", stringValue(t, outputItem.ProviderQuantity))
}

func stringValue(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}

func TestPhase172R4RunnerMatchOutcome(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	// Agreement must span both planes: a local-only pair could never report
	// anything but missing evidence.
	localObs := r4Observation(t, "r4-match-e", 54, metering.OriginLocal, metering.AcquisitionLocalTransport, "100")
	localWork := r4Work(t, localObs, economics.BasisLocalExpected, "r4-head-match-e", 54)
	providerObs := r4Observation(t, "r4-match-q", 54, metering.OriginProvider, metering.AcquisitionProviderResponse, "100")
	providerWork := r4Work(t, providerObs, economics.BasisProviderQuantityLocal, "r4-head-match-q", 55)
	for _, seeded := range []struct {
		work  billing.EconomicRevisionWork
		lines []economics.LineItem
	}{
		{localWork, []economics.LineItem{r4Line(t, "match-e-input", "100", r4TokenKey())}},
		{providerWork, []economics.LineItem{r4Line(t, "match-q-input", "100", r4TokenKey())}},
	} {
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, seeded.work))
		r4SeedValuation(t, ctx, store, seeded.work, seeded.lines)
	}
	reconWork := r4ReconWork(t, r4Dependency(t, localWork), r4Dependency(t, providerWork))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconWork))

	runner, _ := r4Runner(t, store)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 3, summary.Completed)

	envelope := r4ReadEnvelope(t, ctx, store, reconWork)
	require.Equal(t, "matched", envelope.Status)
	require.True(t, envelope.Complete)
	require.Len(t, envelope.Items, 1)
	require.Equal(t, "matched", envelope.Items[0].Status)
	require.Equal(t, "100", stringValue(t, envelope.Items[0].LocalQuantity))
	require.Equal(t, "100", stringValue(t, envelope.Items[0].ProviderQuantity))
}

func TestPhase172R4SubjectMismatchIsIncomparable(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	eWork, _, _, _ := r4SeedEQ(t, ctx, store)
	otherObs := r4Observation(t, "r4-other-obs", 51, metering.OriginProvider, metering.AcquisitionProviderResponse, "110")
	otherObs.Subject.BLegID = "b-r4-other"
	otherObs.Correlation.BLegID = "b-r4-other"
	otherWork := r4Work(t, otherObs, economics.BasisProviderQuantityLocal, "r4-head-other", 57)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, otherWork))
	r4SeedValuation(t, ctx, store, otherWork, []economics.LineItem{r4Line(t, "other-input", "110", r4TokenKey())})
	reconWork := r4ReconWork(t, r4Dependency(t, eWork), r4Dependency(t, otherWork))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconWork))

	runner, calls := r4Runner(t, store)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 4, summary.Completed)
	require.Equal(t, int64(1), calls.Load())
	envelope := r4ReadEnvelope(t, ctx, store, reconWork)
	require.Equal(t, "incomparable", envelope.Status)
	require.False(t, envelope.Complete)
	require.Equal(t, "subject_mismatch", envelope.Reason)
}

func TestPhase172R4MissingDependencyDefers(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	ghostObs := r4Observation(t, "r4-ghost-obs", 56, metering.OriginProvider, metering.AcquisitionProviderResponse, "7")
	ghost := r4Work(t, ghostObs, economics.BasisProviderQuantityLocal, "r4-head-ghost", 56)
	reconWork := r4ReconWork(t, r4Dependency(t, ghost))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconWork))

	// Dependency-gated work is skipped at claim time without consuming an
	// attempt: nothing is computed and no envelope is invented.
	runner, calls := r4Runner(t, store)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 0, summary.Claimed)
	require.Equal(t, 0, summary.Completed)
	require.Equal(t, 0, summary.Retried)
	require.Equal(t, int64(0), calls.Load(), "no computation may run on unresolved inputs")
	identity, err := reconWork.Identity()
	require.NoError(t, err)
	probed, err := store.HasEconomicRevisionReconciliation(ctx, identity)
	require.NoError(t, err)
	require.False(t, probed)

	// Publishing the frozen output unblocks the same claim: the deferral was
	// about evidence availability, not a poisoned work item.
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, ghost))
	r4SeedValuation(t, ctx, store, ghost, []economics.LineItem{r4Line(t, "ghost-input", "7", r4TokenKey())})
	recovered, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 2, recovered.Completed)
	require.Equal(t, int64(1), calls.Load())
	probed, err = store.HasEconomicRevisionReconciliation(ctx, identity)
	require.NoError(t, err)
	require.True(t, probed)
}

func TestPhase172R4ForeignDependencyDefers(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	eWork, _, _, _ := r4SeedEQ(t, ctx, store)
	foreign := r4Dependency(t, eWork)
	foreign.InputSetHash = strings.Repeat("f", 64)
	reconWork := r4ReconWork(t, foreign)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconWork))

	// Content addressing resolves a tampered identity to no output: the claim
	// layer skips it exactly like a missing dependency, without inventing a
	// comparison or recording a failure. The seeded E/Q rating revisions still
	// complete normally around the skipped item.
	runner, calls := r4Runner(t, store)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 2, summary.Claimed)
	require.Equal(t, 2, summary.Completed)
	require.Equal(t, 0, summary.Retried)
	require.Equal(t, int64(0), calls.Load())
	identity, err := reconWork.Identity()
	require.NoError(t, err)
	probed, err := store.HasEconomicRevisionReconciliation(ctx, identity)
	require.NoError(t, err)
	require.False(t, probed)
}

func TestPhase172R4RerunIsIdempotent(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	eWork, qWork, _, _ := r4SeedEQ(t, ctx, store)
	reconWork := r4ReconWork(t, r4Dependency(t, eWork), r4Dependency(t, qWork))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconWork))

	runner, calls := r4Runner(t, store)
	first, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 3, first.Completed)
	require.Equal(t, int64(1), calls.Load())
	before := r4ReadEnvelope(t, ctx, store, reconWork)

	second, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 0, second.Claimed)
	require.Equal(t, int64(1), calls.Load(), "replay must not recompute a persisted comparison")
	after := r4ReadEnvelope(t, ctx, store, reconWork)
	require.Equal(t, before, after)
}

func TestPhase172R4ReopenStable(t *testing.T) {
	path := t.TempDir() + "/r4-reopen.sqlite"
	first, closeFirst := openRefinement82FileBillingStore(t, path, "test")
	ctx := context.Background()
	eWork, qWork, _, _ := r4SeedEQ(t, ctx, first)
	reconWork := r4ReconWork(t, r4Dependency(t, eWork), r4Dependency(t, qWork))
	require.NoError(t, first.AppendEconomicRevisionWork(ctx, reconWork))
	runner, _ := r4Runner(t, first)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 3, summary.Completed)
	before := r4ReadEnvelope(t, ctx, first, reconWork)
	closeFirst()

	second, closeSecond := openRefinement82FileBillingStore(t, path, "test")
	defer closeSecond()
	after := r4ReadEnvelope(t, ctx, second, reconWork)
	require.Equal(t, before, after)
	rerun, _ := r4Runner(t, second)
	rerunSummary, err := rerun.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 0, rerunSummary.Claimed)
}
