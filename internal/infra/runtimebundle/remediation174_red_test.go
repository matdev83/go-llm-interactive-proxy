package runtimebundle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	runtimecore "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Phase 17.4 remediation RED (F1): production recovery-port omission.
//
// An internal store-backed monetary composition must prove the durable
// accounting snapshot before any worker/sink starts. A decorator that
// exposes the existing monetary/claim ports but hides
// GetAccountingRecoverySnapshot must be rejected by
// buildProcessBillingRuntime with zero worker/sink side effects. A store
// that exposes the port but serves a malformed/unsupported snapshot must
// be rejected through the same production entrypoint.
//
// These tests must FAIL before the F1 fix (missing port silently allowed)
// and PASS after.

// remediation174PortHidingStore is a production-shaped decorator: it
// exposes the monetary/claim ports used by process workers but deliberately
// hides the AccountingRecoveryStore snapshot port. Interface embedding (not
// concrete-struct embedding) keeps the snapshot method hidden even when the
// wrapped store implements it.
type remediation174PortHidingStore struct {
	billing.AuthoritativeBilling
	billing.CallUsageStore
}

func (remediation174PortHidingStore) GetCutoverClaimMetadata(context.Context, billing.PostingOperationKind, string) (billing.CutoverClaimMetadata, error) {
	return billing.CutoverClaimMetadata{}, billing.ErrPostingOwnershipNotFound
}

func (remediation174PortHidingStore) ClaimCompleteCallsWithCutover(context.Context, int) ([]billing.ClaimedCompleteCall, error) {
	return nil, nil
}

func (remediation174PortHidingStore) ClaimProviderCostWorkWithCutover(context.Context, int) ([]billing.ClaimedProviderCostWork, error) {
	return nil, nil
}

func (remediation174PortHidingStore) ClaimEconomicRevisionWorkWithCutover(context.Context, billing.EconomicRevisionWork, string, time.Duration) (billing.EconomicRevisionWorkClaim, *billing.CutoverClaimMetadata, bool, error) {
	return billing.EconomicRevisionWorkClaim{}, nil, false, nil
}

// remediation174MalformedSnapshotStore exposes the snapshot port but serves
// a malformed snapshot (empty store scope) that core validation rejects.
type remediation174MalformedSnapshotStore struct {
	remediation174PortHidingStore
}

func (remediation174MalformedSnapshotStore) GetAccountingRecoverySnapshot(context.Context) (billing.AccountingRecoverySnapshot, error) {
	return billing.AccountingRecoverySnapshot{}, nil
}

func remediation174ProcessProd(store billing.AuthoritativeBilling, sink *processBillingSink) ProductionOptions {
	return ProductionOptions{
		BillingStore:             store,
		BillingTerminalUsageSink: sink,
		BillingCreditGate:        processBillingCreditGate{},
		BillingExposureAdmission: processBillingAdmission{},
		BillingIdentity: runtimecore.BillingIdentity{
			AccountID: func(context.Context, lipapi.Call) string { return "account" },
		},
		BillingCallRatingResolver:   processBillingCallResolver{},
		BillingProviderCostResolver: processBillingProviderResolver{},
	}
}

func TestRemediation174MissingRecoveryPortRejectsProcessBuild(t *testing.T) {
	t.Parallel()
	sink := &processBillingSink{}
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}
	inner := processBillingStore{}
	decorator := remediation174PortHidingStore{AuthoritativeBilling: inner, CallUsageStore: inner}
	prod := remediation174ProcessProd(decorator, sink)
	_, err := buildProcessBillingRuntime(owner, "", prod)
	if err == nil {
		t.Fatalf("buildProcessBillingRuntime with hidden recovery port must fail closed, got nil error")
	}
	if len(closers) != 0 {
		t.Fatalf("hidden-port rejection registered %d process resources, want 0 (no worker/sink activity)", len(closers))
	}
	sink.mu.Lock()
	starts := sink.starts
	sink.mu.Unlock()
	if starts != 0 {
		t.Fatalf("hidden-port rejection started terminal sink %d times, want 0", starts)
	}
}

func TestRemediation174MalformedSnapshotRejectsProcessBuild(t *testing.T) {
	t.Parallel()
	sink := &processBillingSink{}
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}
	inner := processBillingStore{}
	bad := remediation174MalformedSnapshotStore{remediation174PortHidingStore{AuthoritativeBilling: inner, CallUsageStore: inner}}
	prod := remediation174ProcessProd(bad, sink)
	_, err := buildProcessBillingRuntime(owner, "", prod)
	if err == nil {
		t.Fatalf("buildProcessBillingRuntime with malformed snapshot must fail closed, got nil error")
	}
	if msg := err.Error(); !strings.Contains(msg, "recovery") && !strings.Contains(msg, "accounting") && !strings.Contains(msg, "invalid") && !strings.Contains(msg, "scope") {
		t.Fatalf("malformed-snapshot error must reference the recovery/accounting contract, got %v", err)
	}
	if len(closers) != 0 {
		t.Fatalf("malformed-snapshot rejection registered %d process resources, want 0", len(closers))
	}
	sink.mu.Lock()
	starts := sink.starts
	sink.mu.Unlock()
	if starts != 0 {
		t.Fatalf("malformed-snapshot rejection started terminal sink %d times, want 0", starts)
	}
}
