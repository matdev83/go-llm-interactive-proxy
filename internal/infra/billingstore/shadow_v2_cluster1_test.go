package billingstore

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type cluster1ProvenanceRater struct {
	allocations []economics.AllocationRef
}

func (r *cluster1ProvenanceRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for _, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return economics.Valuation{}, err
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		refs = append([]metering.ObservationRef(nil), input.ObservationRefs...)
	}
	return economics.Valuation{
		Perspective: input.Perspective, Basis: input.Basis, Subject: input.Subject,
		Scope: input.Scope, InputObservations: refs,
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), r.allocations...),
		Rater:                  economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rater-cluster1", Version: "v1"}, RaterID: "rater-cluster1"},
		Tariff:                 economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-cluster1", Version: "v1"}, RaterID: "tariff-cluster1"},
		Policy:                 economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-cluster1", Version: "v1"}, PolicyID: "policy-cluster1"},
		Payer:                  metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: input.Subject.AccountID},
		Completeness:           economics.CompletenessPartial,
		CreatedAt:              time.Unix(1_700_180_000, 0).UTC(),
	}, nil
}

func cluster1AllocationWork(t *testing.T, label string) (billing.EconomicRevisionWork, economics.AllocationRef) {
	t.Helper()
	observation := phase4EconomicsObservation("test", "cluster1-alloc-"+label, 11)
	alloc := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-cluster1-" + label, Version: 1, PayloadHash: strings.Repeat("c", 64)}
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
		AllocationCoverageRefs: []economics.AllocationRef{alloc},
	}
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: "cluster1-head-" + label,
		Subject: observation.Subject, EvidenceRevision: 11, Input: input,
		CreatedAt: time.Unix(1_700_180_100, 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized, alloc
}

func TestPhase172Cluster1AllocationProvenanceRetained(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	work, alloc := cluster1AllocationWork(t, "retain")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	rater := &cluster1ProvenanceRater{allocations: []economics.AllocationRef{alloc}}
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)
	require.NoError(t, capture.RateAndPersist(ctx, work))
	identity, err := work.Identity()
	require.NoError(t, err)
	got, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, []economics.AllocationRef{alloc}, got.AllocationCoverageRefs)
	require.Equal(t, "rater-cluster1", got.Rater.RaterID)
	require.Equal(t, "tariff-cluster1", got.Tariff.RaterID)
	require.Equal(t, "policy-cluster1", got.Policy.PolicyID)
	require.Equal(t, metering.PaymentPartyCustomer, got.Payer.Kind)
}

type cluster1ForeignRater struct {
	subject metering.SubjectRef
	basis   economics.ValuationBasis
}

func (r *cluster1ForeignRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for _, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return economics.Valuation{}, err
		}
		refs = append(refs, ref)
	}
	subject := r.subject
	if (subject == metering.SubjectRef{}) {
		subject = input.Subject
	}
	basis := r.basis
	if basis == "" {
		basis = input.Basis
	}
	return economics.Valuation{
		Perspective: input.Perspective, Basis: basis, Subject: subject,
		Scope: input.Scope, InputObservations: refs,
		Completeness: economics.CompletenessPartial,
		CreatedAt:    time.Unix(1_700_181_000, 0).UTC(),
	}, nil
}

func TestPhase172Cluster1ForeignRaterOutputRejected(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 12, "cluster1-foreign")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	normalized, err := work.Normalize()
	require.NoError(t, err)
	foreignSubject := normalized.Subject
	foreignSubject.BLegID = "b-foreign"
	rater := &cluster1ForeignRater{subject: foreignSubject}
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)
	err = capture.RateAndPersist(ctx, work)
	require.Error(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	_, getErr := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.Error(t, getErr)
}

func cluster1TwoObservationWork(t *testing.T, revision uint64, label string) billing.EconomicRevisionWork {
	t.Helper()
	first := phase4EconomicsObservation("test", "cluster1-bound-"+label+"-a", revision)
	second := phase4EconomicsObservation("test", "cluster1-bound-"+label+"-b", revision)
	second.ID = "cluster1-bound-" + label + "-b"
	second.SourceEventKey = "cluster1-bound-" + label + "-source-b"
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: first.Subject, Scope: "call", Observations: []metering.Observation{first, second},
	}
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: "cluster1-bound-head-" + label,
		Subject: first.Subject, EvidenceRevision: revision, Input: input,
		CreatedAt: time.Unix(1_700_182_000, 0).UTC(),
	}
	return work
}

func TestPhase172Cluster1EmbeddedBoundEnforcedEverywhere(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	rater := &cluster1ProvenanceRater{}
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 1},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)
	work := cluster1TwoObservationWork(t, 13, "embedded")
	require.Error(t, capture.AppendWork(ctx, work))
	require.Error(t, capture.RateAndPersist(ctx, work))
	reconWork := shadowV2ReconciliationWork(t, economicJobSQLiteWork(t, billing.EconomicQueueProvider, 13, "embedded-recon"))
	_ = reconWork
	require.Error(t, capture.AppendReconciliation(ctx, work, billing.EconomicReconciliation{}))
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_work WHERE store_id = ?`, "test").Scan(ctx, &count))
	require.Equal(t, 0, count)
}

func TestPhase172Cluster1RefOnlyBoundEnforced(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	rater := &cluster1ProvenanceRater{}
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 1},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)
	base := phase4EconomicsObservation("test", "cluster1-refonly", 14)
	refA, err := base.Ref("test")
	require.NoError(t, err)
	second := base
	second.ID = "cluster1-refonly-b"
	second.SourceEventKey = "cluster1-refonly-source-b"
	refB, err := second.Ref("test")
	require.NoError(t, err)
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: "cluster1-refonly-head",
		Subject: base.Subject, EvidenceRevision: 14,
		Input: economics.PostUsageRatingInput{
			Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
			Subject: base.Subject, Scope: "call", ObservationRefs: []metering.ObservationRef{refA, refB},
		},
		CreatedAt: time.Unix(1_700_183_000, 0).UTC(),
	}
	require.Error(t, capture.AppendWork(ctx, work))
	require.Error(t, capture.RateAndPersist(ctx, work))
}

func TestPhase172Cluster1CombinedBoundEnforced(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	rater := &cluster1ProvenanceRater{}
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 1},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)
	base := phase4EconomicsObservation("test", "cluster1-combined", 15)
	ref, err := base.Ref("test")
	require.NoError(t, err)
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: "cluster1-combined-head",
		Subject: base.Subject, EvidenceRevision: 15,
		Input: economics.PostUsageRatingInput{
			Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
			Subject: base.Subject, Scope: "call",
			Observations:    []metering.Observation{base},
			ObservationRefs: []metering.ObservationRef{ref},
		},
		CreatedAt: time.Unix(1_700_184_000, 0).UTC(),
	}
	require.Error(t, capture.AppendWork(ctx, work))
	require.Error(t, capture.RateAndPersist(ctx, work))
}

func TestPhase172Cluster1ForeignStoreLegRejected(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, stubShadowRater{},
	)
	require.NoError(t, err)
	callID := mustShadowCallID(t)
	foreign := phase4EconomicsObservation("foreign", "cluster1-foreign-obs", 16)
	foreign.Subject.ALegID = "a-shared"
	foreign.Subject.BLegID = "b-cluster1-foreign"
	foreign.Subject.BillingCallID = callID.String()
	foreign.Subject.CallID = callID.String()
	foreign.Correlation.ALegID = "a-shared"
	foreign.Correlation.BLegID = "b-cluster1-foreign"
	foreign.Correlation.BillingCallID = callID.String()
	foreign.Correlation.CallID = callID.String()
	require.Error(t, capture.CaptureObservations(ctx, []metering.Observation{foreign}))
}

type shadowNoProbeResultStore struct {
	store *DurableStore
}

func (s *shadowNoProbeResultStore) AppendEconomicRevisionResult(ctx context.Context, work billing.EconomicRevisionWork, result billing.EconomicRevisionResult) error {
	return s.store.AppendEconomicRevisionResult(ctx, work, result)
}

type cluster1WallClockRater struct {
	calls atomic.Int64
}

func (r *cluster1WallClockRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	n := r.calls.Add(1)
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for _, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return economics.Valuation{}, err
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		refs = append([]metering.ObservationRef(nil), input.ObservationRefs...)
	}
	return economics.Valuation{
		Perspective: input.Perspective, Basis: input.Basis, Subject: input.Subject,
		Scope: input.Scope, InputObservations: refs,
		Completeness: economics.CompletenessPartial,
		CreatedAt:    time.Unix(1_700_185_000+n, 0).UTC(),
	}, nil
}

func TestPhase172Cluster1NoProbeRepeatDeterministic(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 17, "cluster1-noprobe")
	rater := &cluster1WallClockRater{}
	captureNoProbe, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, &shadowNoProbeResultStore{store: store}, store, rater,
	)
	require.NoError(t, err)
	require.NoError(t, captureNoProbe.AppendWork(ctx, work))
	require.NoError(t, captureNoProbe.RateAndPersist(ctx, work))
	require.NoError(t, captureNoProbe.RateAndPersist(ctx, work))
	identity, err := work.Identity()
	require.NoError(t, err)
	got, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	normalized, err := work.Normalize()
	require.NoError(t, err)
	require.True(t, got.CreatedAt.Equal(normalized.CreatedAt), "valuation time must anchor to immutable work, got %v want %v", got.CreatedAt, normalized.CreatedAt)
	require.Equal(t, int64(2), rater.calls.Load())
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ? AND valuation_id = ?`, "test", identity.ValuationKey()).Scan(ctx, &count))
	require.Equal(t, 1, count)
}

func TestPhase172Cluster1ConcurrentOverlapDeterministic(t *testing.T) {
	path := t.TempDir() + "/cluster1-overlap.sqlite"
	store := openCluster1FileStore(t, path, "test")
	journal := openF1Journal(t)
	ctx := context.Background()
	work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 18, "cluster1-overlap")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	rater := &cluster1WallClockRater{}
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, &shadowNoProbeResultStore{store: store}, store, rater,
	)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			errs[index] = capture.RateAndPersist(ctx, work)
		}(i)
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	identity, err := work.Identity()
	require.NoError(t, err)
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ? AND valuation_id = ?`, "test", identity.ValuationKey()).Scan(ctx, &count))
	require.Equal(t, 1, count)
}

func TestPhase172Cluster1ReopenStable(t *testing.T) {
	path := t.TempDir() + "/cluster1-reopen.sqlite"
	first := openCluster1FileStore(t, path, "test")
	journal := openF1Journal(t)
	ctx := context.Background()
	work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 19, "cluster1-reopen")
	rater := &cluster1WallClockRater{}
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, first, &shadowNoProbeResultStore{store: first}, first, rater,
	)
	require.NoError(t, err)
	require.NoError(t, capture.AppendWork(ctx, work))
	require.NoError(t, capture.RateAndPersist(ctx, work))
	identity, err := work.Identity()
	require.NoError(t, err)
	firstValuation, err := first.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second := openCluster1FileStore(t, path, "test")
	rater2 := &cluster1WallClockRater{}
	capture2, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, second, &shadowNoProbeResultStore{store: second}, second, rater2,
	)
	require.NoError(t, err)
	require.NoError(t, capture2.AppendWork(ctx, work))
	require.NoError(t, capture2.RateAndPersist(ctx, work))
	secondValuation, err := second.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, firstValuation.Fingerprint(), secondValuation.Fingerprint())
	require.NoError(t, second.Close())
}

func openCluster1FileStore(t *testing.T, path, storeID string) *DurableStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	seedTestSchemaIfEmpty(t, bunDB)
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = sqlDB.Close()
	})
	return store
}
