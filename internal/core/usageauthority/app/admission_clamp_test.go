package app

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func quantityRule(id string, unit domain.AmountUnit, limit int64) domain.Rule {
	return domain.Rule{ID: id, Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict, Unit: unit, Limit: domain.Amount{Unit: unit, Value: limit}, Basis: domain.BasisLegacyProviderPreferredAttempt}
}

func quantityAdmissionInput(unit domain.AmountUnit, value int64) AdmissionInput {
	return AdmissionInput{
		Correlation:    controlplane.Correlation{TraceID: "trace-quantity", RequestID: "request-quantity", ALegID: "a-1", BLegID: "b-1"},
		Scope:          scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1")},
		Dimensions:     domain.Dimensions{Backend: scope.Known("backend-1")},
		Request:        domain.Amount{Unit: unit, Value: value},
		RequestCount:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		ReservationKey: domain.ReservationKey{LogicalRequestID: "request-quantity", ALegID: "a-1", BLegID: "b-1", AttemptID: "attempt-1", RuleID: "quantity", Sequence: 1},
		Authority:      domain.AuthorityLevelAuthoritative,
	}
}

func TestAdmissionQuantityReservationPreservesTokenUnit(t *testing.T) {
	t.Parallel()
	rule := quantityRule("tokens", domain.AmountUnitInputTokens, 100)
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "token-reservation"}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, &fakeEvidenceSink{}, nil)
	got, err := svc.Admit(context.Background(), quantityAdmissionInput(domain.AmountUnitInputTokens, 7))
	if err != nil || !got.Allowed || !got.Reserved {
		t.Fatalf("Admit = %#v, err=%v", got, err)
	}
	if got.ReservedAmount.Unit != domain.AmountUnitInputTokens {
		t.Fatalf("reserved unit = %q, want input_tokens", got.ReservedAmount.Unit)
	}
}

func TestAdmissionRequestQuotaDeniesWithoutMoneyInterpretation(t *testing.T) {
	t.Parallel()
	rule := quantityRule("requests", domain.AmountUnitRequests, 1)
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, &fakeEvidenceSink{}, nil)
	in := quantityAdmissionInput(domain.AmountUnitRequests, 2)
	in.RequestCount.Value = 2
	got, err := svc.Admit(context.Background(), in)
	if err != nil || got.Allowed || got.Outcome != domain.DecisionOutcomeDeny {
		t.Fatalf("request quota admission = %#v, err=%v, want deny", got, err)
	}
}
