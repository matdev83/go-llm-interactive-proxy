package billingstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.5 correction and dispute-like recovery certification (durable
// slice). These tests compose the already implemented 13.1 import ledger, 13.2
// matching, 13.3 selected-cost adjustment and the explicit cost-pass-through
// late-adjustment seam into the recovery scenarios:
//
//   - late provider evidence after call/customer closure updates immutable COGS
//     history without reopening or rebilling the customer by default;
//   - a native currency mismatch without a frozen FX basis stays pending with
//     no journal/link/head advance and never erases incurred COGS or the
//     customer settlement;
//   - a corrected aggregate statement imports, matches many charges and yields
//     one idempotent delta under duplicate imports, replays and racing
//     corrections;
//   - the default independent-retail customer offer has no late-rebill head,
//     while an explicit provisional pass-through policy emits one bounded
//     customer adjustment that never rewrites the original settlement.
//
// Every price below is a synthetic fixture, not a provider tariff.

const (
	correctionRecoveryStoreID   = "test"
	correctionRecoveryTenantID  = "tenant-1"
	correctionRecoveryAccount   = "provider-account"
	correctionRecoveryPeriod    = "period-1"
	correctionRecoveryCustomer  = "customer_call_settlement"
	correctionRecoveryComponent = "correction_recovery_tokens"
)

func correctionRecoveryChargeComponent() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput, Component: correctionRecoveryComponent,
		Unit: "token", SchemaID: "statement:correction:test:v1",
		Dimensions: []metering.Dimension{{Name: "cache", Value: "uncached"}},
	}
}

func correctionRecoveryChargeRef(charge string) metering.ChargeRef {
	return metering.ChargeRef{
		StoreID: correctionRecoveryStoreID, ObservationID: "evidence-observation-" + charge,
		Revision: 1, ChargeItemID: charge,
	}
}

func correctionRecoveryCovered(charge string) metering.ChargeCoverageRef {
	return metering.ChargeCoverageRef{Ref: correctionRecoveryChargeRef(charge), Relation: metering.CoverageInclusive}
}

func correctionRecoveryEvidence(charges ...string) []billing.StatementChargeEvidence {
	component := correctionRecoveryChargeComponent()
	out := make([]billing.StatementChargeEvidence, 0, len(charges))
	for _, charge := range charges {
		out = append(out, billing.StatementChargeEvidence{
			Ref: correctionRecoveryChargeRef(charge), State: billing.StatementEvidenceEligible,
			ReconciliationID: "reconciliation-" + charge,
			TenantID:         correctionRecoveryTenantID, ProviderAccountKey: correctionRecoveryAccount,
			PeriodID: correctionRecoveryPeriod, Kind: metering.ChargeKindComponent,
			Component: &component, Currency: "USD",
		})
	}
	return out
}

type correctionRecoveryStatementLine struct {
	ID           string
	Revision     uint64
	ChargeItemID string
	Amount       string
	AccountTotal bool
}

func correctionRecoveryStatement(t *testing.T, statement string, revision uint64, lines []correctionRecoveryStatementLine) billing.NormalizedStatement {
	t.Helper()
	component := correctionRecoveryChargeComponent()
	scope := billing.TrustedStatementScope{
		StoreID: correctionRecoveryStoreID, TenantID: correctionRecoveryTenantID,
		PrincipalID: "principal-1", ProviderAccountKeys: []string{correctionRecoveryAccount},
	}
	batch := economics.StatementBatch{
		Version: 1, ProviderAccountKey: correctionRecoveryAccount, StatementID: statement,
		Revision: revision, PeriodID: correctionRecoveryPeriod,
	}
	for i, line := range lines {
		subject := metering.SubjectRef{
			Kind: metering.SubjectStatementLine, StoreID: correctionRecoveryStoreID,
			TenantID: correctionRecoveryTenantID, ProviderAccountKey: correctionRecoveryAccount,
			StatementID: statement, StatementLineID: line.ID, PeriodID: correctionRecoveryPeriod,
		}
		if i == 0 {
			batch.Subject = subject
		}
		lineRevision := line.Revision
		if lineRevision == 0 {
			lineRevision = 1
		}
		amount, err := metering.ParseDecimal(line.Amount)
		require.NoError(t, err)
		charge := metering.ReportedCharge{
			ChargeItemID: line.ChargeItemID, Amount: &amount, Currency: "USD",
			Kind: metering.ChargeKindAggregate, Component: &component,
			Covers: []metering.ChargeCoverageRef{
				correctionRecoveryCovered("charge-1"),
				correctionRecoveryCovered("charge-2"),
				correctionRecoveryCovered("charge-3"),
			},
		}
		if line.AccountTotal {
			// An account total is a genuine aggregate claim without an SKU
			// component; it imports as a claim and stays account-scoped when
			// matching.
			charge.Component = nil
			charge.Covers = nil
		}
		observation := metering.Observation{
			Version: 2, ID: "statement-observation-" + line.ID,
			SourceEventKey: "statement-event-" + statement + "-" + line.ID,
			Revision:       1, StreamID: "statement-stream-1", Sequence: uint64(i + 1),
			Origin: metering.OriginStatement, Acquisition: metering.AcquisitionStatementImporter,
			Authority: metering.AuthorityVerifiedStatement, Perspective: metering.PerspectiveOperator,
			Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
			Subject: subject,
			Correlation: metering.CorrelationV2{
				StoreID: correctionRecoveryStoreID, TenantID: correctionRecoveryTenantID,
				ProviderAccountKey: correctionRecoveryAccount, PeriodID: correctionRecoveryPeriod,
			},
			Semantics:  metering.SemanticsDelta,
			ObservedAt: time.Unix(1_700_000_000, 0).UTC(),
			ReceivedAt: time.Unix(1_700_000_000+int64(revision), 0).UTC(),
			MappingRef: "statement:correction:test:v1",
			Charges:    []metering.ReportedCharge{charge},
		}
		ref := metering.ObservationRef{
			StoreID: observation.Subject.StoreID, ObservationID: observation.ID,
			Revision: observation.Revision, PayloadHash: observation.Fingerprint(),
		}
		batch.Observations = append(batch.Observations, observation)
		batch.Lines = append(batch.Lines, economics.StatementLine{
			ID: line.ID, Revision: lineRevision, Subject: subject,
			Observation: ref, ChargeItemID: line.ChargeItemID, Outcome: economics.StatementLineMatched,
		})
	}
	normalized, err := billing.NormalizeStatement(scope, batch)
	require.NoError(t, err)
	return normalized
}

func correctionRecoveryAccountAndCall(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, balanceNano, maxNano int64) (billing.Account, billing.CallUsageRecord, billing.CallExposure) {
	t.Helper()
	ctx := context.Background()
	account := billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: balanceNano, State: billing.AccountReady, Version: 1,
	}
	require.NoError(t, store.CreateAccount(ctx, account))
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
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: maxNano, Currency: account.Currency},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	require.NoError(t, err)
	return account, call, exposure
}

func correctionRecoverySettleIndependent(t *testing.T, store *DurableStore, call billing.CallUsageRecord, exposure billing.CallExposure, chargeNano int64) billing.CallSettlement {
	t.Helper()
	settled, err := store.ApplyCallBillingResult(context.Background(), billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure,
		Result: billing.CallRatingResult{
			CallID: call.CallID, CustomerCharge: billing.Money{Nano: chargeNano, Currency: "USD"},
			Fingerprint: "independent-retail-" + call.CallID.String(),
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, settled.Customer.Transaction.ID)
	return settled
}

func correctionRecoveryCustomerJournals(t *testing.T, store *DurableStore, accountID string) []billing.JournalTransaction {
	t.Helper()
	rows, err := store.JournalTransactions(context.Background(), accountID)
	require.NoError(t, err)
	out := make([]billing.JournalTransaction, 0, len(rows))
	for _, row := range rows {
		if row.OperationKind == correctionRecoveryCustomer || row.OperationKind == billing.CostPassThroughAdjustmentOperationKind {
			out = append(out, row)
		}
	}
	return out
}

// TestCorrectionRecoverySQLiteLateProviderCorrectionAfterClosureDoesNotRebillCustomer
// locks the default customer policy: after the call and its customer settlement
// are durably closed, a late provider correction advances only the immutable
// COGS head/journal chain. The customer balance, version and settlement journal
// stay untouched, and an exact replay after a store reopen gains no effect.
func TestCorrectionRecoverySQLiteLateProviderCorrectionAfterClosureDoesNotRebillCustomer(t *testing.T) {
	store, reopen := selectedCostAdjustmentFileStore(t)
	ctx := context.Background()
	callID := selectedCostAdjustmentCallID(t)
	account, call, exposure := correctionRecoveryAccountAndCall(t, store, "correction-recovery-late", callID, 1_000_000_000, 25_000_000)

	correctionRecoverySettleIndependent(t, store, call, exposure, 25_000_000)
	customerJournals := correctionRecoveryCustomerJournals(t, store, account.ID)
	require.Len(t, customerJournals, 1)
	settlementJournalID := customerJournals[0].ID
	afterSettlement, err := store.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, int64(975_000_000), afterSettlement.BalanceNano)

	var exposureStatus string
	require.NoError(t, store.db.NewRaw(`SELECT status FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &exposureStatus))
	require.Equal(t, "closed", exposureStatus)

	initial := selectedCostAdjustmentValuation(t, "valuation-cr-late-v1", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	appliedInitial, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, appliedInitial.Status)
	require.Len(t, correctionRecoveryCustomerJournals(t, store, account.ID), 1)
	unchanged, err := store.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, afterSettlement.BalanceNano, unchanged.BalanceNano)
	require.Equal(t, afterSettlement.Version, unchanged.Version)

	correction := selectedCostAdjustmentValuation(t, "valuation-cr-late-v2", 2, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
	correctionInput := selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: appliedInitial.HeadVersion, Previous: &initial}, correction)
	applied, err := store.ApplySelectedCostAdjustment(ctx, correctionInput)
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)
	require.Equal(t, "-2", selectedCostAdjustmentAmountRatString(t, applied.Delta), "late correction posts only new-minus-posted")

	// The customer is neither reopened nor rebilled and the settlement journal
	// remains the only customer-facing row with the same identity.
	customerJournals = correctionRecoveryCustomerJournals(t, store, account.ID)
	require.Len(t, customerJournals, 1)
	require.Equal(t, settlementJournalID, customerJournals[0].ID)
	unchanged, err = store.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, afterSettlement.BalanceNano, unchanged.BalanceNano)
	require.Equal(t, afterSettlement.Version, unchanged.Version)

	// Immutable COGS history advanced exactly once: two provider postings and
	// two adjustment rows, with the head at the corrected amount.
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 2, 2)
	head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(8_000_000_000), head.AmountNano)
	require.Equal(t, int64(2), head.HeadVersion)
	require.Equal(t, int64(2), head.ValuationRevision)

	// Interruption/replay across a store reopen: the same correction returns
	// the original operation identity with zero new effects.
	require.NoError(t, store.Close())
	store = reopen()
	t.Cleanup(func() { _ = store.Close() })
	replayed, err := store.ApplySelectedCostAdjustment(ctx, correctionInput)
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionReplay, replayed.Status)
	require.Equal(t, billing.SelectedCostReasonAlreadyApplied, replayed.Reason)
	require.Equal(t, applied.OperationKey, replayed.OperationKey)
	require.Equal(t, applied.TransactionID, replayed.TransactionID)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 2, 2)
	require.Len(t, correctionRecoveryCustomerJournals(t, store, account.ID), 1)
	reopenedHead, err := store.GetSelectedCostHead(ctx, account.ID, callID, selectedCostAdjustmentHeadKey)
	require.NoError(t, err)
	require.Equal(t, uint64(2), reopenedHead.Version)
	require.NotNil(t, reopenedHead.Selected)
	require.True(t, correction.IdentityEqual(*reopenedHead.Selected))
}

// TestCorrectionRecoverySQLiteNoFrozenFXCorrectionRetainsIncurredEconomics
// locks the 10 USD to 8 EUR vector: without a frozen FX basis the correction
// stays pending/incomparable with no journal delta, valuation link or head
// transition, and the incurred COGS posting plus the customer settlement are
// retained unchanged.
func TestCorrectionRecoverySQLiteNoFrozenFXCorrectionRetainsIncurredEconomics(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID := selectedCostAdjustmentCallID(t)
	account, call, exposure := correctionRecoveryAccountAndCall(t, store, "correction-recovery-nofx", callID, 1_000_000_000, 25_000_000)
	correctionRecoverySettleIndependent(t, store, call, exposure, 25_000_000)

	initial := selectedCostAdjustmentValuation(t, "valuation-cr-nofx-v1", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	applied, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, applied.Status)

	eur := selectedCostAdjustmentValuation(t, "valuation-cr-nofx-v2", 2, billing.OperatorCostSelectionStatusFinal, "EUR", 8_000_000_000)
	result, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID,
		billing.SelectedCostHeadExpectation{Version: applied.HeadVersion, Previous: &initial}, eur))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionPending, result.Status)
	require.Equal(t, billing.SelectedCostReasonPostedCurrencyMismatch, result.Reason)
	require.Equal(t, billing.SelectedCostComparisonIncomparable, result.Comparison)
	require.Equal(t, billing.SelectedCostPostingPending, result.Posting)
	require.Nil(t, result.Delta)
	require.Empty(t, result.LinkKey)
	require.Empty(t, result.OperationKey)

	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 1, 1)
	head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(10_000_000_000), head.AmountNano)
	require.Equal(t, int64(1), head.HeadVersion)
	require.Equal(t, int64(1), head.ValuationRevision)

	journals := selectedCostAdjustmentJournals(t, store, account.ID)
	require.Len(t, journals, 1, "the incurred COGS posting is not erased")
	require.Equal(t, "inference_provider_cogs", journals[0].Entries[0].LedgerAccount)
	require.Len(t, correctionRecoveryCustomerJournals(t, store, account.ID), 1, "the customer settlement is not erased")
	got, err := store.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, int64(975_000_000), got.BalanceNano)
}

// TestCorrectionRecoverySQLiteCorrectedAggregateStatementAdjustsOnce locks the
// full durable correction flow: a corrected aggregate statement revision is
// imported through the public importer (duplicate import replays), matches
// complete many-charge coverage while an account total stays unmatched, and
// racing identical corrections converge on exactly one delta.
func TestCorrectionRecoverySQLiteCorrectedAggregateStatementAdjustsOnce(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	scope := billing.TrustedStatementScope{
		StoreID: correctionRecoveryStoreID, TenantID: correctionRecoveryTenantID,
		PrincipalID: "principal-1", ProviderAccountKeys: []string{correctionRecoveryAccount},
	}
	service, err := billing.NewStatementImportService(store)
	require.NoError(t, err)

	original := correctionRecoveryStatement(t, "statement-cr-1", 1, []correctionRecoveryStatementLine{
		{ID: "agg-1", Revision: 1, ChargeItemID: "aggregate-1", Amount: "10"},
		{ID: "total-1", Revision: 1, ChargeItemID: "account-total-1", Amount: "18", AccountTotal: true},
	})
	originalResult, err := service.Import(ctx, scope, original.Batch)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"agg-1", "total-1"}, originalResult.Accepted)

	corrected := correctionRecoveryStatement(t, "statement-cr-1", 2, []correctionRecoveryStatementLine{
		{ID: "agg-1", Revision: 2, ChargeItemID: "aggregate-1", Amount: "8"},
		{ID: "total-1", Revision: 1, ChargeItemID: "account-total-1", Amount: "18", AccountTotal: true},
	})
	correctedBatch := corrected.Batch
	correctedResult, err := service.Import(ctx, scope, correctedBatch)
	require.NoError(t, err)
	require.Equal(t, []string{"agg-1"}, correctedResult.Accepted)
	duplicate, err := service.Import(ctx, scope, correctedBatch)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"agg-1", "total-1"}, duplicate.Replayed, "a duplicate import is an inert replay")

	set, err := billing.MatchStatements([]billing.NormalizedStatement{corrected}, correctionRecoveryEvidence("charge-1", "charge-2", "charge-3"))
	require.NoError(t, err)
	require.Len(t, set.Results, 1)
	require.Len(t, set.Results[0].Links, 1)
	link := set.Results[0].Links[0]
	require.Len(t, link.Charges, 3, "many-charge aggregate coverage retains every charge")
	require.Equal(t, []string{"agg-1"}, link.LineIDs)
	byID := map[string]billing.StatementMatchLine{}
	for _, line := range set.Results[0].Lines {
		byID[line.LineID] = line
	}
	require.Equal(t, billing.StatementMatchStatusUnmatched, byID["total-1"].Status)
	require.Equal(t, billing.StatementMatchReasonAccountScopedTotal, byID["total-1"].Reason)
	require.Empty(t, byID["total-1"].LinkKey, "an account total is never allocated per request")

	account := selectedCostAdjustmentAccount(t, store, "correction-recovery-aggregate")
	initial := selectedCostAdjustmentValuation(t, "valuation-cr-agg-v1", 1, billing.OperatorCostSelectionStatusFinal, "USD", 10_000_000_000)
	appliedInitial, err := store.ApplySelectedCostAdjustment(ctx, selectedCostAdjustmentInput(t, account.ID, billing.SelectedCostHeadExpectation{}, initial))
	require.NoError(t, err)
	require.Equal(t, billing.SelectedCostTransitionApplied, appliedInitial.Status)

	correction := selectedCostAdjustmentValuation(t, "valuation-cr-agg-v2", 2, billing.OperatorCostSelectionStatusFinal, "USD", 8_000_000_000)
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
	require.Equal(t, 1, applied, "racing identical corrections produce one effect")
	require.Equal(t, workers-1, replayed)
	selectedCostAdjustmentAssertZeroNewEffects(t, store, account.ID, 2, 2)
	head := selectedCostAdjustmentHeadColumnsFor(t, store, account.ID, selectedCostAdjustmentHeadKey)
	require.Equal(t, int64(8_000_000_000), head.AmountNano)
	require.Equal(t, int64(2), head.HeadVersion)
}

// TestCorrectionRecoverySQLiteProvisionalPassThroughAdjustmentIsBounded locks
// the customer policy: the default independent-retail offer has no rebill head
// and refuses a late supplier adjustment, while an explicit provisional
// pass-through offer posts one bounded customer adjustment that is replayed
// exactly and never rewrites the original immutable settlement.
func TestCorrectionRecoverySQLiteProvisionalPassThroughAdjustmentIsBounded(t *testing.T) {
	t.Run("independent retail has no late-rebill head", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		callID := selectedCostAdjustmentCallID(t)
		account, call, exposure := correctionRecoveryAccountAndCall(t, store, "correction-recovery-independent", callID, 1_000_000_000, 25_000_000)
		correctionRecoverySettleIndependent(t, store, call, exposure, 25_000_000)
		before, err := store.GetAccount(ctx, account.ID)
		require.NoError(t, err)

		_, err = store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{
			AccountID: account.ID, CallID: call.CallID, ProviderCost: phase10ProviderCost(2, 80, "USD"),
		})
		require.ErrorIs(t, err, billing.ErrCostPassThroughHeadNotFound, "independent retail must fail closed rather than rebill")
		after, err := store.GetAccount(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, before.BalanceNano, after.BalanceNano)
		require.Equal(t, before.Version, after.Version)
		require.Len(t, correctionRecoveryCustomerJournals(t, store, account.ID), 1)
	})

	t.Run("explicit provisional pass-through posts one bounded adjustment", func(t *testing.T) {
		store, ctx, account, call, exposure, state := phase10CostPassThroughStoreFixture(t, true, 100)
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: call, Exposure: exposure,
			Result: billing.CallRatingResult{CallID: call.CallID, CustomerCharge: state.PostedAmount, Fingerprint: "pass-through-cert", CostPassThrough: &state},
		}); err != nil {
			t.Fatal(err)
		}
		original := correctionRecoveryCustomerJournals(t, store, account.ID)
		require.Len(t, original, 1)
		originalTransactionID := original[0].ID
		before, err := store.GetAccount(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, int64(100), before.BalanceNano)

		late := phase10ProviderCost(2, 80, "USD")
		adjusted, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: late})
		require.NoError(t, err)
		require.True(t, adjusted.Applied)
		require.False(t, adjusted.Replayed)
		require.Equal(t, billing.Money{Nano: -20, Currency: "USD"}, adjusted.Delta)
		require.Equal(t, billing.Money{Nano: 80, Currency: "USD"}, adjusted.CurrentAmount)

		// The original settlement row is immutable and the adjustment
		// references it as the correction group; no rewrite happens.
		_, err = store.db.ExecContext(ctx, `UPDATE journal_transactions SET source_key = 'forged' WHERE id = ?`, originalTransactionID)
		require.Error(t, err, "immutable journal transactions reject retroactive rewrites")
		reopened, err := store.GetAccount(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, int64(120), reopened.BalanceNano, "customer is credited only the exact delta")

		// Exact replay has no duplicate effect; the bounded delta equals the
		// original posted amount minus the authoritative cost.
		replay, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: late})
		require.NoError(t, err)
		require.True(t, replay.Replayed)
		require.False(t, replay.Applied)
		journals := correctionRecoveryCustomerJournals(t, store, account.ID)
		require.Len(t, journals, 2, "one provisional debit and one bounded adjustment")

		// Above-bound and incomparable-currency revisions fail closed with no
		// customer effect.
		_, err = store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{
			AccountID: account.ID, CallID: call.CallID, ProviderCost: phase10ProviderCost(3, 130, "USD"),
		})
		require.ErrorIs(t, err, billing.ErrCostPassThroughBoundExceeded)
		_, err = store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{
			AccountID: account.ID, CallID: call.CallID, ProviderCost: phase10ProviderCost(3, 80, "EUR"),
		})
		require.ErrorIs(t, err, billing.ErrCostPassThroughCurrencyMismatch)
		after, err := store.GetAccount(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, int64(120), after.BalanceNano)
		require.Len(t, correctionRecoveryCustomerJournals(t, store, account.ID), 2)
	})

	t.Run("pending policy ignores a late adjustment", func(t *testing.T) {
		store, ctx, account, call, exposure, state := phase10CostPassThroughStoreFixture(t, false, 0)
		state.Status = billing.CostPassThroughSettlementPending
		state.Policy.MissingCost = billing.CostPassThroughMissingCostPending
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: call, Exposure: exposure,
			Result: billing.CallRatingResult{CallID: call.CallID, CustomerCharge: billing.Money{Nano: 0, Currency: "USD"}, Fingerprint: "pass-through-pending-cert", CostPassThrough: &state},
		}); err != nil {
			t.Fatal(err)
		}
		result, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: phase10ProviderCost(2, 80, "USD")})
		require.NoError(t, err)
		require.True(t, result.Ignored)
		require.False(t, result.Applied)
		got, err := store.GetAccount(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, account.BalanceNano, got.BalanceNano)
	})
}
