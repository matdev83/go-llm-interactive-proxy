package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

type cancelingEvidenceSink struct{ cancel context.CancelFunc }

func (s cancelingEvidenceSink) RecordPolicyDecision(context.Context, policydecision.Record) error {
	s.cancel()
	return errors.New("sink down")
}

func (s cancelingEvidenceSink) RecordAccountingAuthority(context.Context, controlplane.Event) error {
	return nil
}

func TestAdmissionCompensationUsesBoundedDetachedContext(t *testing.T) {
	t.Parallel()
	rule := domain.Rule{ID: "strict", Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict, Unit: domain.AmountUnitRequests, Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, FailureBehavior: domain.FailureBehaviorFailClosed}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady}
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation", ReservedAmount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1}}
	store.releaseWaitForContext = true
	parent, cancel := context.WithCancel(context.Background())
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, cancelingEvidenceSink{cancel: cancel}, fixedClock{now: time.Unix(100, 0)}, ServiceOptions{CleanupTimeout: 20 * time.Millisecond})
	started := time.Now()
	result, err := svc.Admit(parent, AdmissionInput{RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1}, Authority: domain.AuthorityLevelAuthoritative, ReservationKey: domain.ReservationKey{LogicalRequestID: "r", AttemptID: "a", Sequence: 1}})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("compensation elapsed %v, want bounded", elapsed)
	}
	if !errors.Is(store.releaseContextErr, context.DeadlineExceeded) {
		t.Fatalf("release context error = %v, want deadline exceeded", store.releaseContextErr)
	}
	if !result.Reserved || result.Allowed {
		t.Fatalf("result = %#v, failed compensation must retain reservation and deny", result)
	}
}
