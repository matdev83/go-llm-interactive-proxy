package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 18 valuation-contract fence: V2 settlement requires a complete
// customer valuation bound to the exact settlement subject/scope, currency,
// and rated amount/result identity. Every negative below must fail closed
// before Apply (zero settlement effects); the bound positive and the
// explicit pass-through / V1 drain controls must succeed.

type tableStubResolver struct {
	result CallRatingResult
	err    error
}

func (s tableStubResolver) ResolveCallRating(_ context.Context, complete CompleteCall, _ CallExposure) (CallRatingResult, error) {
	if s.err != nil {
		return CallRatingResult{}, s.err
	}
	return s.result, nil
}

func (s tableStubResolver) ResolveCallRatingForOwner(_ context.Context, complete CompleteCall, _ CallExposure, _ string) (CallRatingResult, error) {
	if s.err != nil {
		return CallRatingResult{}, s.err
	}
	return s.result, nil
}

func TestOwnerFinalV2SettlementBindingTable(t *testing.T) {
	t.Parallel()
	newBound := func(t *testing.T, complete CompleteCall, chargeNano int64) CallRatingResult {
		t.Helper()
		return ownerFinalBoundResult(t, complete.Closure, chargeNano)
	}
	cases := []struct {
		name    string
		mutate  func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult)
		wantErr bool
	}{
		{
			name: "id-only valuation",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				res.CustomerValuation = economics.Valuation{ID: "bare-label"}
			},
			wantErr: true,
		},
		{
			name: "scalar result",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				res.CustomerValuation = economics.Valuation{}
			},
			wantErr: true,
		},
		{
			name: "subject call mismatch",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				other := complete.Closure
				other.CallID = mustBillingCallID(t)
				bound := ownerFinalBoundResult(t, other, res.CustomerCharge.Nano)
				res.CustomerValuation = bound.CustomerValuation
			},
			wantErr: true,
		},
		{
			name: "result call mismatch",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				res.CallID = mustBillingCallID(t)
			},
			wantErr: true,
		},
		{
			name: "account payer mismatch",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				v := res.CustomerValuation.Clone()
				v.Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "acct-intruder"}
				res.CustomerValuation = v
			},
			wantErr: true,
		},
		{
			name: "charge currency mismatch",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				res.CustomerCharge.Currency = "EUR"
			},
			wantErr: true,
		},
		{
			name: "valuation currency mismatch",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				v := res.CustomerValuation.Clone()
				v.Totals[0].Currency = "EUR"
				v.Totals[0].RoundedAmount.Currency = "EUR"
				res.CustomerValuation = v
			},
			wantErr: true,
		},
		{
			name: "rated amount mismatch",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				res.CustomerCharge.Nano++
			},
			wantErr: true,
		},
		{
			name: "wrong basis",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				v := res.CustomerValuation.Clone()
				v.Basis = economics.BasisLocalExpected
				res.CustomerValuation = v
			},
			wantErr: true,
		},
		{
			name: "partial completeness",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				v := res.CustomerValuation.Clone()
				v.Completeness = economics.CompletenessPartial
				res.CustomerValuation = v
			},
			wantErr: true,
		},
		{
			name: "fixed-only lines lack component basis",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				v := res.CustomerValuation.Clone()
				for i := range v.Lines {
					v.Lines[i].Component = nil
					v.Lines[i].FixedFee = &economics.FixedFeeIdentity{ID: "call-fee", Scope: economics.FixedFeeScopeCall, Version: "v1"}
				}
				res.CustomerValuation = v
			},
			wantErr: true,
		},
		{
			name: "corrupt input hash",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				v := res.CustomerValuation.Clone()
				v.InputSetHash = "0000000000000000000000000000000000000000000000000000000000000000"
				res.CustomerValuation = v
			},
			wantErr: true,
		},
		{
			name: "fingerprint mismatch",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				res.Fingerprint = "stale-fingerprint"
			},
			wantErr: true,
		},
		{
			name: "policy snapshot mismatch",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				v := res.CustomerValuation.Clone()
				v.Policy.Version = "v9"
				res.CustomerValuation = v
			},
			wantErr: true,
		},
		{
			name: "tariff snapshot mismatch",
			mutate: func(t *testing.T, complete CompleteCall, exposure CallExposure, res *CallRatingResult) {
				t.Helper()
				v := res.CustomerValuation.Clone()
				v.Tariff.Version = "v9"
				v.Rater.Version = "v9"
				res.CustomerValuation = v
			},
			wantErr: true,
		},
		{
			name:    "bound component succeeds",
			mutate:  nil,
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			complete, exposure := ownerFinalComplete(t)
			res := newBound(t, complete, 120)
			if tc.mutate != nil {
				tc.mutate(t, complete, exposure, &res)
			}
			settlement := &countingSettlementStore{}
			worker, err := NewCallPostUsageWorker(&fakeCallUsageStore{}, settlement, tableStubResolver{result: res}, 8)
			if err != nil {
				t.Fatal(err)
			}
			_, err = worker.resolveCallRatingWithOwner(context.Background(), complete, exposure, PostingOwnerV2)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("V2 %s must fail closed", tc.name)
				}
				if !errors.Is(err, ErrRetailRateIncomplete) {
					t.Fatalf("V2 %s error = %v, want %v", tc.name, err, ErrRetailRateIncomplete)
				}
				if settlement.calls != 0 {
					t.Fatalf("V2 %s reached Apply %d times, want 0", tc.name, settlement.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("V2 %s must succeed: %v", tc.name, err)
			}
		})
	}
}

func TestOwnerFinalV2PassThroughBindingTable(t *testing.T) {
	t.Parallel()
	complete, _ := ownerFinalComplete(t)
	validState := func() *CostPassThroughSettlement {
		bound := Money{Nano: 800, Currency: "USD"}
		return &CostPassThroughSettlement{
			PolicyRef: complete.Closure.ChargePolicyRef,
			Policy: CostPassThroughPolicy{
				MissingCost: CostPassThroughMissingCostProvisional,
				SafeBound:   &bound, AllowLateAdjustment: true,
			},
			Status:    CostPassThroughSettlementProvisional,
			SafeBound: bound, PostedAmount: Money{Nano: 60, Currency: "USD"},
		}
	}
	cases := []struct {
		name    string
		mutate  func(res *CallRatingResult)
		wantErr bool
	}{
		{name: "valid pass-through", mutate: nil, wantErr: false},
		{
			name: "pass-through with ID-only valuation is not a bypass",
			mutate: func(res *CallRatingResult) {
				res.CustomerValuation = economics.Valuation{ID: "label-bypass"}
			},
			wantErr: true,
		},
		{
			name: "pass-through policy mismatch",
			mutate: func(res *CallRatingResult) {
				res.CostPassThrough.PolicyRef.Version = "v9"
			},
			wantErr: true,
		},
		{
			name: "pass-through amount mismatch",
			mutate: func(res *CallRatingResult) {
				res.CustomerCharge.Nano = 61
			},
			wantErr: true,
		},
		{
			name: "pass-through missing bound",
			mutate: func(res *CallRatingResult) {
				res.CostPassThrough.Policy.SafeBound = nil
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			complete, exposure := ownerFinalComplete(t)
			res := CallRatingResult{
				CallID: complete.Closure.CallID, CustomerCharge: Money{Nano: 60, Currency: "USD"}, Fingerprint: "pass-through-fp",
				CostPassThrough: validState(),
			}
			// Rebind the policy snapshot to this subtest's closure identity.
			res.CostPassThrough.PolicyRef = complete.Closure.ChargePolicyRef
			if tc.mutate != nil {
				tc.mutate(&res)
			}
			settlement := &countingSettlementStore{}
			worker, err := NewCallPostUsageWorker(&fakeCallUsageStore{}, settlement, tableStubResolver{result: res}, 8)
			if err != nil {
				t.Fatal(err)
			}
			_, err = worker.resolveCallRatingWithOwner(context.Background(), complete, exposure, PostingOwnerV2)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("V2 %s must fail closed", tc.name)
				}
				if settlement.calls != 0 {
					t.Fatalf("V2 %s reached Apply %d times, want 0", tc.name, settlement.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("V2 %s must succeed: %v", tc.name, err)
			}
		})
	}
}
