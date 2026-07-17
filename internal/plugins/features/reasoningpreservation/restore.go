package reasoningpreservation

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

type RestoreResult struct {
	Mutated       bool
	RestoredCount int
	RestoredBytes int
	Exclude       bool
}

type RestoreInput struct {
	Action            string
	OnUnrepresentable string
	OnStateError      string
	Call              *lipapi.Call
	Artifacts         []TurnArtifact
	ReplaySupport     lipapi.ReasoningReplaySupport
	Eligible          bool
}

func RestoreMissingReasoning(in RestoreInput) (RestoreResult, error) {
	_ = in
	return RestoreResult{}, ErrNotImplemented
}
