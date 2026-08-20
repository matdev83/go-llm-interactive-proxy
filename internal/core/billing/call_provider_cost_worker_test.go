package billing

import (
	"context"
	"errors"
	"testing"
)

type providerCostWorkReaderStub struct {
	listCalls int
	lastLimit int
	work      []ProviderCostWork
}

func (s *providerCostWorkReaderStub) ListCallUsage(context.Context, string) ([]CallUsageRecord, error) {
	s.listCalls++
	return nil, errors.New("historical call listing must not be used")
}

func (s *providerCostWorkReaderStub) ListPendingProviderCostWork(_ context.Context, limit int) ([]ProviderCostWork, error) {
	s.lastLimit = limit
	if limit > len(s.work) {
		limit = len(s.work)
	}
	out := append([]ProviderCostWork(nil), s.work[:limit]...)
	s.work = s.work[limit:]
	return out, nil
}

func (s *providerCostWorkReaderStub) ListCallLegUsage(context.Context, BillingCallID) ([]CallLegUsageRecord, error) {
	return nil, errors.New("leg-by-call fallback must not be used")
}

type providerCostStoreStub struct {
	applied []ApplyProviderCostInput
}

func (s *providerCostStoreStub) ApplyProviderCost(_ context.Context, in ApplyProviderCostInput) (Posting, error) {
	s.applied = append(s.applied, in)
	return Posting{}, nil
}

type providerCostResolverStub struct{}

func (providerCostResolverStub) ResolveProviderCost(_ context.Context, leg CallLegUsageRecord) (OperatorCostResult, error) {
	sealed, err := leg.Seal()
	if err != nil {
		return OperatorCostResult{}, err
	}
	return OperatorCostResult{LURKey: sealed.Key, Amount: Money{Currency: "USD"}, AmountPresent: true, Reconciled: true}, nil
}

func TestCallProviderCostWorkerUsesBoundedPendingWorkQueue(t *testing.T) {
	t.Parallel()
	firstID := mustBillingCallID(t)
	secondID := mustBillingCallID(t)
	reader := &providerCostWorkReaderStub{work: []ProviderCostWork{
		{AccountID: "acct", CallID: firstID, Leg: testCallLegUsageRecord(firstID, "b-first")},
		{AccountID: "acct", CallID: secondID, Leg: testCallLegUsageRecord(secondID, "b-second")},
	}}
	store := &providerCostStoreStub{}
	worker, err := NewCallProviderCostWorker(reader, store, providerCostResolverStub{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := worker.ProcessOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if reader.listCalls != 0 {
		t.Fatalf("historical call listings = %d, want 0", reader.listCalls)
	}
	if reader.lastLimit != 1 {
		t.Fatalf("pending queue limit = %d, want 1", reader.lastLimit)
	}
	if len(store.applied) != 2 || store.applied[0].Leg.BLegID != "b-first" || store.applied[1].Leg.BLegID != "b-second" {
		t.Fatalf("provider-cost applications = %#v", store.applied)
	}
}
