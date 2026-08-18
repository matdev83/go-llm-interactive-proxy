package app

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func advisoryAdmissionRule() domain.Rule {
	return domain.Rule{
		ID:    "tenant.requests.advisory",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeAdvisory,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}
}

func advisoryAdmissionInput(requestValue int64) AdmissionInput {
	return AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  "request-1",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 1,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("principal-1"),
			TenantID:    scope.Known("tenant-1"),
		},
		Dimensions: domain.Dimensions{
			Backend: scope.Known("backend-1"),
			Model:   scope.Known("model-1"),
		},
		Request:   domain.Amount{Unit: domain.AmountUnitRequests, Value: requestValue},
		Authority: domain.AuthorityLevelAuthoritative,
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: "request-1",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-1",
			RuleID:           "tenant.requests.advisory",
			Sequence:         1,
		},
	}
}

// TestAdmitPopulatesAdvisoryRuleIDs proves Admit surfaces matched advisory rule
// IDs separately from the strict reservation RuleIDs (requirement 7.7).
func TestAdmitPopulatesAdvisoryRuleIDs(t *testing.T) {
	t.Parallel()

	store := newFakeStateStore()
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateAdvisoryOnly, Reason: domain.StatusReasonAdvisoryOnly},
		Rules:  []domain.Rule{advisoryAdmissionRule()},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	got, err := svc.Admit(context.Background(), advisoryAdmissionInput(11))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(got.AdvisoryRuleIDs) != 1 || got.AdvisoryRuleIDs[0] != "tenant.requests.advisory" {
		t.Fatalf("AdvisoryRuleIDs = %#v, want [tenant.requests.advisory]", got.AdvisoryRuleIDs)
	}
}

// TestAdmitAdvisoryOnlyDoesNotReserve proves a request matching only advisory
// rules is allowed but never reserves (requirement 6.7).
func TestAdmitAdvisoryOnlyDoesNotReserve(t *testing.T) {
	t.Parallel()

	store := newFakeStateStore()
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateAdvisoryOnly, Reason: domain.StatusReasonAdvisoryOnly},
		Rules:  []domain.Rule{advisoryAdmissionRule()},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	got, err := svc.Admit(context.Background(), advisoryAdmissionInput(11))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !got.Allowed {
		t.Fatalf("advisory-only admission must allow: %#v", got)
	}
	if got.Reserved || got.ReservationID != "" {
		t.Fatalf("advisory-only admission must not reserve: %#v", got)
	}
	if len(store.reserveCalls) != 0 {
		t.Fatalf("advisory-only admission must not mutate store: %#v", store.reserveCalls)
	}
	if len(got.AdvisoryRuleIDs) != 1 {
		t.Fatalf("advisory-only admission must still surface AdvisoryRuleIDs: %#v", got.AdvisoryRuleIDs)
	}
}

// TestAdmitStrictAndAdvisoryMixPopulatesBoth proves a request matching a strict
// rule and an advisory rule reserves against the strict rule AND surfaces the
// advisory rule on AdvisoryRuleIDs (so the runtime can apply advisory usage
// later) without reserving against it.
func TestAdmitStrictAndAdvisoryMixPopulatesBoth(t *testing.T) {
	t.Parallel()

	strictRule := domain.Rule{
		ID:    "tenant.requests.strict",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 100},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}
	advisory := advisoryAdmissionRule()

	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-strict", ReservedAmount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 3}}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{strictRule, advisory},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	in := advisoryAdmissionInput(3)
	in.ReservationKey.RuleID = strictRule.ID
	got, err := svc.Admit(context.Background(), in)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !got.Allowed || !got.Reserved {
		t.Fatalf("strict+advisory mix must allow and reserve: %#v", got)
	}
	if len(got.AdvisoryRuleIDs) != 1 || got.AdvisoryRuleIDs[0] != advisory.ID {
		t.Fatalf("AdvisoryRuleIDs = %#v, want [tenant.requests.advisory]", got.AdvisoryRuleIDs)
	}
	// Only the strict rule reserves.
	if len(store.reserveCalls) != 1 {
		t.Fatalf("strict+advisory mix must reserve only against the strict rule, got %d: %#v", len(store.reserveCalls), store.reserveCalls)
	}
	if store.reserveCalls[0].RuleID != strictRule.ID {
		t.Fatalf("reserve must target the strict rule, got %q", store.reserveCalls[0].RuleID)
	}
}
