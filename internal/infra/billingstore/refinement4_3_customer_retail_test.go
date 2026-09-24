package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

type refinement43StaticCallRatingResolver struct {
	result billing.CallRatingResult
}

func (r refinement43StaticCallRatingResolver) ResolveCallRating(context.Context, billing.CompleteCall, billing.CallExposure) (billing.CallRatingResult, error) {
	return r.result, nil
}

func TestRefinement43CustomerRetailRequiresDurableCallClosure(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{
		ID: "refinement43-retail-closure", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 100, State: billing.AccountReady, Version: 1,
	}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		CallID:        callID,
		AccountID:     account.ID,
		ALegID:        "a-refinement43",
		SessionID:     "session-refinement43",
		StartedAt:     time.Unix(100, 0).UTC(),
		FinishedAt:    time.Unix(101, 0).UTC(),
		Outcome:       billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{
			ID: "prices", Version: "v1",
		},
		ChargePolicyRef: billing.VersionRef{
			ID: "policy", Version: "v1",
		},
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID:       account.ID,
		CallID:          callID.String(),
		Max:             billing.Money{Nano: 60, Currency: "USD"},
		PricingRef:      call.CustomerPricingRef,
		ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := billing.CallRatingResult{
		CallID:         callID,
		CustomerCharge: billing.Money{Nano: 25, Currency: "USD"},
		Fingerprint:    "refinement43-retail-result-v1",
	}

	before, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure, Result: result,
	}); !errors.Is(err, billing.ErrCallIncomplete) {
		t.Fatalf("pre-closure customer settlement = %v, want billing.ErrCallIncomplete", err)
	}
	afterRejected, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRejected.BalanceNano != before.BalanceNano || afterRejected.Version != before.Version {
		t.Fatalf("pre-closure settlement mutated account: before=%+v after=%+v", before, afterRejected)
	}
	open, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if !open.IsOpen() {
		t.Fatalf("pre-closure settlement closed exposure: %+v", open)
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, transaction := range transactions {
		if transaction.OperationKind == "customer_call_settlement" {
			t.Fatalf("pre-closure settlement posted journal: %+v", transaction)
		}
	}

	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure, Result: result,
	})
	if err != nil {
		t.Fatalf("settlement after durable call closure: %v", err)
	}
	if settled.Replayed || settled.Customer.Transaction.ID == "" {
		t.Fatalf("first settlement = %+v, want one customer posting", settled)
	}
	settledAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settledAccount.BalanceNano != 75 {
		t.Fatalf("account after closure settlement = %+v, want balance 75", settledAccount)
	}

	replay, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure, Result: result,
	})
	if err != nil {
		t.Fatalf("identical settlement replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("identical settlement replay = %+v, want Replayed", replay)
	}
	transactions, err = store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	settlementCount := 0
	for _, transaction := range transactions {
		if transaction.OperationKind == "customer_call_settlement" {
			settlementCount++
		}
	}
	if settlementCount != 1 {
		t.Fatalf("customer settlement journal count = %d, want one", settlementCount)
	}

	late := result
	late.CustomerCharge.Nano++
	late.Fingerprint = "refinement43-retail-result-late-correction"
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure, Result: late,
	}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("late changed retail evidence = %v, want ErrOperationConflict", err)
	}
	afterLate, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterLate.BalanceNano != settledAccount.BalanceNano || afterLate.Version != settledAccount.Version {
		t.Fatalf("late changed retail evidence mutated account: before=%+v after=%+v", settledAccount, afterLate)
	}
}

func TestRefinement43CustomerRetailSettlesEachResumedBillingCallIndependently(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{
		ID: "refinement43-retail-resume", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 100, State: billing.AccountReady, Version: 1,
	}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	firstID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	base := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		AccountID:     account.ID,
		ALegID:        "a-refinement43-resumed",
		SessionID:     "session-refinement43-resumed",
		Outcome:       billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{
			ID: "prices", Version: "v1",
		},
		ChargePolicyRef: billing.VersionRef{
			ID: "policy", Version: "v1",
		},
	}
	first := base
	first.CallID = firstID
	first.StartedAt = time.Unix(200, 0).UTC()
	first.FinishedAt = time.Unix(201, 0).UTC()
	second := base
	second.CallID = secondID
	second.StartedAt = time.Unix(300, 0).UTC()
	second.FinishedAt = time.Unix(301, 0).UTC()
	for _, call := range []billing.CallUsageRecord{first, second} {
		if err := store.AppendCallUsage(ctx, call); err != nil {
			t.Fatal(err)
		}
	}
	firstExposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID:       account.ID,
		CallID:          firstID.String(),
		Max:             billing.Money{Nano: 40, Currency: "USD"},
		PricingRef:      first.CustomerPricingRef,
		ChargePolicyRef: first.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondExposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID:       account.ID,
		CallID:          secondID.String(),
		Max:             billing.Money{Nano: 40, Currency: "USD"},
		PricingRef:      second.CustomerPricingRef,
		ChargePolicyRef: second.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, settlement := range []struct {
		call     billing.CallUsageRecord
		exposure billing.CallExposure
		charge   int64
		fp       string
	}{
		{call: first, exposure: firstExposure, charge: 15, fp: "refinement43-resume-first"},
		{call: second, exposure: secondExposure, charge: 20, fp: "refinement43-resume-second"},
	} {
		if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: settlement.call, Exposure: settlement.exposure,
			Result: billing.CallRatingResult{
				CallID:         settlement.call.CallID,
				CustomerCharge: billing.Money{Nano: settlement.charge, Currency: "USD"},
				Fingerprint:    settlement.fp,
			},
		}); err != nil {
			t.Fatalf("settle resumed call %s: %v", settlement.call.CallID, err)
		}
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 65 || got.Version != 3 {
		t.Fatalf("resumed-call account = %+v, want balance 65/version 3", got)
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	settlementCount := 0
	for _, transaction := range transactions {
		if transaction.OperationKind == "customer_call_settlement" {
			settlementCount++
		}
	}
	if settlementCount != 2 {
		t.Fatalf("resumed-call customer settlement count = %d, want two BillingCallID postings", settlementCount)
	}
}

func TestRefinement43CustomerRetailWorkerWaitsForCallClosure(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{
		ID: "refinement43-retail-worker", Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 100, State: billing.AccountReady, Version: 1,
	}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		CallID:        callID,
		AccountID:     account.ID,
		ALegID:        "a-refinement43-worker",
		SessionID:     "session-refinement43-worker",
		StartedAt:     time.Unix(400, 0).UTC(),
		FinishedAt:    time.Unix(401, 0).UTC(),
		Outcome:       billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{
			ID: "prices", Version: "v1",
		},
		ChargePolicyRef: billing.VersionRef{
			ID: "policy", Version: "v1",
		},
		ExpectedBLegIDs: []string{"b-retry", "b-winner"},
	}
	retry := testIndependentCallLegFor(callID, "b-retry")
	retry.ALegID = call.ALegID
	retry.Outcome = billing.LegOutcomeFailed
	retry.Surfaced = billing.SurfacedNo
	retry.AttemptSeq = 1
	winner := testIndependentCallLegFor(callID, "b-winner")
	winner.ALegID = call.ALegID
	winner.Outcome = billing.LegOutcomeWinner
	winner.Surfaced = billing.SurfacedYes
	winner.AttemptSeq = 2
	for _, leg := range []billing.CallLegUsageRecord{retry, winner} {
		if err := store.AppendCallLegUsage(ctx, leg); err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID:       account.ID,
		CallID:          callID.String(),
		Max:             billing.Money{Nano: 60, Currency: "USD"},
		PricingRef:      call.CustomerPricingRef,
		ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := billing.NewCallPostUsageWorker(store, store, refinement43StaticCallRatingResolver{
		result: billing.CallRatingResult{
			CallID:         callID,
			CustomerCharge: billing.Money{Nano: 25, Currency: "USD"},
			Fingerprint:    "refinement43-worker-result-v1",
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("worker before call closure: %v", err)
	}
	beforeClosure, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeClosure.BalanceNano != account.BalanceNano {
		t.Fatalf("worker settled before call closure: %+v", beforeClosure)
	}

	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("worker after call closure: %v", err)
	}
	afterClosure, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterClosure.BalanceNano != 75 {
		t.Fatalf("worker settlement after closure = %+v, want balance 75", afterClosure)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("worker replay: %v", err)
	}
	afterReplay, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterReplay.BalanceNano != afterClosure.BalanceNano || afterReplay.Version != afterClosure.Version {
		t.Fatalf("worker replay mutated account: before=%+v after=%+v", afterClosure, afterReplay)
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	settlementCount := 0
	for _, transaction := range transactions {
		if transaction.OperationKind == "customer_call_settlement" {
			settlementCount++
		}
	}
	if settlementCount != 1 {
		t.Fatalf("worker customer settlement count = %d, want one", settlementCount)
	}
}
