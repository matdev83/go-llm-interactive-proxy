//go:build integration

package billingstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// Phase 16 eighth-pass Finding 3 PostgreSQL-direct proofs: same allocation-aware
// provider-posting recovery composition as SQLite, through the actual worker.

func newFinding3PostgresStore(t *testing.T) *DurableStore {
	t.Helper()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))
	return store
}

func TestPhase16Finding3PostgresAllocationAwareRetryUsesFullSourceKey(t *testing.T) {
	store := newFinding3PostgresStore(t)
	ctx := context.Background()
	accountID := "finding3-pg-alloc-retry"
	finding3CreateAccount(t, store, accountID)
	callID := billing.BillingCallID("bc_0000000000000000000000000000f311")
	alloc := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-f3", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	work := finding3ProviderWork(t, accountID, callID, "finding3-pg-alloc-head", "b-leg-f3-pg-alloc", metering.PaymentParty{Kind: metering.PaymentPartyOperator}, "10", []economics.AllocationRef{alloc}, time.Unix(1_700_500_100, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))

	sentinel := errors.New("finding3 pg crash after provider posting")
	store.SetEconomicFaultHook(func(stage string) error {
		if stage == "after_provider_cost_revision" {
			return sentinel
		}
		return nil
	})
	recorder := &finding3RecordingProvider{inner: store}
	first, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.ErrorIs(t, first.ProcessOnce(ctx), sentinel)
	store.SetEconomicFaultHook(nil)

	restarted, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, restarted.ProcessOnce(ctx))
	require.Len(t, recorder.inputs, 2)
	require.Equal(t, recorder.inputs[0].InputSetHash, recorder.inputs[1].InputSetHash, "fresh/retry source identity must be byte-identical")
	require.Equal(t, finding3SourceKey(t, recorder.inputs[0]), finding3SourceKey(t, recorder.inputs[1]))

	identity, err := work.Identity()
	require.NoError(t, err)
	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	head, err := store.GetProviderCostHead(ctx, accountID, callID, work.HeadKey)
	require.NoError(t, err)
	require.Equal(t, valuation.InputSetHash, head.InputSetHash)
	require.Len(t, refinement43ProviderJournals(t, store, accountID), 1)
}

func TestPhase16Finding3PostgresProcessedRecoveryIsIdempotent(t *testing.T) {
	store := newFinding3PostgresStore(t)
	ctx := context.Background()
	accountID := "finding3-pg-idem"
	finding3CreateAccount(t, store, accountID)
	callID := billing.BillingCallID("bc_0000000000000000000000000000f312")
	alloc := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-f3", Version: 1, PayloadHash: strings.Repeat("b", 64)}
	work := finding3ProviderWork(t, accountID, callID, "finding3-pg-idem-head", "b-leg-f3-pg-idem", metering.PaymentParty{Kind: metering.PaymentPartyOperator}, "10", []economics.AllocationRef{alloc}, time.Unix(1_700_500_200, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	recorder := &finding3RecordingProvider{inner: store}
	worker, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Len(t, refinement43ProviderJournals(t, store, accountID), 1)

	finding3ReopenWork(t, store, work)
	recovery, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, recovery.ProcessOnce(ctx))
	require.Len(t, refinement43ProviderJournals(t, store, accountID), 1)
	require.Len(t, recorder.inputs, 2)
	require.Equal(t, recorder.inputs[0].InputSetHash, recorder.inputs[1].InputSetHash)
	require.Equal(t, finding3SourceKey(t, recorder.inputs[0]), finding3SourceKey(t, recorder.inputs[1]))
}

func TestPhase16Finding3PostgresNonpayableExclusionStableAcrossRetry(t *testing.T) {
	store := newFinding3PostgresStore(t)
	ctx := context.Background()
	accountID := "finding3-pg-nonpay"
	finding3CreateAccount(t, store, accountID)
	callID := billing.BillingCallID("bc_0000000000000000000000000000f313")
	alloc := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-f3", Version: 1, PayloadHash: strings.Repeat("c", 64)}
	customer := metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	work := finding3ProviderWork(t, accountID, callID, "finding3-pg-nonpay-head", "b-leg-f3-pg-nonpay", customer, "10", []economics.AllocationRef{alloc}, time.Unix(1_700_500_300, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))

	sentinel := errors.New("finding3 pg nonpayable crash")
	store.SetEconomicFaultHook(func(stage string) error {
		if stage == "after_provider_cost_revision" {
			return sentinel
		}
		return nil
	})
	recorder := &finding3RecordingProvider{inner: store}
	first, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.ErrorIs(t, first.ProcessOnce(ctx), sentinel)
	store.SetEconomicFaultHook(nil)

	second, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, second.ProcessOnce(ctx))
	require.Empty(t, refinement43ProviderJournals(t, store, accountID))
	require.Len(t, recorder.inputs, 2)
	require.Equal(t, recorder.inputs[0].InputSetHash, recorder.inputs[1].InputSetHash)
	identity, err := work.Identity()
	require.NoError(t, err)
	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, valuation.InputSetHash, finding3FenceInputHash(t, store, accountID, callID, "b-leg-f3-pg-nonpay"))
}

func TestPhase16Finding3PostgresLegacyObservationOnlyKeyUnchanged(t *testing.T) {
	store := newFinding3PostgresStore(t)
	ctx := context.Background()
	accountID := "finding3-pg-legacy"
	finding3CreateAccount(t, store, accountID)
	callID := billing.BillingCallID("bc_0000000000000000000000000000f314")
	work := finding3ProviderWork(t, accountID, callID, "finding3-pg-legacy-head", "b-leg-f3-pg-legacy", metering.PaymentParty{Kind: metering.PaymentPartyOperator}, "10", nil, time.Unix(1_700_500_400, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	identity, err := work.Identity()
	require.NoError(t, err)
	require.Empty(t, identity.DerivationHash)

	sentinel := errors.New("finding3 pg legacy crash")
	store.SetEconomicFaultHook(func(stage string) error {
		if stage == "after_provider_cost_revision" {
			return sentinel
		}
		return nil
	})
	recorder := &finding3RecordingProvider{inner: store}
	first, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.ErrorIs(t, first.ProcessOnce(ctx), sentinel)
	store.SetEconomicFaultHook(nil)

	second, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, second.ProcessOnce(ctx))
	require.Len(t, recorder.inputs, 2)
	require.Equal(t, recorder.inputs[0].InputSetHash, recorder.inputs[1].InputSetHash)
	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, identity.InputSetHash, valuation.InputSetHash)
	head, err := store.GetProviderCostHead(ctx, accountID, callID, work.HeadKey)
	require.NoError(t, err)
	require.Equal(t, identity.InputSetHash, head.InputSetHash)
}

func TestPhase16Finding3PostgresReplacementDerivationDistinctStable(t *testing.T) {
	store := newFinding3PostgresStore(t)
	ctx := context.Background()
	accountID := "finding3-pg-replace"
	finding3CreateAccount(t, store, accountID)
	callID := billing.BillingCallID("bc_0000000000000000000000000000f315")
	allocV1 := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-f3-replace", Version: 1, PayloadHash: strings.Repeat("1", 64)}
	allocV2 := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-f3-replace", Version: 2, PayloadHash: strings.Repeat("2", 64)}
	operator := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	base := finding3ProviderObservation(t, accountID, callID, "b-leg-f3-pg-replace", 1, "10", operator)
	mkWork := func(alloc economics.AllocationRef, headKey string, createdAt time.Time) billing.EconomicRevisionWork {
		input := economics.PostUsageRatingInput{
			Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
			Subject: base.Subject, Scope: "finding3", Payer: operator,
			Observations:           []metering.Observation{base.Clone()},
			AllocationCoverageRefs: []economics.AllocationRef{alloc},
		}
		return billing.EconomicRevisionWork{
			Queue: billing.EconomicQueueProvider, HeadKey: headKey, Subject: base.Subject,
			EvidenceRevision: 1, Input: input, CreatedAt: createdAt,
		}
	}
	first := mkWork(allocV1, "finding3-pg-replace-head", time.Unix(1_700_500_500, 0).UTC())
	second := mkWork(allocV2, "finding3-pg-replace-head", time.Unix(1_700_500_600, 0).UTC())
	firstID, err := first.Identity()
	require.NoError(t, err)
	secondID, err := second.Identity()
	require.NoError(t, err)
	require.NotEqual(t, firstID.DerivationHash, secondID.DerivationHash)

	require.NoError(t, store.AppendEconomicRevisionWork(ctx, first))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, second))
	recorder := &finding3RecordingProvider{inner: store}
	worker, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Len(t, recorder.inputs, 2)

	firstVal, err := store.GetValuation(ctx, firstID.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	secondVal, err := store.GetValuation(ctx, secondID.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.NotEqual(t, firstVal.InputSetHash, secondVal.InputSetHash)

	for _, w := range []billing.EconomicRevisionWork{first, second} {
		finding3ReopenWork(t, store, w)
	}
	retry, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, retry.ProcessOnce(ctx))
	require.Len(t, recorder.inputs, 4)
	freshKeys := map[string]string{
		recorder.inputs[0].InputSetHash: finding3SourceKey(t, recorder.inputs[0]),
		recorder.inputs[1].InputSetHash: finding3SourceKey(t, recorder.inputs[1]),
	}
	require.Len(t, freshKeys, 2)
	for _, retryInput := range recorder.inputs[2:] {
		freshKey, ok := freshKeys[retryInput.InputSetHash]
		require.True(t, ok, "retry posting %q must match one fresh derivation", retryInput.InputSetHash)
		require.Equal(t, freshKey, finding3SourceKey(t, retryInput))
	}
}
