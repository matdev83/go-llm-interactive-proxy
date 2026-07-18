package reasoningpreservation

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

type Classification string

const (
	ClassMissing     Classification = "missing"
	ClassPreserved   Classification = "preserved"
	ClassConflicting Classification = "conflicting"
	ClassAmbiguous   Classification = "ambiguous"
	ClassUnmatched   Classification = "unmatched"
)

type ClassifiedTurn struct {
	Classification Classification
	ArtifactID     string
}

func ClassifyAssistantTurns(messages []lipapi.Message, artifacts []TurnArtifact) ([]ClassifiedTurn, error) {
	_ = messages
	_ = artifacts
	return nil, ErrNotImplemented
}
