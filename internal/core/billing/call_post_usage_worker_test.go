package billing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeCallUsageStore struct {
	calls       []CompleteCall
	exposure    CallExposure
	retried     []string
	retryCodes  []string
	claimErr    error
	exposureErr error
}

func (f *fakeCallUsageStore) ClaimCompleteCalls(context.Context, int) ([]CompleteCall, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	out := f.calls
	f.calls = nil
	return out, nil
}

func (f *fakeCallUsageStore) ClaimCompleteCall(context.Context, BillingCallID) (CompleteCall, error) {
	return CompleteCall{}, errors.New("unused")
}

func (f *fakeCallUsageStore) GetCallExposure(_ context.Context, callID BillingCallID) (CallExposure, error) {
	if f.exposureErr != nil {
		return CallExposure{}, f.exposureErr
	}
	return f.exposure, nil
}

func (f *fakeCallUsageStore) RetryCompleteCall(_ context.Context, callID BillingCallID, code string) error {
	f.retried = append(f.retried, callID.String())
	f.retryCodes = append(f.retryCodes, code)
	return nil
}

type fakeCallRatingResolver struct {
	result CallRatingResult
	err    error
}

func (f fakeCallRatingResolver) ResolveCallRating(context.Context, CompleteCall, CallExposure) (CallRatingResult, error) {
	return f.result, f.err
}

type fakeCallSettlementStore struct {
	err error
}

func (f fakeCallSettlementStore) ApplyCallBillingResult(context.Context, ApplyCallBillingInput) (CallSettlement, error) {
	return CallSettlement{}, f.err
}

func TestCallPostUsageWorkerRetriesReconcileRequiredWithStableCode(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	closure := testCallUsageRecord(callID)
	usage := &fakeCallUsageStore{
		calls: []CompleteCall{{Closure: closure, Legs: []CallLegUsageRecord{testCallLegUsageRecord(callID, "b-win")}}},
		exposure: CallExposure{
			AccountID: closure.AccountID, CallID: callID.String(),
			Max: Money{Nano: 10, Currency: "USD"}, Status: ExposureOpen, CreatedAt: time.Unix(1, 0).UTC(),
			PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
		},
	}
	worker, err := NewCallPostUsageWorker(usage, fakeCallSettlementStore{
		err: errors.Join(ErrSettlementReconcileRequired, ErrExposureActualExceedsMax),
	}, fakeCallRatingResolver{
		result: CallRatingResult{CallID: callID, CustomerCharge: Money{Nano: 25, Currency: "USD"}, Fingerprint: "fp"},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	err = worker.ProcessOnce(context.Background())
	if !errors.Is(err, ErrSettlementReconcileRequired) {
		t.Fatalf("ProcessOnce = %v, want ErrSettlementReconcileRequired", err)
	}
	if !strings.Contains(err.Error(), "settlement_reconcile_required") {
		t.Fatalf("error = %v, want settlement_reconcile_required code", err)
	}
	if len(usage.retried) != 1 || usage.retried[0] != callID.String() {
		t.Fatalf("retried = %v, want %s", usage.retried, callID)
	}
	if len(usage.retryCodes) != 1 || usage.retryCodes[0] != "settlement_reconcile_required" {
		t.Fatalf("retry codes = %v, want settlement_reconcile_required", usage.retryCodes)
	}
}
