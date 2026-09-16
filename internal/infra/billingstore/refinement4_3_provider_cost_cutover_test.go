package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type refinement43LegacyProviderResolver struct {
	amount billing.Money
}

type refinement43FailingLegacyProviderResolver struct{}

func (refinement43FailingLegacyProviderResolver) ResolveProviderCost(context.Context, billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	return billing.OperatorCostResult{}, fmt.Errorf("legacy resolver must not run after revision cutover")
}

func (r refinement43LegacyProviderResolver) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	sealed, err := leg.Seal()
	if err != nil {
		return billing.OperatorCostResult{}, err
	}
	return billing.OperatorCostResult{
		LURKey: sealed.Key, Amount: r.amount, AmountPresent: true,
		Reconciled: true, Authoritative: true,
	}, nil
}

func refinement43LegacyCutoverInput(accountID string, callID billing.BillingCallID, leg billing.CallLegUsageRecord, amount billing.Money) billing.ProviderCostRevisionInput {
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "test", AccountID: accountID,
		ALegID: leg.ALegID, BillingCallID: callID.String(), BLegID: leg.BLegID,
	}
	amountDecimal := metering.DecimalFromNanoUnits(amount.Nano)
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "refinement43-revision-cutover",
		Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator},
		Observations: []metering.Observation{{
			Version: metering.ObservationVersionV2, ID: "refinement43-cutover-provider-charge",
			SourceEventKey: "refinement43-cutover-provider-charge", Revision: 1,
			StreamID: "refinement43-cutover-provider-stream", Sequence: 1, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
			Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID,
				BillingCallID: subject.BillingCallID, BLegID: subject.BLegID},
			Semantics: metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(),
			ReceivedAt: time.Unix(100, 0).UTC(), MappingRef: "refinement43.cutover",
			Charges: []metering.ReportedCharge{{ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate,
				Amount: &amountDecimal, Currency: amount.Currency, Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator}}},
		}},
		Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "refinement43-rater", Version: "v1"}, RaterID: "reference"},
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject,
		// Deliberately use a revision head identity different from the legacy
		// LUR key. The durable lineage fence, not process-local coordination,
		// must identify these as one B-leg economic stream.
		HeadKey: "revision-cutover-head", EvidenceRevision: 1,
		InputSetHash: strings.Repeat("a", 64), ValuationID: "revision-cutover-valuation",
		Cost: billing.OperatorCOGSResult{
			KnownSubtotalByCurrency: map[string]billing.Money{amount.Currency: amount},
			KnownSubtotal:           amount, Completeness: billing.CostCompletenessKnown,
			Payable: true, IncludedLegKeys: []string{leg.Key},
		},
		Authoritative: true, Evidence: evidence,
	}
}

func refinement43AppendLegacyWork(t *testing.T, store *DurableStore, account billing.Account, callID billing.BillingCallID, leg billing.CallLegUsageRecord) {
	t.Helper()
	ctx := context.Background()
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID,
		AccountID: account.ID, ALegID: leg.ALegID, SessionID: "session-cutover",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.TurnOutcomeCompleted, ExpectedBLegIDs: []string{leg.BLegID},
	}
	require.NoError(t, store.AppendCallUsage(ctx, call))
	require.NoError(t, store.AppendCallLegUsage(ctx, leg))
}

func refinement43ProviderCostCountAndTotal(t *testing.T, store *DurableStore, accountID string) (int, int64) {
	t.Helper()
	journals := refinement43ProviderJournals(t, store, accountID)
	var total int64
	for _, journal := range journals {
		for _, entry := range journal.Entries {
			if entry.LedgerAccount != "inference_provider_cogs" {
				continue
			}
			if entry.Side == billing.JournalDebit {
				total += entry.Amount.Nano
			} else if entry.Side == billing.JournalCredit {
				total -= entry.Amount.Nano
			}
		}
	}
	return len(journals), total
}

func refinement43RunLegacyCutoverOrder(t *testing.T, legacyFirst bool) {
	t.Helper()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-cutover-order", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	// Each subtest owns its own in-memory database, so the fixed account ID is
	// intentionally scoped to the test rather than generated.
	if !strings.HasSuffix(t.Name(), "/legacy-first") {
		account.ID += "-revision-first"
	}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000045")
	leg := testIndependentCallLegFor(callID, "b-cutover")
	leg.ALegID = "a-cutover"
	leg, err := leg.Seal()
	require.NoError(t, err)
	amount := billing.Money{Nano: 11, Currency: "USD"}
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	revision := refinement43LegacyCutoverInput(account.ID, callID, leg, amount)

	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: amount}, 1)
	require.NoError(t, err)
	if legacyFirst {
		require.NoError(t, legacyWorker.ProcessOnce(ctx))
		_, err = store.ApplyProviderCostRevision(ctx, revision)
		require.NoError(t, err)
	} else {
		_, err = store.ApplyProviderCostRevision(ctx, revision)
		require.NoError(t, err)
		require.NoError(t, legacyWorker.ProcessOnce(ctx))
	}

	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, 1, count)
	require.Equal(t, amount.Nano, total)
	report, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 10}})
	require.NoError(t, err)
	require.Equal(t, amount.Nano, report.ProviderCost.Nano)
	state, err := store.GetProviderCostWorkState(ctx, leg.Key)
	require.NoError(t, err)
	require.Equal(t, "processed", state.Status)
}

func TestRefinement43ProviderCostLegacyAndRevisionShareDurablePostingAuthority(t *testing.T) {
	t.Run("legacy-first", func(t *testing.T) { refinement43RunLegacyCutoverOrder(t, true) })
	t.Run("revision-first", func(t *testing.T) { refinement43RunLegacyCutoverOrder(t, false) })
}

func TestRefinement43ProviderCostLegacyAndRevisionConcurrentLoserIsFenced(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-cutover-concurrent", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000052")
	leg, err := testIndependentCallLegFor(callID, "b-cutover-concurrent").Seal()
	require.NoError(t, err)
	amount := billing.Money{Nano: 23, Currency: "USD"}
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	revision := refinement43LegacyCutoverInput(account.ID, callID, leg, amount)
	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: amount}, 1)
	require.NoError(t, err)
	legacyErr := make(chan error, 1)
	revisionErr := make(chan error, 1)
	go func() { legacyErr <- legacyWorker.ProcessOnce(ctx) }()
	go func() {
		_, applyErr := store.ApplyProviderCostRevision(ctx, revision)
		revisionErr <- applyErr
	}()
	require.NoError(t, <-legacyErr)
	require.NoError(t, <-revisionErr)
	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, 1, count)
	require.Equal(t, amount.Nano, total)
	state, err := store.GetProviderCostWorkState(ctx, leg.Key)
	require.NoError(t, err)
	require.Equal(t, "processed", state.Status)
}

func TestRefinement43ProviderCostLegacyAndRevisionCutoverSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)", filepath.ToSlash(filepath.Join(t.TempDir(), "billing.db")))
	open := func() (*DurableStore, *sql.DB) {
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(8)
		bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
		require.NoError(t, err)
		store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
		require.NoError(t, err)
		return store, sqlDB
	}
	store, sqlDB := open()
	account := billing.Account{ID: "refinement43-cutover-restart", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000046")
	leg := testIndependentCallLegFor(callID, "b-cutover-restart")
	leg, err := leg.Seal()
	require.NoError(t, err)
	amount := billing.Money{Nano: 13, Currency: "USD"}
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	revision := refinement43LegacyCutoverInput(account.ID, callID, leg, amount)
	// Leave the legacy work pending, then let the revision writer claim the
	// durable posting identity before the process is restarted.
	_, err = store.ApplyProviderCostRevision(ctx, revision)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, sqlDB.Close())

	reopened, reopenedSQL := open()
	t.Cleanup(func() { _ = reopened.Close(); _ = reopenedSQL.Close() })
	legacyWorker, err := billing.NewCallProviderCostWorker(reopened, reopened, refinement43LegacyProviderResolver{amount: amount}, 1)
	require.NoError(t, err)
	require.NoError(t, legacyWorker.ProcessOnce(ctx))
	count, total := refinement43ProviderCostCountAndTotal(t, reopened, account.ID)
	require.Equal(t, 1, count)
	require.Equal(t, amount.Nano, total)
	report, err := reopened.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 10}})
	require.NoError(t, err)
	require.Equal(t, amount.Nano, report.ProviderCost.Nano)
}

func TestRefinement43ProviderCostRevisionRecoversLegacyJournalWithoutDuplicate(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-cutover-legacy-recovery", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000053")
	leg, err := testIndependentCallLegFor(callID, "b-cutover-legacy-recovery").Seal()
	require.NoError(t, err)
	amount := billing.Money{Nano: 29, Currency: "USD"}
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: amount}, 1)
	require.NoError(t, err)
	require.NoError(t, legacyWorker.ProcessOnce(ctx))
	// Simulate a database upgraded from the legacy writer immediately before
	// the fence migration: the journal and operation marker survive, but the
	// new fence row does not.
	_, err = store.db.NewRaw(`DELETE FROM billing_provider_cost_posting_fences WHERE account_id = ? AND call_id = ?`, account.ID, callID.String()).Exec(ctx)
	require.NoError(t, err)
	revision := refinement43LegacyCutoverInput(account.ID, callID, leg, amount)
	_, err = store.ApplyProviderCostRevision(ctx, revision)
	require.NoError(t, err)
	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, 1, count)
	require.Equal(t, amount.Nano, total)
	var authority string
	require.NoError(t, store.db.NewRaw(`SELECT authority FROM billing_provider_cost_posting_fences WHERE account_id = ? AND call_id = ?`, account.ID, callID.String()).Scan(ctx, &authority))
	require.Equal(t, providerCostFenceAuthorityRevision, authority)
}

func TestRefinement43ProviderCostCutoverKeepsRevisionCorrectionDeltaSingleWriter(t *testing.T) {
	for _, legacyFirst := range []bool{true, false} {
		name := "revision-first"
		if legacyFirst {
			name = "legacy-first"
		}
		t.Run(name, func(t *testing.T) {
			store := newSQLiteTestStore(t)
			ctx := context.Background()
			account := billing.Account{ID: "refinement43-cutover-correction-" + name, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
			require.NoError(t, store.CreateAccount(ctx, account))
			callID := billing.BillingCallID("bc_00000000000000000000000000000047")
			leg, err := testIndependentCallLegFor(callID, "b-cutover-correction").Seal()
			require.NoError(t, err)
			firstAmount := billing.Money{Nano: 17, Currency: "USD"}
			refinement43AppendLegacyWork(t, store, account, callID, leg)
			legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: firstAmount}, 1)
			require.NoError(t, err)
			first := refinement43LegacyCutoverInput(account.ID, callID, leg, firstAmount)
			if legacyFirst {
				require.NoError(t, legacyWorker.ProcessOnce(ctx))
			} else {
				require.NoError(t, func() error { _, err := store.ApplyProviderCostRevision(ctx, first); return err }())
				require.NoError(t, legacyWorker.ProcessOnce(ctx))
			}
			correction := first
			correction.EvidenceRevision = 2
			correction.InputSetHash = strings.Repeat("b", 64)
			correction.ValuationID = "revision-cutover-correction"
			correction.Cost.KnownSubtotalByCurrency["USD"] = billing.Money{Nano: 9, Currency: "USD"}
			correction.Cost.KnownSubtotal = billing.Money{Nano: 9, Currency: "USD"}
			correction.Evidence = correction.Evidence.Clone()
			correctionAmount := metering.DecimalFromNanoUnits(9)
			correction.Evidence.Observations[0].Charges[0].Amount = &correctionAmount
			_, err = store.ApplyProviderCostRevision(ctx, correction)
			require.NoError(t, err)
			// A pending legacy retry after the correction remains a replay, not a
			// third journal or a rollback of the durable selected-cost head.
			require.NoError(t, legacyWorker.ProcessOnce(ctx))
			count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
			require.Equal(t, 2, count)
			require.Equal(t, int64(9), total)
			report, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 10}})
			require.NoError(t, err)
			require.Equal(t, int64(9), report.ProviderCost.Nano)
		})
	}
}

func TestRefinement43ProviderCostRevisionExclusionFencesLegacyProviderWork(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-cutover-exclusion", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000048")
	leg, err := testIndependentCallLegFor(callID, "b-cutover-exclusion").Seal()
	require.NoError(t, err)
	amount := billing.Money{Nano: 19, Currency: "USD"}
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	revision := refinement43LegacyCutoverInput(account.ID, callID, leg, amount)
	revision.Cost.KnownSubtotalByCurrency = map[string]billing.Money{"USD": {Currency: "USD"}}
	revision.Cost.KnownSubtotal = billing.Money{Currency: "USD"}
	revision.Cost.Payable = false
	revision.Cost.IncludedLegKeys = nil
	ignored, err := store.ApplyProviderCostRevision(ctx, revision)
	require.NoError(t, err)
	require.True(t, ignored.Ignored)
	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: amount}, 1)
	require.NoError(t, err)
	require.NoError(t, legacyWorker.ProcessOnce(ctx))
	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Zero(t, count)
	require.Zero(t, total)
	report, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 10}})
	require.NoError(t, err)
	require.Zero(t, report.ProviderCost.Nano)
	state, err := store.GetProviderCostWorkState(ctx, leg.Key)
	require.NoError(t, err)
	require.Equal(t, "processed", state.Status)
}

func refinement43CutoverEconomicWork(t *testing.T, accountID string, callID billing.BillingCallID, leg billing.CallLegUsageRecord, amount string) billing.EconomicRevisionWork {
	t.Helper()
	value, err := metering.ParseDecimal(amount)
	require.NoError(t, err)
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "test", AccountID: accountID,
		ALegID: leg.ALegID, BillingCallID: callID.String(), BLegID: leg.BLegID,
	}
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "cutover-economic-charge", SourceEventKey: "cutover-economic-charge", Revision: 1,
		StreamID: "cutover-economic-stream", Sequence: 1, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID},
		Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(1_700_043_101, 0).UTC(), ReceivedAt: time.Unix(1_700_043_101, 0).UTC(), MappingRef: "cutover-economic.v1",
		Charges: []metering.ReportedCharge{{ChargeItemID: "cutover-economic-charge", Amount: &value, Currency: "USD", Kind: metering.ChargeKindAggregate, Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator}}},
	}
	return billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: "revision-worker-cutover-head", Subject: subject,
		EvidenceRevision: 1, Input: economics.PostUsageRatingInput{Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported, Subject: subject, Scope: "call", Observations: []metering.Observation{observation}},
		CreatedAt: time.Unix(1_700_043_102, 0).UTC(),
	}
}

func TestRefinement43ProviderCostRevisionWorkerAndLegacyWorkerUseOneDurableWriter(t *testing.T) {
	for _, legacyFirst := range []bool{true, false} {
		name := "revision-first"
		if legacyFirst {
			name = "legacy-first"
		}
		t.Run(name, func(t *testing.T) {
			store := newSQLiteTestStore(t)
			ctx := context.Background()
			account := billing.Account{ID: "refinement43-worker-cutover-" + name, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
			require.NoError(t, store.CreateAccount(ctx, account))
			callID := billing.BillingCallID("bc_00000000000000000000000000000049")
			leg, err := testIndependentCallLegFor(callID, "b-worker-cutover").Seal()
			require.NoError(t, err)
			leg.ALegID = "a-worker-cutover"
			leg, err = leg.Seal()
			require.NoError(t, err)
			amount := billing.Money{Nano: 11, Currency: "USD"}
			refinement43AppendLegacyWork(t, store, account, callID, leg)
			work := refinement43CutoverEconomicWork(t, account.ID, callID, leg, "0.000000011")
			require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
			legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: amount}, 1)
			require.NoError(t, err)
			economicWorker, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, refinement43DurableRater{}, store, billing.EconomicQueueProvider, 1)
			require.NoError(t, err)
			if legacyFirst {
				require.NoError(t, legacyWorker.ProcessOnce(ctx))
				require.NoError(t, economicWorker.ProcessOnce(ctx))
			} else {
				require.NoError(t, economicWorker.ProcessOnce(ctx))
				require.NoError(t, legacyWorker.ProcessOnce(ctx))
			}
			count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
			require.Equal(t, 1, count)
			require.Equal(t, amount.Nano, total)
			report, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 10}})
			require.NoError(t, err)
			require.Equal(t, amount.Nano, report.ProviderCost.Nano)
		})
	}
}

func TestRefinement43ProviderCostRevisionFenceRetiresLegacyWorkBeforeResolverFailure(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-worker-cutover-failure", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000050")
	leg, err := testIndependentCallLegFor(callID, "b-worker-cutover-failure").Seal()
	require.NoError(t, err)
	amount := billing.Money{Nano: 11, Currency: "USD"}
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	work := refinement43CutoverEconomicWork(t, account.ID, callID, leg, "0.000000011")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	economicWorker, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, refinement43DurableRater{}, store, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, economicWorker.ProcessOnce(ctx))
	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43FailingLegacyProviderResolver{}, 1)
	require.NoError(t, err)
	require.NoError(t, legacyWorker.ProcessOnce(ctx))
	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, 1, count)
	require.Equal(t, amount.Nano, total)
	var markers int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind = 'provider_cost_unreconciled'`, account.ID).Scan(ctx, &markers))
	require.Zero(t, markers)
	state, err := store.GetProviderCostWorkState(ctx, leg.Key)
	require.NoError(t, err)
	require.Equal(t, "processed", state.Status)
}

func TestRefinement43ProviderCostLegacyFenceCorrectionRollsBackAndRetries(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-cutover-correction-retry", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000051")
	leg, err := testIndependentCallLegFor(callID, "b-cutover-correction-retry").Seal()
	require.NoError(t, err)
	firstAmount := billing.Money{Nano: 17, Currency: "USD"}
	refinement43AppendLegacyWork(t, store, account, callID, leg)
	legacyWorker, err := billing.NewCallProviderCostWorker(store, store, refinement43LegacyProviderResolver{amount: firstAmount}, 1)
	require.NoError(t, err)
	require.NoError(t, legacyWorker.ProcessOnce(ctx))

	correction := refinement43LegacyCutoverInput(account.ID, callID, leg, billing.Money{Nano: 9, Currency: "USD"})
	correction.EvidenceRevision = 2
	correction.InputSetHash = strings.Repeat("c", 64)
	correction.ValuationID = "revision-cutover-correction-retry"
	sentinel := fmt.Errorf("refinement43 legacy-fence correction crash")
	store.SetEconomicFaultHook(func(stage string) error {
		if stage == "after_provider_cost_revision" {
			return sentinel
		}
		return nil
	})
	_, err = store.ApplyProviderCostRevision(ctx, correction)
	require.ErrorIs(t, err, sentinel)
	count, total := refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, 1, count)
	require.Equal(t, firstAmount.Nano, total)
	_, err = store.GetProviderCostHead(ctx, account.ID, callID, correction.HeadKey)
	require.ErrorIs(t, err, billing.ErrProviderCostHeadNotFound)
	var authority string
	require.NoError(t, store.db.NewRaw(`SELECT authority FROM billing_provider_cost_posting_fences WHERE account_id = ? AND call_id = ?`, account.ID, callID.String()).Scan(ctx, &authority))
	require.Equal(t, providerCostFenceAuthorityLegacy, authority)

	store.SetEconomicFaultHook(nil)
	corrected, err := store.ApplyProviderCostRevision(ctx, correction)
	require.NoError(t, err)
	require.True(t, corrected.Applied)
	require.Equal(t, int64(-8), corrected.Delta.Nano)
	count, total = refinement43ProviderCostCountAndTotal(t, store, account.ID)
	require.Equal(t, 2, count)
	require.Equal(t, int64(9), total)
}
