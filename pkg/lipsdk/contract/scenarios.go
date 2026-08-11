// Package contract contains dependency-neutral semantic TCK metadata shared by
// frontend, core, backend, and executable connector contract tests.
package contract

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

type (
	SemanticFeature   string
	ScenarioID        string
	ScenarioTransport string
)

const (
	FeatureText             SemanticFeature = "text"
	FeatureStreaming        SemanticFeature = "streaming"
	FeatureTools            SemanticFeature = "tools"
	FeatureParallelTools    SemanticFeature = "parallel_tools"
	FeatureVision           SemanticFeature = "vision"
	FeatureVideo            SemanticFeature = "video"
	FeatureDocuments        SemanticFeature = "documents"
	FeatureAnnotations      SemanticFeature = "annotations"
	FeatureStructuredOutput SemanticFeature = "structured_output"
	FeatureReasoning        SemanticFeature = "reasoning"
	FeatureReasoningReplay  SemanticFeature = "reasoning_replay"
	FeatureAssistantPhase   SemanticFeature = "assistant_phase"
	FeatureAssistantMedia   SemanticFeature = "assistant_media"
	FeatureOrderedItems     SemanticFeature = "ordered_items"
	FeatureItemReferences   SemanticFeature = "item_references"
	FeatureCompaction       SemanticFeature = "compaction"
	FeatureExtensions       SemanticFeature = "extensions"
	FeatureUsage            SemanticFeature = "usage"
	FeatureErrors           SemanticFeature = "errors"
	FeatureCancellation     SemanticFeature = "cancellation"
	FeatureLifecycle        SemanticFeature = "lifecycle"

	TransportHTTP      ScenarioTransport = "http"
	TransportStreaming ScenarioTransport = "streaming"
	TransportWebSocket ScenarioTransport = "websocket"
	TransportConnector ScenarioTransport = "connector"
)

type ScenarioDescriptor struct {
	ID        ScenarioID                  `json:"id"`
	Feature   SemanticFeature             `json:"feature"`
	Requires  lipapi.ProtocolRequirements `json:"requires"`
	Transport ScenarioTransport           `json:"transport"`
	// Prerequisites and ExpectedEvidence make scenario selection auditable:
	// runners must satisfy the former and record the latter at their boundary.
	Prerequisites    []string `json:"prerequisites,omitempty"`
	ExpectedEvidence []string `json:"expected_evidence,omitempty"`
}

func (s ScenarioDescriptor) IsCancellation() bool {
	return s.Feature == FeatureCancellation
}

// BaselineScenarioCorpus is the single metadata source shared by all TCKs.
// Scenario builders remain in the transport-specific runners.
func BaselineScenarioCorpus() []ScenarioDescriptor {
	return []ScenarioDescriptor{
		{ID: "text-baseline", Feature: FeatureText, Transport: TransportHTTP, Prerequisites: []string{"valid text request"}, ExpectedEvidence: []string{"canonical call", "terminal response"}},
		{ID: "text-streaming", Feature: FeatureStreaming, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"streaming transport", "valid text request"}, ExpectedEvidence: []string{"ordered canonical events", "terminal response"}},
		{ID: "usage-present", Feature: FeatureUsage, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"usage reporting enabled"}, ExpectedEvidence: []string{"usage event present", "explicit counters including zero"}},
		{ID: "usage-zero", Feature: FeatureUsage, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"provider reports zero counters"}, ExpectedEvidence: []string{"usage event present", "zero counters preserved"}},
		{ID: "tools-execution", Feature: FeatureTools, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityTools}}, Transport: TransportHTTP, Prerequisites: []string{"tool declaration", "tool call result"}, ExpectedEvidence: []string{"tool call lifecycle", "tool result replay"}},
		{ID: "parallel-tools", Feature: FeatureParallelTools, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityParallelToolCalls}}, Transport: TransportHTTP, Prerequisites: []string{"parallel tool declarations", "independent tool calls"}, ExpectedEvidence: []string{"parallel tool call identity", "ordered tool results"}},
		{ID: "tool-call-replay", Feature: FeatureTools, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityTools}}, Transport: TransportHTTP, Prerequisites: []string{"prior tool call item"}, ExpectedEvidence: []string{"tool call ID and arguments preserved"}},
		{ID: "tool-result-replay", Feature: FeatureTools, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityTools}}, Transport: TransportHTTP, Prerequisites: []string{"matching prior tool call"}, ExpectedEvidence: []string{"tool result references call ID"}},
		{ID: "vision-input", Feature: FeatureVision, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityVision}}, Transport: TransportHTTP, Prerequisites: []string{"valid image part"}, ExpectedEvidence: []string{"canonical image part"}},
		{ID: "video-input", Feature: FeatureVideo, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityVideoInput}}, Transport: TransportHTTP, Prerequisites: []string{"valid video part"}, ExpectedEvidence: []string{"canonical video requirement or explicit rejection"}},
		{ID: "annotations-output", Feature: FeatureAnnotations, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityAnnotations}}, Transport: TransportHTTP, Prerequisites: []string{"provider annotations"}, ExpectedEvidence: []string{"annotation provenance or explicit rejection"}},
		{ID: "documents-input", Feature: FeatureDocuments, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityDocuments}}, Transport: TransportHTTP, Prerequisites: []string{"valid document part"}, ExpectedEvidence: []string{"canonical document part"}},
		{ID: "structured-output", Feature: FeatureStructuredOutput, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStructuredOutputs}}, Transport: TransportHTTP, Prerequisites: []string{"valid response schema"}, ExpectedEvidence: []string{"structured-output requirement"}},
		{ID: "reasoning-output", Feature: FeatureReasoning, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityReasoning}}, Transport: TransportHTTP, Prerequisites: []string{"reasoning option"}, ExpectedEvidence: []string{"reasoning dialect or event"}},
		{ID: "assistant-phase", Feature: FeatureAssistantPhase, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityAssistantPhase}}, Transport: TransportHTTP, Prerequisites: []string{"assistant phase metadata"}, ExpectedEvidence: []string{"assistant phase preserved"}},
		{ID: "assistant-media", Feature: FeatureAssistantMedia, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityAssistantMediaRefs}}, Transport: TransportHTTP, Prerequisites: []string{"assistant media reference"}, ExpectedEvidence: []string{"assistant media preserved"}},
		{ID: "ordered-items", Feature: FeatureOrderedItems, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityOrderedItems}}, Transport: TransportHTTP, Prerequisites: []string{"item authority"}, ExpectedEvidence: []string{"ordered item sequence"}},
		{ID: "compaction-lifecycle", Feature: FeatureCompaction, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityCompaction}}, Transport: TransportHTTP, Prerequisites: []string{"compaction operation", "item authority"}, ExpectedEvidence: []string{"compaction operation", "compaction item/state"}},
		{ID: "item-reference-dialect", Feature: FeatureItemReferences, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityItemReferences}, ItemDialects: []lipapi.DialectRequirement{{Kind: "item", Dialect: "item_reference"}}}, Transport: TransportHTTP, Prerequisites: []string{"backward item reference"}, ExpectedEvidence: []string{"reference preserved"}},
		{ID: "reasoning-replay-dialect", Feature: FeatureReasoningReplay, Requires: lipapi.ProtocolRequirements{ReasoningDialects: []lipapi.DialectRequirement{{Kind: "reasoning", Dialect: "reasoning_replay"}}}, Transport: TransportHTTP, Prerequisites: []string{"historical reasoning item"}, ExpectedEvidence: []string{"reasoning replay dialect"}},
		{ID: "opaque-extension-type", Feature: FeatureExtensions, Requires: lipapi.ProtocolRequirements{ExtensionTypes: []lipapi.ExtensionRequirement{{Namespace: "com.example", Type: "custom"}}}, Transport: TransportHTTP, Prerequisites: []string{"bounded extension"}, ExpectedEvidence: []string{"extension preserved"}},
		{ID: "recoverable-error", Feature: FeatureErrors, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"error after response start", "retryable provider error"}, ExpectedEvidence: []string{"error event", "recoverable classification"}},
		{ID: "terminal-error", Feature: FeatureErrors, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"terminal provider error"}, ExpectedEvidence: []string{"error event", "terminal classification"}},
		{ID: "cancellation", Feature: FeatureCancellation, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"in-flight stream", "cancel signal"}, ExpectedEvidence: []string{"cancel outcome", "no post-cancel events"}},
		{ID: "lifecycle-close", Feature: FeatureLifecycle, Transport: TransportConnector, Prerequisites: []string{"configured session"}, ExpectedEvidence: []string{"first close success", "second close success"}},
		{ID: "close-idempotent", Feature: FeatureLifecycle, Transport: TransportConnector, Prerequisites: []string{"configured session"}, ExpectedEvidence: []string{"first close success", "second close success"}},
	}
}
