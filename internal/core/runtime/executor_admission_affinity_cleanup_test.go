package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity/memorystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNoteCandidateAdmissionReject_ClearsStickyAffinityBinding(t *testing.T) {
	ctx := context.Background()
	affinityStore := memorystore.New()
	key := affinity.Key{Scope: affinity.ScopeSession, ID: "session-affinity-id"}
	if err := affinityStore.Set(ctx, affinity.Binding{
		Key:          key,
		BackendID:    "rejected-backend",
		CandidateKey: "rejected-backend:model",
	}); err != nil {
		t.Fatal(err)
	}

	ex := TestExecutor()
	ex.AffinityStore = affinityStore
	ex.Bus = hooks.New(hooks.Config{})
	p := attemptOpenParams{
		bus:         ex.Bus,
		traceID:     "trace-affinity-reject",
		affinityKey: key,
		affinitySet: true,
		excluded:    map[string]struct{}{},
	}
	candidate := routing.AttemptCandidate{
		Key:     "rejected-backend:model",
		Primary: routing.Primary{Backend: "rejected-backend", Model: "model"},
	}
	out := candidateAdmissionOutcome{admitRes: lipapi.CandidateAdmissionResult{
		Kind:       lipapi.NegotiationReject,
		Capability: lipapi.NegotiationResult{Kind: lipapi.NegotiationReject},
	}}

	// This is the production rejection handler used by openPlannedCandidate after
	// real capability/transport admission, not the core TCK's synthetic callback.
	ex.noteCandidateAdmissionReject(ctx, p, candidate, "rejected-backend", true, out, "pre_open")

	if _, ok, err := affinityStore.Get(ctx, key); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("sticky affinity binding survived rejected candidate admission")
	}
}
