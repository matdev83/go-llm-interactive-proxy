package runtimebundle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	runtimecore "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type processBillingSink struct {
	mu     sync.Mutex
	starts int
	stops  int
	closes int
}

func (s *processBillingSink) AppendCall(context.Context, billing.CallUsageRecord) error { return nil }

func (s *processBillingSink) AppendLeg(context.Context, billing.CallLegUsageRecord) error { return nil }

func (s *processBillingSink) Start(context.Context) error {
	s.mu.Lock()
	s.starts++
	s.mu.Unlock()
	return nil
}

func (s *processBillingSink) Stop(context.Context) error {
	s.mu.Lock()
	s.stops++
	s.mu.Unlock()
	return nil
}

func (s *processBillingSink) Close() error { s.mu.Lock(); s.closes++; s.mu.Unlock(); return nil }

type processBillingStore struct{}

// GetCutoverClaimMetadata exposes the narrow B2a claim port for production
// test doubles. It returns NotFound so workers degrade to legacy empty-claim
// behavior in these unit tests; production DurableStore returns durable pins.
func (processBillingStore) GetCutoverClaimMetadata(context.Context, billing.PostingOperationKind, string) (billing.CutoverClaimMetadata, error) {
	return billing.CutoverClaimMetadata{}, billing.ErrPostingOwnershipNotFound
}

func (processBillingStore) ClaimCompleteCallsWithCutover(context.Context, int) ([]billing.ClaimedCompleteCall, error) {
	return nil, nil
}

func (processBillingStore) ClaimProviderCostWorkWithCutover(context.Context, int) ([]billing.ClaimedProviderCostWork, error) {
	return nil, nil
}

func (processBillingStore) ClaimEconomicRevisionWorkWithCutover(context.Context, billing.EconomicRevisionWork, string, time.Duration) (billing.EconomicRevisionWorkClaim, *billing.CutoverClaimMetadata, bool, error) {
	return billing.EconomicRevisionWorkClaim{}, nil, false, nil
}

// customerOnlyProcessBillingStore deliberately exposes the customer settlement
// and reporting ports without exposing provider-cost work. A store may be in
// this state while supplier queue infrastructure is unavailable; independent
// retail settlement must still be constructible and runnable.
type customerOnlyProcessBillingStore struct {
	billing.AuthoritativeBilling
	billing.CallUsageStore
}

// GetCutoverClaimMetadata exposes the narrow B2a claim port for the customer-
// only production test double (explicit port, legacy empty-claim behavior).
func (customerOnlyProcessBillingStore) GetCutoverClaimMetadata(context.Context, billing.PostingOperationKind, string) (billing.CutoverClaimMetadata, error) {
	return billing.CutoverClaimMetadata{}, billing.ErrPostingOwnershipNotFound
}

func (customerOnlyProcessBillingStore) ClaimCompleteCallsWithCutover(context.Context, int) ([]billing.ClaimedCompleteCall, error) {
	return nil, nil
}

func (processBillingStore) ApplyCallBillingResult(context.Context, billing.ApplyCallBillingInput) (billing.CallSettlement, error) {
	return billing.CallSettlement{}, nil
}

func (processBillingStore) AccountReport(context.Context, string, billing.PageRequest) (billing.AccountReport, error) {
	return billing.AccountReport{}, nil
}

func (processBillingStore) CallExplanation(context.Context, string) (billing.CallExplanation, error) {
	return billing.CallExplanation{}, nil
}

func (processBillingStore) OperatorCostReport(context.Context, billing.ReportFilter) (billing.OperatorCostReport, error) {
	return billing.OperatorCostReport{}, nil
}

func (processBillingStore) TrialBalanceReport(context.Context, billing.ReportFilter) (billing.TrialBalanceReport, error) {
	return billing.TrialBalanceReport{}, nil
}

func (processBillingStore) QueryOpenExposures(context.Context, string, billing.PageRequest) (billing.ExposurePage, error) {
	return billing.ExposurePage{}, nil
}

func (processBillingStore) QueryReconcileRequired(context.Context, billing.PageRequest) (billing.AccountStatePage, error) {
	return billing.AccountStatePage{}, nil
}

func (processBillingStore) ClaimCompleteCalls(context.Context, int) ([]billing.CompleteCall, error) {
	return nil, nil
}

func (processBillingStore) ClaimCompleteCall(context.Context, billing.BillingCallID) (billing.CompleteCall, error) {
	return billing.CompleteCall{}, nil
}

func (processBillingStore) GetCallExposure(context.Context, billing.BillingCallID) (billing.CallExposure, error) {
	return billing.CallExposure{}, nil
}

func (processBillingStore) RetryCompleteCall(context.Context, billing.BillingCallID, string) error {
	return nil
}

func (processBillingStore) ListPendingProviderCostWork(context.Context, int) ([]billing.ProviderCostWork, error) {
	return nil, nil
}

func (processBillingStore) ApplyProviderCost(context.Context, billing.ApplyProviderCostInput) (billing.Posting, error) {
	return billing.Posting{}, nil
}

type processBillingCreditGate struct{}

func (processBillingCreditGate) Check(context.Context, string) error { return nil }

type processBillingAdmission struct{}

func (processBillingAdmission) Admit(context.Context, runtimecore.BillingExposureAdmissionInput) (billing.CallExposure, error) {
	return billing.CallExposure{}, nil
}

type processBillingCallResolver struct{}

func (processBillingCallResolver) ResolveCallRating(context.Context, billing.CompleteCall, billing.CallExposure) (billing.CallRatingResult, error) {
	return billing.CallRatingResult{}, nil
}

type processBillingProviderResolver struct{}

func (processBillingProviderResolver) ResolveProviderCost(context.Context, billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	return billing.OperatorCostResult{}, nil
}

func TestBuildProcessBillingRuntimeOwnsResourcesAcrossGenerationLifetime(t *testing.T) {
	sink := &processBillingSink{}
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}
	accountID := func(context.Context, lipapi.Call) string { return "account" }
	prod := ProductionOptions{
		BillingStore:                processBillingStore{},
		BillingTerminalUsageSink:    sink,
		BillingCreditGate:           processBillingCreditGate{},
		BillingExposureAdmission:    processBillingAdmission{},
		BillingIdentity:             runtimecore.BillingIdentity{AccountID: accountID},
		BillingCallRatingResolver:   processBillingCallResolver{},
		BillingProviderCostResolver: processBillingProviderResolver{},
	}
	if _, err := buildProcessBillingRuntime(owner, "", prod); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	starts, stops, closes := sink.starts, sink.stops, sink.closes
	sink.mu.Unlock()
	if starts != 1 || stops != 0 || closes != 0 {
		t.Fatalf("lifecycle after process construction = starts:%d stops:%d closes:%d", starts, stops, closes)
	}
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i](); err != nil {
			t.Fatal(err)
		}
	}
	sink.mu.Lock()
	starts, stops, closes = sink.starts, sink.stops, sink.closes
	sink.mu.Unlock()
	if starts != 1 || stops != 1 || closes != 1 {
		t.Fatalf("lifecycle after process close = starts:%d stops:%d closes:%d", starts, stops, closes)
	}
}

func TestBuildProcessBillingRuntimeRequiresInjectedTerminalSink(t *testing.T) {
	t.Parallel()
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}
	prod := ProductionOptions{
		BillingStore:                processBillingStore{},
		BillingCreditGate:           processBillingCreditGate{},
		BillingExposureAdmission:    processBillingAdmission{},
		BillingIdentity:             runtimecore.BillingIdentity{AccountID: func(context.Context, lipapi.Call) string { return "account" }},
		BillingCallRatingResolver:   processBillingCallResolver{},
		BillingProviderCostResolver: processBillingProviderResolver{},
	}
	_, err := buildProcessBillingRuntime(owner, "", prod)
	if !errors.Is(err, ErrAuthoritativeBillingRequired) {
		t.Fatalf("buildProcessBillingRuntime error = %v, want injected terminal sink requirement", err)
	}
	if len(closers) != 0 {
		t.Fatalf("incomplete billing composition registered %d process resources", len(closers))
	}
}

func TestBuildProcessBillingRuntimeAllowsIndependentRetailWithoutSupplierWorker(t *testing.T) {
	t.Parallel()
	sink := &processBillingSink{}
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}
	t.Cleanup(func() {
		for i := len(closers) - 1; i >= 0; i-- {
			if err := closers[i](); err != nil {
				t.Errorf("cleanup process resource: %v", err)
			}
		}
	})
	store := customerOnlyProcessBillingStore{
		AuthoritativeBilling: processBillingStore{},
		CallUsageStore:       processBillingStore{},
	}
	prod := ProductionOptions{
		BillingStore:             store,
		BillingTerminalUsageSink: sink,
		BillingCreditGate:        processBillingCreditGate{},
		BillingExposureAdmission: processBillingAdmission{},
		BillingIdentity: runtimecore.BillingIdentity{
			AccountID: func(context.Context, lipapi.Call) string { return "account" },
		},
		BillingCallRatingResolver: processBillingCallResolver{},
	}
	if _, err := buildProcessBillingRuntime(owner, "", prod); err != nil {
		t.Fatalf("buildProcessBillingRuntime = %v, want independent customer runtime", err)
	}
	if len(closers) != 3 {
		t.Fatalf("registered process resources = %d, want terminal sink plus customer worker only", len(closers))
	}
}
