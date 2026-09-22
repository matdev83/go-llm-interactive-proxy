package billingstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

type stubShadowRater struct{}

func (stubShadowRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
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
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), input.AllocationCoverageRefs...),
		Payer:                  input.Payer,
		Completeness:           economics.CompletenessPartial,
	}, nil
}

func shadowV2TestRater(_ *testing.T) billing.PostUsageRater {
	return stubShadowRater{}
}

func mustShadowCallID(t *testing.T) billing.BillingCallID {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	return callID
}

func shadowV2JournalCount(t *testing.T, ctx context.Context, store *DurableStore) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM journal_transactions`).Scan(ctx, &count))
	return count
}

func shadowV2UnitOperationCount(t *testing.T, ctx context.Context, store *DurableStore) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_unit_operations`).Scan(ctx, &count))
	return count
}

func shadowV2ProviderCostHeadCount(t *testing.T, ctx context.Context, store *DurableStore) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_provider_cost_heads`).Scan(ctx, &count))
	return count
}

func shadowV2SelectedAdjustmentCount(t *testing.T, ctx context.Context, store *DurableStore) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_selected_cost_adjustments`).Scan(ctx, &count))
	return count
}

func shadowV2AccountBalance(t *testing.T, ctx context.Context, store *DurableStore, accountID string) int64 {
	t.Helper()
	account, err := store.GetAccount(ctx, accountID)
	require.NoError(t, err, "durable account read must fail closed, never mask an outage as zero")
	return account.BalanceNano
}

func shadowV2WorkCount(t *testing.T, ctx context.Context, store *DurableStore) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_work WHERE store_id = ?`, "test").Scan(ctx, &count))
	return count
}

func shadowV2ValuationCountFor(t *testing.T, ctx context.Context, store *DurableStore, work billing.EconomicRevisionWork) int {
	t.Helper()
	identity, err := work.Identity()
	require.NoError(t, err)
	var count int
	require.NoError(t, store.db.NewRaw(
		`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ? AND valuation_id = ?`,
		"test", identity.ValuationKey()).Scan(ctx, &count))
	return count
}

func shadowV2ValuationExists(t *testing.T, ctx context.Context, store *DurableStore, work billing.EconomicRevisionWork) bool {
	t.Helper()
	return shadowV2ValuationCountFor(t, ctx, store, work) == 1
}

func shadowV2ReconciliationExists(t *testing.T, ctx context.Context, store *DurableStore, work billing.EconomicRevisionWork) bool {
	t.Helper()
	identity, err := work.Identity()
	require.NoError(t, err)
	var count int
	require.NoError(t, store.db.NewRaw(
		`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ? AND reconciliation_id = ? AND reconciliation_version = ?`,
		"test", identity.ReconciliationKey(), int64(identity.EvidenceRevision)).Scan(ctx, &count))
	return count == 1
}

// shadowV2ReconciliationWork builds dependency-anchored reconciliation work
// for one persisted rating work.
func shadowV2ReconciliationWork(t *testing.T, rating billing.EconomicRevisionWork) billing.EconomicRevisionWork {
	t.Helper()
	normalized, err := rating.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	dependency, err := billing.NewEconomicJobDependency(billing.EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	observation := phase4EconomicsObservation("test", "shadow-172-recon-evidence", 7)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, Kind: billing.EconomicWorkKindReconciliation,
		HeadKey: "shadow-172-recon-head", Subject: observation.Subject,
		EvidenceRevision: 7, Input: input, Dependencies: []billing.EconomicJobDependency{dependency},
		CreatedAt: time.Unix(1_700_172_500, 0).UTC(),
	}
	normalizedRecon, err := work.Normalize()
	require.NoError(t, err)
	return normalizedRecon
}

func shadowV2ReconciliationFor(t *testing.T, reconWork billing.EconomicRevisionWork) billing.EconomicReconciliation {
	t.Helper()
	normalized, err := reconWork.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	return billing.EconomicReconciliation{
		ID:           identity.ReconciliationKey(),
		Version:      identity.EvidenceRevision,
		Subject:      normalized.Subject,
		Scope:        normalized.Input.Scope,
		Basis:        normalized.Input.Basis,
		InputSetHash: normalized.InputSetHash,
		ResultJSON:   json.RawMessage(`{"status":"matched"}`),
		CreatedAt:    time.Unix(1_700_172_600, 0).UTC(),
	}
}

// shadowV2ObservationForLeg returns a V2 observation whose B-leg lineage
// matches one shadow leg envelope.
func shadowV2ObservationForLeg(callID billing.BillingCallID, aLegID, bLegID, obsID string, revision uint64) metering.Observation {
	observation := phase4EconomicsObservation("test", obsID, revision)
	observation.Subject.ALegID = aLegID
	observation.Subject.BLegID = bLegID
	observation.Subject.BillingCallID = callID.String()
	observation.Subject.CallID = callID.String()
	observation.Subject.AttemptSeq = revision
	observation.Correlation.ALegID = aLegID
	observation.Correlation.BLegID = bLegID
	observation.Correlation.BillingCallID = callID.String()
	observation.Correlation.CallID = callID.String()
	observation.Correlation.AttemptSeq = revision
	return observation
}

func TestPhase172ShadowV2CapturePersistsWithoutMonetaryMutation(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	provisioned := billing.Account{ID: "shadow-acct-172", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 250_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, provisioned))

	baselineJournals := shadowV2JournalCount(t, ctx, store)
	baselineUnits := shadowV2UnitOperationCount(t, ctx, store)
	baselineProviderHeads := shadowV2ProviderCostHeadCount(t, ctx, store)
	baselineAdjustments := shadowV2SelectedAdjustmentCount(t, ctx, store)
	baselineBalance := shadowV2AccountBalance(t, ctx, store, "shadow-acct-172")
	require.Equal(t, int64(250_000), baselineBalance)

	rater := shadowV2TestRater(t)
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)

	callID := mustShadowCallID(t)
	observation := shadowV2ObservationForLeg(callID, "a-shared", "b-shadow-172", "shadow-172-obs", 1)
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))

	rating := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "shadow-172")
	require.NoError(t, capture.AppendWork(ctx, rating))
	require.NoError(t, capture.RateAndPersist(ctx, rating))

	reconWork := shadowV2ReconciliationWork(t, rating)
	require.NoError(t, capture.AppendWork(ctx, reconWork))
	reconciliation := shadowV2ReconciliationFor(t, reconWork)
	require.NoError(t, capture.AppendReconciliation(ctx, reconWork, reconciliation))

	// V2 records are retained and queryable under stable IDs.
	require.True(t, shadowV2ValuationExists(t, ctx, store, rating))
	require.True(t, shadowV2ReconciliationExists(t, ctx, store, reconWork))
	require.Len(t, f1ObservationsForBLeg(t, journal, "b-shadow-172"), 1)

	// Zero monetary effects: no journals, unit ops, provider heads,
	// adjustments, or balance movement.
	require.Equal(t, baselineJournals, shadowV2JournalCount(t, ctx, store))
	require.Equal(t, baselineUnits, shadowV2UnitOperationCount(t, ctx, store))
	require.Equal(t, baselineProviderHeads, shadowV2ProviderCostHeadCount(t, ctx, store))
	require.Equal(t, baselineAdjustments, shadowV2SelectedAdjustmentCount(t, ctx, store))
	require.Equal(t, baselineBalance, shadowV2AccountBalance(t, ctx, store, "shadow-acct-172"))
}

func TestPhase172ShadowV2ReplayIsIdempotent(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()

	rater := shadowV2TestRater(t)
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)

	callID := mustShadowCallID(t)
	observation := shadowV2ObservationForLeg(callID, "a-shared", "b-shadow-172-replay", "shadow-172-replay", 3)
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))
	require.Len(t, f1ObservationsForBLeg(t, journal, "b-shadow-172-replay"), 1)

	rating := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 3, "shadow-172-replay")
	require.NoError(t, capture.AppendWork(ctx, rating))
	require.NoError(t, capture.AppendWork(ctx, rating))
	require.NoError(t, capture.RateAndPersist(ctx, rating))
	require.NoError(t, capture.RateAndPersist(ctx, rating))

	reconWork := shadowV2ReconciliationWork(t, rating)
	require.NoError(t, capture.AppendWork(ctx, reconWork))
	require.NoError(t, capture.AppendWork(ctx, reconWork))
	reconciliation := shadowV2ReconciliationFor(t, reconWork)
	require.NoError(t, capture.AppendReconciliation(ctx, reconWork, reconciliation))
	require.NoError(t, capture.AppendReconciliation(ctx, reconWork, reconciliation))

	require.Equal(t, 2, shadowV2WorkCount(t, ctx, store))
	require.Equal(t, 1, shadowV2ValuationCountFor(t, ctx, store, rating))
	require.True(t, shadowV2ReconciliationExists(t, ctx, store, reconWork))
	require.Equal(t, 0, shadowV2JournalCount(t, ctx, store))
	require.Equal(t, 0, shadowV2UnitOperationCount(t, ctx, store))
	require.Equal(t, 0, shadowV2ProviderCostHeadCount(t, ctx, store))
	require.Equal(t, 0, shadowV2SelectedAdjustmentCount(t, ctx, store))
}

func TestPhase172ShadowV2RestartDoesNotDuplicateOrPost(t *testing.T) {
	fileStore := openRefinement52BillingStore(t, "test")
	journal := openF1Journal(t)
	ctx := context.Background()
	rater := shadowV2TestRater(t)
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, fileStore, fileStore, fileStore, rater,
	)
	require.NoError(t, err)

	rating := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "shadow-172-restart")
	require.NoError(t, capture.AppendWork(ctx, rating))
	require.NoError(t, capture.RateAndPersist(ctx, rating))
	beforeValuations := shadowV2ValuationCountFor(t, ctx, fileStore, rating)
	require.Equal(t, 1, beforeValuations)

	// Replay after a logical restart reuses stable IDs: no duplicate V2
	// records and no financial effects.
	require.NoError(t, capture.AppendWork(ctx, rating))
	require.NoError(t, capture.RateAndPersist(ctx, rating))
	require.Equal(t, 1, shadowV2ValuationCountFor(t, ctx, fileStore, rating))
	require.Equal(t, 0, shadowV2JournalCount(t, ctx, fileStore))
	require.Equal(t, 0, shadowV2UnitOperationCount(t, ctx, fileStore))
}

func TestPhase172ShadowV2CanceledContextFailsClosed(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	rater := shadowV2TestRater(t)
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, rater,
	)
	require.NoError(t, err)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	callID := mustShadowCallID(t)
	observation := shadowV2ObservationForLeg(callID, "a-shared", "b-shadow-172-cancel", "shadow-172-cancel", 5)
	require.Error(t, capture.CaptureObservations(canceled, []metering.Observation{observation}))

	work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 5, "shadow-172-cancel")
	require.Error(t, capture.AppendWork(canceled, work))
	require.Error(t, capture.RateAndPersist(canceled, work))

	ctx := context.Background()
	require.Equal(t, 0, shadowV2JournalCount(t, ctx, store))
	require.Equal(t, 0, shadowV2UnitOperationCount(t, ctx, store))
}

func TestPhase172ShadowV2ConfigRejectsIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	rater := shadowV2TestRater(t)

	if _, err := NewShadowV2Capture(ShadowV2CaptureConfig{}, journal, store, store, store, rater); err == nil {
		t.Fatal("empty config accepted")
	}
	if _, err := NewShadowV2Capture(ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 1 << 20}, journal, store, store, store, rater); err == nil {
		t.Fatal("unbounded observations accepted")
	}
	if _, err := NewShadowV2Capture(ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16}, nil, store, store, store, rater); err == nil {
		t.Fatal("nil evidence accepted")
	}
	valid, err := NewShadowV2Capture(ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16}, journal, store, store, store, rater)
	require.NoError(t, err)
	_ = valid
}
