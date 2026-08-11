package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/semantic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// RequirementDerivationView tests canonical core requirement derivation and matching without real providers.
type RequirementDerivationView interface {
	DeriveRequirements(call *lipapi.Call) (lipapi.ProtocolRequirements, error)
	MatchRequirements(reqs lipapi.ProtocolRequirements, caps []lipapi.Capability) bool
	Probe(ctx context.Context, scenario semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error)
}

// CoreHarness provides test interfaces for canonical-core certification.
type CoreHarness interface {
	Subject() semantic.SubjectDescriptor
	Derivation(ctx context.Context) (RequirementDerivationView, error)
}

// SemanticInvariantHooks is the dependency-neutral seam used by the core TCK.
// The default hooks call canonical lipapi/core operations; tests can replace one
// hook with a mutation double and prove that the corresponding invariant turns RED.
// No frontend or backend adapter is involved in this suite.
type SemanticInvariantHooks struct {
	Derive        func(lipapi.Call) lipapi.ProtocolRequirements
	Match         func(lipapi.ProtocolRequirements, lipapi.ProtocolRequirements) bool
	ProjectItems  func(lipapi.Call, lipapi.LegacyProjectionTarget) (lipapi.LegacyProjectionResult, error)
	ProjectLegacy func(lipapi.Call, lipapi.OrderedItemProjectionTarget) ([]lipapi.Item, lipapi.ProtocolRequirements, error)
	Admit         func(lipapi.CandidateAdmissionInput) lipapi.CandidateAdmissionResult
	Validate      func([]lipapi.Event) error
	OutputPolicy  func(committed, retry bool) error
	StickyCleanup func(affinityID string, admitted bool) bool
}

func DefaultSemanticInvariantHooks() SemanticInvariantHooks {
	return SemanticInvariantHooks{
		Derive: lipapi.DeriveProtocolRequirements,
		Match: func(req, supported lipapi.ProtocolRequirements) bool {
			return lipapi.MatchRequirements(req, supported, lipapi.ReasoningReplaySupport{}).Kind != lipapi.NegotiationReject
		},
		ProjectItems:  lipapi.ProjectItemsToLegacyView,
		ProjectLegacy: lipapi.ProjectLegacyToOrderedItems,
		Admit:         lipapi.AdmitCandidate,
		Validate:      lipapi.ValidateEventSequence,
		OutputPolicy: func(committed, retry bool) error {
			if committed && retry {
				return errors.New("output committed: retry/failover is forbidden")
			}
			return nil
		},
		StickyCleanup: func(affinityID string, admitted bool) bool {
			return !admitted
		},
	}
}

// RunSemanticInvariantSuite exercises the complete canonical semantic boundary.
// It intentionally validates presence-bearing projectors, transport admission,
// frozen failover obligations, output commitment, and terminal sequence rules.
func RunSemanticInvariantSuite(h SemanticInvariantHooks) error {
	if h.Derive == nil || h.Match == nil || h.ProjectItems == nil || h.ProjectLegacy == nil || h.Admit == nil || h.Validate == nil || h.OutputPolicy == nil {
		return errors.New("core TCK: incomplete semantic invariant hooks")
	}
	text := "hello"
	call := lipapi.Call{
		Items:      []lipapi.Item{{Kind: lipapi.ItemKindMessage, ID: "m1", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: text}}}},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: text}}}},
		Tools:      []lipapi.ToolDef{{Name: "weather", Parameters: []byte(`{"type":"object"}`)}},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}
	req := h.Derive(call)
	if !slices.Contains(req.Capabilities, lipapi.CapabilityOrderedItems) || !slices.Contains(req.Capabilities, lipapi.CapabilityTools) {
		return fmt.Errorf("core TCK: derivation lost required capabilities: %v", req.Capabilities)
	}
	if !h.Match(req, lipapi.ProtocolRequirements{Capabilities: append([]lipapi.Capability(nil), req.Capabilities...)}) {
		return errors.New("core TCK: exact requirement matching rejected supported capabilities")
	}
	if h.Match(req, lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityOrderedItems}}) {
		return errors.New("core TCK: exact matching accepted missing tools capability")
	}

	itemCall := lipapi.Call{Items: []lipapi.Item{{Kind: lipapi.ItemKindMessage, ID: "presence", Status: lipapi.ItemStatusCompleted, Role: lipapi.RoleUser, Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: text}}}}}
	legacy, err := h.ProjectItems(itemCall, lipapi.DefaultLegacyProjectionTarget(lipapi.NewBackendCaps(lipapi.CapabilityStreaming), lipapi.ReasoningReplaySupport{}))
	if err != nil || len(legacy.Messages) != 1 || legacy.Messages[0].Parts[0].Text != text {
		return fmt.Errorf("core TCK: item->legacy projection lost presence/value: %v", err)
	}
	ordered, _, err := h.ProjectLegacy(legacyCall(legacy.Messages), lipapi.DefaultOrderedItemProjectionTarget())
	if err != nil || len(ordered) != 1 || ordered[0].Content[0].Text != text {
		return fmt.Errorf("core TCK: legacy->item projection lost presence/value: %v", err)
	}

	transport := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{Operation: call.Invocation.Operation, Modes: []lipapi.TransportMode{lipapi.TransportModeStreaming}})
	admission := h.Admit(lipapi.CandidateAdmissionInput{Call: call, Invocation: call.Invocation, BackendCaps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools, lipapi.CapabilityOrderedItems), TransportCaps: transport})
	if admission.Kind == lipapi.NegotiationReject {
		return fmt.Errorf("core TCK: transport admission rejected compatible candidate: %v", admission.Err())
	}
	badTransport := h.Admit(lipapi.CandidateAdmissionInput{Call: call, Invocation: call.Invocation, BackendCaps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools, lipapi.CapabilityOrderedItems), TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{Operation: call.Invocation.Operation, Modes: []lipapi.TransportMode{lipapi.TransportModeNonStreaming}})})
	if badTransport.Kind != lipapi.NegotiationReject {
		return errors.New("core TCK: transport admission accepted undeclared streaming mode")
	}
	if h.StickyCleanup != nil && !h.StickyCleanup("session-affinity-id", false) {
		return errors.New("core TCK: sticky affinity state was retained when candidate admission was rejected")
	}

	frozen := h.Derive(call)
	frozen.Capabilities = append(frozen.Capabilities, lipapi.CapabilityVision)
	frozen = lipapi.NormalizeProtocolRequirements(frozen)
	failoverCaps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools, lipapi.CapabilityOrderedItems)
	failover := h.Admit(lipapi.CandidateAdmissionInput{Call: call, Invocation: call.Invocation, BackendCaps: failoverCaps, TransportCaps: transport, FrozenRequirements: &frozen})
	if failover.Kind != lipapi.NegotiationReject {
		return errors.New("core TCK: failover candidate weakened frozen requirement union")
	}

	if err := h.OutputPolicy(false, true); err != nil {
		return fmt.Errorf("core TCK: pre-output retry unexpectedly forbidden: %w", err)
	}
	if err := h.OutputPolicy(true, true); err == nil {
		return errors.New("core TCK: output commitment allowed retry/failover")
	}
	events := []lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}, {Kind: lipapi.EventTextDelta, Delta: text}, {Kind: lipapi.EventResponseFinished}}
	if err := h.Validate(events); err != nil {
		return fmt.Errorf("core TCK: valid terminal sequence rejected: %w", err)
	}
	broken := append([]lipapi.Event(nil), events...)
	broken[2], broken[1] = broken[1], broken[2]
	if err := h.Validate(broken); err == nil {
		return errors.New("core TCK: terminal validator accepted delta before message start")
	}
	return nil
}

func legacyCall(messages []lipapi.Message) lipapi.Call {
	return lipapi.Call{Messages: append([]lipapi.Message(nil), messages...)}
}

// CertifyCore selects and records scenarios for one canonical-core harness.
func CertifyCore(ctx context.Context, h CoreHarness) (semantic.Certification, error) {
	if h == nil {
		return semantic.Certification{}, fmt.Errorf("core TCK: nil harness")
	}
	subject := h.Subject()
	if subject.Kind != semantic.KindCanonicalCore {
		return semantic.Certification{}, fmt.Errorf("core TCK: subject %q must use canonical_core kind, got %q", subject.ID, subject.Kind)
	}
	positive, negative := semantic.SelectScenariosForSubject(subject, semantic.BaselineScenarioCorpus())
	view, err := h.Derivation(ctx)
	if err != nil || view == nil {
		if err == nil {
			err = fmt.Errorf("derivation returned nil")
		}
		return semantic.Certification{}, fmt.Errorf("core TCK: derivation boundary failed: %w", err)
	}
	call := &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser}}}
	derived, err := view.DeriveRequirements(call)
	if err != nil {
		return semantic.Certification{}, fmt.Errorf("core TCK: derive requirements: %w", err)
	}
	if !view.MatchRequirements(derived, derived.Capabilities) {
		return semantic.Certification{}, fmt.Errorf("core TCK: exact matching rejected derived requirements")
	}
	corpus := semantic.BaselineScenarioCorpus()
	selected := append(append([]semantic.ScenarioID(nil), positive...), negative...)
	for _, id := range selected {
		for _, scenario := range corpus {
			if scenario.ID == id {
				evidence, err := view.Probe(ctx, scenario)
				if err != nil {
					return semantic.Certification{}, fmt.Errorf("core TCK: scenario %s: %w", id, err)
				}
				isPositive := slices.Contains(positive, id)
				if !evidence.Executed || evidence.ScenarioID != id || evidence.BoundaryCalls == 0 || !evidence.Derived || !evidence.ExactMatch {
					return semantic.Certification{}, fmt.Errorf("core TCK: scenario %s lacks derive/exact-match evidence", id)
				}
				if isPositive && !evidence.Accepted {
					return semantic.Certification{}, fmt.Errorf("core TCK: positive scenario %s lacks acceptance evidence", id)
				}
				if !isPositive && (!evidence.Rejected || evidence.Accepted) {
					return semantic.Certification{}, fmt.Errorf("core TCK: negative scenario %s lacks rejection evidence", id)
				}
				break
			}
		}
	}
	return semantic.Certification{SubjectID: subject.ID, SubjectKind: subject.Kind, Profile: subject.Profile, Capabilities: subject.Capabilities, Dialects: subject.Dialects, Transports: subject.Transports, Passed: positive, Negative: negative, Executed: true, ExecutedScenarioIDs: selected, HarnessCalls: 3 + len(selected)}, nil
}
