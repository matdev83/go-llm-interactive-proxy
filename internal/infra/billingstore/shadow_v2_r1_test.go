package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

type r1ProviderCostResolver struct{}

func (r1ProviderCostResolver) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	return billing.RateProviderCost(leg, billing.OperatorRateSet{}, "USD")
}

func r1ShadowObservation(t *testing.T) (metering.Observation, billing.BillingCallID) {
	t.Helper()
	callID := mustShadowCallID(t)
	observation := shadowV2ObservationForLeg(callID, "a-shared", "b-r1-shadow", "r1-shadow-obs", 1)
	return observation, callID
}

func TestPhase172R1ShadowCaptureEnqueuesNoProviderWork(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, shadowV2TestRater(t),
	)
	require.NoError(t, err)

	observation, _ := r1ShadowObservation(t)
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))

	pending, err := store.ListPendingProviderCostWork(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, pending, "shadow capture must not enqueue ordinary provider-cost work")
	require.Len(t, f1ObservationsForBLeg(t, journal, "b-r1-shadow"), 1, "V2 observation must be queryable without V1 rows")
}

func TestPhase172R1ShadowCallIsNeverClaimable(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, shadowV2TestRater(t),
	)
	require.NoError(t, err)

	observation, callID := r1ShadowObservation(t)
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))

	claimed, err := store.ClaimCompleteCalls(ctx, 10)
	require.NoError(t, err)
	for _, complete := range claimed {
		require.NotEqual(t, callID, complete.Closure.CallID, "shadow observation must never become claimable settlement work")
	}
	_, err = store.GetCallUsage(ctx, callID)
	require.Error(t, err, "shadow-only identity must own no ordinary V1 call row")
}

func TestPhase172R1V1WorkersAddNoShadowJournal(t *testing.T) {
	store := newSQLiteTestStore(t)
	journal := openF1Journal(t)
	ctx := context.Background()
	account := billing.Account{ID: "r1-acct", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	capture, err := NewShadowV2Capture(
		ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
		journal, store, store, store, shadowV2TestRater(t),
	)
	require.NoError(t, err)

	observation, _ := r1ShadowObservation(t)
	require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))
	before, err := store.JournalTransactions(ctx, account.ID)
	require.NoError(t, err)

	worker, err := billing.NewCallProviderCostWorker(store, store, r1ProviderCostResolver{}, 4)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))

	after, err := store.JournalTransactions(ctx, account.ID)
	require.NoError(t, err)
	require.Len(t, after, len(before), "eligible V1 workers must add no journal for a shadow-only identity")
}
