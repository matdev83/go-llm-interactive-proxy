//go:build integration

package billingstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func correctionRecoveryPostgresAdjustmentInput(t *testing.T, accountID string, callID billing.BillingCallID, expected billing.SelectedCostHeadExpectation, selected billing.SelectedCostValuation) billing.SelectedCostAdjustmentInput {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: correctionRecoveryStoreID, AccountID: accountID,
		ALegID: "a-" + accountID, BillingCallID: callID.String(), BLegID: "b-leg-" + accountID,
	}
	return billing.SelectedCostAdjustmentInput{
		AccountID: accountID, CallID: callID, HeadKey: selectedCostAdjustmentHeadKey,
		Subject: subject, Expected: expected, Selected: selected,
	}
}

// Task 13.5 correction and dispute-like recovery certification on direct
// PostgreSQL. Transaction behavior matters here: row-locked selected-cost CAS,
// racing identical corrections and the pass-through late-adjustment head
// transition must match the SQLite contract under a real pooler-safe
// transaction. Statements and pricing are synthetic fixtures.

func correctionRecoveryPostgresPassThroughFixture(t *testing.T, store *DurableStore, accountID string) (billing.Account, billing.CallUsageRecord, billing.CallExposure, billing.CostPassThroughSettlement) {
	t.Helper()
	ctx := context.Background()
	account := billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 200, State: billing.AccountReady, Version: 1,
	}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID, err := billing.NewBillingCallID()
	require.NoError(t, err)
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID,
		ALegID: "a-" + accountID, SessionID: "session-" + accountID,
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{
			ID: "prices-" + accountID, Version: "v1",
		},
		ChargePolicyRef: billing.VersionRef{ID: "policy-" + accountID, Version: "v1"},
	}
	require.NoError(t, store.AppendCallUsage(ctx, call))
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 100, Currency: account.Currency},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	require.NoError(t, err)
	policy := billing.CostPassThroughPolicy{
		MissingCost: billing.CostPassThroughMissingCostProvisional,
		SafeBound:   &billing.Money{Nano: 100, Currency: account.Currency}, AllowLateAdjustment: true,
	}
	state := billing.CostPassThroughSettlement{
		PolicyRef: call.ChargePolicyRef, Policy: policy, Status: billing.CostPassThroughSettlementProvisional,
		SafeBound: billing.Money{Nano: 100, Currency: account.Currency}, PostedAmount: billing.Money{Nano: 100, Currency: account.Currency},
	}
	return account, call, exposure, state
}

func TestCorrectionRecoveryPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: correctionRecoveryStoreID})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	require.NoError(t, VerifySchema(ctx, store.db))

	t.Run("late correction after closure does not rebill the customer", func(t *testing.T) {
		callID, err := billing.NewBillingCallID()
		require.NoError(t, err)
		account, call, exposure := correctionRecoveryAccountAndCall(t, store, "correction-recovery-pg-late", callID, 1_000_000_000, 25_000_000)
		correctionRecoverySettleIndependent(t, store, call, exposure, 25_000_000)
		require.Len(t, correctionRecoveryCustomerJournals(t, store, account.ID), 1)
		settledAccount, err := store.GetAccount(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, int64(975_000_000), settledAccount.BalanceNano)

		initial := selectedCostAdjustmentValuation(t, "valuation-cr-pg-v1", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
		appliedInitial, err := store.ApplySelectedCostAdjustment(ctx, correctionRecoveryPostgresAdjustmentInput(t, account.ID, callID, billing.SelectedCostHeadExpectation{}, initial))
		require.NoError(t, err)
		require.Equal(t, billing.SelectedCostTransitionApplied, appliedInitial.Status)

		correction := selectedCostAdjustmentValuation(t, "valuation-cr-pg-v2", 2, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
		input := correctionRecoveryPostgresAdjustmentInput(t, account.ID, callID,
			billing.SelectedCostHeadExpectation{Version: appliedInitial.HeadVersion, Previous: &initial}, correction)
		applied, err := store.ApplySelectedCostAdjustment(ctx, input)
		require.NoError(t, err)
		require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)
		require.Equal(t, "-2", selectedCostAdjustmentAmountRatString(t, applied.Delta))

		replayed, err := store.ApplySelectedCostAdjustment(ctx, input)
		require.NoError(t, err)
		require.Equal(t, billing.SelectedCostTransitionReplay, replayed.Status)
		require.Equal(t, applied.OperationKey, replayed.OperationKey)
		selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 2, 2)

		require.Len(t, correctionRecoveryCustomerJournals(t, store, account.ID), 1)
		unchanged, err := store.GetAccount(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, settledAccount.BalanceNano, unchanged.BalanceNano)
		require.Equal(t, settledAccount.Version, unchanged.Version)
		head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
		require.Equal(t, int64(8_000_000_000), head.AmountNano)
		require.Equal(t, int64(2), head.HeadVersion)
	})

	t.Run("no frozen FX correction stays pending without erasing economics", func(t *testing.T) {
		callID, err := billing.NewBillingCallID()
		require.NoError(t, err)
		account, call, exposure := correctionRecoveryAccountAndCall(t, store, "correction-recovery-pg-nofx", callID, 1_000_000_000, 25_000_000)
		correctionRecoverySettleIndependent(t, store, call, exposure, 25_000_000)

		initial := selectedCostAdjustmentValuation(t, "valuation-cr-pg-nofx-v1", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
		applied, err := store.ApplySelectedCostAdjustment(ctx, correctionRecoveryPostgresAdjustmentInput(t, account.ID, callID, billing.SelectedCostHeadExpectation{}, initial))
		require.NoError(t, err)
		require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)

		eur := selectedCostAdjustmentValuation(t, "valuation-cr-pg-nofx-v2", 2, billing.OperatorCostSelectionStatusFinal, "EUR", 8_000_000_000)
		result, err := store.ApplySelectedCostAdjustment(ctx, correctionRecoveryPostgresAdjustmentInput(t, account.ID, callID,
			billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &initial}, eur))
		require.NoError(t, err)
		require.Equal(t, billing.SelectedCostTransitionPending, result.Status)
		require.Equal(t, billing.SelectedCostComparisonIncomparable, result.Comparison)
		require.Nil(t, result.Delta)
		selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)
		head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
		require.Equal(t, int64(10_000_000_000), head.AmountNano)
		require.Equal(t, int64(1), head.HeadVersion)
		require.Len(t, correctionRecoveryCustomerJournals(t, store, account.ID), 1)
	})

	t.Run("racing identical corrections produce one effect", func(t *testing.T) {
		account := selectedCostAdjustmentAccount(t, store, "correction-recovery-pg-race")
		initial := selectedCostAdjustmentValuation(t, "valuation-cr-pg-race-v1", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
		appliedInitial, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
		require.NoError(t, err)
		require.Equal(t, billing.SelectedCostTransitionApplied, appliedInitial.Status)

		correction := selectedCostAdjustmentValuation(t, "valuation-cr-pg-race-v2", 2, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
		const workers = 4
		inputs := make([]billing.SelectedCostAdjustmentInput, workers)
		for i := range inputs {
			inputs[i] = selectedCostAdjustmentInput(t, account.ID,
				billing.SelectedCostHeadExpectation{Version: appliedInitial.HeadVersion, Previous: &initial}, correction)
		}
		results := make([]billing.SelectedCostAdjustmentResult, workers)
		errs := make([]error, workers)
		var wg sync.WaitGroup
		for i := range inputs {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				results[index], errs[index] = store.ApplySelectedCostAdjustment(ctx, inputs[index])
			}(i)
		}
		wg.Wait()
		applied, replayed := 0, 0
		for i := range results {
			require.NoError(t, errs[i], "racing correction worker %d", i)
			switch results[i].Status {
			case billing.SelectedCostTransitionApplied:
				applied++
			case billing.SelectedCostTransitionReplay:
				replayed++
			default:
				t.Fatalf("racing correction worker %d unexpected status %s", i, results[i].Status)
			}
		}
		require.Equal(t, 1, applied)
		require.Equal(t, workers-1, replayed)
		selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 2, 2)
	})

	t.Run("provisional pass-through posts one bounded adjustment", func(t *testing.T) {
		account, call, exposure, state := correctionRecoveryPostgresPassThroughFixture(t, store, "correction-recovery-pg-passthrough")
		_, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: call, Exposure: exposure,
			Result: billing.CallRatingResult{CallID: call.CallID, CustomerCharge: state.PostedAmount, Fingerprint: "pass-through-pg-cert", CostPassThrough: &state},
		})
		require.NoError(t, err)
		require.Len(t, correctionRecoveryCustomerJournals(t, store, account.ID), 1)

		late := phase10ProviderCost(2, 80, "USD")
		adjusted, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: late})
		require.NoError(t, err)
		require.True(t, adjusted.Applied)
		require.Equal(t, billing.Money{Nano: -20, Currency: "USD"}, adjusted.Delta)
		replay, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: late})
		require.NoError(t, err)
		require.True(t, replay.Replayed)
		require.False(t, replay.Applied)
		require.Len(t, correctionRecoveryCustomerJournals(t, store, account.ID), 2)
		got, err := store.GetAccount(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, int64(120), got.BalanceNano)
	})
}
