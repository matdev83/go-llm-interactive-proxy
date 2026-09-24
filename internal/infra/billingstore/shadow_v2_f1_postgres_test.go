//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func openF1PostgresPair(t *testing.T, dsn string) (*DurableStore, *journalstore.DurableStore) {
	t.Helper()
	billingBun, _ := openIsolatedPostgresBun(t, dsn, 8)
	billingStore, err := NewDurableStore(context.Background(), billingBun, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = billingStore.Close() })
	journalBun, _ := openIsolatedPostgresBun(t, dsn, 4)
	journal, err := journalstore.NewDurableStore(context.Background(), journalBun, journalstore.DurableConfig{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = journal.Close() })
	return billingStore, journal
}

// TestPhase172F1SameIdentityCoexistencePostgresDirect proves the F1
// capture-only boundary on PostgreSQL-direct: shadow V2 observations under the
// SAME CallID/BLegID never create ordinary V1 rows or claim state, in both
// append orders, with existing exposure and exact provider-work accounting.
func TestPhase172F1SameIdentityCoexistencePostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()

	t.Run("shadow-first", func(t *testing.T) {
		store, journal := openF1PostgresPair(t, dsn)
		account := billing.Account{ID: "f1-acct", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000_000, State: billing.AccountReady, Version: 1}
		require.NoError(t, store.CreateAccount(ctx, account))
		callID := mustShadowCallID(t)

		capture, err := NewShadowV2Capture(
			ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
			journal, store, store, store, shadowV2TestRater(t),
		)
		require.NoError(t, err)
		require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{f1Observation(t, callID, "b-f1-pg", "f1-pg-obs-1", 1)}))
		require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{f1Observation(t, callID, "b-f1-pg", "f1-pg-obs-1", 1)}))

		call := f1Call(t, callID, "b-f1-pg")
		_, err = store.AdmitExposure(ctx, billing.AdmitExposureInput{AccountID: account.ID, CallID: callID.String(),
			Max: billing.Money{Nano: 100_000, Currency: "USD"}, PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef})
		require.NoError(t, err)
		require.NoError(t, store.AppendCallUsage(ctx, call))
		require.NoError(t, store.AppendCallLegUsage(ctx, f1ScalarLeg(t, callID, "b-f1-pg")))

		claimed, err := store.ClaimCompleteCalls(ctx, 10)
		require.NoError(t, err)
		found := false
		for _, complete := range claimed {
			if complete.Closure.CallID == callID {
				found = true
			}
		}
		require.True(t, found, "PG shadow-first: legitimate V1 closure must stay claimable")
		pending, err := store.ListPendingProviderCostWork(ctx, 100)
		require.NoError(t, err)
		require.Len(t, pending, 1, "PG shadow-first: exactly the legitimate V1 leg may be pending")
	})

	t.Run("v1-first", func(t *testing.T) {
		store, journal := openF1PostgresPair(t, dsn)
		account := billing.Account{ID: "f1-acct", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000_000, State: billing.AccountReady, Version: 1}
		require.NoError(t, store.CreateAccount(ctx, account))
		callID := mustShadowCallID(t)

		call := f1Call(t, callID, "b-f1-pg-v1")
		require.NoError(t, store.AppendCallUsage(ctx, call))
		require.NoError(t, store.AppendCallLegUsage(ctx, f1ScalarLeg(t, callID, "b-f1-pg-v1")))

		capture, err := NewShadowV2Capture(
			ShadowV2CaptureConfig{StoreID: "test", MaxObservations: 16},
			journal, store, store, store, shadowV2TestRater(t),
		)
		require.NoError(t, err)
		observation := f1Observation(t, callID, "b-f1-pg-v1", "f1-pg-obs-2", 1)
		require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))
		require.NoError(t, capture.CaptureObservations(ctx, []metering.Observation{observation}))

		pending, err := store.ListPendingProviderCostWork(ctx, 100)
		require.NoError(t, err)
		require.Len(t, pending, 1, "PG v1-first: exactly the legitimate V1 leg may be pending")
		row, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, "b-f1-pg-v1"))
		require.NoError(t, err)
		require.Empty(t, row.Observations, "PG v1-first: V1 row must keep scalar projection without V2 observations")
		page, err := journal.ListObservations(ctx, journalstore.ObservationQuery{
			StoreID: "test", SubjectKind: metering.SubjectBLeg, SubjectID: "b-f1-pg-v1", Limit: 10,
		})
		require.NoError(t, err)
		require.Len(t, page.Observations, 1, "PG v1-first: V2 observation must live source-separated")
	})
}
