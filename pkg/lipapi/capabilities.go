package lipapi

import "slices"

// CapabilitySet is a declared capability bundle for a plugin registration surface.
type CapabilitySet struct {
	Provides []Capability
}

// Capability names the semantic features a frontend call may require and a backend may provide.
type Capability string

const (
	CapabilityStreaming         Capability = "streaming"
	CapabilityTools             Capability = "tools"
	CapabilityVision            Capability = "vision"
	CapabilityDocuments         Capability = "documents"
	CapabilityStructuredOutputs Capability = "structured_outputs"
	CapabilityReasoning         Capability = "reasoning"
	CapabilityParallelToolCalls Capability = "parallel_tool_calls"
)

// BackendCaps is a set of capabilities supported by a backend adapter instance.
type BackendCaps map[Capability]struct{}

// NewBackendCaps builds a set for negotiation helpers and tests.
func NewBackendCaps(caps ...Capability) BackendCaps {
	m := make(BackendCaps, len(caps))
	for _, c := range caps {
		m[c] = struct{}{}
	}
	return m
}

// NegotiationKind classifies the outcome of comparing a call against backend capabilities.
type NegotiationKind string

const (
	NegotiationLossless  NegotiationKind = "lossless"
	NegotiationDowngrade NegotiationKind = "downgrade"
	NegotiationReject    NegotiationKind = "reject"
)

// NegotiationResult is a deterministic capability negotiation outcome.
type NegotiationResult struct {
	Kind       NegotiationKind
	Missing    []Capability // hard rejects only
	Downgraded []Capability // capabilities that will be stripped or softened
}

// Err returns a typed reject error for Kind==NegotiationReject, otherwise nil.
func (r NegotiationResult) Err() error {
	if r.Kind != NegotiationReject {
		return nil
	}
	return &RejectError{Missing: append([]Capability{}, r.Missing...)}
}

// RequiredCapabilities derives required capabilities from call shape.
func RequiredCapabilities(c Call) []Capability {
	out := []Capability{}
	add := func(cap Capability) {
		if slices.Contains(out, cap) {
			return
		}
		out = append(out, cap)
	}

	scanMessageParts := func(msgs []Message) {
		for _, m := range msgs {
			for _, p := range m.Parts {
				if p.Kind == PartImageRef {
					add(CapabilityVision)
				}
				if p.Kind == PartFileRef {
					add(CapabilityDocuments)
				}
			}
		}
	}
	scanMessageParts(c.Instructions)
	scanMessageParts(c.Messages)
	if len(c.Tools) > 0 {
		add(CapabilityTools)
	}
	if c.Options.ResponseMIMEType != "" {
		add(CapabilityStructuredOutputs)
	}
	if c.Options.ReasoningEffort != "" {
		add(CapabilityReasoning)
	}
	if c.Options.ParallelToolCalls != nil && *c.Options.ParallelToolCalls {
		add(CapabilityParallelToolCalls)
	}
	return out
}

// Negotiate compares required capabilities with backend-provided capabilities.
//
// Downgrade is returned only when every missing capability is explicitly soft:
// reasoning and parallel tool calls may be stripped by the executor before upstream calls.
// Any other missing capability is a hard reject before upstream work begins.
func Negotiate(required []Capability, backend BackendCaps) NegotiationResult {
	missing := []Capability{}
	for _, r := range required {
		if backend == nil {
			missing = append(missing, r)
			continue
		}
		if _, ok := backend[r]; !ok {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		return NegotiationResult{Kind: NegotiationLossless}
	}

	downgradable := map[Capability]struct{}{
		CapabilityReasoning:         {},
		CapabilityParallelToolCalls: {},
	}

	soft := []Capability{}
	hard := []Capability{}
	for _, m := range missing {
		if _, ok := downgradable[m]; ok {
			soft = append(soft, m)
			continue
		}
		hard = append(hard, m)
	}
	if len(hard) > 0 {
		return NegotiationResult{Kind: NegotiationReject, Missing: hard}
	}
	return NegotiationResult{Kind: NegotiationDowngrade, Downgraded: soft}
}

// ApplyNegotiatedDowngrades mutates c.Options to strip capabilities classified as soft
// downgrade by Negotiate. Call only when NegotiationResult.Kind == NegotiationDowngrade.
func ApplyNegotiatedDowngrades(c *Call, down NegotiationResult) {
	if c == nil || down.Kind != NegotiationDowngrade {
		return
	}
	for _, cap := range down.Downgraded {
		switch cap {
		case CapabilityReasoning:
			c.Options.ReasoningEffort = ""
		case CapabilityParallelToolCalls:
			c.Options.ParallelToolCalls = nil
		}
	}
}
