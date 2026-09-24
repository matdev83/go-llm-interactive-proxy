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

// F3: reconciler must bind every dependency valuation subject/scope to the
// reconciliation work subject/scope before generating items. Two mutually
// matching foreign B-leg outputs must never become a matched result stamped
// as the work subject. Exact binding holds; ComponentComparisonReconciler
// supports no cross-subject allocation join (design D1/C4: resource costs
// contribute only through explicit conserved allocation, never as a direct
// cross-subject quantity comparison).

func f3ComponentKey() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		SchemaID:  metering.DefaultInclusionSchemaID,
	}
}

func f3Decimal(t *testing.T, value string) *metering.Decimal {
	t.Helper()
	decimal, err := metering.ParseDecimal(value)
	require.NoError(t, err)
	return &decimal
}

func f3LineKnown(t *testing.T, id string, key metering.ComponentKey, quantity string) economics.LineItem {
	t.Helper()
	component := key.Clone()
	return economics.LineItem{
		ID:        "line-f3-" + id,
		RuleID:    "rule-f3",
		ItemID:    "item-f3-" + id,
		Component: &component,
		Quantity:  f3Decimal(t, quantity),
		Unit:      component.Unit,
		Status:    economics.RatingLineRated,
	}
}

func f3WorkSubjectA() metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "store",
		ALegID: "a", BillingCallID: "call", BLegID: "b-leg-a",
	}
}

func f3ForeignSubjectB() metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "store",
		ALegID: "a", BillingCallID: "call", BLegID: "b-leg-b",
	}
}

func f3Observation(t *testing.T, id string, origin string, subject metering.SubjectRef, quantity string) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionLocalTransport
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	key := f3ComponentKey()
	correlation := metering.CorrelationV2{
		StoreID: subject.StoreID, ALegID: subject.ALegID,
		BillingCallID: subject.BillingCallID, BLegID: subject.BLegID,
		RequestID: subject.RequestID, ProviderAccountKey: subject.ProviderAccountKey,
		ResourceID: subject.ResourceID, PeriodID: subject.PeriodID,
	}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id, Revision: 1,
		StreamID: "stream-f3", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: subject, Correlation: correlation,
		Semantics:  metering.SemanticsDelta,
		ObservedAt: time.Unix(90, 0).UTC(), ReceivedAt: time.Unix(91, 0).UTC(), MappingRef: "f3",
		Measures: []metering.Measure{{Key: key, Value: f3Decimal(t, quantity), Quality: metering.QualityObserved}},
	}
}

func f3ResourceObservation(t *testing.T, id string, subject metering.SubjectRef) metering.Observation {
	t.Helper()
	require.Equal(t, metering.SubjectResource, subject.Kind)
	amount := f3Decimal(t, "12")
	charge := metering.ReportedCharge{
		ChargeItemID: "charge-f3-resource",
		Component: &metering.ComponentKey{
			Direction: metering.DirectionNone, Component: "provider:resource",
			Unit: metering.UnitSecond, SchemaID: "provider:resource:v1",
		},
		Currency: "USD", Kind: metering.ChargeKindComponent,
		Payer:  metering.PaymentParty{Kind: metering.PaymentPartyOperator, ID: "op-acct"},
		Amount: amount,
	}
	now := time.Unix(1_700_310_000, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id, Revision: 1,
		StreamID: "stream-f3-resource", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: subject,
		Correlation: metering.CorrelationV2{
			StoreID: subject.StoreID, ResourceID: subject.ResourceID, PeriodID: subject.PeriodID,
		},
		Semantics:  metering.SemanticsDelta,
		ObservedAt: now, ReceivedAt: now, MappingRef: "f3",
		Charges: []metering.ReportedCharge{charge},
	}
}

func f3AccountWindowObservation(t *testing.T, id string, subject metering.SubjectRef) metering.Observation {
	t.Helper()
	require.Equal(t, metering.SubjectAccountWindow, subject.Kind)
	measure := metering.Measure{
		Key: metering.ComponentKey{
			Direction: metering.DirectionNone, Component: "provider:account_utilization",
			Unit: metering.UnitPercent, SchemaID: "provider:account:v1",
		},
		Value:   f3Decimal(t, "12.5"),
		Quality: metering.QualityObserved,
	}
	now := time.Unix(1_700_310_000, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id, Revision: 1,
		StreamID: "stream-f3-window", Sequence: 1,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: subject,
		Correlation: metering.CorrelationV2{
			StoreID: subject.StoreID, ProviderAccountKey: subject.ProviderAccountKey,
		},
		Semantics:  metering.SemanticsGauge,
		ObservedAt: now, ReceivedAt: now, MappingRef: "f3",
		Measures: []metering.Measure{measure},
	}
}

func f3ObservationForSubject(t *testing.T, id string, origin string, subject metering.SubjectRef, quantity string) metering.Observation {
	t.Helper()
	switch subject.Kind {
	case metering.SubjectResource:
		return f3ResourceObservation(t, id, subject)
	case metering.SubjectAccountWindow:
		return f3AccountWindowObservation(t, id, subject)
	default:
		return f3Observation(t, id, origin, subject, quantity)
	}
}

func f3RatingInput(t *testing.T, basis economics.ValuationBasis, subject metering.SubjectRef, scope string, observations []metering.Observation) economics.RatingInput {
	t.Helper()
	input := economics.RatingInput{
		Version: 2, Perspective: metering.PerspectiveCustomer, Basis: basis,
		Subject: subject, Scope: scope, Observations: observations,
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "f3-rater", Version: "v1"}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "catalog://rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff:               economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "f3-tariff", Version: "v1"}, RaterID: "reference"},
		TariffContent:        &economics.SnapshotContentRef{ContentRef: "catalog://tariff/v1", ContentHash: strings.Repeat("2", 64)},
		InputSetHash:         "",
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://qualifiers/v1", ContentHash: strings.Repeat("4", 64)},
		AsOf:                 time.Unix(100, 0).UTC(),
	}
	return input
}

func f3RatingWork(t *testing.T, revision uint64, headKey, obsID string, basis economics.ValuationBasis, subject metering.SubjectRef, scope string, allocRefs []economics.AllocationRef) EconomicRevisionWork {
	t.Helper()
	origin := metering.OriginLocal
	if basis == economics.BasisProviderQuantityLocal || basis == economics.BasisProviderReported {
		origin = metering.OriginProvider
	}
	observation := f3ObservationForSubject(t, obsID, origin, subject, "1")
	input := f3RatingInput(t, basis, subject, scope, []metering.Observation{observation})
	if len(allocRefs) != 0 {
		input.AllocationCoverageRefs = append([]economics.AllocationRef(nil), allocRefs...)
	}
	work := EconomicRevisionWork{
		Queue:            EconomicQueueProvider,
		HeadKey:          headKey,
		Subject:          subject,
		EvidenceRevision: revision,
		Input:            input,
		CreatedAt:        time.Unix(1_700_310_000+int64(revision), 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

func f3AllocationRef(t *testing.T, store string, id string) economics.AllocationRef {
	t.Helper()
	ref := economics.AllocationRef{
		StoreID: store, AllocationID: id, Version: 1, PayloadHash: strings.Repeat("a", 64),
	}
	require.NoError(t, ref.Validate())
	return ref
}

func f3Dependency(t *testing.T, work EconomicRevisionWork) EconomicJobDependency {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	dependency, err := NewEconomicJobDependency(EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	return dependency
}

func f3SeedValuation(t *testing.T, store *economicJobRunnerTestStore, work EconomicRevisionWork, lines []economics.LineItem) {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	inputHash := normalized.InputSetHash
	if identity.DerivationHash != "" {
		inputHash = identity.DerivationHash
	}
	valuation := economics.Valuation{
		ID:                     identity.ValuationKey(),
		Version:                economics.ValuationVersionV2,
		Perspective:            normalized.Input.Perspective,
		Basis:                  normalized.Input.Basis,
		Subject:                normalized.Subject,
		Scope:                  normalized.Input.Scope,
		InputObservations:      append([]metering.ObservationRef(nil), normalized.Input.ObservationRefs...),
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), normalized.Input.AllocationCoverageRefs...),
		InputSetHash:           inputHash,
		Lines:                  append([]economics.LineItem(nil), lines...),
		Completeness:           economics.CompletenessComplete,
		CreatedAt:              normalized.CreatedAt,
	}
	store.mu.Lock()
	store.valuations[identity.ValuationKey()] = valuation
	store.mu.Unlock()
}

func f3ReconWork(t *testing.T, subject metering.SubjectRef, scope string, deps ...EconomicJobDependency) EconomicRevisionWork {
	t.Helper()
	probe := f3ObservationForSubject(t, "f3-recon-obs", metering.OriginProvider, subject, "1")
	input := f3RatingInput(t, economics.BasisProviderReported, subject, scope, []metering.Observation{probe})
	work := EconomicRevisionWork{
		Queue:            EconomicQueueProvider,
		Kind:             EconomicWorkKindReconciliation,
		HeadKey:          "f3-recon-head",
		Subject:          subject,
		EvidenceRevision: 199,
		Input:            input,
		Dependencies:     deps,
		CreatedAt:        time.Unix(1_700_399_100, 0).UTC(),
	}
	normalized, err := work.Normalize()
	require.NoError(t, err)
	return normalized
}

type f3EnvelopeItem struct {
	Component        string  `json:"component"`
	Status           string  `json:"status"`
	Reason           string  `json:"reason"`
	LocalQuantity    *string `json:"local_quantity"`
	ProviderQuantity *string `json:"provider_quantity"`
}

type f3Envelope struct {
	Status   string           `json:"status"`
	Complete bool             `json:"complete"`
	Reason   string           `json:"reason"`
	Items    []f3EnvelopeItem `json:"items"`
}

func f3RunReconciliation(t *testing.T, store *economicJobRunnerTestStore, reconWork EconomicRevisionWork) (EconomicJobRunSummary, f3Envelope, EconomicReconciliation) {
	t.Helper()
	store.appendWork(t, reconWork)
	runner := newEconomicJobRunnerTest(t, EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: store, Dependencies: store, Reconciliations: store,
		Rater: &economicJobRunnerTestRater{}, Reconciler: ComponentComparisonReconciler{},
		Owner: "f3-test", Batch: 8,
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
	var envelope f3Envelope
	require.NoError(t, json.Unmarshal(record.ResultJSON, &envelope))
	return summary, envelope, record
}

func f3RequireIncomparableNoLeak(t *testing.T, envelope f3Envelope, record EconomicReconciliation, work EconomicRevisionWork, wantReason string, foreignLiterals ...string) {
	t.Helper()
	require.Equal(t, "incomparable", envelope.Status)
	require.False(t, envelope.Complete)
	require.Equal(t, wantReason, envelope.Reason)
	require.Empty(t, envelope.Items, "foreign quantities must not appear as comparison items")
	require.True(t, sameSubject(record.Subject, work.Subject), "envelope must be stamped with the work subject, not a foreign subject")
	require.Equal(t, work.Input.Scope, record.Scope)
	raw := string(record.ResultJSON)
	for _, literal := range foreignLiterals {
		require.NotContains(t, raw, `"`+literal+`"`, "result JSON must not leak foreign quantities")
	}
}

func TestPhase172F3AllForeignMatchingIsIncomparableThroughRunner(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	key := f3ComponentKey()
	workSubject := f3WorkSubjectA()
	foreign := f3ForeignSubjectB()
	scope := "call:f3"

	foreignLocal := f3RatingWork(t, 91, "f3-head-foreign-local", "f3-obs-foreign-local", economics.BasisLocalExpected, foreign, scope, nil)
	foreignProvider := f3RatingWork(t, 92, "f3-head-foreign-provider", "f3-obs-foreign-provider", economics.BasisProviderQuantityLocal, foreign, scope, nil)
	f3SeedValuation(t, store, foreignLocal, []economics.LineItem{f3LineKnown(t, "foreign-local", key, "200")})
	f3SeedValuation(t, store, foreignProvider, []economics.LineItem{f3LineKnown(t, "foreign-provider", key, "200")})

	recon := f3ReconWork(t, workSubject, scope, f3Dependency(t, foreignLocal), f3Dependency(t, foreignProvider))
	_, envelope, record := f3RunReconciliation(t, store, recon)
	f3RequireIncomparableNoLeak(t, envelope, record, recon, "subject_mismatch", "200")
}

func TestPhase172F3OneLocalOneForeignIsIncomparableThroughRunner(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	key := f3ComponentKey()
	workSubject := f3WorkSubjectA()
	foreign := f3ForeignSubjectB()
	scope := "call:f3"

	local := f3RatingWork(t, 93, "f3-head-local", "f3-obs-local", economics.BasisLocalExpected, workSubject, scope, nil)
	foreignProvider := f3RatingWork(t, 94, "f3-head-foreign-provider2", "f3-obs-foreign-provider2", economics.BasisProviderQuantityLocal, foreign, scope, nil)
	f3SeedValuation(t, store, local, []economics.LineItem{f3LineKnown(t, "local", key, "200")})
	f3SeedValuation(t, store, foreignProvider, []economics.LineItem{f3LineKnown(t, "foreign-provider", key, "200")})

	recon := f3ReconWork(t, workSubject, scope, f3Dependency(t, local), f3Dependency(t, foreignProvider))
	_, envelope, record := f3RunReconciliation(t, store, recon)
	f3RequireIncomparableNoLeak(t, envelope, record, recon, "subject_mismatch", "200")
}

func TestPhase172F3ExactMatchingWorkSubjectControlThroughRunner(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	key := f3ComponentKey()
	workSubject := f3WorkSubjectA()
	scope := "call:f3"

	local := f3RatingWork(t, 95, "f3-head-control-local", "f3-obs-control-local", economics.BasisLocalExpected, workSubject, scope, nil)
	provider := f3RatingWork(t, 96, "f3-head-control-provider", "f3-obs-control-provider", economics.BasisProviderQuantityLocal, workSubject, scope, nil)
	f3SeedValuation(t, store, local, []economics.LineItem{f3LineKnown(t, "control-local", key, "100")})
	f3SeedValuation(t, store, provider, []economics.LineItem{f3LineKnown(t, "control-provider", key, "100")})

	recon := f3ReconWork(t, workSubject, scope, f3Dependency(t, local), f3Dependency(t, provider))
	_, envelope, record := f3RunReconciliation(t, store, recon)
	require.Equal(t, "matched", envelope.Status)
	require.True(t, envelope.Complete)
	require.Empty(t, envelope.Reason)
	require.Len(t, envelope.Items, 1)
	require.Equal(t, "matched", envelope.Items[0].Status)
	require.NotNil(t, envelope.Items[0].LocalQuantity)
	require.NotNil(t, envelope.Items[0].ProviderQuantity)
	require.Equal(t, "100", *envelope.Items[0].LocalQuantity)
	require.Equal(t, "100", *envelope.Items[0].ProviderQuantity)
	require.True(t, sameSubject(record.Subject, recon.Subject))
	require.Equal(t, recon.Input.Scope, record.Scope)
	require.Equal(t, recon.Input.Basis, record.Basis)
	require.Equal(t, recon.InputSetHash, record.InputSetHash)

	// Deterministic: an identical second scenario in a fresh store renders
	// byte-identical result JSON.
	secondStore := newEconomicJobRunnerTestStore()
	local2 := f3RatingWork(t, 95, "f3-head-control-local", "f3-obs-control-local", economics.BasisLocalExpected, workSubject, scope, nil)
	provider2 := f3RatingWork(t, 96, "f3-head-control-provider", "f3-obs-control-provider", economics.BasisProviderQuantityLocal, workSubject, scope, nil)
	f3SeedValuation(t, secondStore, local2, []economics.LineItem{f3LineKnown(t, "control-local", key, "100")})
	f3SeedValuation(t, secondStore, provider2, []economics.LineItem{f3LineKnown(t, "control-provider", key, "100")})
	recon2 := f3ReconWork(t, workSubject, scope, f3Dependency(t, local2), f3Dependency(t, provider2))
	_, secondEnvelope, secondRecord := f3RunReconciliation(t, secondStore, recon2)
	require.JSONEq(t, string(record.ResultJSON), string(secondRecord.ResultJSON))
	require.Equal(t, envelope, secondEnvelope)
}

func TestPhase172F3SubjectVariantsAreIncomparableThroughRunner(t *testing.T) {
	t.Parallel()
	workSubject := f3WorkSubjectA()
	scope := "call:f3"
	key := f3ComponentKey()

	variants := []struct {
		name    string
		foreign metering.SubjectRef
	}{
		{"store", metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "foreign-store", ALegID: "a", BillingCallID: "call", BLegID: "b-leg-a"}},
		{"account", func() metering.SubjectRef {
			s := f3WorkSubjectA()
			s.AccountID = "acct-foreign"
			return s
		}()},
		{"call", func() metering.SubjectRef {
			s := f3WorkSubjectA()
			s.BillingCallID = "call-foreign"
			return s
		}()},
		{"a-leg", func() metering.SubjectRef {
			s := f3WorkSubjectA()
			s.ALegID = "a-foreign"
			return s
		}()},
		{"b-leg", f3ForeignSubjectB()},
		{"resource", metering.SubjectRef{Kind: metering.SubjectResource, StoreID: "store", ResourceID: "res-f3", PeriodID: "period-f3"}},
		{"account-window", metering.SubjectRef{
			Kind: metering.SubjectAccountWindow, StoreID: "store",
			ProviderAccountKey: "key-f3", PoolID: "pool-f3", WindowID: "window-f3",
			ResetAt: time.Unix(1_700_310_000, 0).UTC(),
		}},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			t.Parallel()
			store := newEconomicJobRunnerTestStore()
			foreignLocal := f3RatingWork(t, 101, "f3-head-"+variant.name+"-local", "f3-obs-"+variant.name+"-local", economics.BasisLocalExpected, variant.foreign, scope, nil)
			foreignProvider := f3RatingWork(t, 102, "f3-head-"+variant.name+"-provider", "f3-obs-"+variant.name+"-provider", economics.BasisProviderQuantityLocal, variant.foreign, scope, nil)
			f3SeedValuation(t, store, foreignLocal, []economics.LineItem{f3LineKnown(t, variant.name+"-local", key, "200")})
			f3SeedValuation(t, store, foreignProvider, []economics.LineItem{f3LineKnown(t, variant.name+"-provider", key, "200")})
			recon := f3ReconWork(t, workSubject, scope, f3Dependency(t, foreignLocal), f3Dependency(t, foreignProvider))
			_, envelope, record := f3RunReconciliation(t, store, recon)
			f3RequireIncomparableNoLeak(t, envelope, record, recon, "subject_mismatch", "200")
		})
	}
}

func TestPhase172F3ScopeMismatchIsIncomparableThroughRunner(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	key := f3ComponentKey()
	workSubject := f3WorkSubjectA()
	workScope := "call:f3-scope-a"
	dependencyScope := "call:f3-scope-b"

	local := f3RatingWork(t, 111, "f3-head-scope-local", "f3-obs-scope-local", economics.BasisLocalExpected, workSubject, dependencyScope, nil)
	provider := f3RatingWork(t, 112, "f3-head-scope-provider", "f3-obs-scope-provider", economics.BasisProviderQuantityLocal, workSubject, dependencyScope, nil)
	f3SeedValuation(t, store, local, []economics.LineItem{f3LineKnown(t, "scope-local", key, "200")})
	f3SeedValuation(t, store, provider, []economics.LineItem{f3LineKnown(t, "scope-provider", key, "200")})

	recon := f3ReconWork(t, workSubject, workScope, f3Dependency(t, local), f3Dependency(t, provider))
	_, envelope, record := f3RunReconciliation(t, store, recon)
	f3RequireIncomparableNoLeak(t, envelope, record, recon, "scope_mismatch", "200")
}

func TestPhase172F3AllocationDoesNotExcuseForeignSubjectThroughRunner(t *testing.T) {
	t.Parallel()
	store := newEconomicJobRunnerTestStore()
	key := f3ComponentKey()
	workSubject := f3WorkSubjectA()
	foreign := f3ForeignSubjectB()
	scope := "call:f3"

	// No cross-subject allocation join is approved for this comparer (design
	// D1/C4). A foreign B-leg valuation that carries conserved allocation
	// coverage still requires exact subject/scope binding.
	alloc := f3AllocationRef(t, foreign.StoreID, "alloc-f3")
	foreignLocal := f3RatingWork(t, 121, "f3-head-alloc-local", "f3-obs-alloc-local", economics.BasisLocalExpected, foreign, scope, []economics.AllocationRef{alloc})
	foreignProvider := f3RatingWork(t, 122, "f3-head-alloc-provider", "f3-obs-alloc-provider", economics.BasisProviderQuantityLocal, foreign, scope, []economics.AllocationRef{alloc})
	f3SeedValuation(t, store, foreignLocal, []economics.LineItem{f3LineKnown(t, "alloc-local", key, "200")})
	f3SeedValuation(t, store, foreignProvider, []economics.LineItem{f3LineKnown(t, "alloc-provider", key, "200")})

	recon := f3ReconWork(t, workSubject, scope, f3Dependency(t, foreignLocal), f3Dependency(t, foreignProvider))
	_, envelope, record := f3RunReconciliation(t, store, recon)
	f3RequireIncomparableNoLeak(t, envelope, record, recon, "subject_mismatch", "200")
}

func TestPhase172F3AttemptLineageWithinSameBLegStillComparesThroughRunner(t *testing.T) {
	t.Parallel()
	// Requirement 6.2: AttemptSeq/AttemptID are child execution lineage within
	// the same B-leg, not alternative accounting subjects. Same B-leg with
	// different attempt lineage must still compare; different B-legs must not.
	store := newEconomicJobRunnerTestStore()
	key := f3ComponentKey()
	scope := "call:f3"

	workSubject := f3WorkSubjectA()
	workSubject.AttemptID = "attempt-f3-work"
	workSubject.AttemptSeq = 7
	localSubject := f3WorkSubjectA()
	localSubject.AttemptID = "attempt-f3-local"
	localSubject.AttemptSeq = 3
	providerSubject := f3WorkSubjectA()
	providerSubject.AttemptID = "attempt-f3-provider"
	providerSubject.AttemptSeq = 9

	local := f3RatingWork(t, 131, "f3-head-attempt-local", "f3-obs-attempt-local", economics.BasisLocalExpected, localSubject, scope, nil)
	provider := f3RatingWork(t, 132, "f3-head-attempt-provider", "f3-obs-attempt-provider", economics.BasisProviderQuantityLocal, providerSubject, scope, nil)
	f3SeedValuation(t, store, local, []economics.LineItem{f3LineKnown(t, "attempt-local", key, "100")})
	f3SeedValuation(t, store, provider, []economics.LineItem{f3LineKnown(t, "attempt-provider", key, "100")})

	recon := f3ReconWork(t, workSubject, scope, f3Dependency(t, local), f3Dependency(t, provider))
	_, envelope, record := f3RunReconciliation(t, store, recon)
	require.Equal(t, "matched", envelope.Status)
	require.True(t, envelope.Complete)
	require.Len(t, envelope.Items, 1)
	require.Equal(t, "matched", envelope.Items[0].Status)
	require.True(t, sameSubject(record.Subject, recon.Subject))
	require.Equal(t, scope, record.Scope)
}
