package billingstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

const refinement43ProviderCallID billing.BillingCallID = "bc_00000000000000000000000000000043"

func refinement43ProviderRevisionInput(accountID string, callID billing.BillingCallID, headKey string, revision uint64, amount int64, payable bool) billing.ProviderCostRevisionInput {
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "test", AccountID: accountID,
		ALegID: "a-leg-43", BillingCallID: callID.String(), BLegID: "b-leg-43",
	}
	amountDecimal := metering.DecimalFromNanoUnits(amount)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	if !payable {
		payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	}
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "refinement43-provider-cost", Payer: payer,
		Observations: []metering.Observation{{
			Version: metering.ObservationVersionV2, ID: fmt.Sprintf("refinement43-provider-charge-%d", revision),
			SourceEventKey: fmt.Sprintf("refinement43-provider-charge-%d", revision), Revision: revision,
			StreamID: "refinement43-provider-stream", Sequence: revision, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
			Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID,
				BillingCallID: subject.BillingCallID, BLegID: subject.BLegID},
			Semantics: metering.SemanticsCumulative, ObservedAt: time.Unix(43, 0).UTC(),
			ReceivedAt: time.Unix(43, 0).UTC(), MappingRef: "refinement43.provider.cost",
			Charges: []metering.ReportedCharge{{ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate,
				Amount: &amountDecimal, Currency: "USD", Payer: payer}},
		}},
		Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "refinement43-rater", Version: "v1"}, RaterID: "reference"},
	}
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: amount, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: amount, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 payable,
		IncludedLegKeys:         []string{"b-leg-43"},
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: fmt.Sprintf("%064x", revision),
		ValuationID: fmt.Sprintf("provider-valuation-%d", revision), Cost: cost,
		Authoritative: payable, Evidence: evidence,
	}
}

func refinement43ProviderJournals(t *testing.T, store *DurableStore, accountID string) []billing.JournalTransaction {
	t.Helper()
	rows, err := store.JournalTransactions(context.Background(), accountID)
	require.NoError(t, err)
	filtered := make([]billing.JournalTransaction, 0, len(rows))
	for _, row := range rows {
		if row.OperationKind == "provider_call_cogs" {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func TestRefinement43ProviderCostRevisionAccruesBeforeCallClosureAndFencesDelta(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-delta", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))

	first := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "b-leg-43-head", 1, 10, true)
	posted, err := store.ApplyProviderCostRevision(ctx, first)
	require.NoError(t, err)
	require.True(t, posted.Applied)
	require.Equal(t, int64(10), posted.Delta.Nano)
	require.Equal(t, int64(10), posted.CurrentAmount.Nano)
	journals := refinement43ProviderJournals(t, store, account.ID)
	require.Len(t, journals, 1)
	require.Zero(t, journals[0].AccountSequence)
	require.Equal(t, account.BalanceNano, posted.Posting.Before.BalanceNano)
	require.Equal(t, account.BalanceNano, posted.Posting.After.BalanceNano)

	unchanged, err := store.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.BalanceNano, unchanged.BalanceNano)
	require.Equal(t, account.Version, unchanged.Version)

	head, err := store.GetProviderCostHead(ctx, account.ID, refinement43ProviderCallID, first.HeadKey)
	require.NoError(t, err)
	require.Equal(t, uint64(1), head.EvidenceRevision)
	require.Equal(t, uint64(1), head.HeadVersion)
	require.Equal(t, uint64(1), head.Fence)
	require.Equal(t, int64(10), head.CurrentAmount.Nano)
	require.Equal(t, first.Subject, head.Subject)

	correction := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, first.HeadKey, 2, 8, true)
	corrected, err := store.ApplyProviderCostRevision(ctx, correction)
	require.NoError(t, err)
	require.True(t, corrected.Applied)
	require.Equal(t, int64(-2), corrected.Delta.Nano)
	require.Equal(t, int64(8), corrected.CurrentAmount.Nano)
	journals = refinement43ProviderJournals(t, store, account.ID)
	require.Len(t, journals, 2)
	require.Equal(t, correction.HeadKey, journals[1].CorrectionGroupID)
	require.Equal(t, "provider_call_cogs", journals[1].OperationKind)
	require.Equal(t, "provider_payable_clearing", journals[1].Entries[0].LedgerAccount)
	require.Equal(t, billing.JournalDebit, journals[1].Entries[0].Side)
	require.Equal(t, int64(2), journals[1].Entries[0].Amount.Nano)
	require.Equal(t, "inference_provider_cogs", journals[1].Entries[1].LedgerAccount)
	require.Equal(t, billing.JournalCredit, journals[1].Entries[1].Side)

	duplicate, err := store.ApplyProviderCostRevision(ctx, correction)
	require.NoError(t, err)
	require.True(t, duplicate.Replayed)
	require.False(t, duplicate.Applied)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 2)

	reordered, err := store.ApplyProviderCostRevision(ctx, first)
	require.NoError(t, err)
	require.True(t, reordered.Stale)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 2)

	head, err = store.GetProviderCostHead(ctx, account.ID, refinement43ProviderCallID, first.HeadKey)
	require.NoError(t, err)
	require.Equal(t, uint64(2), head.EvidenceRevision)
	require.Equal(t, uint64(2), head.HeadVersion)
	require.Equal(t, uint64(2), head.Fence)
	require.Equal(t, int64(8), head.CurrentAmount.Nano)
	unchanged, err = store.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.BalanceNano, unchanged.BalanceNano)
	require.Equal(t, account.Version, unchanged.Version)
}

func TestRefinement43ProviderPayerCorrectionReversesPriorCOGS(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-payer-correction", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	first := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "payer-correction-head", 1, 10, true)
	posted, err := store.ApplyProviderCostRevision(ctx, first)
	require.NoError(t, err)
	require.True(t, posted.Applied)

	correction := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, first.HeadKey, 2, 10, false)
	corrected, err := store.ApplyProviderCostRevision(ctx, correction)
	require.NoError(t, err)
	require.True(t, corrected.Applied)
	require.Equal(t, int64(-10), corrected.Delta.Nano)
	require.Equal(t, int64(0), corrected.CurrentAmount.Nano)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 2)

	head, err := store.GetProviderCostHead(ctx, account.ID, refinement43ProviderCallID, first.HeadKey)
	require.NoError(t, err)
	require.Equal(t, int64(0), head.CurrentAmount.Nano)
	require.Equal(t, uint64(2), head.EvidenceRevision)
}

type refinement43DurableRater struct{}

func (refinement43DurableRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for _, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return economics.Valuation{}, err
		}
		refs = append(refs, ref)
	}
	return economics.Valuation{
		ID: "refinement43-durable-rater", Version: economics.ValuationVersionV2,
		Perspective: input.Perspective, Basis: input.Basis, Subject: input.Subject,
		Scope: input.Scope, InputObservations: refs, Completeness: economics.CompletenessPartial,
		CreatedAt: time.Unix(1_700_043_000, 0).UTC(),
	}, nil
}

type refinement43UnexpectedRater struct{}

func (refinement43UnexpectedRater) Rate(context.Context, economics.PostUsageRatingInput) (economics.Valuation, error) {
	return economics.Valuation{}, errors.New("refinement43 restart must use the durable valuation probe")
}

func refinement43DurableWork(t *testing.T, accountID string, amount int64) billing.EconomicRevisionWork {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "test", AccountID: accountID,
		ALegID: "a-leg-durable-43", BillingCallID: refinement43ProviderCallID.String(), BLegID: "b-leg-durable-43",
	}
	value, err := metering.ParseDecimal(fmt.Sprintf("%d", amount))
	require.NoError(t, err)
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "refinement43-durable-charge", SourceEventKey: "refinement43-durable-charge", Revision: 1,
		StreamID: "refinement43-durable-stream", Sequence: 1, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID},
		Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(1_700_043_001, 0).UTC(), ReceivedAt: time.Unix(1_700_043_001, 0).UTC(), MappingRef: "refinement43-durable.v1",
		Charges: []metering.ReportedCharge{{ChargeItemID: "refinement43-durable-charge", Amount: &value, Currency: "USD", Kind: metering.ChargeKindAggregate, Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator}}},
	}
	return billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: "refinement43-durable-head", Subject: subject, EvidenceRevision: 1,
		Input:     economics.PostUsageRatingInput{Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported, Subject: subject, Scope: "call", Observations: []metering.Observation{observation}},
		CreatedAt: time.Unix(1_700_043_002, 0).UTC(),
	}
}

func refinement43DurableWorkForBLeg(t *testing.T, accountID, bLegID string, amount int64) billing.EconomicRevisionWork {
	t.Helper()
	work := refinement43DurableWork(t, accountID, amount)
	subject := work.Subject
	subject.BLegID = bLegID
	work.Subject = subject
	work.HeadKey = "refinement43-durable-head-" + bLegID
	work.Input.Subject = subject
	observation := work.Input.Observations[0]
	observation.ID = "refinement43-durable-charge-" + bLegID
	observation.SourceEventKey = observation.ID
	observation.Subject = subject
	observation.Correlation.BLegID = bLegID
	observation.Charges[0].ChargeItemID = observation.ID
	work.Input.Observations[0] = observation
	return work
}

func TestRefinement43EconomicWorkerIncludesEveryPayableBLegAttempt(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-all-legs", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	// These independently scoped revisions represent a failed attempt, a retry,
	// and a parallel loser. Operator COGS owns all three, even though retail
	// settlement would later select at most one surfaced/winning B-leg.
	works := []billing.EconomicRevisionWork{
		refinement43DurableWorkForBLeg(t, account.ID, "b-leg-failed", 3),
		refinement43DurableWorkForBLeg(t, account.ID, "b-leg-retry", 5),
		refinement43DurableWorkForBLeg(t, account.ID, "b-leg-parallel-loser", 2),
	}
	for _, work := range works {
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	}
	worker, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, refinement43DurableRater{}, store, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))

	journals := refinement43ProviderJournals(t, store, account.ID)
	require.Len(t, journals, len(works))
	var total int64
	for _, journal := range journals {
		total += journal.Entries[0].Amount.Nano
	}
	require.Equal(t, int64(10_000_000_000), total)
}

func TestRefinement43EconomicWorkerPostsDurableProviderCostBeforeCallClosure(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-worker", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	work := refinement43DurableWork(t, account.ID, 10)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))

	worker, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, refinement43DurableRater{}, store, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))

	journals := refinement43ProviderJournals(t, store, account.ID)
	require.Len(t, journals, 1)
	require.Equal(t, int64(10_000_000_000), journals[0].Entries[0].Amount.Nano)
	head, err := store.GetProviderCostHead(ctx, account.ID, refinement43ProviderCallID, work.HeadKey)
	require.NoError(t, err)
	require.Equal(t, uint64(1), head.EvidenceRevision)
	require.Equal(t, int64(10_000_000_000), head.CurrentAmount.Nano)
	var callClosureCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM usage_call_records WHERE call_id = ?`, refinement43ProviderCallID.String()).Scan(ctx, &callClosureCount))
	require.Zero(t, callClosureCount)
}

func TestRefinement43EconomicWorkerRestartFencesProviderPostingAfterCrash(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-restart", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	work := refinement43DurableWork(t, account.ID, 10)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	sentinel := errors.New("refinement43 restart crash")
	store.SetEconomicFaultHook(func(stage string) error {
		if stage == "after_provider_cost_revision" {
			return sentinel
		}
		return nil
	})
	first, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, refinement43DurableRater{}, store, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.ErrorIs(t, first.ProcessOnce(ctx), sentinel)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 0)

	store.SetEconomicFaultHook(nil)
	restarted, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, refinement43UnexpectedRater{}, store, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, restarted.ProcessOnce(ctx))
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 1)
	head, err := store.GetProviderCostHead(ctx, account.ID, refinement43ProviderCallID, work.HeadKey)
	require.NoError(t, err)
	require.Equal(t, uint64(1), head.EvidenceRevision)
}

func TestRefinement43ProviderCostRevisionExcludesNonOperatorPayersAndPartialEvidence(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-excluded", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	callID := billing.BillingCallID("bc_00000000000000000000000000000044")

	customer := refinement43ProviderRevisionInput(account.ID, callID, "customer-head", 1, 12, false)
	ignored, err := store.ApplyProviderCostRevision(ctx, customer)
	require.NoError(t, err)
	require.True(t, ignored.Ignored)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 0)
	_, err = store.GetProviderCostHead(ctx, account.ID, callID, customer.HeadKey)
	require.ErrorIs(t, err, billing.ErrProviderCostHeadNotFound)

	partial := refinement43ProviderRevisionInput(account.ID, callID, "partial-head", 1, 12, true)
	partial.Cost.Completeness = billing.CostCompletenessPartial
	partial.Cost.Payable = true
	ignored, err = store.ApplyProviderCostRevision(ctx, partial)
	require.NoError(t, err)
	require.True(t, ignored.Ignored)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 0)
}

func TestRefinement43ProviderCostRevisionRejectsUnconvertedNativeCurrency(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-currency", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	input := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "currency-head", 1, 10, true)
	input.Cost.KnownSubtotalByCurrency["EUR"] = billing.Money{Nano: 4, Currency: "EUR"}

	_, err := store.ApplyProviderCostRevision(ctx, input)
	require.ErrorIs(t, err, billing.ErrMoneyCurrencyMismatch)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 0)
	_, err = store.GetProviderCostHead(ctx, account.ID, refinement43ProviderCallID, input.HeadKey)
	require.ErrorIs(t, err, billing.ErrProviderCostHeadNotFound)
}

func TestRefinement43ProviderCostRevisionSameRevisionUsesDeterministicHashOrder(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-tie", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	first := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "tie-head", 1, 10, true)
	require.NoError(t, func() error {
		_, err := store.ApplyProviderCostRevision(ctx, first)
		return err
	}())
	higher := first
	higher.InputSetHash = strings.Repeat("f", 64)
	higher.ValuationID = "provider-valuation-higher"
	higher.Cost.KnownSubtotalByCurrency = map[string]billing.Money{"USD": {Nano: 12, Currency: "USD"}}
	higher.Cost.KnownSubtotal = billing.Money{Nano: 12, Currency: "USD"}
	higher.Evidence = higher.Evidence.Clone()
	higherAmount := metering.DecimalFromNanoUnits(12)
	higher.Evidence.Observations[0].Charges[0].Amount = &higherAmount
	advanced, err := store.ApplyProviderCostRevision(ctx, higher)
	require.NoError(t, err)
	require.True(t, advanced.Applied)
	require.Equal(t, int64(2), advanced.Delta.Nano)
	lower, err := store.ApplyProviderCostRevision(ctx, first)
	require.NoError(t, err)
	require.True(t, lower.Stale)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 2)
}

func TestRefinement43ProviderCostRevisionEquivalentSelectedSetsReplay(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-set-order", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	first := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "set-order-head", 1, 10, true)
	first.Cost.IncludedLegKeys = []string{"z-leg", "a-leg", "a-leg"}
	first.Cost.UnknownLegKeys = []string{"z-unknown", "a-unknown"}
	first.Cost.PendingCoverage = []metering.ChargeCoverageRef{{Ref: metering.ChargeRef{StoreID: "test", ObservationID: "obs", Revision: 1, ChargeItemID: "charge"}, Relation: metering.CoverageInclusive}}
	require.NoError(t, func() error {
		_, err := store.ApplyProviderCostRevision(ctx, first)
		return err
	}())
	duplicate := first
	duplicate.Cost.IncludedLegKeys = []string{"a-leg", "z-leg"}
	duplicate.Cost.UnknownLegKeys = []string{"a-unknown", "z-unknown", "a-unknown"}
	duplicate.Cost.PendingCoverage = append([]metering.ChargeCoverageRef(nil), first.Cost.PendingCoverage...)
	replayed, err := store.ApplyProviderCostRevision(ctx, duplicate)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.False(t, replayed.Applied)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 1)
}

func TestRefinement43ProviderCostRevisionRollsBackAfterCrashAndRetries(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-retry", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	input := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "retry-head", 1, 10, true)
	sentinel := errors.New("refinement43 provider crash")
	store.SetEconomicFaultHook(func(stage string) error {
		if stage == "after_provider_cost_revision" {
			return sentinel
		}
		return nil
	})
	_, err := store.ApplyProviderCostRevision(ctx, input)
	require.ErrorIs(t, err, sentinel)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 0)
	_, err = store.GetProviderCostHead(ctx, account.ID, refinement43ProviderCallID, input.HeadKey)
	require.ErrorIs(t, err, billing.ErrProviderCostHeadNotFound)

	store.SetEconomicFaultHook(nil)
	retried, err := store.ApplyProviderCostRevision(ctx, input)
	require.NoError(t, err)
	require.True(t, retried.Applied)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 1)
}

func TestRefinement43ProviderCostRevisionParallelSameRevisionConverges(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "refinement43-provider-parallel", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000, State: billing.AccountReady, Version: 1}
	require.NoError(t, store.CreateAccount(ctx, account))
	input := refinement43ProviderRevisionInput(account.ID, refinement43ProviderCallID, "parallel-head", 1, 10, true)

	results := make(chan billing.ProviderCostRevisionResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.ApplyProviderCostRevision(ctx, input)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var applied, replayed int
	for result := range results {
		if result.Applied {
			applied++
		}
		if result.Replayed {
			replayed++
		}
	}
	require.Equal(t, 1, applied)
	require.Equal(t, 1, replayed)
	require.Len(t, refinement43ProviderJournals(t, store, account.ID), 1)
	head, err := store.GetProviderCostHead(ctx, account.ID, refinement43ProviderCallID, input.HeadKey)
	require.NoError(t, err)
	require.Equal(t, uint64(1), head.EvidenceRevision)
	require.Equal(t, uint64(1), head.HeadVersion)
	require.Equal(t, uint64(1), head.Fence)
}
