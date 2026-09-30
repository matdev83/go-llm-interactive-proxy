package runtimebundle

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// R10 candidate-budget permanence contract: the candidate-budget sentinel is a
// permanent, fail-closed refusal under immutable append-only evidence. It must
// be deferred with a bounded exponential backoff derived from the persisted
// attempt count instead of being re-scanned on every 100ms relay tick, while
// ordinary transient failures keep the existing short retry and independent
// outbox items still complete.

func TestObservationEconomicRelayCandidateBudgetBackoffDefersWithoutReclaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := newDeterministicBridgeClock(time.Unix(1_800_200_000, 0).UTC())
	const storeID = "bridge-budget-backoff"
	meteringStore := newBridgeMeteringStoreWithClock(t, storeID, clock.Now)
	billingStore := newBridgeBillingStore(t, storeID)
	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)

	pathological := bridgeRuntimeObservation("budget-backoff-pathological", 1)
	pathological.Subject.StoreID = storeID
	pathological.Correlation.StoreID = storeID
	pathological.Subject.BLegID = "b-budget-backoff"
	pathological.Correlation.BLegID = "b-budget-backoff"
	pathological.Correlation.ProviderRequestID = "request-pathological"
	require.NoError(t, pathological.Validate())

	const candidateCount = 4
	candidates := make([]metering.Observation, 0, candidateCount)
	for i := 0; i < candidateCount; i++ {
		candidate := bridgeStatementObservation(t, fmt.Sprintf("line-backoff-%d", i), "b-budget-backoff", storeID, uint64(i+1))
		candidate.Correlation.ProviderRequestID = "request-other"
		require.NoError(t, candidate.Validate())
		candidates = append(candidates, candidate)
	}

	// An independent, well-formed B-leg that must still complete.
	clean := bridgeRuntimeObservation("budget-backoff-clean", 1)
	clean.Subject.StoreID = storeID
	clean.Correlation.StoreID = storeID
	clean.Subject.BLegID = "b-clean-backoff"
	clean.Correlation.BLegID = "b-clean-backoff"
	require.NoError(t, clean.Validate())
	cleanStatement := bridgeStatementObservation(t, "line-clean-backoff", "b-clean-backoff", storeID, 1)

	sink := journalstore.NewObservationSinkWithOutbox(meteringStore)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	require.True(t, ok)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{pathological, clean}))
	require.NoError(t, meteringStore.AppendObservations(ctx, candidates))
	require.NoError(t, meteringStore.AppendObservation(ctx, cleanStatement))

	var logBuf bytes.Buffer
	relay := newObservationEconomicRelay(meteringStore, billingStore, builder)
	relay.statementCandidateBudget = 3
	relay.log = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	require.ErrorIs(t, relay.ProcessOnce(ctx), errObservationEconomicStatementCandidateBudget)

	pending, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "only the over-budget item may remain; the healthy item must complete")
	require.Equal(t, pathological.ID, pending[0].Observation.ID)
	require.Equal(t, "pending", pending[0].Status)
	require.NotEmpty(t, pending[0].LastError)
	require.Contains(t, pending[0].LastError, errObservationEconomicStatementCandidateBudget.Error())

	firstDelay := pending[0].NextAttemptAt.Sub(clock.Now())
	require.GreaterOrEqual(t, firstDelay, time.Minute,
		"over-budget deferral must use a long floor, not the 100ms transient retry")
	require.LessOrEqual(t, firstDelay, time.Hour)
	require.Equal(t, 1, pending[0].AttemptCount)

	require.Contains(t, logBuf.String(), "lip.observation_economic_relay_candidate_budget_deferred",
		"the swallowed permanent failure must be operator-visible at the relay boundary")
	require.Contains(t, logBuf.String(), "attempt_count=1")
	require.Contains(t, logBuf.String(), firstDelay.String())

	// A next 100ms relay tick must not reclaim or rescan the deferred item.
	clock.Advance(observationEconomicRelayInterval)
	require.NoError(t, relay.ProcessOnce(ctx), "no due work after the over-budget deferral")
	pendingAfterTick, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pendingAfterTick, 1)
	require.Equal(t, 1, pendingAfterTick[0].AttemptCount, "the 100ms tick must not reclaim the deferred item")

	// A restarted relay over the same durable store honors the persisted
	// next-attempt time rather than immediately re-scanning.
	restarted := newObservationEconomicRelay(meteringStore, billingStore, builder)
	restarted.statementCandidateBudget = 3
	require.NoError(t, restarted.ProcessOnce(ctx))
	pendingAfterRestart, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pendingAfterRestart, 1)
	require.Equal(t, 1, pendingAfterRestart[0].AttemptCount)

	// Once the durable due time elapses the item is retried with a larger backoff.
	clock.Advance(pendingAfterRestart[0].NextAttemptAt.Sub(clock.Now()))
	require.ErrorIs(t, relay.ProcessOnce(ctx), errObservationEconomicStatementCandidateBudget)
	pendingDue, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pendingDue, 1)
	require.Equal(t, 2, pendingDue[0].AttemptCount)
	secondDelay := pendingDue[0].NextAttemptAt.Sub(clock.Now())
	require.Greater(t, secondDelay, firstDelay, "backoff must grow with the persisted attempt count")
	require.LessOrEqual(t, secondDelay, time.Hour)

	// No work marker may be emitted for the truncated over-budget prefix.
	budgetWork, workErr := billingStore.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	require.NoError(t, workErr)
	for _, work := range budgetWork {
		require.NotEqual(t, "b-budget-backoff", work.Subject.BLegID, "no work marker may be emitted from a truncated prefix")
	}
}

// Astra R10 follow-up contract: a durable-deferral warning must be emitted only
// after the retry update actually commits. When the update fails (lost claim or
// database error) the relay must not claim a durable deferral, must emit only a
// bounded safe diagnostic with no raw error text, and must leave the durable row
// untouched for normal lease recovery.
func TestObservationEconomicRelayCandidateBudgetPersistFailureDoesNotClaimDeferral(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := newDeterministicBridgeClock(time.Unix(1_800_400_000, 0).UTC())
	const storeID = "bridge-budget-persist-failure"
	meteringStore := newBridgeMeteringStoreWithClock(t, storeID, clock.Now)
	billingStore := newBridgeBillingStore(t, storeID)
	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)

	pathological := bridgeRuntimeObservation("budget-persist-pathological", 1)
	pathological.Subject.StoreID = storeID
	pathological.Correlation.StoreID = storeID
	pathological.Subject.BLegID = "b-budget-persist"
	pathological.Correlation.BLegID = "b-budget-persist"
	pathological.Correlation.ProviderRequestID = "request-pathological"
	require.NoError(t, pathological.Validate())

	const candidateCount = 4
	candidates := make([]metering.Observation, 0, candidateCount)
	for i := 0; i < candidateCount; i++ {
		candidate := bridgeStatementObservation(t, fmt.Sprintf("line-persist-%d", i), "b-budget-persist", storeID, uint64(i+1))
		candidate.Correlation.ProviderRequestID = "request-other"
		require.NoError(t, candidate.Validate())
		candidates = append(candidates, candidate)
	}

	sink := journalstore.NewObservationSinkWithOutbox(meteringStore)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	require.True(t, ok)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{pathological}))
	require.NoError(t, meteringStore.AppendObservations(ctx, candidates))

	// Force the retry update (processing -> pending) to fail, modelling a lost
	// claim or a database write error, without sleeps or polling.
	_, triggerErr := meteringStore.DB().NewRaw(`
CREATE TRIGGER IF NOT EXISTS test_fail_outbox_retry
BEFORE UPDATE ON metering_observation_economic_outbox
WHEN OLD.status = 'processing' AND NEW.status = 'pending'
BEGIN SELECT RAISE(ABORT, 'injected outbox retry failure'); END`).Exec(ctx)
	require.NoError(t, triggerErr)

	var logBuf bytes.Buffer
	relay := newObservationEconomicRelay(meteringStore, billingStore, builder)
	relay.statementCandidateBudget = 3
	relay.log = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	err = relay.ProcessOnce(ctx)
	require.ErrorIs(t, err, errObservationEconomicStatementCandidateBudget)
	require.NotContains(t, logBuf.String(), "lip.observation_economic_relay_candidate_budget_deferred",
		"a failed retry must never be logged as a durable deferral")
	require.Contains(t, logBuf.String(), "lip.observation_economic_relay_candidate_budget_deferral_persist_failed",
		"the failed persistence attempt must be operator-visible")
	require.NotContains(t, logBuf.String(), "injected outbox retry failure",
		"the bounded diagnostic must not carry raw error text")

	pending, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, pathological.ID, pending[0].Observation.ID)
	require.Equal(t, "processing", pending[0].Status, "a failed retry must leave the durable claim as-is")
	require.Equal(t, 1, pending[0].AttemptCount)
}

func TestObservationEconomicCandidateBudgetRetryScheduleIsBounded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, time.Minute},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 32 * time.Minute},
		{7, time.Hour},
		{8, time.Hour},
		{1_000_000, time.Hour},
		{int(^uint(0) >> 1), time.Hour},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, observationEconomicCandidateBudgetRetryDelay(tc.attempts), "attempts=%d", tc.attempts)
	}
}

func TestObservationEconomicRelayTransientFailureKeepsShortRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := newDeterministicBridgeClock(time.Unix(1_800_300_000, 0).UTC())
	const storeID = "bridge-transient-retry"
	meteringStore := newBridgeMeteringStoreWithClock(t, storeID, clock.Now)
	observation := bridgeRuntimeObservation("transient-retry", 1)
	observation.Subject.StoreID = storeID
	observation.Correlation.StoreID = storeID
	require.NoError(t, observation.Validate())
	sink := journalstore.NewObservationSinkWithOutbox(meteringStore)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	require.True(t, ok)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{observation}))

	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)
	relay := newObservationEconomicRelay(meteringStore, failingEconomicAppender{}, builder)
	require.Error(t, relay.ProcessOnce(ctx))

	pending, err := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "pending", pending[0].Status)
	require.Equal(t, observationEconomicRelayRetry, pending[0].NextAttemptAt.Sub(clock.Now()),
		"ordinary transient failures must keep the existing short retry policy")
}
