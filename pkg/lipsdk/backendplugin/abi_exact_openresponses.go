package backendplugin

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// RequireExactOpenResponsesABISupport rejects calls that require exact OpenAI
// Responses semantics (PromptCacheKey, inline FileData, opaque extension content
// parts, reasoning Summary/Content/EncryptedContent presence, compaction
// EncryptedContent) when negotiation did not reach minor 3 with the feature
// enabled. Plain calls without those semantics remain backward-compatible.
func RequireExactOpenResponsesABISupport(neg Negotiation, call lipapi.Call) error {
	if !callRequiresExactOpenResponses(call) {
		return nil
	}
	return requireExactOpenResponsesNegotiation(neg)
}

// RequireExactOpenResponsesEventABISupport rejects canonical events that carry
// exact OpenAI Responses reasoning fields when negotiation cannot represent them,
// so hosts never silently drop encrypted content or summary/content presence.
func RequireExactOpenResponsesEventABISupport(neg Negotiation, ev *CanonicalEvent) error {
	if !eventRequiresExactOpenResponses(ev) {
		return nil
	}
	return requireExactOpenResponsesNegotiation(neg)
}

func requireExactOpenResponsesNegotiation(neg Negotiation) error {
	if !neg.Compatible {
		return fmt.Errorf("%w: negotiation incompatible", ErrExactOpenResponsesUnsupported)
	}
	if neg.NegotiatedMinor < ProtocolMinorExactOpenResponsesFields {
		return fmt.Errorf("%w: negotiated minor %d", ErrExactOpenResponsesUnsupported, neg.NegotiatedMinor)
	}
	for _, name := range neg.EnabledFeatures {
		if name == FeatureExactOpenResponsesFields {
			return nil
		}
	}
	return fmt.Errorf("%w: feature %q not enabled", ErrExactOpenResponsesUnsupported, FeatureExactOpenResponsesFields)
}

func callRequiresExactOpenResponses(call lipapi.Call) bool {
	if strings.TrimSpace(call.PromptCacheKey) != "" {
		return true
	}
	for i := range call.Instructions {
		for _, p := range call.Instructions[i].Parts {
			if partRequiresExactOpenResponses(p) {
				return true
			}
		}
	}
	for i := range call.Messages {
		for _, p := range call.Messages[i].Parts {
			if partRequiresExactOpenResponses(p) {
				return true
			}
		}
	}
	for _, item := range call.Items {
		if itemRequiresExactOpenResponses(item) {
			return true
		}
	}
	return false
}

func partRequiresExactOpenResponses(p lipapi.Part) bool {
	return p.Kind == lipapi.PartReasoning && lipapi.ReasoningHasExactResponsesFields(p.Reasoning)
}

func itemRequiresExactOpenResponses(item lipapi.Item) bool {
	if item.Reasoning != nil && item.Reasoning.Reasoning != nil && lipapi.ReasoningHasExactResponsesFields(item.Reasoning.Reasoning) {
		return true
	}
	if item.Compaction != nil && strings.TrimSpace(item.Compaction.EncryptedContent) != "" {
		return true
	}
	if itemContentRequiresExactOpenResponses(item.Content) {
		return true
	}
	if item.ToolResult != nil && itemContentRequiresExactOpenResponses(item.ToolResult.Parts) {
		return true
	}
	return false
}

func itemContentRequiresExactOpenResponses(parts []lipapi.ContentPart) bool {
	for _, cp := range parts {
		switch cp.Kind {
		case lipapi.ContentPartExtension:
			return true
		case lipapi.ContentPartFileRef:
			if cp.FileData != "" {
				return true
			}
		case lipapi.ContentPartReasoning:
			if lipapi.ReasoningHasExactResponsesFields(cp.Reasoning) {
				return true
			}
		}
	}
	return false
}

func eventRequiresExactOpenResponses(ev *CanonicalEvent) bool {
	if ev == nil {
		return false
	}
	return ev.ReasoningSummary.State() != RawJSONAbsent ||
		ev.ReasoningContent.State() != RawJSONAbsent ||
		ev.ReasoningEncryptedContent.State() != RawJSONAbsent
}
