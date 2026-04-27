package modelcatalog_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestEligibilityResolver_noLimit_eligible(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitUnknown},
		},
	}
	call := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if !got.IsEligible || got.Reason != modelcatalog.EligibilityEligible {
		t.Fatalf("got %+v", got)
	}
}

func TestEligibilityResolver_noEstimate_eligible(t *testing.T) {
	t.Parallel()
	// Estimator returns unavailable when session hints without contribution
	est := modelcatalog.DefaultSizeEstimator{}
	r := modelcatalog.NewEligibilityResolver(est)
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 10},
		},
	}
	call := lipapi.Call{
		Session:  lipapi.SessionRef{ResumeToken: "t"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("manybyteshere")}}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if !got.IsEligible {
		t.Fatalf("expected eligible when estimate unavailable, got %+v", got)
	}
}

func TestEligibilityResolver_underLimit_eligible(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 1000},
		},
	}
	call := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if !got.IsEligible || got.Reason != modelcatalog.EligibilityEligible {
		t.Fatalf("got %+v", got)
	}
	if !got.Estimate.Available {
		t.Fatalf("estimate should be available: %+v", got.Estimate)
	}
}

func TestEligibilityResolver_overLimit_ineligible(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 1},
		},
	}
	call := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("ab")}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if got.IsEligible || got.Reason != modelcatalog.EligibilityContextLimitExceeded {
		t.Fatalf("got %+v", got)
	}
}

func TestEligibilityResolver_exactlyAtLimit_eligible(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 2},
		},
	}
	call := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("ab")}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if !got.IsEligible {
		t.Fatalf("at limit should be eligible, got %+v", got)
	}
}
