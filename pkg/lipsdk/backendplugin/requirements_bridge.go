package backendplugin

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	// MetaProtocolRequirements is deprecated; use Invocation.ProtocolRequirements.
	MetaProtocolRequirements = "lip.protocol_requirements"
)

// ApplyItemAuthorityMetadata is deprecated; ordered item wire uses first-class invocation fields via ApplyOrderedItemWire.
func ApplyItemAuthorityMetadata(inv *Invocation, call lipapi.Call) {
	ApplyOrderedItemWire(inv, call)
}

// HasItemAuthorityMetadata reports whether the invocation declares item authority.
func HasItemAuthorityMetadata(meta map[string]string) bool {
	_ = meta
	return false
}

// HasItemAuthorityInvocation reports item authority from first-class invocation fields.
func HasItemAuthorityInvocation(inv Invocation) bool {
	return HasItemAuthorityWire(inv)
}

// ProtocolRequirementsFromInvocation decodes requirements from first-class invocation fields.
func ProtocolRequirementsFromInvocation(inv Invocation) (lipapi.ProtocolRequirements, bool) {
	if !inv.ItemAuthority {
		return lipapi.ProtocolRequirements{}, false
	}
	req := lipapi.ProtocolRequirements{}
	for _, c := range inv.ProtocolRequirements.Capabilities {
		req.Capabilities = append(req.Capabilities, lipapi.Capability(c))
	}
	for _, d := range inv.ProtocolRequirements.ItemDialects {
		req.ItemDialects = append(req.ItemDialects, lipapi.DialectRequirement{
			Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor,
		})
	}
	for _, d := range inv.ProtocolRequirements.ReasoningDialects {
		req.ReasoningDialects = append(req.ReasoningDialects, lipapi.DialectRequirement{
			Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor,
		})
	}
	for _, d := range inv.ProtocolRequirements.CompactionDialects {
		req.CompactionDialects = append(req.CompactionDialects, lipapi.DialectRequirement{
			Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor,
		})
	}
	for _, e := range inv.ProtocolRequirements.ExtensionTypes {
		req.ExtensionTypes = append(req.ExtensionTypes, lipapi.ExtensionRequirement{
			Namespace: e.Namespace, Type: e.Type, Implementor: e.Implementor,
		})
	}
	return lipapi.NormalizeProtocolRequirements(req), true
}

// ProtocolRequirementsFromMetadata is deprecated; requirements are carried on Invocation.ProtocolRequirements.
func ProtocolRequirementsFromMetadata(meta map[string]string) (lipapi.ProtocolRequirements, bool) {
	_ = meta
	return lipapi.ProtocolRequirements{}, false
}

// CapabilitySummaryFromLipapi maps canonical backend caps into plugin DTO form.
func CapabilitySummaryFromLipapi(caps lipapi.BackendCaps) CapabilitySummary {
	return CapabilitySummary{
		Streaming:          capsHas(caps, lipapi.CapabilityStreaming),
		Tools:              capsHas(caps, lipapi.CapabilityTools),
		Vision:             capsHas(caps, lipapi.CapabilityVision),
		Documents:          capsHas(caps, lipapi.CapabilityDocuments),
		StructuredOutputs:  capsHas(caps, lipapi.CapabilityStructuredOutputs),
		Reasoning:          capsHas(caps, lipapi.CapabilityReasoning),
		ReasoningReplay:    capsHas(caps, lipapi.CapabilityReasoningReplay),
		ParallelToolCalls:  capsHas(caps, lipapi.CapabilityParallelToolCalls),
		OrderedItems:       capsHas(caps, lipapi.CapabilityOrderedItems),
		ItemReferences:     capsHas(caps, lipapi.CapabilityItemReferences),
		Compaction:         capsHas(caps, lipapi.CapabilityCompaction),
		AssistantPhase:     capsHas(caps, lipapi.CapabilityAssistantPhase),
		OpaqueExtensions:   capsHas(caps, lipapi.CapabilityOpaqueExtensions),
		VideoInput:         capsHas(caps, lipapi.CapabilityVideoInput),
		Annotations:        capsHas(caps, lipapi.CapabilityAnnotations),
		AssistantMediaRefs: capsHas(caps, lipapi.CapabilityAssistantMediaRefs),
	}
}

// DialectSupportFromLipapi maps canonical dialect support into plugin DTO form.
func DialectSupportFromLipapi(s lipapi.DialectSupport) DialectSupport {
	out := DialectSupport{}
	for _, d := range s.ItemDialects {
		out.ItemDialects = append(out.ItemDialects, DialectRequirementDTO{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, d := range s.ReasoningDialects {
		out.ReasoningDialects = append(out.ReasoningDialects, DialectRequirementDTO{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, d := range s.CompactionDialects {
		out.CompactionDialects = append(out.CompactionDialects, DialectRequirementDTO{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, e := range s.ExtensionTypes {
		out.ExtensionTypes = append(out.ExtensionTypes, ExtensionRequirementDTO{Namespace: e.Namespace, Type: e.Type, Implementor: e.Implementor})
	}
	return out
}

// DialectSupportToLipapi maps plugin dialect support into canonical form.
func DialectSupportToLipapi(d DialectSupport) lipapi.DialectSupport {
	out := lipapi.DialectSupport{}
	for _, d := range d.ItemDialects {
		out.ItemDialects = append(out.ItemDialects, lipapi.DialectRequirement{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, d := range d.ReasoningDialects {
		out.ReasoningDialects = append(out.ReasoningDialects, lipapi.DialectRequirement{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, d := range d.CompactionDialects {
		out.CompactionDialects = append(out.CompactionDialects, lipapi.DialectRequirement{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, e := range d.ExtensionTypes {
		out.ExtensionTypes = append(out.ExtensionTypes, lipapi.ExtensionRequirement{Namespace: e.Namespace, Type: e.Type, Implementor: e.Implementor})
	}
	return lipapi.NormalizeDialectSupport(out)
}

func capsHas(caps lipapi.BackendCaps, targetCap lipapi.Capability) bool {
	if caps == nil {
		return false
	}
	_, ok := caps[targetCap]
	return ok
}
