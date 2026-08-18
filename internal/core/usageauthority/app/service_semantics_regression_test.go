package app

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func TestAdmissionQuantityRuleFailureSemantics(t *testing.T) {
	t.Parallel()
	rule := domain.Rule{ID: "quota.decisive", Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict, Unit: domain.AmountUnitInputTokens, Limit: domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 10}, Basis: domain.BasisLegacyProviderPreferredAttempt}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})
	in := AdmissionInput{Correlation: controlplane.Correlation{TraceID: "trace", RequestID: "request"}, Request: domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 11}, Authority: domain.AuthorityLevelAuthoritative, ReservationKey: domain.ReservationKey{LogicalRequestID: "request", RuleID: rule.ID, Sequence: 1}}
	got, err := svc.Admit(context.Background(), in)
	if err != nil || got.Allowed || got.Outcome != domain.DecisionOutcomeDeny {
		t.Fatalf("admission = %#v, err=%v, want quantity denial", got, err)
	}
}
