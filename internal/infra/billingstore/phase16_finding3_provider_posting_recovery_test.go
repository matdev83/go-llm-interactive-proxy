package billingstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// Phase 16 eighth-pass Finding 3 RED: provider-posting recovery must reuse the
// exact allocation-aware valuation identity. These SQLite proofs run the actual
// EconomicRevisionWorker against the actual DurableStore.

type finding3EchoRater struct{}

func (finding3EchoRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	refs := append([]metering.ObservationRef(nil), input.ObservationRefs...)
	if len(refs) == 0 {
		for _, observation := range input.Observations {
			ref, err := observation.Ref(input.Subject.StoreID)
			if err != nil {
				return economics.Valuation{}, err
			}
			refs = append(refs, ref)
		}
	}
	return economics.Valuation{
		ID:                     "finding3-echo-rater",
		Version:                economics.ValuationVersionV2,
		Perspective:            input.Perspective,
		Basis:                  input.Basis,
		Subject:                input.Subject,
		Scope:                  input.Scope,
		InputObservations:      refs,
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), input.AllocationCoverageRefs...),
		Completeness:           economics.CompletenessPartial,
		CreatedAt:              time.Unix(1_700_500_000, 0).UTC(),
	}, nil
}

func finding3ProviderObservation(t *testing.T, accountID string, callID billing.BillingCallID, bLegID string, revision uint64, amount string, payer metering.PaymentParty) metering.Observation {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "test", AccountID: accountID,
		ALegID: "a-leg-finding3", BillingCallID: callID.String(), BLegID: bLegID,
	}
	value, err := metering.ParseDecimal(amount)
	require.NoError(t, err)
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "finding3-charge-" + bLegID, SourceEventKey: "finding3-charge-" + bLegID, Revision: revision,
		StreamID: "finding3-stream", Sequence: revision, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID},
		Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(1_700_500_001, 0).UTC(), ReceivedAt: time.Unix(1_700_500_001, 0).UTC(), MappingRef: "finding3.v1",
		Charges: []metering.ReportedCharge{{ChargeItemID: "finding3-charge-" + bLegID, Amount: &value, Currency: "USD", Kind: metering.ChargeKindAggregate, Payer: payer}},
	}
}

func finding3ProviderWork(t *testing.T, accountID string, callID billing.BillingCallID, headKey, bLegID string, payer metering.PaymentParty, amount string, allocations []economics.AllocationRef, createdAt time.Time) billing.EconomicRevisionWork {
	t.Helper()
	observation := finding3ProviderObservation(t, accountID, callID, bLegID, 1, amount, payer)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "finding3", Payer: payer,
		Observations:           []metering.Observation{observation},
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), allocations...),
	}
	return billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: headKey, Subject: observation.Subject,
		EvidenceRevision: 1, Input: input, CreatedAt: createdAt,
	}
}

func finding3CreateAccount(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	require.NoError(t, store.CreateAccount(context.Background(), billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1_000_000_000_000, State: billing.AccountReady, Version: 1,
	}))
}

func finding3ReopenWork(t *testing.T, store *DurableStore, work billing.EconomicRevisionWork) {
	t.Helper()
	identity, err := work.Identity()
	require.NoError(t, err)
	_, err = store.db.NewRaw(`UPDATE billing_economic_revision_work_state SET status = 'pending', next_attempt_at_unix = 0, lease_owner = '', lease_until_unix = 0 WHERE store_id = ? AND work_id = ? AND work_version = 1`, "test", identity.Key()).Exec(context.Background())
	require.NoError(t, err)
	// Verify reopen took effect (pending row exists).
	var status string
	require.NoError(t, store.db.NewRaw(`SELECT status FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ? AND work_version = 1`, "test", identity.Key()).Scan(context.Background(), &status))
	require.Equal(t, "pending", status)
}

func finding3FenceInputHash(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID string) string {
	t.Helper()
	lineage, err := billing.CallLegUsageKey(callID, bLegID)
	require.NoError(t, err)
	var hash string
	require.NoError(t, store.db.NewRaw(`SELECT input_set_hash FROM billing_provider_cost_posting_fences WHERE store_id = ? AND account_id = ? AND call_id = ? AND lineage_key = ?`, "test", accountID, callID.String(), lineage).Scan(context.Background(), &hash))
	return hash
}

type finding3RecordingProvider struct {
	inner   billing.ProviderCostRevisionStore
	inputs  []billing.ProviderCostRevisionInput
	results []billing.ProviderCostRevisionResult
	errs    []error
}

func (r *finding3RecordingProvider) ApplyProviderCostRevision(ctx context.Context, input billing.ProviderCostRevisionInput) (billing.ProviderCostRevisionResult, error) {
	r.inputs = append(r.inputs, input)
	res, err := r.inner.ApplyProviderCostRevision(ctx, input)
	r.results = append(r.results, res)
	r.errs = append(r.errs, err)
	return res, err
}

func finding3SourceKey(t *testing.T, input billing.ProviderCostRevisionInput) string {
	t.Helper()
	key, err := billing.ProviderCostRevisionSourceKey(input)
	require.NoError(t, err)
	return key
}

func TestPhase16Finding3SQLiteAllocationAwareRetryUsesFullSourceKey(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "finding3-alloc-retry"
	finding3CreateAccount(t, store, accountID)
	callID := billing.BillingCallID("bc_0000000000000000000000000000f301")
	alloc := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-f3", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	work := finding3ProviderWork(t, accountID, callID, "finding3-alloc-head", "b-leg-f3-alloc", metering.PaymentParty{Kind: metering.PaymentPartyOperator}, "10", []economics.AllocationRef{alloc}, time.Unix(1_700_500_100, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))

	sentinel := errors.New("finding3 crash after provider posting")
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
	require.Empty(t, refinement43ProviderJournals(t, store, accountID))
	store.SetEconomicFaultHook(nil)

	restarted, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, restarted.ProcessOnce(ctx))
	require.Len(t, recorder.inputs, 2, "fresh and retry must both attempt the provider posting")
	require.Equal(t, recorder.inputs[0].InputSetHash, recorder.inputs[1].InputSetHash, "fresh/retry source identity must be byte-identical")
	require.Equal(t, finding3SourceKey(t, recorder.inputs[0]), finding3SourceKey(t, recorder.inputs[1]))

	identity, err := work.Identity()
	require.NoError(t, err)
	require.NotEmpty(t, identity.DerivationHash, "allocation-aware work must carry a derivation hash")
	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	head, err := store.GetProviderCostHead(ctx, accountID, callID, work.HeadKey)
	require.NoError(t, err)
	require.Equal(t, valuation.InputSetHash, head.InputSetHash, "retry provider posting must reuse the full allocation-aware identity")
	require.Equal(t, identity.ValuationKey(), head.ValuationID)

	freshInput, err := billing.BuildProviderCostRevisionInput(work, valuation)
	require.NoError(t, err)
	freshKey, err := billing.ProviderCostRevisionSourceKey(freshInput)
	require.NoError(t, err)
	freshFP, err := freshInput.SemanticFingerprint()
	require.NoError(t, err)
	headInput := freshInput
	headInput.InputSetHash = head.InputSetHash
	headInput.ValuationID = head.ValuationID
	headKey, err := billing.ProviderCostRevisionSourceKey(headInput)
	require.NoError(t, err)
	headFP, err := headInput.SemanticFingerprint()
	require.NoError(t, err)
	require.Equal(t, freshKey, headKey, "fresh and retry source keys must be byte-identical")
	require.Equal(t, freshFP, headFP)
	require.Len(t, refinement43ProviderJournals(t, store, accountID), 1, "exactly one posting")
}

func TestPhase16Finding3SQLiteProcessedRecoveryIsIdempotent(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "finding3-recovery-idem"
	finding3CreateAccount(t, store, accountID)
	callID := billing.BillingCallID("bc_0000000000000000000000000000f302")
	alloc := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-f3", Version: 1, PayloadHash: strings.Repeat("b", 64)}
	work := finding3ProviderWork(t, accountID, callID, "finding3-idem-head", "b-leg-f3-idem", metering.PaymentParty{Kind: metering.PaymentPartyOperator}, "10", []economics.AllocationRef{alloc}, time.Unix(1_700_500_200, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	recorder := &finding3RecordingProvider{inner: store}
	worker, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Len(t, refinement43ProviderJournals(t, store, accountID), 1)

	// Reopen the same immutable work to force the processed-result branch with
	// an existing provider posting.
	finding3ReopenWork(t, store, work)
	recovery, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, recovery.ProcessOnce(ctx), "processed-result recovery must replay idempotently")
	require.Len(t, refinement43ProviderJournals(t, store, accountID), 1, "recovery must not duplicate the posting")
	require.Len(t, recorder.inputs, 2)
	require.Equal(t, recorder.inputs[0].InputSetHash, recorder.inputs[1].InputSetHash, "recovery must reuse the exact full identity")
	require.Equal(t, finding3SourceKey(t, recorder.inputs[0]), finding3SourceKey(t, recorder.inputs[1]))

	identity, err := work.Identity()
	require.NoError(t, err)
	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	head, err := store.GetProviderCostHead(ctx, accountID, callID, work.HeadKey)
	require.NoError(t, err)
	require.Equal(t, valuation.InputSetHash, head.InputSetHash)
}

func TestPhase16Finding3SQLiteNonpayableExclusionStableAcrossRetry(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "finding3-nonpayable"
	finding3CreateAccount(t, store, accountID)
	callID := billing.BillingCallID("bc_0000000000000000000000000000f303")
	alloc := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-f3", Version: 1, PayloadHash: strings.Repeat("c", 64)}
	customer := metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	work := finding3ProviderWork(t, accountID, callID, "finding3-nonpay-head", "b-leg-f3-nonpay", customer, "10", []economics.AllocationRef{alloc}, time.Unix(1_700_500_300, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))

	sentinel := errors.New("finding3 nonpayable crash")
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
	require.NoError(t, second.ProcessOnce(ctx), "nonpayable retry must reuse the same exclusion identity, not conflict")
	require.Empty(t, refinement43ProviderJournals(t, store, accountID), "exclusion must not post")
	require.Len(t, recorder.inputs, 2)
	require.Equal(t, recorder.inputs[0].InputSetHash, recorder.inputs[1].InputSetHash)
	require.Equal(t, finding3SourceKey(t, recorder.inputs[0]), finding3SourceKey(t, recorder.inputs[1]))

	identity, err := work.Identity()
	require.NoError(t, err)
	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, identity.DerivationHash, valuation.InputSetHash)
	fenceHash := finding3FenceInputHash(t, store, accountID, callID, "b-leg-f3-nonpay")
	require.Equal(t, valuation.InputSetHash, fenceHash, "fresh and retry exclusions must share the full source key")

	// Reopen again: recovery with an existing exclusion must stay idempotent.
	finding3ReopenWork(t, store, work)
	third, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.NoError(t, third.ProcessOnce(ctx))
	require.Empty(t, refinement43ProviderJournals(t, store, accountID))
	require.Equal(t, valuation.InputSetHash, finding3FenceInputHash(t, store, accountID, callID, "b-leg-f3-nonpay"))
	require.Len(t, recorder.inputs, 3)
	require.Equal(t, recorder.inputs[0].InputSetHash, recorder.inputs[2].InputSetHash)
}

func TestPhase16Finding3SQLiteLegacyObservationOnlyKeyUnchanged(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "finding3-legacy"
	finding3CreateAccount(t, store, accountID)
	callID := billing.BillingCallID("bc_0000000000000000000000000000f304")
	work := finding3ProviderWork(t, accountID, callID, "finding3-legacy-head", "b-leg-f3-legacy", metering.PaymentParty{Kind: metering.PaymentPartyOperator}, "10", nil, time.Unix(1_700_500_400, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	identity, err := work.Identity()
	require.NoError(t, err)
	require.Empty(t, identity.DerivationHash, "legacy work must have no derivation hash")

	sentinel := errors.New("finding3 legacy crash")
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
	require.Equal(t, recorder.inputs[0].InputSetHash, recorder.inputs[1].InputSetHash, "legacy retry must preserve the observation-only key")

	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, identity.InputSetHash, valuation.InputSetHash, "legacy valuation keeps the observation-only identity")
	head, err := store.GetProviderCostHead(ctx, accountID, callID, work.HeadKey)
	require.NoError(t, err)
	require.Equal(t, identity.InputSetHash, head.InputSetHash, "legacy provider posting keeps the observation-only source key")
	require.Len(t, refinement43ProviderJournals(t, store, accountID), 1)
}

func TestPhase16Finding3SQLiteReplacementDerivationDistinctStable(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "finding3-replacement"
	finding3CreateAccount(t, store, accountID)
	callID := billing.BillingCallID("bc_0000000000000000000000000000f305")
	// Same observation plane (same B-leg), distinct allocation derivations.
	allocV1 := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-f3-replace", Version: 1, PayloadHash: strings.Repeat("1", 64)}
	allocV2 := economics.AllocationRef{StoreID: "test", AllocationID: "alloc-f3-replace", Version: 2, PayloadHash: strings.Repeat("2", 64)}
	operator := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	// Build both works from the same base observation so the observation hash
	// is shared while the full derivation identities differ.
	base := finding3ProviderObservation(t, accountID, callID, "b-leg-f3-replace", 1, "10", operator)
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
	first := mkWork(allocV1, "finding3-replace-head", time.Unix(1_700_500_500, 0).UTC())
	second := mkWork(allocV2, "finding3-replace-head", time.Unix(1_700_500_600, 0).UTC())
	firstID, err := first.Identity()
	require.NoError(t, err)
	secondID, err := second.Identity()
	require.NoError(t, err)
	require.Equal(t, firstID.InputSetHash, secondID.InputSetHash, "observation plane is shared")
	require.NotEqual(t, firstID.DerivationHash, secondID.DerivationHash)
	require.NotEqual(t, firstID.Key(), secondID.Key())

	require.NoError(t, store.AppendEconomicRevisionWork(ctx, first))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, second))
	recorder := &finding3RecordingProvider{inner: store}
	worker, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Len(t, recorder.inputs, 2, "both derivations must post")

	firstVal, err := store.GetValuation(ctx, firstID.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	secondVal, err := store.GetValuation(ctx, secondID.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.NotEqual(t, firstVal.InputSetHash, secondVal.InputSetHash, "replacement derivations need distinct stable source keys")
	firstInput, err := billing.BuildProviderCostRevisionInput(first, firstVal)
	require.NoError(t, err)
	secondInput, err := billing.BuildProviderCostRevisionInput(second, secondVal)
	require.NoError(t, err)
	firstKey, err := billing.ProviderCostRevisionSourceKey(firstInput)
	require.NoError(t, err)
	secondKey, err := billing.ProviderCostRevisionSourceKey(secondInput)
	require.NoError(t, err)
	require.NotEqual(t, firstKey, secondKey)

	// Both immutable valuations survive; reopen each and prove the retry posts
	// the identical source key without cross-derivation scope confusion.
	for _, w := range []billing.EconomicRevisionWork{first, second} {
		finding3ReopenWork(t, store, w)
	}
	retry, err := billing.NewEconomicRevisionWorkerWithProviderCost(store, store, finding3EchoRater{}, recorder, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, retry.ProcessOnce(ctx))
	require.Len(t, recorder.inputs, 4, "both derivations must repost on retry")
	// Fresh inputs are recorder[0:2] (order by queue listing), retry inputs are
	// recorder[2:4]. Match them by valuation identity rather than order: each
	// retry must equal its own fresh derivation and the two derivations must
	// stay distinct.
	freshKeys := map[string]string{
		recorder.inputs[0].InputSetHash: finding3SourceKey(t, recorder.inputs[0]),
		recorder.inputs[1].InputSetHash: finding3SourceKey(t, recorder.inputs[1]),
	}
	require.Len(t, freshKeys, 2, "fresh replacement postings must be distinct")
	for _, retryInput := range recorder.inputs[2:] {
		freshKey, ok := freshKeys[retryInput.InputSetHash]
		require.True(t, ok, "retry posting %q must match one fresh derivation", retryInput.InputSetHash)
		require.Equal(t, freshKey, finding3SourceKey(t, retryInput), "replacement retry must be byte-identical")
	}
	retryFirstVal, err := store.GetValuation(ctx, firstID.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	retrySecondVal, err := store.GetValuation(ctx, secondID.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, firstVal.InputSetHash, retryFirstVal.InputSetHash)
	require.Equal(t, secondVal.InputSetHash, retrySecondVal.InputSetHash)
	retryFirstInput, err := billing.BuildProviderCostRevisionInput(first, retryFirstVal)
	require.NoError(t, err)
	retrySecondInput, err := billing.BuildProviderCostRevisionInput(second, retrySecondVal)
	require.NoError(t, err)
	retryFirstKey, err := billing.ProviderCostRevisionSourceKey(retryFirstInput)
	require.NoError(t, err)
	retrySecondKey, err := billing.ProviderCostRevisionSourceKey(retrySecondInput)
	require.NoError(t, err)
	require.Equal(t, firstKey, retryFirstKey, "replacement retry must be byte-identical")
	require.Equal(t, secondKey, retrySecondKey)
	var valuationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, "test").Scan(ctx, &valuationCount))
	require.Equal(t, 2, valuationCount, "both immutable postings remain")
	_ = fmt.Sprint(firstKey, secondKey)
}
