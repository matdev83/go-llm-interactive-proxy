package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type refinement43LegacyAuthorityReader struct {
	work      []ProviderCostWork
	listCalls int
}

func (r *refinement43LegacyAuthorityReader) ListPendingProviderCostWork(context.Context, int) ([]ProviderCostWork, error) {
	r.listCalls++
	work := append([]ProviderCostWork(nil), r.work...)
	r.work = nil
	return work, nil
}

type refinement43LegacyAuthorityStore struct {
	applied  []ApplyProviderCostInput
	marked   []ApplyProviderCostInput
	deferred []ProviderCostWork
}

func (s *refinement43LegacyAuthorityStore) ApplyProviderCost(_ context.Context, input ApplyProviderCostInput) (Posting, error) {
	s.applied = append(s.applied, input)
	return Posting{}, nil
}

func (s *refinement43LegacyAuthorityStore) MarkProviderCostUnreconciled(_ context.Context, input ApplyProviderCostInput, _ string) error {
	s.marked = append(s.marked, input)
	return nil
}

func (s *refinement43LegacyAuthorityStore) DeferProviderCostWork(_ context.Context, work ProviderCostWork, _ string) error {
	s.deferred = append(s.deferred, work)
	return nil
}

type refinement43LegacyAuthorityResolver struct {
	result OperatorCostResult
	err    error
}

func (r refinement43LegacyAuthorityResolver) ResolveProviderCost(_ context.Context, _ CallLegUsageRecord) (OperatorCostResult, error) {
	return r.result, r.err
}

func refinement43LegacyAuthorityWork(t *testing.T, cost MoneyEvidence) ProviderCostWork {
	t.Helper()
	callID := mustBillingCallID(t)
	leg := testCallLegUsageRecord(callID, "b-legacy-authority")
	leg.Evidence.Cost = cost
	leg.OperatorRateRef = operatorRate().Ref
	if !cost.Present {
		leg.Evidence.Cost = MoneyEvidence{}
	}
	sealed, err := leg.Seal()
	require.NoError(t, err)
	return ProviderCostWork{AccountID: "acct-legacy-authority", CallID: callID, Leg: sealed}
}

func TestRefinement43LegacyProviderCostWorkerRejectsLocalRateFallback(t *testing.T) {
	t.Parallel()
	work := refinement43LegacyAuthorityWork(t, MoneyEvidence{})
	// Task 18.1: the scalar token-to-money fallback is retired at the
	// resolver. Token-only evidence is unreconciled before any worker
	// authority check; estimates belong to the V2 provider-quantity
	// valuation.
	_, err := RateProviderCost(work.Leg, OperatorRateSet{operatorRate()}, "USD")
	require.ErrorIs(t, err, ErrUnreconciledCost)

	reader := &refinement43LegacyAuthorityReader{work: []ProviderCostWork{work}}
	store := &refinement43LegacyAuthorityStore{}
	worker, err := NewCallProviderCostWorker(reader, store, refinement43LegacyAuthorityResolver{err: err}, 1)
	require.NoError(t, err)

	err = worker.ProcessOnce(context.Background())
	require.ErrorIs(t, err, ErrUnreconciledCost)
	require.Empty(t, store.applied)
	require.Len(t, store.marked, 1)
	require.Len(t, store.deferred, 1)
}

func TestRefinement43LegacyProviderCostWorkerAcceptsAuthoritativeProviderReport(t *testing.T) {
	t.Parallel()
	work := refinement43LegacyAuthorityWork(t, MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true})
	result, err := RateProviderCost(work.Leg, nil, "USD")
	require.NoError(t, err)
	require.True(t, result.Authoritative)

	reader := &refinement43LegacyAuthorityReader{work: []ProviderCostWork{work}}
	store := &refinement43LegacyAuthorityStore{}
	worker, err := NewCallProviderCostWorker(reader, store, refinement43LegacyAuthorityResolver{result: result}, 1)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(context.Background()))
	require.Len(t, store.applied, 1)
	require.Empty(t, store.marked)
	require.Empty(t, store.deferred)
}

func TestRefinement43LegacyProviderCostWorkerRetainsUnknownPayerWorkForRetry(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	leg := testCallLegUsageRecord(callID, "b-unknown-payer")
	sealed, err := leg.Seal()
	require.NoError(t, err)
	work := ProviderCostWork{CallID: callID, Leg: sealed}
	reader := &refinement43LegacyAuthorityReader{work: []ProviderCostWork{work}}
	store := &refinement43LegacyAuthorityStore{}
	resolver := refinement43LegacyAuthorityResolver{result: OperatorCostResult{LURKey: sealed.Key, Amount: Money{Currency: "USD"}, AmountPresent: true, Reconciled: true}}
	worker, err := NewCallProviderCostWorker(reader, store, resolver, 1)
	require.NoError(t, err)

	err = worker.ProcessOnce(context.Background())
	require.ErrorIs(t, err, ErrProviderCostCallUnavailable)
	require.Empty(t, store.applied)
	require.Empty(t, store.marked)
	require.Len(t, store.deferred, 1)
}

func TestRefinement43LegacyProviderCostAuthorityErrorIsTypedAndRetryable(t *testing.T) {
	t.Parallel()
	work := refinement43LegacyAuthorityWork(t, MoneyEvidence{})
	sealed, err := work.Leg.Seal()
	require.NoError(t, err)
	result := OperatorCostResult{LURKey: sealed.Key, Amount: Money{Nano: 7, Currency: "USD"}, AmountPresent: true, Reconciled: true}

	validationErr := result.ValidateProviderAuthority()
	var authorityErr *ProviderCostAuthorityError
	require.ErrorAs(t, validationErr, &authorityErr)
	require.ErrorIs(t, validationErr, ErrProviderCostAuthority)
	require.Equal(t, "authoritative", authorityErr.Field)
	require.Equal(t, "false", authorityErr.Value)

	// The legacy queue's existing failure contract is retry-based. The typed
	// authority error must therefore remain inspectable after worker wrapping.
	require.True(t, errors.Is(validationErr, ErrProviderCostUntrusted))
}

func TestRefinement43LegacyProviderCostRejectsAuthoritativeFlagOnLocalEvidence(t *testing.T) {
	t.Parallel()
	work := refinement43LegacyAuthorityWork(t, MoneyEvidence{})
	work.Leg.Evidence.Source = EvidenceSourceLocalEstimator
	work.Leg.Evidence.Authority = EvidenceAuthorityEstimated
	sealed, err := work.Leg.Seal()
	require.NoError(t, err)
	result := OperatorCostResult{LURKey: sealed.Key, Amount: Money{Nano: 7, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}

	err = ValidateProviderCostAuthority(sealed, result)
	var authorityErr *ProviderCostAuthorityError
	require.ErrorAs(t, err, &authorityErr)
	require.ErrorIs(t, err, ErrProviderCostAuthority)
	require.Equal(t, "evidence", authorityErr.Field)
}
