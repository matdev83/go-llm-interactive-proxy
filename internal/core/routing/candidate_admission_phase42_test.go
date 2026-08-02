package routing_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

func TestContinuationCandidatePortableReroutes(t *testing.T) {
	err := routing.CheckContinuationCandidate(routing.ContinuationConstraint{}, routing.AttemptCandidate{Key: "b:m", Primary: routing.Primary{Backend: "b", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestContinuationCandidatePinsProviderBoundLineage(t *testing.T) {
	err := routing.CheckContinuationCandidate(routing.ContinuationConstraint{Lineage: lipcont.Lineage{ProviderBound: true, ProviderID: "provider-a", Model: "model-a", CandidateKey: "provider-a:model-a"}}, routing.AttemptCandidate{Key: "provider-b:model-a", Primary: routing.Primary{Backend: "provider-b", Model: "model-a"}})
	if !errors.Is(err, routing.ErrContinuationCandidatePinned) {
		t.Fatalf("err=%v", err)
	}
}

func TestContinuationCandidateRejectsNativeMismatch(t *testing.T) {
	err := routing.CheckContinuationCandidate(routing.ContinuationConstraint{NativeRequirements: []lipcont.NativeRequirement{{BackendID: "provider-a", Model: "model-a", Kind: "reasoning"}}}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "provider-a", Model: "model-b"}})
	if !errors.Is(err, routing.ErrContinuationNativeMismatch) {
		t.Fatalf("err=%v", err)
	}
}

func TestContinuationCandidateAdmissionRetainsStoredRequirements(t *testing.T) {
	result := (routing.CandidateAdmissionCheck{
		Call:         lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("new")}}}},
		Candidate:    routing.AttemptCandidate{Key: "provider-a:model-a", Primary: routing.Primary{Backend: "provider-a", Model: "model-a"}},
		BackendCaps:  lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Continuation: &routing.ContinuationConstraint{Requirements: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityCompaction}}},
	}).Evaluate()
	if result.Kind != lipapi.NegotiationReject {
		t.Fatalf("continuation requirements were weakened: %+v", result)
	}
}
