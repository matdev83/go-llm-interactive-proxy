package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type blockingAuthorityService struct {
	settleCalls          atomic.Int64
	releaseCalls         atomic.Int64
	applyCalls           atomic.Int64
	releaseSucceeds      atomic.Bool
	releaseHadLiveBudget atomic.Bool
	lastErr              atomic.Value
}

func (s *blockingAuthorityService) Admit(context.Context, authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	return authorityapp.AdmissionResult{}, nil
}

func (s *blockingAuthorityService) wait(ctx context.Context) error {
	<-ctx.Done()
	err := ctx.Err()
	s.lastErr.Store(err)
	return err
}

func (s *blockingAuthorityService) Settle(ctx context.Context, _ authorityapp.SettleInput) (authorityapp.SettleResult, error) {
	s.settleCalls.Add(1)
	return authorityapp.SettleResult{}, s.wait(ctx)
}

func (s *blockingAuthorityService) Release(ctx context.Context, _ authorityapp.ReleaseInput) (authorityapp.ReleaseResult, error) {
	s.releaseCalls.Add(1)
	if s.releaseSucceeds.Load() {
		deadline, ok := ctx.Deadline()
		s.releaseHadLiveBudget.Store(ctx.Err() == nil && ok && time.Until(deadline) > 0)
		return authorityapp.ReleaseResult{Applied: true}, nil
	}
	return authorityapp.ReleaseResult{}, s.wait(ctx)
}

func (s *blockingAuthorityService) ApplyUsage(ctx context.Context, _ authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	s.applyCalls.Add(1)
	return authorityapp.ApplyUsageResult{}, s.wait(ctx)
}

func boundedAuthorityLifecycle(svc UsageAuthorityService) authorityLifecycle {
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(1),
		admissionResult: authorityapp.AdmissionResult{Reserved: true, ReservationID: "reservation", ReservedAmount: authorityInputAmount(1), RuleIDs: []string{"rule"}, UnreservedRuleIDs: []string{"advisory"}},
		cleanupTimeout:  500 * time.Millisecond,
	}
	return newAuthorityLifecycle(svc, nil, state, authorityCandidate())
}

func assertBoundedAuthorityCall(t *testing.T, call func()) {
	t.Helper()
	started := time.Now()
	call()
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("authority cleanup elapsed %v, want bounded", elapsed)
	}
}

func TestAuthorityLifecycleDetachedOperationsAreBounded(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("fallback release gets a fresh deadline", func(t *testing.T) {
		t.Parallel()
		svc := &blockingAuthorityService{}
		svc.releaseSucceeds.Store(true)
		lifecycle := boundedAuthorityLifecycle(svc)
		assertBoundedAuthorityCall(t, func() { lifecycle.Settle(parent, authorityapp.SettlementKindCancellation, lipapi.Event{}, true) })
		if svc.settleCalls.Load() != 1 || svc.releaseCalls.Load() != 1 {
			t.Fatalf("calls settle=%d release=%d, want one each", svc.settleCalls.Load(), svc.releaseCalls.Load())
		}
		if err, _ := svc.lastErr.Load().(error); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("context error = %v, want deadline exceeded", err)
		}
		if !svc.releaseHadLiveBudget.Load() || !lifecycle.Settled() {
			t.Fatal("fallback release must receive a fresh live budget and settle the lifecycle")
		}
	})

	t.Run("release", func(t *testing.T) {
		t.Parallel()
		svc := &blockingAuthorityService{}
		lifecycle := boundedAuthorityLifecycle(svc)
		assertBoundedAuthorityCall(t, func() { lifecycle.Release(parent, authorityapp.ReleaseKindLosing) })
		if svc.releaseCalls.Load() != 1 {
			t.Fatalf("release calls = %d", svc.releaseCalls.Load())
		}
	})

	t.Run("unreserved usage", func(t *testing.T) {
		t.Parallel()
		svc := &blockingAuthorityService{}
		lifecycle := boundedAuthorityLifecycle(svc)
		assertBoundedAuthorityCall(t, func() {
			lifecycle.ApplyUnreservedUsage(parent, authorityapp.SettlementKindCancellation, lipapi.Event{})
		})
		if svc.applyCalls.Load() != 1 {
			t.Fatalf("apply calls = %d", svc.applyCalls.Load())
		}
	})

	t.Run("authoritative reconciliation", func(t *testing.T) {
		t.Parallel()
		svc := &blockingAuthorityService{}
		lifecycle := boundedAuthorityLifecycle(svc)
		lifecycle.control.mu.Lock()
		lifecycle.control.terminal = authorityTerminalSettled
		lifecycle.control.mu.Unlock()
		event := lipapi.Event{Kind: lipapi.EventUsageDelta, Accounting: lipapi.UsageAccountingMetadata{Authority: lipapi.UsageAuthorityAuthoritative, Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported}}
		assertBoundedAuthorityCall(t, func() { lifecycle.ReconcileAuthoritative(parent, event) })
		if svc.settleCalls.Load() != 1 {
			t.Fatalf("settle calls = %d", svc.settleCalls.Load())
		}
	})
}
