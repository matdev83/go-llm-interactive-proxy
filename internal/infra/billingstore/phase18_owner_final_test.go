package billingstore

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 18 final owner remediation (R2): generic worker/store V2 valuation
// fence with real durable effects.
//
// B: V2 cutover worker with old/decorated owner-unaware scalar resolver
// fails closed before effects (balance/journal/exposure/pin unchanged).
// C: Direct store ApplyCallBillingResult with V2 + scalar-only fails
// transactionally with zero effects.
// D: Valid V2 component valuation and explicit pass-through succeed.
// E: V1 drain with legacy resolver succeeds/replays; V2 legacy fallback
// never does.

type ownerFinalLegacyScalarStub struct {
	charge int64
	fp     string
}

func (s ownerFinalLegacyScalarStub) ResolveCallRating(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure) (billing.CallRatingResult, error) {
	return billing.CallRatingResult{CallID: complete.Closure.CallID, CustomerCharge: billing.Money{Nano: s.charge, Currency: "USD"}, Fingerprint: s.fp}, nil
}

type ownerFinalDecoratedOldStub struct {
	charge int64
	fp     string
}

func (s ownerFinalDecoratedOldStub) ResolveCallRating(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure) (billing.CallRatingResult, error) {
	return billing.CallRatingResult{CallID: complete.Closure.CallID, CustomerCharge: billing.Money{Nano: s.charge, Currency: "USD"}, Fingerprint: s.fp}, nil
}

func ownerFinalV2Setup(t *testing.T, storeID, accountID string, balance int64) (*DurableStore, context.Context, billing.BillingCallID, billing.CallExposure) {
	t.Helper()
	ctx := context.Background()
	store := f3NewStore(t, storeID)
	f3SetupAccount(t, store, accountID, balance)
	f3ActivateEmpty(t, store)
	callID := f3MustCallID(t)
	closure := testIndependentCallUsageFor(callID, []string{"b-v2"})
	closure.AccountID = accountID
	exp, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2)
	if err != nil {
		t.Fatalf("V2 admit: %v", err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 closure: %v", err)
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, testIndependentCallLegFor(callID, "b-v2"), billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 leg: %v", err)
	}
	return store, ctx, callID, exp
}

func ownerFinalBalance(t *testing.T, store *DurableStore, accountID string) int64 {
	t.Helper()
	return f3Balance(t, store, accountID)
}

func ownerFinalJournals(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	return f3JournalCount(t, store, accountID)
}

func TestOwnerFinalBCutoverWorkerLegacyScalarFailsClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		stub billing.CallRatingResolver
	}{
		{"old-scalar", ownerFinalLegacyScalarStub{charge: 120, fp: "owner-final-b-old"}},
		{"decorated-old", ownerFinalDecoratedOldStub{charge: 120, fp: "owner-final-b-decorated"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx, callID, _ := ownerFinalV2Setup(t, "owner-final-b-"+tc.name, "acct-owner-final-b-"+tc.name, 100000)
			accountID := "acct-owner-final-b-" + tc.name
			balanceBefore := ownerFinalBalance(t, store, accountID)
			journalsBefore := ownerFinalJournals(t, store, accountID)
			expBefore, err := store.GetCallExposure(ctx, callID)
			if err != nil {
				t.Fatal(err)
			}
			if !expBefore.IsOpen() {
				t.Fatalf("V2 exposure must start open")
			}
			opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
			if err != nil {
				t.Fatal(err)
			}
			pinBefore, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
			if err != nil {
				t.Fatalf("V2 admission pin: %v", err)
			}
			worker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, tc.stub, store, 8)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.ProcessOnce(ctx); err == nil {
				t.Fatalf("V2 cutover worker with legacy scalar resolver must fail closed")
			} else if !errors.Is(err, billing.ErrPostingOwnershipInvalid) && !errors.Is(err, billing.ErrRetailRateIncomplete) {
				t.Fatalf("worker error = %v, want ownership/retail fence", err)
			}
			if got := ownerFinalBalance(t, store, accountID); got != balanceBefore {
				t.Fatalf("balance moved %d -> %d on rejected V2 scalar", balanceBefore, got)
			}
			if n := ownerFinalJournals(t, store, accountID); n != journalsBefore {
				t.Fatalf("journals moved %d -> %d on rejected V2 scalar", journalsBefore, n)
			}
			expAfter, err := store.GetCallExposure(ctx, callID)
			if err != nil {
				t.Fatal(err)
			}
			if !expAfter.IsOpen() {
				t.Fatalf("rejected V2 scalar must leave exposure open")
			}
			pinAfter, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
			if err != nil {
				t.Fatalf("pin after: %v", err)
			}
			if pinAfter.Owner != billing.PostingOwnerV2 || pinAfter.IsCompleted() {
				t.Fatalf("rejected V2 scalar must leave V2 pin pinned (not completed), got %#v (before %#v)", pinAfter, pinBefore)
			}
		})
	}
}

func TestOwnerFinalCDirectStoreV2ScalarFailsTransactionally(t *testing.T) {
	t.Parallel()
	store, ctx, callID, exp := ownerFinalV2Setup(t, "owner-final-c", "acct-owner-final-c", 100000)
	accountID := "acct-owner-final-c"
	balanceBefore := ownerFinalBalance(t, store, accountID)
	journalsBefore := ownerFinalJournals(t, store, accountID)
	durableCall, err := store.GetCallUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	scalar := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 120, Currency: "USD"}, Fingerprint: "owner-final-c-scalar"}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: durableCall, Exposure: exp, Result: scalar, PostingOwner: billing.PostingOwnerV2,
	}); !errors.Is(err, billing.ErrRetailRateIncomplete) {
		t.Fatalf("direct V2 scalar Apply err = %v, want %v", err, billing.ErrRetailRateIncomplete)
	}
	if got := ownerFinalBalance(t, store, accountID); got != balanceBefore {
		t.Fatalf("balance moved %d -> %d on direct V2 scalar", balanceBefore, got)
	}
	if n := ownerFinalJournals(t, store, accountID); n != journalsBefore {
		t.Fatalf("journals moved %d -> %d on direct V2 scalar", journalsBefore, n)
	}
	expAfter, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if !expAfter.IsOpen() {
		t.Fatalf("direct V2 scalar must leave exposure open")
	}
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("pin after direct V2 scalar: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 || pin.IsCompleted() {
		t.Fatalf("direct V2 scalar must leave V2 pin pinned, got %#v", pin)
	}
}

func TestOwnerFinalDValidV2ComponentAndPassThroughSucceed(t *testing.T) {
	t.Parallel()
	t.Run("component", func(t *testing.T) {
		t.Parallel()
		store, ctx, callID, exp := ownerFinalV2Setup(t, "owner-final-d-comp", "acct-owner-final-d-comp", 100000)
		accountID := "acct-owner-final-d-comp"
		durableCall, err := store.GetCallUsage(ctx, callID)
		if err != nil {
			t.Fatal(err)
		}
		valid := f3BoundResult(t, durableCall, 70)
		settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: durableCall, Exposure: exp, Result: valid, PostingOwner: billing.PostingOwnerV2,
		})
		if err != nil {
			t.Fatalf("valid V2 component must succeed: %v", err)
		}
		if settled.Replayed {
			t.Fatalf("first V2 component must not be replayed")
		}
		if got := ownerFinalBalance(t, store, accountID); got != 100000-70 {
			t.Fatalf("balance = %d, want %d", got, 100000-70)
		}
		if n := ownerFinalJournals(t, store, accountID); n != 1 {
			t.Fatalf("journals = %d, want 1 customer settlement", n)
		}
		if expAfter, err := store.GetCallExposure(ctx, callID); err != nil {
			t.Fatal(err)
		} else if expAfter.IsOpen() {
			t.Fatalf("valid V2 component must close exposure")
		}
		replayed, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: durableCall, Exposure: exp, Result: valid, PostingOwner: billing.PostingOwnerV2,
		})
		if err != nil {
			t.Fatalf("valid V2 component replay: %v", err)
		}
		if !replayed.Replayed {
			t.Fatalf("second V2 component must be replayed")
		}
		if got := ownerFinalBalance(t, store, accountID); got != 100000-70 {
			t.Fatalf("replay moved balance to %d", got)
		}
		if n := ownerFinalJournals(t, store, accountID); n != 1 {
			t.Fatalf("replay moved journals to %d", n)
		}
	})
	t.Run("pass-through", func(t *testing.T) {
		t.Parallel()
		store, ctx, callID, exp := ownerFinalV2Setup(t, "owner-final-d-pass", "acct-owner-final-d-pass", 100000)
		accountID := "acct-owner-final-d-pass"
		durableCall, err := store.GetCallUsage(ctx, callID)
		if err != nil {
			t.Fatal(err)
		}
		bound := billing.Money{Nano: 800, Currency: "USD"}
		state := billing.CostPassThroughSettlement{
			PolicyRef: durableCall.ChargePolicyRef,
			Policy: billing.CostPassThroughPolicy{
				MissingCost: billing.CostPassThroughMissingCostProvisional,
				SafeBound:   &bound, AllowLateAdjustment: true,
			},
			Status:    billing.CostPassThroughSettlementProvisional,
			SafeBound: bound, PostedAmount: billing.Money{Nano: 60, Currency: "USD"},
		}
		valid := billing.CallRatingResult{
			CallID: callID, CustomerCharge: billing.Money{Nano: 60, Currency: "USD"}, Fingerprint: "owner-final-d-pass",
			CostPassThrough: &state,
		}
		if err := billing.ValidateCallRatingResultForOwner(valid, billing.PostingOwnerV2); err != nil {
			t.Fatalf("V2 pass-through must validate: %v", err)
		}
		settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: durableCall, Exposure: exp, Result: valid, PostingOwner: billing.PostingOwnerV2,
		})
		if err != nil {
			t.Fatalf("valid V2 pass-through must succeed: %v", err)
		}
		if settled.Replayed {
			t.Fatalf("first V2 pass-through must not be replayed")
		}
		if got := ownerFinalBalance(t, store, accountID); got != 100000-60 {
			t.Fatalf("balance = %d, want %d", got, 100000-60)
		}
	})
}

func TestOwnerFinalEV1DrainLegacySucceedsV2LegacyNeverDoes(t *testing.T) {

	t.Parallel()
	// V1 drain with legacy resolver succeeds and replays exactly once.
	t.Run("v1-drain-legacy-succeeds", func(t *testing.T) {
		t.Parallel()
		store := f3NewStore(t, "owner-final-e-v1")
		ctx := context.Background()
		accountID := "acct-owner-final-e-v1"
		f3SetupAccount(t, store, accountID, 100000)
		callID := f3MustCallID(t)
		closure := testIndependentCallUsageFor(callID, []string{"b-v1"})
		closure.AccountID = accountID
		exp, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
			AccountID: accountID, CallID: callID.String(),
			Max:        billing.Money{Nano: 800, Currency: "USD"},
			PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
		})
		if err != nil {
			t.Fatalf("V1 admit: %v", err)
		}
		if err := store.AppendCallUsage(ctx, closure); err != nil {
			t.Fatalf("V1 closure: %v", err)
		}
		if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-v1")); err != nil {
			t.Fatalf("V1 leg: %v", err)
		}
		// Legacy test-only worker (no cutover claim port) with old scalar
		// resolver is the authorized V1 drain path.
		legacy := ownerFinalLegacyScalarStub{charge: 25, fp: "owner-final-e-v1"}
		worker, err := billing.NewCallPostUsageWorker(store, store, legacy, 8)
		if err != nil {
			t.Fatal(err)
		}
		_ = exp
		if err := worker.ProcessOnce(ctx); err != nil {
			t.Fatalf("V1 drain legacy worker: %v", err)
		}
		if got := ownerFinalBalance(t, store, accountID); got != 100000-25 {
			t.Fatalf("V1 drain balance = %d, want %d", got, 100000-25)
		}
		journalsBefore := ownerFinalJournals(t, store, accountID)
		if journalsBefore == 0 {
			t.Fatalf("V1 drain must post journals")
		}
		// Replay posts nothing.
		if err := worker.ProcessOnce(ctx); err != nil {
			t.Fatalf("V1 drain replay: %v", err)
		}
		if got := ownerFinalBalance(t, store, accountID); got != 100000-25 {
			t.Fatalf("V1 replay moved balance to %d", got)
		}
		if n := ownerFinalJournals(t, store, accountID); n != journalsBefore {
			t.Fatalf("V1 replay moved journals %d -> %d", journalsBefore, n)
		}
	})
	// V2 legacy fallback never succeeds (covered with zero effects in B/C).
	t.Run("v2-legacy-never", func(t *testing.T) {
		t.Parallel()
		store, ctx, callID, exp := ownerFinalV2Setup(t, "owner-final-e-v2", "acct-owner-final-e-v2", 100000)
		durableCall, err := store.GetCallUsage(ctx, callID)
		if err != nil {
			t.Fatal(err)
		}
		scalar := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: "owner-final-e-v2-scalar"}
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: durableCall, Exposure: exp, Result: scalar, PostingOwner: billing.PostingOwnerV2,
		}); !errors.Is(err, billing.ErrRetailRateIncomplete) {
			t.Fatalf("V2 legacy scalar must never succeed, got %v", err)
		}
	})
}

// Phase 18 valuation-contract fence at the durable boundary: every V2
// negative below must fail transactionally with zero balance, journal,
// exposure, and pin effects; the bound positive posts exactly once and
// replays without second money.
func TestOwnerFinalDSettlementBindingTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(t *testing.T, call billing.CallUsageRecord, exp billing.CallExposure, res *billing.CallRatingResult)
	}{
		{
			name: "id-only valuation",
			mutate: func(t *testing.T, call billing.CallUsageRecord, exp billing.CallExposure, res *billing.CallRatingResult) {
				t.Helper()
				res.CustomerValuation = economics.Valuation{ID: "bare-label"}
			},
		},
		{
			name: "subject call mismatch",
			mutate: func(t *testing.T, call billing.CallUsageRecord, exp billing.CallExposure, res *billing.CallRatingResult) {
				t.Helper()
				otherID, err := billing.NewBillingCallID()
				if err != nil {
					t.Fatal(err)
				}
				other := call
				other.CallID = otherID
				bound := f3BoundResult(t, other, res.CustomerCharge.Nano)
				res.CustomerValuation = bound.CustomerValuation
			},
		},
		{
			name: "account payer mismatch",
			mutate: func(t *testing.T, call billing.CallUsageRecord, exp billing.CallExposure, res *billing.CallRatingResult) {
				t.Helper()
				v := res.CustomerValuation.Clone()
				v.Payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "acct-intruder"}
				res.CustomerValuation = v
			},
		},
		{
			name: "charge currency mismatch",
			mutate: func(t *testing.T, call billing.CallUsageRecord, exp billing.CallExposure, res *billing.CallRatingResult) {
				t.Helper()
				res.CustomerCharge.Currency = "EUR"
			},
		},
		{
			name: "rated amount mismatch",
			mutate: func(t *testing.T, call billing.CallUsageRecord, exp billing.CallExposure, res *billing.CallRatingResult) {
				t.Helper()
				res.CustomerCharge.Nano++
			},
		},
		{
			name: "malformed valuation basis",
			mutate: func(t *testing.T, call billing.CallUsageRecord, exp billing.CallExposure, res *billing.CallRatingResult) {
				t.Helper()
				v := res.CustomerValuation.Clone()
				v.Basis = economics.BasisLocalExpected
				res.CustomerValuation = v
			},
		},
		{
			name: "result fingerprint mismatch",
			mutate: func(t *testing.T, call billing.CallUsageRecord, exp billing.CallExposure, res *billing.CallRatingResult) {
				t.Helper()
				res.Fingerprint = "stale-fingerprint"
			},
		},
		{
			name: "pass-through posted amount mismatch",
			mutate: func(t *testing.T, call billing.CallUsageRecord, exp billing.CallExposure, res *billing.CallRatingResult) {
				t.Helper()
				bound := billing.Money{Nano: 800, Currency: "USD"}
				state := billing.CostPassThroughSettlement{
					PolicyRef: call.ChargePolicyRef,
					Policy: billing.CostPassThroughPolicy{
						MissingCost: billing.CostPassThroughMissingCostProvisional,
						SafeBound:   &bound, AllowLateAdjustment: true,
					},
					Status:    billing.CostPassThroughSettlementProvisional,
					SafeBound: bound, PostedAmount: billing.Money{Nano: 60, Currency: "USD"},
				}
				*res = billing.CallRatingResult{
					CallID: call.CallID, CustomerCharge: billing.Money{Nano: 61, Currency: "USD"}, Fingerprint: "owner-final-d-pass-bad",
					CostPassThrough: &state,
				}
			},
		},
	}
	for idx, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			suffix := string(rune('a' + idx))
			store, ctx, callID, exp := ownerFinalV2Setup(t, "owner-final-dt-"+suffix, "acct-owner-final-dt-"+suffix, 100000)
			accountID := "acct-owner-final-dt-" + suffix
			durableCall, err := store.GetCallUsage(ctx, callID)
			if err != nil {
				t.Fatal(err)
			}
			res := f3BoundResult(t, durableCall, 70)
			tc.mutate(t, durableCall, exp, &res)
			balanceBefore := ownerFinalBalance(t, store, accountID)
			journalsBefore := ownerFinalJournals(t, store, accountID)
			opKey, err := billing.CustomerPostingOperationKey(accountID, durableCall.CallID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
				Call: durableCall, Exposure: exp, Result: res, PostingOwner: billing.PostingOwnerV2,
			}); !errors.Is(err, billing.ErrRetailRateIncomplete) {
				t.Fatalf("V2 %s Apply err = %v, want %v", tc.name, err, billing.ErrRetailRateIncomplete)
			}
			if got := ownerFinalBalance(t, store, accountID); got != balanceBefore {
				t.Fatalf("V2 %s moved balance %d -> %d", tc.name, balanceBefore, got)
			}
			if n := ownerFinalJournals(t, store, accountID); n != journalsBefore {
				t.Fatalf("V2 %s moved journals %d -> %d", tc.name, journalsBefore, n)
			}
			expAfter, err := store.GetCallExposure(ctx, durableCall.CallID)
			if err != nil {
				t.Fatal(err)
			}
			if !expAfter.IsOpen() {
				t.Fatalf("V2 %s must leave exposure open", tc.name)
			}
			pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
			if err != nil {
				t.Fatalf("V2 %s pin after: %v", tc.name, err)
			}
			if pin.Owner != billing.PostingOwnerV2 || pin.IsCompleted() {
				t.Fatalf("V2 %s must leave V2 pin pinned, got %#v", tc.name, pin)
			}
		})
	}
}
