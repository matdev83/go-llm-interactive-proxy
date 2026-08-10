package core

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/semantic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type runtimeCoreView struct{}

func (runtimeCoreView) DeriveRequirements(call *lipapi.Call) (lipapi.ProtocolRequirements, error) {
	if call == nil {
		return lipapi.ProtocolRequirements{}, errors.New("nil call")
	}
	return lipapi.DeriveProtocolRequirements(*call), nil
}
func (runtimeCoreView) MatchRequirements(req lipapi.ProtocolRequirements, caps []lipapi.Capability) bool {
	return lipapi.MatchRequirements(req, lipapi.ProtocolRequirements{Capabilities: caps}, lipapi.ReasoningReplaySupport{}).Kind != lipapi.NegotiationReject
}
func (runtimeCoreView) Probe(ctx context.Context, scenario semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	if err := ctx.Err(); err != nil {
		return semantic.ExecutionEvidence{}, err
	}
	call := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "t"}}}}}
	req, err := runtimeCoreView{}.DeriveRequirements(&call)
	if err != nil {
		return semantic.ExecutionEvidence{}, err
	}
	if !(runtimeCoreView{}).MatchRequirements(req, req.Capabilities) {
		return semantic.ExecutionEvidence{}, errors.New("match rejected")
	}
	return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, Derived: true, ExactMatch: true}, nil
}

type runtimeCoreHarness struct{}

func (runtimeCoreHarness) Subject() semantic.SubjectDescriptor {
	return semantic.SubjectDescriptor{ID: "canonical-core", Kind: semantic.KindCanonicalCore, Transports: []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming}}
}
func (runtimeCoreHarness) Derivation(context.Context) (RequirementDerivationView, error) {
	return runtimeCoreView{}, nil
}

func TestCanonicalCoreTCK_UsesRealCanonicalOperations(t *testing.T) {
	if err := RunSemanticInvariantSuite(DefaultSemanticInvariantHooks()); err != nil {
		t.Fatal(err)
	}
	cert, err := CertifyCore(context.Background(), runtimeCoreHarness{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.ValidateReleaseReady(); err != nil {
		t.Fatal(err)
	}
	if len(cert.ExecutedScenarioIDs) == 0 || cert.HarnessCalls <= len(cert.ExecutedScenarioIDs) {
		t.Fatalf("missing core execution evidence: %+v", cert)
	}
}

func TestCanonicalCoreTCK_MutationDoublesTurnEachInvariantRED(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*SemanticInvariantHooks)
	}{
		{"derivation", func(h *SemanticInvariantHooks) {
			h.Derive = func(lipapi.Call) lipapi.ProtocolRequirements { return lipapi.ProtocolRequirements{} }
		}},
		{"matching", func(h *SemanticInvariantHooks) {
			h.Match = func(lipapi.ProtocolRequirements, lipapi.ProtocolRequirements) bool { return true }
		}},
		{"item_projection", func(h *SemanticInvariantHooks) {
			h.ProjectItems = func(lipapi.Call, lipapi.LegacyProjectionTarget) (lipapi.LegacyProjectionResult, error) {
				return lipapi.LegacyProjectionResult{}, nil
			}
		}},
		{"legacy_projection", func(h *SemanticInvariantHooks) {
			h.ProjectLegacy = func(lipapi.Call, lipapi.OrderedItemProjectionTarget) ([]lipapi.Item, lipapi.ProtocolRequirements, error) {
				return nil, lipapi.ProtocolRequirements{}, nil
			}
		}},
		{"admission", func(h *SemanticInvariantHooks) {
			h.Admit = func(lipapi.CandidateAdmissionInput) lipapi.CandidateAdmissionResult {
				return lipapi.CandidateAdmissionResult{Kind: lipapi.NegotiationLossless}
			}
		}},
		{"terminal_validation", func(h *SemanticInvariantHooks) { h.Validate = func([]lipapi.Event) error { return nil } }},
		{"commitment", func(h *SemanticInvariantHooks) { h.OutputPolicy = func(bool, bool) error { return nil } }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			mutated := DefaultSemanticInvariantHooks()
			tc.mutate(&mutated)
			if err := RunSemanticInvariantSuite(mutated); err == nil {
				t.Fatalf("mutation %q did not turn the invariant suite RED", tc.name)
			}
		})
	}
}

func TestCanonicalCoreTCK_AdmissionRejectsBeforeProvider(t *testing.T) {
	call := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartImageRef, ImageRef: "img"}}}}}
	res := capabilities.AdmitCandidate(context.Background(), call, lipapi.Invocation{}, routing.AttemptCandidate{}, capabilities.CandidateFacts{Caps: lipapi.NewBackendCaps(), TransportCaps: lipapi.BackendTransportCaps{}})
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("expected hard capability rejection, got %+v", res)
	}
}
