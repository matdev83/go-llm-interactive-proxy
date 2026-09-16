package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/stretchr/testify/require"
)

func refinement43LegacyProviderCostFenceCounts(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID) (posting, execution, heads int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_provider_cost_posting_fences WHERE store_id = ? AND account_id = ? AND call_id = ?`, store.storeID, accountID, callID.String()).Scan(ctx, &posting))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_provider_cost_execution_fences WHERE store_id = ? AND account_id = ? AND call_id = ?`, store.storeID, accountID, callID.String()).Scan(ctx, &execution))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_provider_cost_heads WHERE store_id = ? AND account_id = ? AND call_id = ?`, store.storeID, accountID, callID.String()).Scan(ctx, &heads))
	return posting, execution, heads
}

func refinement43LegacyEstimatedLeg(t *testing.T, callID billing.BillingCallID, bLegID string) billing.CallLegUsageRecord {
	t.Helper()
	leg := testIndependentCallLegFor(callID, bLegID)
	leg.OperatorRateRef = billing.VersionRef{ID: "operator-rates", Version: "v1"}
	leg.Evidence.Cost = billing.MoneyEvidence{}
	leg.Evidence.Source = billing.EvidenceSourceLocalEstimator
	leg.Evidence.Authority = billing.EvidenceAuthorityEstimated
	sealed, err := leg.Seal()
	require.NoError(t, err)
	return sealed
}

func TestRefinement43LegacyApplyProviderCostRejectsEstimatedResultBeforeMutation(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID := billing.BillingCallID("bc_00000000000000000000000000000061")
	account := billing.Account{ID: "refinement43-legacy-authority", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	leg := refinement43LegacyEstimatedLeg(t, callID, "b-legacy-estimated")
	refinement43AppendLegacyWork(t, store, account, callID, leg)

	result := billing.OperatorCostResult{
		LURKey: leg.Key, Amount: billing.Money{Nano: 11, Currency: "USD"},
		AmountPresent: true, Reconciled: true, Authoritative: false,
	}
	posting, execution, heads := refinement43LegacyProviderCostFenceCounts(t, store, account.ID, callID)
	_, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg, Result: result})
	var authorityErr *billing.ProviderCostAuthorityError
	require.ErrorAs(t, err, &authorityErr)
	require.ErrorIs(t, err, billing.ErrProviderCostAuthority)
	require.Equal(t, "authoritative", authorityErr.Field)

	// Authority rejection is before the durable writer transaction. No money,
	// selected-cost head, or cross-worker fence may be left behind.
	require.Empty(t, refinement43ProviderJournals(t, store, account.ID))
	gotPosting, gotExecution, gotHeads := refinement43LegacyProviderCostFenceCounts(t, store, account.ID, callID)
	require.Equal(t, posting, gotPosting)
	require.Equal(t, execution, gotExecution)
	require.Equal(t, heads, gotHeads)
	state, err := store.GetProviderCostWorkState(ctx, leg.Key)
	require.NoError(t, err)
	require.Equal(t, "pending", state.Status)
	require.Zero(t, state.AttemptCount)
}

type refinement43LegacyAuthorityDurableResolver struct {
	operatorRate billing.OperatorRateSnapshot
	calls        int
}

func (r *refinement43LegacyAuthorityDurableResolver) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	r.calls++
	return billing.RateProviderCost(leg, billing.OperatorRateSet{r.operatorRate}, "USD")
}

func TestRefinement43LegacyProviderCostWorkerDefersAuthorityRejectionForRetry(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID := billing.BillingCallID("bc_00000000000000000000000000000062")
	account := billing.Account{ID: "refinement43-legacy-authority-retry", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	leg := refinement43LegacyEstimatedLeg(t, callID, "b-legacy-authority-retry")
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	resolver := &refinement43LegacyAuthorityDurableResolver{operatorRate: billing.OperatorRateSnapshot{
		Ref: leg.OperatorRateRef, Currency: "USD", InputPerMillionNano: 50, InputRatePresent: true,
		OutputPerMillionNano: 75, OutputRatePresent: true,
	}}
	worker, err := billing.NewCallProviderCostWorker(store, store, resolver, 1)
	require.NoError(t, err)

	err = worker.ProcessOnce(ctx)
	var authorityErr *billing.ProviderCostAuthorityError
	require.ErrorAs(t, err, &authorityErr)
	require.ErrorIs(t, err, billing.ErrProviderCostAuthority)
	require.Equal(t, 1, resolver.calls)
	require.Empty(t, refinement43ProviderJournals(t, store, account.ID))
	posting, execution, heads := refinement43LegacyProviderCostFenceCounts(t, store, account.ID, callID)
	require.Zero(t, posting)
	require.Zero(t, execution)
	require.Zero(t, heads)
	state, err := store.GetProviderCostWorkState(ctx, leg.Key)
	require.NoError(t, err)
	require.Equal(t, "pending", state.Status)
	require.Equal(t, 1, state.AttemptCount)
	require.Contains(t, state.LastError, billing.ErrProviderCostAuthority.Error())

	var markers int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind = 'provider_cost_unreconciled' AND source_key = ?`, account.ID, leg.Key).Scan(ctx, &markers))
	require.Equal(t, 1, markers)
}

func TestRefinement43LegacyProviderCostCutoverSkipsUntrustedResolver(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID := billing.BillingCallID("bc_00000000000000000000000000000063")
	account := billing.Account{ID: "refinement43-legacy-authority-cutover", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	leg := refinement43LegacyEstimatedLeg(t, callID, "b-legacy-authority-cutover")
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	revision := refinement43LegacyCutoverInput(account.ID, callID, leg, billing.Money{Nano: 13, Currency: "USD"})
	_, err := store.ApplyProviderCostRevision(ctx, revision)
	require.NoError(t, err)

	resolver := &refinement43LegacyAuthorityDurableResolver{operatorRate: billing.OperatorRateSnapshot{
		Ref: leg.OperatorRateRef, Currency: "USD", InputPerMillionNano: 50, InputRatePresent: true,
		OutputPerMillionNano: 75, OutputRatePresent: true,
	}}
	worker, err := billing.NewCallProviderCostWorker(store, store, resolver, 1)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Zero(t, resolver.calls)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 1)
	state, err := store.GetProviderCostWorkState(ctx, leg.Key)
	require.NoError(t, err)
	require.Equal(t, "processed", state.Status)
}
