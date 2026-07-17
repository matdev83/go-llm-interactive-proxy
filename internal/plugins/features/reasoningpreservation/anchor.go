package reasoningpreservation

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

func ComputeAnchor(msg lipapi.Message) ([32]byte, error) {
	_ = msg
	return [32]byte{}, ErrNotImplemented
}

func DerivePlacements(nonReasoningCount int, reasoning []lipapi.Part) ([]PlacedReasoning, error) {
	_ = nonReasoningCount
	_ = reasoning
	return nil, ErrNotImplemented
}
