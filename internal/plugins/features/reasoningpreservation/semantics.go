package reasoningpreservation

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ReplaySemantics is the bounded typed classification for reasoning replay.
// It is derived from canonical dialect plus structure/presence, not provider strings.
type ReplaySemantics uint8

const (
	ReplayUnknown ReplaySemantics = iota
	ReplayExactRequired
	ReplaySemanticText
)

// SegmentSemantics describes the classification of a single placed reasoning segment.
type SegmentSemantics struct {
	PlacementIndex int
	Dialect        lipapi.ReasoningDialect
	Semantics      ReplaySemantics
	SourceBytes    int
}

// ClassifyReasoningPart returns the replay semantics for a single reasoning part.
// Pure function: provider-name free, no I/O, deterministic.
func ClassifyReasoningPart(part lipapi.Part) ReplaySemantics {
	if part.Kind != lipapi.PartReasoning || part.Reasoning == nil {
		return ReplayUnknown
	}
	rp := part.Reasoning
	normalized := lipapi.NormalizeReasoningDialect(rp.Dialect)
	if normalized == "" {
		return ReplayUnknown
	}
	switch normalized {
	case lipapi.ReasoningDialectOpenAIChatTextV1,
		lipapi.ReasoningDialectOpenAIResponsesItemV1,
		lipapi.ReasoningDialectAnthropicThinkingV1,
		lipapi.ReasoningDialectAnthropicRedactedThinkingV1:
	default:
		return ReplayUnknown
	}
	// Exact authority wins over readable text. Check exact fields after dialect
	// known/unknown decision so unknown dialects map to ReplayUnknown.
	if lipapi.ReasoningHasExactResponsesFields(rp) {
		return ReplayExactRequired
	}
	if rp.Signature != "" {
		return ReplayExactRequired
	}
	if len(rp.Opaque) > 0 {
		return ReplayExactRequired
	}
	switch normalized {
	case lipapi.ReasoningDialectOpenAIChatTextV1:
		if strings.TrimSpace(rp.Text) == "" {
			return ReplayUnknown
		}
		return ReplaySemanticText
	case lipapi.ReasoningDialectOpenAIResponsesItemV1:
		return ReplayExactRequired
	case lipapi.ReasoningDialectAnthropicThinkingV1:
		return ReplayExactRequired
	case lipapi.ReasoningDialectAnthropicRedactedThinkingV1:
		return ReplayExactRequired
	default:
		return ReplayUnknown
	}
}

// ClassifyPlacement returns per-placement semantics for a single placed reasoning.
func ClassifyPlacement(idx int, pr PlacedReasoning) SegmentSemantics {
	dialect := lipapi.ReasoningDialect("")
	srcBytes := 0
	if pr.Part.Kind == lipapi.PartReasoning && pr.Part.Reasoning != nil {
		dialect = lipapi.NormalizeReasoningDialect(pr.Part.Reasoning.Dialect)
		srcBytes = lipapi.ReasoningPayloadBytes(pr.Part.Reasoning)
	}
	return SegmentSemantics{
		PlacementIndex: idx,
		Dialect:        dialect,
		Semantics:      ClassifyReasoningPart(pr.Part),
		SourceBytes:    srcBytes,
	}
}

// ClassifyPlacements returns per-placement semantics for all placements.
// The PlacementIndex reflects input order, not BeforeNonReasoningPart.
func ClassifyPlacements(placements []PlacedReasoning) []SegmentSemantics {
	out := make([]SegmentSemantics, 0, len(placements))
	for i, pr := range placements {
		out = append(out, ClassifyPlacement(i, pr))
	}
	return out
}
