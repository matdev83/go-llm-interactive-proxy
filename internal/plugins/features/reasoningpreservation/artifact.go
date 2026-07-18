package reasoningpreservation

import (
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type PlacedReasoning struct {
	BeforeNonReasoningPart int
	Part                   lipapi.Part
}

type TurnArtifact struct {
	ID             string
	Anchor         [32]byte
	SourceBackend  string
	SourceModel    string
	Reasoning      []PlacedReasoning
	CreatedAt      time.Time
	ReasoningBytes int
}
