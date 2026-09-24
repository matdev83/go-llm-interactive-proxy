package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// refinement43PartialProviderRevision is trusted provider evidence whose
// coverage is deliberately unavailable to the monetary reducer. It is still a
// durable revision of the execution, so the cross-path execution fence must be
// claimed even though no provider journal amount is selected.
func refinement43PartialProviderRevision(t *testing.T, accountID string, callID billing.BillingCallID, leg billing.CallLegUsageRecord, revision uint64) billing.ProviderCostRevisionInput {
	t.Helper()
	input := refinement43LegacyCutoverInput(accountID, callID, leg, billing.Money{Nano: 0, Currency: "USD"})
	ref, err := input.Evidence.Observations[0].Ref(input.Subject.StoreID)
	require.NoError(t, err)
	input.Evidence.Observations = nil
	input.Evidence.ObservationRefs = []metering.ObservationRef{ref}
	input.EvidenceRevision = revision
	input.Revision = revision
	input.InputSetHash = fmt.Sprintf("%064x", revision)
	input.ValuationID = fmt.Sprintf("refinement43-partial-execution-fence-%d", revision)
	input.Cost = billing.OperatorCOGSResult{
		Completeness: billing.CostCompletenessPartial,
		Payable:      false,
	}
	input.Authoritative = false
	return input
}

func refinement43ProviderChargeRevision(t *testing.T, accountID string, callID billing.BillingCallID, leg billing.CallLegUsageRecord, chargeID string, revision uint64, amount int64) billing.ProviderCostRevisionInput {
	t.Helper()
	input := refinement43LegacyCutoverInput(accountID, callID, leg, billing.Money{Nano: amount, Currency: "USD"})
	subject := input.Subject
	subject.Kind = metering.SubjectProviderCharge
	subject.ProviderAccountKey = "provider-account-43"
	subject.ProviderChargeID = chargeID
	input.Subject = subject
	input.HeadKey = "provider-charge-head-" + chargeID
	input.EvidenceRevision = revision
	input.Revision = revision
	input.InputSetHash = fmt.Sprintf("%064x", revision)
	input.ValuationID = "provider-charge-valuation-" + chargeID
	input.Evidence = input.Evidence.Clone()
	input.Evidence.Subject = subject
	input.Evidence.Observations[0].ID = "provider-charge-observation-" + chargeID
	input.Evidence.Observations[0].SourceEventKey = "provider-charge-event-" + chargeID
	input.Evidence.Observations[0].Subject = subject
	input.Evidence.Observations[0].Correlation.ProviderAccountKey = subject.ProviderAccountKey
	input.Evidence.Observations[0].Correlation.ProviderChargeID = chargeID
	input.Evidence.Observations[0].Charges[0].ChargeItemID = "provider-charge-item-" + chargeID
	input.Cost.IncludedLegKeys = []string{leg.Key}
	return input
}

func refinement43ExecutionFence(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID) (authority, subjectKind, headKey string) {
	t.Helper()
	ctx := context.Background()
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_provider_cost_execution_fences WHERE store_id = ? AND account_id = ? AND call_id = ?`, store.storeID, accountID, callID.String()).Scan(ctx, &count))
	require.Equal(t, 1, count)
	require.NoError(t, store.db.NewRaw(`SELECT authority, owner_subject_kind, owner_head_key FROM billing_provider_cost_execution_fences WHERE store_id = ? AND account_id = ? AND call_id = ?`, store.storeID, accountID, callID.String()).Scan(ctx, &authority, &subjectKind, &headKey))
	return authority, subjectKind, headKey
}

func TestRefinement43PartialProviderRevisionFencesBeforeLegacyResolution(t *testing.T) {
	for _, legacyFirst := range []bool{false, true} {
		name := "revision-first"
		if legacyFirst {
			name = "legacy-first"
		}
		t.Run(name, func(t *testing.T) {
			store := newSQLiteTestStore(t)
			ctx := context.Background()
			account := billing.Account{ID: "refinement43-partial-fence-" + name, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
			require.NoError(t, store.CreateAccount(ctx, account))
			callID := billing.BillingCallID("bc_00000000000000000000000000000061")
			leg, err := testIndependentCallLegFor(callID, "b-partial-fence").Seal()
			require.NoError(t, err)
			refinement43AppendLegacyWork(t, store, account, callID, leg)
			legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: billing.Money{Nano: 11, Currency: "USD"}}, 1)
			require.NoError(t, err)
			if legacyFirst {
				require.NoError(t, legacyWorker.ProcessOnce(ctx))
			}

			partial := refinement43PartialProviderRevision(t, account.ID, callID, leg, 1)
			result, err := store.ApplyProviderCostRevision(ctx, partial)
			require.NoError(t, err)
			require.True(t, result.Ignored)
			count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
			if legacyFirst {
				require.Equal(t, 1, count)
				require.Equal(t, int64(11), total)
			} else {
				require.Zero(t, count)
				require.Zero(t, total)
			}
			authority, subjectKind, headKey := refinement43ExecutionFence(t, store, account.ID, callID)
			wantAuthority := providerCostFenceAuthorityRevision
			wantHeadKey := partial.HeadKey
			if legacyFirst {
				wantAuthority = providerCostFenceAuthorityLegacy
				wantHeadKey = leg.Key
			}
			require.Equal(t, wantAuthority, authority)
			require.Equal(t, string(metering.SubjectBLeg), subjectKind)
			require.Equal(t, wantHeadKey, headKey)

			// The legacy resolver is never allowed to monetize after the partial
			// revision has fenced this execution.
			if legacyFirst {
				require.NoError(t, legacyWorker.ProcessOnce(ctx))
			}
			count, total = refinement43ProviderCostCountAndTotal(t, store, account.ID)
			if legacyFirst {
				require.Equal(t, int64(11), total)
			} else {
				require.Zero(t, count)
				require.Zero(t, total)
			}

			payable := refinement43LegacyCutoverInput(account.ID, callID, leg, billing.Money{Nano: 11, Currency: "USD"})
			payable.EvidenceRevision = 2
			payable.Revision = 2
			payable.InputSetHash = strings.Repeat("b", 64)
			payable.ValuationID = "refinement43-partial-fence-payable"
			posted, err := store.ApplyProviderCostRevision(ctx, payable)
			require.NoError(t, err)
			require.True(t, posted.Applied || posted.Replayed)
			count, total = refinement43ProviderCostCountAndTotal(t, store, account.ID)
			require.Equal(t, 1, count)
			require.Equal(t, int64(11), total)
		})
	}
}

func TestRefinement43ProviderChargeAndLegacyAggregateShareExecutionFence(t *testing.T) {
	for _, legacyFirst := range []bool{false, true} {
		name := "provider-charge-first"
		if legacyFirst {
			name = "legacy-aggregate-first"
		}
		t.Run(name, func(t *testing.T) {
			store := newSQLiteTestStore(t)
			ctx := context.Background()
			account := billing.Account{ID: "refinement43-charge-fence-" + name, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
			require.NoError(t, store.CreateAccount(ctx, account))
			callID := billing.BillingCallID("bc_00000000000000000000000000000062")
			leg, err := testIndependentCallLegFor(callID, "b-charge-fence").Seal()
			require.NoError(t, err)
			refinement43AppendLegacyWork(t, store, account, callID, leg)
			legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: billing.Money{Nano: 10, Currency: "USD"}}, 1)
			require.NoError(t, err)
			if legacyFirst {
				require.NoError(t, legacyWorker.ProcessOnce(ctx))
			}

			first := refinement43ProviderChargeRevision(t, account.ID, callID, leg, "charge-1", 1, 10)
			firstResult, err := store.ApplyProviderCostRevision(ctx, first)
			require.NoError(t, err)
			if legacyFirst {
				require.True(t, firstResult.Replayed || firstResult.Ignored)
			} else {
				require.True(t, firstResult.Applied)
			}
			count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
			require.Equal(t, 1, count)
			require.Equal(t, int64(10), total)

			// A second, distinct provider charge adds its exact amount only when
			// V2 owns the execution; a legacy-first aggregate fences all children.
			second := refinement43ProviderChargeRevision(t, account.ID, callID, leg, "charge-2", 1, 5)
			secondResult, err := store.ApplyProviderCostRevision(ctx, second)
			require.NoError(t, err)
			if legacyFirst {
				require.True(t, secondResult.Ignored || secondResult.Replayed)
			} else {
				require.True(t, secondResult.Applied)
			}
			count, total = refinement43ProviderCostCountAndTotal(t, store, account.ID)
			wantCount, wantTotal := 2, int64(15)
			if legacyFirst {
				wantCount, wantTotal = 1, 10
			}
			require.Equal(t, wantCount, count)
			require.Equal(t, wantTotal, total)

			if legacyFirst {
				require.NoError(t, legacyWorker.ProcessOnce(ctx))
			} else {
				failingWorker, workerErr := billing.NewCallProviderCostWorker(store, store, refinement43FailingLegacyProviderResolver{}, 1)
				require.NoError(t, workerErr)
				require.NoError(t, failingWorker.ProcessOnce(ctx))
			}
			count, total = refinement43ProviderCostCountAndTotal(t, store, account.ID)
			require.Equal(t, wantCount, count)
			require.Equal(t, wantTotal, total)
			report, reportErr := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 10}})
			require.NoError(t, reportErr)
			require.Equal(t, wantTotal, report.ProviderCost.Nano)
			authority, subjectKind, headKey := refinement43ExecutionFence(t, store, account.ID, callID)
			if legacyFirst {
				require.Equal(t, providerCostFenceAuthorityLegacy, authority)
				require.Equal(t, string(metering.SubjectBLeg), subjectKind)
				require.Equal(t, leg.Key, headKey)
			} else {
				require.Equal(t, providerCostFenceAuthorityRevision, authority)
				require.Equal(t, string(metering.SubjectProviderCharge), subjectKind)
				// The shared gate names the most recently applied child:
				// every applied child revision advances it atomically.
				require.Equal(t, second.HeadKey, headKey)
			}
		})
	}
}

func TestRefinement43ProviderChargeRevisionCorrectionUsesExactDelta(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-charge-correction-fence", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000067")
	leg, err := testIndependentCallLegFor(callID, "b-charge-correction-fence").Seal()
	require.NoError(t, err)
	refinement43AppendLegacyWork(t, store, account, callID, leg)

	first := refinement43ProviderChargeRevision(t, account.ID, callID, leg, "charge-correction", 1, 10)
	posted, err := store.ApplyProviderCostRevision(ctx, first)
	require.NoError(t, err)
	require.True(t, posted.Applied)
	require.Equal(t, int64(10), posted.Delta.Nano)

	correction := first
	correction.EvidenceRevision = 2
	correction.Revision = 2
	correction.InputSetHash = strings.Repeat("c", 64)
	correction.ValuationID = "provider-charge-correction-v2"
	correction.Cost.KnownSubtotalByCurrency = map[string]billing.Money{"USD": {Nano: 6, Currency: "USD"}}
	correction.Cost.KnownSubtotal = billing.Money{Nano: 6, Currency: "USD"}
	correction.Evidence = correction.Evidence.Clone()
	correctionAmount := metering.DecimalFromNanoUnits(6)
	correction.Evidence.Observations[0].Charges[0].Amount = &correctionAmount
	corrected, err := store.ApplyProviderCostRevision(ctx, correction)
	require.NoError(t, err)
	require.True(t, corrected.Applied)
	require.Equal(t, int64(-4), corrected.Delta.Nano)
	require.Equal(t, int64(6), corrected.CurrentAmount.Nano)

	replayed, err := store.ApplyProviderCostRevision(ctx, correction)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	reordered, err := store.ApplyProviderCostRevision(ctx, first)
	require.NoError(t, err)
	require.True(t, reordered.Stale)

	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43FailingLegacyProviderResolver{}, 1)
	require.NoError(t, err)
	require.NoError(t, legacyWorker.ProcessOnce(ctx))
	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, 2, count)
	require.Equal(t, int64(6), total)
	journals := refinement43ProviderJournals(t, store, account.ID)
	require.Equal(t, int64(4), journals[1].Entries[0].Amount.Nano)
	require.Equal(t, "provider_payable_clearing", journals[1].Entries[0].LedgerAccount)
	require.Equal(t, billing.JournalDebit, journals[1].Entries[0].Side)
	require.Equal(t, "inference_provider_cogs", journals[1].Entries[1].LedgerAccount)
	require.Equal(t, billing.JournalCredit, journals[1].Entries[1].Side)

	head, err := store.GetProviderCostHead(ctx, account.ID, callID, first.HeadKey)
	require.NoError(t, err)
	require.Equal(t, int64(6), head.CurrentAmount.Nano)
	require.Equal(t, uint64(2), head.EvidenceRevision)
}

func TestRefinement43ProviderChargeFenceRecoveryBlocksAggregateAfterGateLoss(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-charge-fence-recovery", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000068")
	leg, err := testIndependentCallLegFor(callID, "b-charge-fence-recovery").Seal()
	require.NoError(t, err)
	refinement43AppendLegacyWork(t, store, account, callID, leg)

	child := refinement43ProviderChargeRevision(t, account.ID, callID, leg, "charge-fence-recovery", 1, 10)
	posted, err := store.ApplyProviderCostRevision(ctx, child)
	require.NoError(t, err)
	require.True(t, posted.Applied)
	_, err = store.db.NewRaw(`DELETE FROM billing_provider_cost_execution_fences WHERE account_id = ? AND call_id = ?`, account.ID, callID.String()).Exec(ctx)
	require.NoError(t, err)

	// The child posting fence is the only surviving V2 authority marker. A
	// restart-era legacy retry must recover it before invoking its resolver.
	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43FailingLegacyProviderResolver{}, 1)
	require.NoError(t, err)
	require.NoError(t, legacyWorker.ProcessOnce(ctx))

	aggregate := refinement43LegacyCutoverInput(account.ID, callID, leg, billing.Money{Nano: 10, Currency: "USD"})
	ignored, err := store.ApplyProviderCostRevision(ctx, aggregate)
	require.NoError(t, err)
	require.True(t, ignored.Ignored)
	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, 1, count)
	require.Equal(t, int64(10), total)
	authority, subjectKind, _ := refinement43ExecutionFence(t, store, account.ID, callID)
	require.Equal(t, providerCostFenceAuthorityRevision, authority)
	require.Equal(t, string(metering.SubjectProviderCharge), subjectKind)
}

func TestRefinement43UnavailableProviderChargeFenceTransitionsToPayableWithoutZeroJournal(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-unavailable-charge-fence", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000063")
	leg, err := testIndependentCallLegFor(callID, "b-unavailable-charge-fence").Seal()
	require.NoError(t, err)
	refinement43AppendLegacyWork(t, store, account, callID, leg)

	unavailable := refinement43ProviderChargeRevision(t, account.ID, callID, leg, "charge-unavailable", 1, 9)
	unavailable.Cost = billing.OperatorCOGSResult{Completeness: billing.CostCompletenessPartial, Payable: false}
	unavailable.Authoritative = false
	unavailable.Evidence = unavailable.Evidence.Clone()
	unavailable.Evidence.Observations[0].Authority = metering.AuthorityUnavailableClaim
	ignored, err := store.ApplyProviderCostRevision(ctx, unavailable)
	require.NoError(t, err)
	require.True(t, ignored.Ignored)
	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Zero(t, count)
	require.Zero(t, total)

	payable := refinement43ProviderChargeRevision(t, account.ID, callID, leg, "charge-unavailable", 2, 9)
	payable.InputSetHash = strings.Repeat("b", 64)
	payable.ValuationID = "provider-charge-unavailable-payable"
	posted, err := store.ApplyProviderCostRevision(ctx, payable)
	require.NoError(t, err)
	require.True(t, posted.Applied)
	count, total = refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, 1, count)
	require.Equal(t, int64(9), total)
	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: billing.Money{Nano: 9, Currency: "USD"}}, 1)
	require.NoError(t, err)
	require.NoError(t, legacyWorker.ProcessOnce(ctx))
	count, total = refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, 1, count)
	require.Equal(t, int64(9), total)
}

func TestRefinement43ExecutionFenceRollbackDoesNotPoisonPayableTransition(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-execution-fence-rollback", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000064")
	leg, err := testIndependentCallLegFor(callID, "b-execution-fence-rollback").Seal()
	require.NoError(t, err)
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	partial := refinement43PartialProviderRevision(t, account.ID, callID, leg, 1)
	sentinel := context.DeadlineExceeded
	store.SetEconomicFaultHook(func(stage string) error {
		if stage == "after_provider_cost_revision" {
			return sentinel
		}
		return nil
	})
	_, err = store.ApplyProviderCostRevision(ctx, partial)
	// The fence and its no-money result are one transaction. A fault before
	// commit must leave neither a durable fence nor a journal behind.
	require.ErrorIs(t, err, sentinel)
	var fences int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_provider_cost_execution_fences WHERE account_id = ? AND call_id = ?`, account.ID, callID.String()).Scan(ctx, &fences))
	require.Zero(t, fences)
	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Zero(t, count)
	require.Zero(t, total)

	store.SetEconomicFaultHook(nil)
	_, err = store.ApplyProviderCostRevision(ctx, partial)
	require.NoError(t, err)
	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: billing.Money{Nano: 7, Currency: "USD"}}, 1)
	require.NoError(t, err)
	err = legacyWorker.ProcessOnce(ctx)
	require.NoError(t, err)
	count, total = refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Zero(t, count)
	require.Zero(t, total)
}

func TestRefinement43ProviderChargeFenceConcurrentWithLegacyAndSurvivesRetry(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-execution-fence-concurrent", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000065")
	leg, err := testIndependentCallLegFor(callID, "b-execution-fence-concurrent").Seal()
	require.NoError(t, err)
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: billing.Money{Nano: 10, Currency: "USD"}}, 1)
	require.NoError(t, err)
	first := refinement43ProviderChargeRevision(t, account.ID, callID, leg, "charge-concurrent-1", 1, 10)
	legacyErr := make(chan error, 1)
	revisionErr := make(chan error, 1)
	go func() { legacyErr <- legacyWorker.ProcessOnce(ctx) }()
	go func() {
		_, applyErr := store.ApplyProviderCostRevision(ctx, first)
		revisionErr <- applyErr
	}()
	require.NoError(t, <-legacyErr)
	require.NoError(t, <-revisionErr)

	second := refinement43ProviderChargeRevision(t, account.ID, callID, leg, "charge-concurrent-2", 1, 5)
	secondResult, err := store.ApplyProviderCostRevision(ctx, second)
	require.NoError(t, err)
	authority, subjectKind, _ := refinement43ExecutionFence(t, store, account.ID, callID)
	wantCount, wantTotal := 2, int64(15)
	if authority == providerCostFenceAuthorityLegacy {
		require.Equal(t, string(metering.SubjectBLeg), subjectKind)
		require.True(t, secondResult.Ignored || secondResult.Replayed)
		wantCount, wantTotal = 1, 10
	} else {
		require.Equal(t, string(metering.SubjectProviderCharge), subjectKind)
		require.True(t, secondResult.Applied)
	}
	// Replaying both paths after the race is a durable no-op.
	require.NoError(t, legacyWorker.ProcessOnce(ctx))
	_, err = store.ApplyProviderCostRevision(ctx, first)
	require.NoError(t, err)
	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, wantCount, count)
	require.Equal(t, wantTotal, total)
}

func TestRefinement43PartialExecutionFenceSurvivesRestartBeforePayableRevision(t *testing.T) {
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", filepath.ToSlash(filepath.Join(t.TempDir(), "billing.db")))
	open := func() (*DurableStore, *sql.DB) {
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(8)
		bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
		require.NoError(t, err)
		seedTestSchemaIfEmpty(t, bunDB)
		store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
		require.NoError(t, err)
		return store, sqlDB
	}
	store, sqlDB := open()
	account := billing.Account{ID: "refinement43-execution-fence-restart", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000066")
	leg, err := testIndependentCallLegFor(callID, "b-execution-fence-restart").Seal()
	require.NoError(t, err)
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	partial := refinement43PartialProviderRevision(t, account.ID, callID, leg, 1)
	ignored, err := store.ApplyProviderCostRevision(ctx, partial)
	require.NoError(t, err)
	require.True(t, ignored.Ignored)
	require.NoError(t, store.Close())
	require.NoError(t, sqlDB.Close())

	reopened, reopenedSQL := open()
	t.Cleanup(func() { _ = reopened.Close(); _ = reopenedSQL.Close() })
	legacyWorker, err := billing.NewCallProviderCostWorker(reopened, reopened, refinement43LegacyProviderResolver{amount: billing.Money{Nano: 10, Currency: "USD"}}, 1)
	require.NoError(t, err)
	require.NoError(t, legacyWorker.ProcessOnce(ctx))
	require.Len(t, refinement43ProviderJournals(t, reopened, account.ID), 0)
	payable := refinement43LegacyCutoverInput(account.ID, callID, leg, billing.Money{Nano: 10, Currency: "USD"})
	payable.EvidenceRevision = 2
	payable.Revision = 2
	payable.InputSetHash = strings.Repeat("b", 64)
	payable.ValuationID = "refinement43-execution-fence-restart-payable"
	posted, err := reopened.ApplyProviderCostRevision(ctx, payable)
	require.NoError(t, err)
	require.True(t, posted.Applied)
	count, total := refinement43ProviderCostCountAndTotal(t, reopened, account.ID)
	require.Equal(t, 1, count)
	require.Equal(t, int64(10), total)
}

// c2r1ExecutionFence reads the full durable execution-fence envelope for
// one B-leg lineage: the report must prove every field, not just the
// owner kind and head key.
func c2r1ExecutionFence(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID string) providerCostExecutionFenceRow {
	t.Helper()
	lineage, err := billing.CallLegUsageKey(callID, bLegID)
	require.NoError(t, err)
	var row providerCostExecutionFenceRow
	require.NoError(t, store.db.NewRaw(providerCostExecutionFenceSelect+` WHERE store_id = ? AND account_id = ? AND call_id = ? AND execution_lineage_key = ? LIMIT 1`,
		"test", accountID, callID.String(), lineage).Scan(context.Background(), &row))
	return row
}

func c2r1ExecutionOwner(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID string) (revision uint64, inputHash, fingerprint, operationKey, transactionID string, fence int64) {
	t.Helper()
	row := c2r1ExecutionFence(t, store, accountID, callID, bLegID)
	return uint64(row.OwnerRevision), row.OwnerInputSetHash, row.OwnerFingerprint, row.LastOperationKey, row.LastTransactionID, row.Fence
}

// TestC2R1RevisionAdvancesExistingExecutionFence proves the trusted
// writer advances an existing execution fence atomically with every
// applied revision. A stale owner revision, input hash, fingerprint,
// fence, operation, or transaction must not survive rev2.
func TestC2R1RevisionAdvancesExistingExecutionFence(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "c2r1-execution-advance", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000071")

	first := refinement43ProviderRevisionInput(account.ID, callID, "advance-head", 1, 50, true)
	posted, err := store.ApplyProviderCostRevision(ctx, first)
	require.NoError(t, err)
	require.True(t, posted.Applied)
	firstFP, err := first.SemanticFingerprint()
	require.NoError(t, err)
	rev, hash, fp, op, tx, fence := c2r1ExecutionOwner(t, store, account.ID, callID, "b-leg-43")
	require.Equal(t, uint64(1), rev)
	require.Equal(t, first.InputSetHash, hash)
	require.Equal(t, firstFP, fp)
	require.Equal(t, posted.Posting.OperationKey, op)
	require.Equal(t, posted.Posting.Transaction.ID, tx)

	second := refinement43ProviderRevisionInput(account.ID, callID, "advance-head", 2, 40, true)
	corrected, err := store.ApplyProviderCostRevision(ctx, second)
	require.NoError(t, err)
	require.True(t, corrected.Applied)
	secondFP, err := second.SemanticFingerprint()
	require.NoError(t, err)
	rev, hash, fp, op, tx, nextFence := c2r1ExecutionOwner(t, store, account.ID, callID, "b-leg-43")
	require.Equal(t, uint64(2), rev, "stale owner revision must not survive rev2")
	require.Equal(t, second.InputSetHash, hash)
	require.Equal(t, secondFP, fp)
	require.Equal(t, corrected.Posting.OperationKey, op)
	require.Equal(t, corrected.Posting.Transaction.ID, tx)
	require.Equal(t, fence+1, nextFence, "fence counter must advance monotonically")
}

// TestC2R1ZeroDeltaRevisionAdvancesExecutionFence proves a same-amount
// revision advances the execution owner without posting a monetary
// journal: the head operation pointer moves while the last monetary
// transaction stays put.
func TestC2R1ZeroDeltaRevisionAdvancesExecutionFence(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "c2r1-execution-zerodelta", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000072")

	first := refinement43ProviderRevisionInput(account.ID, callID, "zerodelta-head", 1, 50, true)
	posted, err := store.ApplyProviderCostRevision(ctx, first)
	require.NoError(t, err)
	require.True(t, posted.Applied)
	require.NotEmpty(t, posted.Posting.Transaction.ID)

	second := refinement43ProviderRevisionInput(account.ID, callID, "zerodelta-head", 2, 50, true)
	same, err := store.ApplyProviderCostRevision(ctx, second)
	require.NoError(t, err)
	require.True(t, same.Applied)
	require.Equal(t, int64(0), same.Delta.Nano)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 1,
		"a zero delta posts no monetary journal")
	secondFP, err := second.SemanticFingerprint()
	require.NoError(t, err)
	rev, hash, fp, op, tx, _ := c2r1ExecutionOwner(t, store, account.ID, callID, "b-leg-43")
	require.Equal(t, uint64(2), rev)
	require.Equal(t, second.InputSetHash, hash)
	require.Equal(t, secondFP, fp)
	require.Equal(t, same.Posting.OperationKey, op)
	require.Equal(t, posted.Posting.Transaction.ID, tx,
		"the last monetary transaction stays put across a zero delta")
	head, err := store.GetProviderCostHead(ctx, account.ID, callID, "zerodelta-head")
	require.NoError(t, err)
	require.Equal(t, uint64(2), head.EvidenceRevision)
	require.Equal(t, int64(50), head.CurrentAmount.Nano)
	require.Equal(t, same.Posting.OperationKey, head.LastOperationKey)
	require.Equal(t, posted.Posting.Transaction.ID, head.LastTransactionID)
	var snapshotCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind = 'provider_call_cogs' AND operation_key = ?`,
		account.ID, same.Posting.OperationKey).Scan(ctx, &snapshotCount))
	require.Equal(t, 1, snapshotCount, "the current operation needs its immutable snapshot")
}

// TestC2R1ReplayLeavesExecutionFence guards replay purity: a replayed
// revision commits without advancing any fence or owner pointer.
func TestC2R1ReplayLeavesExecutionFence(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "c2r1-execution-replay", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000073")

	input := refinement43ProviderRevisionInput(account.ID, callID, "replay-head", 1, 50, true)
	posted, err := store.ApplyProviderCostRevision(ctx, input)
	require.NoError(t, err)
	require.True(t, posted.Applied)
	before := c2r1ExecutionFence(t, store, account.ID, callID, "b-leg-43")

	replayed, err := store.ApplyProviderCostRevision(ctx, input)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	after := c2r1ExecutionFence(t, store, account.ID, callID, "b-leg-43")
	require.Equal(t, before, after, "replay must not advance the execution fence")
}

// TestC2R1StaleLeavesExecutionFence guards stale purity: an older
// revision returns without touching the current owner envelope.
func TestC2R1StaleLeavesExecutionFence(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "c2r1-execution-stale", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000074")

	require.NoError(t, func() error {
		_, err := store.ApplyProviderCostRevision(ctx, refinement43ProviderRevisionInput(account.ID, callID, "stale-head", 1, 50, true))
		return err
	}())
	second := refinement43ProviderRevisionInput(account.ID, callID, "stale-head", 2, 40, true)
	_, err := store.ApplyProviderCostRevision(ctx, second)
	require.NoError(t, err)
	before := c2r1ExecutionFence(t, store, account.ID, callID, "b-leg-43")

	first := refinement43ProviderRevisionInput(account.ID, callID, "stale-head", 1, 50, true)
	stale, err := store.ApplyProviderCostRevision(ctx, first)
	require.NoError(t, err)
	require.True(t, stale.Stale)
	after := c2r1ExecutionFence(t, store, account.ID, callID, "b-leg-43")
	require.Equal(t, before, after, "stale revisions must not touch the execution fence")
}

// TestC2R1ExclusionPromotionAdvancesExecutionFence proves a payable
// promotion after a recorded exclusion advances the execution fence to
// the new owner: the exclusion envelope must not survive.
func TestC2R1ExclusionPromotionAdvancesExecutionFence(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "c2r1-execution-promotion", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000075")

	excluded, err := store.ApplyProviderCostRevision(ctx, refinement43ProviderRevisionInput(account.ID, callID, "promotion-head", 1, 12, false))
	require.NoError(t, err)
	require.True(t, excluded.Ignored)

	promoted, err := store.ApplyProviderCostRevision(ctx, refinement43ProviderRevisionInput(account.ID, callID, "promotion-head", 2, 5, true))
	require.NoError(t, err)
	require.True(t, promoted.Applied)
	require.Equal(t, int64(5), promoted.CurrentAmount.Nano)
	head, err := store.GetProviderCostHead(ctx, account.ID, callID, "promotion-head")
	require.NoError(t, err)
	require.Equal(t, uint64(2), head.EvidenceRevision)
	rev, hash, fp, op, tx, _ := c2r1ExecutionOwner(t, store, account.ID, callID, "b-leg-43")
	promotion := refinement43ProviderRevisionInput(account.ID, callID, "promotion-head", 2, 5, true)
	promotionFP, err := promotion.SemanticFingerprint()
	require.NoError(t, err)
	require.Equal(t, uint64(2), rev)
	require.Equal(t, promotion.InputSetHash, hash)
	require.Equal(t, promotionFP, fp)
	require.Equal(t, promoted.Posting.OperationKey, op)
	require.Equal(t, promoted.Posting.Transaction.ID, tx)
}
