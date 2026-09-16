package billing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type phase10CustomerUsageStore struct {
	mu       sync.Mutex
	calls    []CompleteCall
	exposure CallExposure
	retried  []BillingCallID
	claimErr error
	getErr   error
}

func (s *phase10CustomerUsageStore) ClaimCompleteCalls(_ context.Context, limit int) ([]CompleteCall, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit > len(s.calls) {
		limit = len(s.calls)
	}
	out := append([]CompleteCall(nil), s.calls[:limit]...)
	s.calls = s.calls[limit:]
	return out, nil
}

func (s *phase10CustomerUsageStore) ClaimCompleteCall(context.Context, BillingCallID) (CompleteCall, error) {
	return CompleteCall{}, errors.New("unused")
}

func (s *phase10CustomerUsageStore) GetCallExposure(context.Context, BillingCallID) (CallExposure, error) {
	if s.getErr != nil {
		return CallExposure{}, s.getErr
	}
	return s.exposure, nil
}

func (s *phase10CustomerUsageStore) RetryCompleteCall(_ context.Context, callID BillingCallID, _ string) error {
	s.mu.Lock()
	s.retried = append(s.retried, callID)
	s.mu.Unlock()
	return nil
}

type phase10CustomerSettlementStore struct {
	applied chan ApplyCallBillingInput
}

func (s *phase10CustomerSettlementStore) ApplyCallBillingResult(_ context.Context, in ApplyCallBillingInput) (CallSettlement, error) {
	s.applied <- in
	return CallSettlement{}, nil
}

type phase10CustomerRatingResolver struct {
	result CallRatingResult
}

func (r phase10CustomerRatingResolver) ResolveCallRating(context.Context, CompleteCall, CallExposure) (CallRatingResult, error) {
	return r.result, nil
}

type phase10ProviderWorkReader struct {
	mu        sync.Mutex
	work      []ProviderCostWork
	err       error
	listCalls int
	lastLimit int
}

func (r *phase10ProviderWorkReader) ListPendingProviderCostWork(_ context.Context, limit int) ([]ProviderCostWork, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	r.lastLimit = limit
	if limit > len(r.work) {
		limit = len(r.work)
	}
	out := append([]ProviderCostWork(nil), r.work[:limit]...)
	r.work = r.work[limit:]
	return out, nil
}

type phase10ProviderStore struct {
	mu      sync.Mutex
	applied []ApplyProviderCostInput
}

func (s *phase10ProviderStore) ApplyProviderCost(_ context.Context, in ApplyProviderCostInput) (Posting, error) {
	s.mu.Lock()
	s.applied = append(s.applied, in)
	s.mu.Unlock()
	return Posting{}, nil
}

type phase10ProviderResolver struct {
	err error
}

func (r phase10ProviderResolver) ResolveProviderCost(_ context.Context, leg CallLegUsageRecord) (OperatorCostResult, error) {
	if r.err != nil {
		return OperatorCostResult{}, r.err
	}
	sealed, err := leg.Seal()
	if err != nil {
		return OperatorCostResult{}, err
	}
	return OperatorCostResult{LURKey: sealed.Key, Amount: Money{Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}, nil
}

func phase10IndependentCustomerWorkers(t *testing.T, supplierWork []ProviderCostWork, supplierResolver ProviderCostResolver) (*CallPostUsageWorker, *CallProviderCostWorker, *phase10CustomerSettlementStore, *phase10ProviderStore) {
	t.Helper()
	for i := range supplierWork {
		if supplierWork[i].AccountID == "" {
			supplierWork[i].AccountID = "acct-1"
		}
	}
	callID := supplierWork[0].CallID
	closure, err := testCallUsageRecord(callID).Seal()
	if err != nil {
		t.Fatal(err)
	}
	leg, err := testCallLegUsageRecord(callID, "b-retail").Seal()
	if err != nil {
		t.Fatal(err)
	}
	usage := &phase10CustomerUsageStore{
		calls: []CompleteCall{{Closure: closure, Legs: []CallLegUsageRecord{leg}}},
		exposure: CallExposure{
			AccountID:       closure.AccountID,
			CallID:          callID.String(),
			Max:             Money{Nano: 100, Currency: "USD"},
			Status:          ExposureOpen,
			PricingRef:      closure.CustomerPricingRef,
			ChargePolicyRef: closure.ChargePolicyRef,
		},
	}
	settlement := &phase10CustomerSettlementStore{applied: make(chan ApplyCallBillingInput, 1)}
	customerWorker, err := NewCallPostUsageWorker(usage, settlement, phase10CustomerRatingResolver{
		result: CallRatingResult{CallID: callID, CustomerCharge: Money{Nano: 7, Currency: "USD"}, Fingerprint: "retail-frozen"},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	providerStore := &phase10ProviderStore{}
	providerWorker, err := NewCallProviderCostWorker(&phase10ProviderWorkReader{work: supplierWork}, providerStore, supplierResolver, 1)
	if err != nil {
		t.Fatal(err)
	}
	return customerWorker, providerWorker, settlement, providerStore
}

func TestPhase10SupplierQueueErrorDoesNotBlockIndependentCustomerSettlement(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	queueErr := errors.New("supplier queue unavailable")
	customerWorker, _, settlement, _ := phase10IndependentCustomerWorkers(t, []ProviderCostWork{{CallID: callID}}, phase10ProviderResolver{})

	providerReader := &phase10ProviderWorkReader{err: queueErr}
	providerStore := &phase10ProviderStore{}
	providerWorker, err := NewCallProviderCostWorker(providerReader, providerStore, phase10ProviderResolver{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := providerWorker.ProcessOnce(context.Background()); !errors.Is(err, queueErr) {
		t.Fatalf("supplier queue error = %v, want %v", err, queueErr)
	}
	if err := customerWorker.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("customer settlement after supplier queue error = %v", err)
	}
	select {
	case got := <-settlement.applied:
		if got.Result.CustomerCharge.Nano != 7 || got.Result.Fingerprint != "retail-frozen" {
			t.Fatalf("customer result = %+v, want frozen independent retail result", got.Result)
		}
	case <-time.After(time.Second):
		t.Fatal("customer settlement did not complete after supplier queue error")
	}
}

func TestPhase10SupplierResolverErrorDoesNotBlockIndependentCustomerSettlement(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	customerWorker, providerWorker, settlement, providerStore := phase10IndependentCustomerWorkers(t, []ProviderCostWork{{CallID: callID}}, phase10ProviderResolver{err: errors.New("supplier valuation failed")})

	if err := providerWorker.ProcessOnce(context.Background()); err == nil {
		t.Fatal("supplier resolver error = nil, want supplier failure")
	}
	if err := customerWorker.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("customer settlement after supplier resolver error = %v", err)
	}
	select {
	case got := <-settlement.applied:
		if got.Result.CustomerCharge.Nano != 7 {
			t.Fatalf("customer amount = %d, want 7", got.Result.CustomerCharge.Nano)
		}
	case <-time.After(time.Second):
		t.Fatal("customer settlement did not complete after supplier resolver error")
	}
	providerStore.mu.Lock()
	defer providerStore.mu.Unlock()
	if len(providerStore.applied) != 0 {
		t.Fatalf("provider postings after resolver error = %d, want 0", len(providerStore.applied))
	}
}

func TestPhase10SupplierBacklogKeepsCustomerSettlementIndependent(t *testing.T) {
	t.Parallel()
	firstID := mustBillingCallID(t)
	secondID := mustBillingCallID(t)
	reader := &phase10ProviderWorkReader{work: []ProviderCostWork{
		{AccountID: "acct-1", CallID: firstID, Leg: testCallLegUsageRecord(firstID, "b-first")},
		{AccountID: "acct-1", CallID: secondID, Leg: testCallLegUsageRecord(secondID, "b-second")},
	}}
	providerStore := &phase10ProviderStore{}
	providerWorker, err := NewCallProviderCostWorker(reader, providerStore, phase10ProviderResolver{}, 1)
	if err != nil {
		t.Fatal(err)
	}

	// The customer worker has no dependency on the pending supplier batch. The
	// supplier queue remains bounded and can drain independently afterwards.
	customerWorker, _, settlement, _ := phase10IndependentCustomerWorkers(t, []ProviderCostWork{{CallID: firstID}}, phase10ProviderResolver{})
	if err := customerWorker.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("customer settlement while supplier queue is backlogged = %v", err)
	}
	select {
	case <-settlement.applied:
	case <-time.After(time.Second):
		t.Fatal("customer settlement did not complete while supplier queue was backlogged")
	}
	if err := providerWorker.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader.mu.Lock()
	listCalls, lastLimit, remaining := reader.listCalls, reader.lastLimit, len(reader.work)
	reader.mu.Unlock()
	if listCalls != 1 || lastLimit != 1 || remaining != 1 {
		t.Fatalf("supplier queue after bounded drain = calls:%d limit:%d remaining:%d", listCalls, lastLimit, remaining)
	}
}

func TestPhase10SupplierCancellationDoesNotCancelCustomerSettlement(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	providerStarted := make(chan struct{})
	providerCanceled := make(chan struct{})
	var startOnce sync.Once
	var cancelOnce sync.Once
	reader := &phase10ProviderWorkReader{work: []ProviderCostWork{{AccountID: "acct-1", CallID: callID}}}
	providerResolver := phase10BlockingProviderResolver{
		started:    providerStarted,
		canceled:   providerCanceled,
		startOnce:  &startOnce,
		cancelOnce: &cancelOnce,
	}
	providerStore := &phase10ProviderStore{}
	providerWorker, err := NewCallProviderCostWorker(reader, providerStore, providerResolver, 1)
	if err != nil {
		t.Fatal(err)
	}
	supplierCtx, cancelSupplier := context.WithCancel(context.Background())
	defer cancelSupplier()
	if err := providerWorker.Start(supplierCtx); err != nil {
		t.Fatal(err)
	}
	stopSupplier := func() {
		cancelSupplier()
		stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
		defer cancelStop()
		if err := providerWorker.Stop(stopCtx); err != nil {
			t.Errorf("stop supplier worker: %v", err)
		}
	}
	defer stopSupplier()
	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("supplier resolver did not start")
	}

	customerWorker, _, settlement, _ := phase10IndependentCustomerWorkers(t, []ProviderCostWork{{CallID: callID}}, phase10ProviderResolver{})
	if err := customerWorker.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("customer settlement while supplier resolver is delayed = %v", err)
	}
	select {
	case got := <-settlement.applied:
		if got.Result.CustomerCharge.Nano != 7 {
			t.Fatalf("customer amount = %d, want committed retail amount 7", got.Result.CustomerCharge.Nano)
		}
	case <-time.After(time.Second):
		t.Fatal("customer settlement did not complete while supplier resolver was delayed")
	}
	cancelSupplier()
	select {
	case <-providerCanceled:
	case <-time.After(time.Second):
		t.Fatal("supplier resolver did not observe cancellation")
	}
	stopSupplier()
	providerStore.mu.Lock()
	defer providerStore.mu.Unlock()
	if len(providerStore.applied) != 0 {
		t.Fatalf("provider postings after canceled supplier work = %d, want 0", len(providerStore.applied))
	}
}

type phase10BlockingProviderResolver struct {
	started    chan struct{}
	canceled   chan struct{}
	startOnce  *sync.Once
	cancelOnce *sync.Once
}

func (r phase10BlockingProviderResolver) ResolveProviderCost(ctx context.Context, _ CallLegUsageRecord) (OperatorCostResult, error) {
	r.startOnce.Do(func() { close(r.started) })
	<-ctx.Done()
	r.cancelOnce.Do(func() { close(r.canceled) })
	return OperatorCostResult{}, ctx.Err()
}
