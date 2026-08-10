// Package contract contains dependency-neutral semantic TCK metadata shared by
// frontend, core, backend, and executable connector contract tests.
package contract

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

type SemanticFeature string
type ScenarioID string
type ScenarioTransport string

const (
	FeatureText             SemanticFeature = "text"
	FeatureStreaming        SemanticFeature = "streaming"
	FeatureTools            SemanticFeature = "tools"
	FeatureVision           SemanticFeature = "vision"
	FeatureDocuments        SemanticFeature = "documents"
	FeatureStructuredOutput SemanticFeature = "structured_output"
	FeatureReasoning        SemanticFeature = "reasoning"
	FeatureReasoningReplay  SemanticFeature = "reasoning_replay"
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

// BaselineScenarioCorpus is the single metadata source shared by all TCKs.
// Scenario builders remain in the transport-specific runners.
func BaselineScenarioCorpus() []ScenarioDescriptor {
	return []ScenarioDescriptor{
		{ID: "text-baseline", Feature: FeatureText, Transport: TransportHTTP, Prerequisites: []string{"valid text request"}, ExpectedEvidence: []string{"canonical call", "terminal response"}},
		{ID: "text-streaming", Feature: FeatureStreaming, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"streaming transport", "valid text request"}, ExpectedEvidence: []string{"ordered canonical events", "terminal response"}},
		{ID: "usage-present", Feature: FeatureUsage, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"usage reporting enabled"}, ExpectedEvidence: []string{"usage event present", "explicit counters including zero"}},
		{ID: "usage-zero", Feature: FeatureUsage, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"provider reports zero counters"}, ExpectedEvidence: []string{"usage event present", "zero counters preserved"}},
		{ID: "tools-execution", Feature: FeatureTools, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityTools}}, Transport: TransportHTTP, Prerequisites: []string{"tool declaration", "tool call result"}, ExpectedEvidence: []string{"tool call lifecycle", "tool result replay"}},
		{ID: "tool-call-replay", Feature: FeatureTools, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityTools}}, Transport: TransportHTTP, Prerequisites: []string{"prior tool call item"}, ExpectedEvidence: []string{"tool call ID and arguments preserved"}},
		{ID: "tool-result-replay", Feature: FeatureTools, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityTools}}, Transport: TransportHTTP, Prerequisites: []string{"matching prior tool call"}, ExpectedEvidence: []string{"tool result references call ID"}},
		{ID: "vision-input", Feature: FeatureVision, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityVision}}, Transport: TransportHTTP, Prerequisites: []string{"valid image part"}, ExpectedEvidence: []string{"canonical image part"}},
		{ID: "documents-input", Feature: FeatureDocuments, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityDocuments}}, Transport: TransportHTTP, Prerequisites: []string{"valid document part"}, ExpectedEvidence: []string{"canonical document part"}},
		{ID: "structured-output", Feature: FeatureStructuredOutput, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityStructuredOutputs}}, Transport: TransportHTTP, Prerequisites: []string{"valid response schema"}, ExpectedEvidence: []string{"structured-output requirement"}},
		{ID: "reasoning-output", Feature: FeatureReasoning, Requires: lipapi.ProtocolRequirements{Capabilities: []lipapi.Capability{lipapi.CapabilityReasoning}}, Transport: TransportHTTP, Prerequisites: []string{"reasoning option"}, ExpectedEvidence: []string{"reasoning dialect or event"}},
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
