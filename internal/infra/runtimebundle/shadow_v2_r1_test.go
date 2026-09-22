package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

type r1EchoRater struct{}

func (r1EchoRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
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
	}, nil
}

func TestPhase172R1ComposedShadowCreatesNoV1Work(t *testing.T) {
	store := newShadowBillingStore(t)
	journal := newShadowJournal(t)
	ctx := context.Background()

	handle, err := runtimebundle.ComposeShadowV2Capture(runtimebundle.ShadowV2CaptureInput{
		StoreID: c4StoreID, MaxObservations: 16,
		EvidenceSink: journal, WorkAppender: store, ResultStore: store, ReconciliationStore: store,
		Rater: r1EchoRater{}, V1Settlement: store,
	})
	require.NoError(t, err)

	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	observation := c4Observation(t, callID, "b-r1-shadow", "r1-shadow-obs", 7)
	require.NoError(t, handle.CaptureObservations(ctx, []metering.Observation{observation}))
	require.NoError(t, handle.CaptureObservations(ctx, []metering.Observation{observation}))

	pending, err := store.ListPendingProviderCostWork(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, pending, "composed shadow capture must not enqueue ordinary provider-cost work")

	claimed, err := store.ClaimCompleteCalls(ctx, 10)
	require.NoError(t, err)
	for _, complete := range claimed {
		require.NotEqual(t, callID, complete.Closure.CallID, "composed shadow observation must never become claimable")
	}
	_, err = store.GetCallUsage(ctx, callID)
	require.Error(t, err, "shadow-only identity must own no ordinary V1 call row")
}
