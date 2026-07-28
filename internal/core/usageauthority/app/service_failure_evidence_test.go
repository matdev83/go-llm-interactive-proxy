package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestSettlementFailureEmitsUnavailableLifecycleEvidence(t *testing.T) {
	t.Parallel()

	rule := domain.Rule{
		ID:    "failure.evidence",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	store.settleErr = errors.New("transaction rolled back")
	evidence := &fakeEvidenceSink{}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{rule},
	}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

	_, err := svc.Settle(context.Background(), SettleInput{
		Correlation:      controlplane.Correlation{TraceID: "trace-failure", RequestID: "request-failure", BackendID: "backend-1"},
		Scope:            scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1")},
		RuleID:           rule.ID,
		ReservationID:    "reservation-failure",
		Kind:             SettlementKindFinal,
		FinalUsage:       domain.Amount{Unit: domain.AmountUnitRequests, Value: 4},
		ReservedUsage:    domain.Amount{Unit: domain.AmountUnitRequests, Value: 5},
		Authority:        domain.AuthorityLevelAuthoritative,
		Stage:            feature.StageIDAttemptLifecycle,
		BackendAttempted: true,
		OutputCommitted:  true,
	})
	if err == nil {
		t.Fatal("settlement must return the store failure")
	}
	if len(evidence.policy) != 1 || len(evidence.accounting) != 1 {
		t.Fatalf("failure evidence counts = policy=%d accounting=%d, want one each", len(evidence.policy), len(evidence.accounting))
	}
	policy := evidence.policy[0]
	if policy.Stage != feature.StageIDAttemptLifecycle || !policy.BackendAttempted || !policy.OutputCommitted {
		t.Fatalf("failure policy lifecycle fields = %#v", policy)
	}
	if policy.ReasonCode != "unavailable" || policy.Outcome == "deny" {
		t.Fatalf("failure policy posture = outcome=%q reason=%q, want non-denial unavailable", policy.Outcome, policy.ReasonCode)
	}
	detail := evidence.accounting[0].AccountingAuthority()
	if detail == nil || detail.Outcome != controlplane.AccountingOutcomeUnavailable || detail.Authority != controlplane.AccountingAuthoritySourceUnavailable {
		t.Fatalf("failure accounting detail = %#v, want unavailable evidence", detail)
	}
}
