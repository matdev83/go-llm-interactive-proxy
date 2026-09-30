package runtimebundle

import (
	"context"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// R10-B adversarial-repair contract: the linked-statement lookup selects only
// statement-line candidates for the trusted store + B-leg through a dedicated
// index, so same-B-leg usage history never contributes pages. When verified
// statement candidates exceed the documented hard budget the relay fails
// closed with a retryable error and never acknowledges a truncated prefix.

func TestObservationEconomicRelayLinkedStatementSameBLegHistoryStaysBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newBridgeMeteringStore(t, "bridge-store")
	source := bridgeRuntimeObservation("same-bleg-source", 1)
	statement := bridgeStatementObservation(t, "line-same-bleg", "b", "bridge-store", 1)
	require.NoError(t, store.AppendObservation(ctx, source))
	require.NoError(t, store.AppendObservation(ctx, statement))
	require.NoError(t, appendSameBLegHistory(ctx, store, "b", 0, 50))

	relay := &observationEconomicRelay{journal: store}
	recorder := &bridgeListQueryRecorder{}
	store.DB().AddQueryHook(recorder)

	evidence := make([]metering.Observation, 0)
	require.NoError(t, relay.appendLinkedStatementEvidence(ctx, source, "b", &evidence))
	require.True(t, bridgeEvidenceContains(evidence, statement.IdentityKey()), "linked statement must be discovered")
	small := recorder.pages

	require.NoError(t, appendSameBLegHistory(ctx, store, "b", 50, 1000))
	recorder.reset()
	evidence = evidence[:0]
	require.NoError(t, relay.appendLinkedStatementEvidence(ctx, source, "b", &evidence))
	require.True(t, bridgeEvidenceContains(evidence, statement.IdentityKey()), "linked statement must remain discoverable")
	grown := recorder.pages

	require.Equal(t, small, grown,
		"same-B-leg usage history must not add linked-statement candidate pages (small=%d grown=%d)", small, grown)
	require.LessOrEqual(t, grown, 2, "linked-statement lookup must stay within a bounded page count, got %d", grown)
}

func TestObservationEconomicRelayLinkedStatementCandidateBudgetFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	meteringStore := newBridgeMeteringStore(t, "bridge-store")
	billingStore := newBridgeBillingStore(t, "bridge-store")
	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)

	// A pathological B-leg whose verified statement lines all fail payload-level
	// lineage, so unbounded candidate paging never yields evidence.
	pathological := bridgeRuntimeObservation("budget-pathological", 1)
	pathological.Subject.BLegID = "b-budget"
	pathological.Correlation.BLegID = "b-budget"
	pathological.Correlation.ProviderRequestID = "request-pathological"
	require.NoError(t, pathological.Validate())

	const candidateCount = 4
	candidates := make([]metering.Observation, 0, candidateCount)
	for i := 0; i < candidateCount; i++ {
		candidate := bridgeStatementObservation(t, fmt.Sprintf("line-budget-%d", i), "b-budget", "bridge-store", uint64(i+1))
		candidate.Correlation.ProviderRequestID = "request-other"
		require.NoError(t, candidate.Validate())
		candidates = append(candidates, candidate)
	}

	// An independent, well-formed B-leg that must still make progress.
	clean := bridgeRuntimeObservation("budget-clean", 1)
	clean.Subject.BLegID = "b-clean"
	clean.Correlation.BLegID = "b-clean"
	require.NoError(t, clean.Validate())
	cleanStatement := bridgeStatementObservation(t, "line-clean", "b-clean", "bridge-store", 1)

	sink := journalstore.NewObservationSinkWithOutbox(meteringStore)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	require.True(t, ok)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{pathological, clean}))
	require.NoError(t, meteringStore.AppendObservations(ctx, candidates))
	require.NoError(t, meteringStore.AppendObservation(ctx, cleanStatement))

	relay := newObservationEconomicRelay(meteringStore, billingStore, builder)
	relay.statementCandidateBudget = 3

	err = relay.ProcessOnce(ctx)
	require.ErrorIs(t, err, errObservationEconomicStatementCandidateBudget,
		"candidate work beyond the hard budget must fail closed, not silently acknowledge a prefix")

	pending, listErr := meteringStore.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, listErr)
	require.Len(t, pending, 1, "the pathological outbox item must remain unacknowledged")
	require.Equal(t, pathological.ID, pending[0].Observation.ID)

	budgetWork, workErr := billingStore.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	require.NoError(t, workErr)
	for _, work := range budgetWork {
		require.NotEqual(t, "b-budget", work.Subject.BLegID, "no work marker may be emitted from a truncated prefix")
	}

	var cleanDelivered int
	require.NoError(t, meteringStore.DB().NewRaw(
		`SELECT COUNT(1) FROM metering_observation_economic_outbox WHERE store_id = ? AND observation_id = ? AND status = 'delivered'`,
		"bridge-store", clean.ID).Scan(ctx, &cleanDelivered))
	require.Equal(t, 1, cleanDelivered, "independent outbox items must still be acknowledged")
}

// appendSameBLegHistory appends count non-statement observations that share the
// requested B-leg, modelling unrelated usage history bound to the same B-leg.
func appendSameBLegHistory(ctx context.Context, store *journalstore.DurableStore, blegID string, start, count int) error {
	const chunk = 250
	for offset := 0; offset < count; offset += chunk {
		size := min(count-offset, chunk)
		batch := make([]metering.Observation, 0, size)
		for i := 0; i < size; i++ {
			index := start + offset + i
			observation := bridgeRuntimeObservation(fmt.Sprintf("same-bleg-%06d", index), uint64(index+1))
			observation.StreamID = "bridge-same-bleg-stream"
			observation.Subject.BLegID = blegID
			observation.Correlation.BLegID = blegID
			if err := observation.Validate(); err != nil {
				return err
			}
			batch = append(batch, observation)
		}
		if err := store.AppendObservations(ctx, batch); err != nil {
			return err
		}
	}
	return nil
}
