package reasoningpreservation

import (
	"bytes"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

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
	type keyedMsg struct {
		idx    int
		anchor [32]byte
		msg    lipapi.Message
	}
	var assistant []keyedMsg
	msgCount := make(map[[32]byte]int)
	for i, m := range messages {
		if m.Role != lipapi.RoleAssistant {
			continue
		}
		anchor, err := ComputeAnchor(m)
		if err != nil {
			return nil, err
		}
		assistant = append(assistant, keyedMsg{idx: i, anchor: anchor, msg: m})
		msgCount[anchor]++
	}
	artByAnchor := make(map[[32]byte][]TurnArtifact)
	for _, a := range artifacts {
		artByAnchor[a.Anchor] = append(artByAnchor[a.Anchor], a)
	}

	out := make([]ClassifiedTurn, 0, len(assistant))
	for _, am := range assistant {
		arts := artByAnchor[am.anchor]
		switch {
		case len(arts) == 0:
			out = append(out, ClassifiedTurn{Classification: ClassUnmatched})
		case len(arts) > 1 || msgCount[am.anchor] > 1:
			out = append(out, ClassifiedTurn{Classification: ClassAmbiguous})
		default:
			art := arts[0]
			class, err := classifyUnique(am.msg, art)
			if err != nil {
				return nil, err
			}
			out = append(out, ClassifiedTurn{Classification: class, ArtifactID: art.ID})
		}
	}
	return out, nil
}

func classifyUnique(msg lipapi.Message, art TurnArtifact) (Classification, error) {
	clientPlaced, _, err := DerivePlacementsFromParts(msg.Parts)
	if err != nil {
		return "", err
	}
	if len(clientPlaced) == 0 {
		return ClassMissing, nil
	}
	if placementsEqual(clientPlaced, art.Reasoning) {
		return ClassPreserved, nil
	}
	return ClassConflicting, nil
}

func placementsEqual(a, b []PlacedReasoning) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].BeforeNonReasoningPart != b[i].BeforeNonReasoningPart {
			return false
		}
		if !reasoningPartsEqual(a[i].Part, b[i].Part) {
			return false
		}
	}
	return true
}

func reasoningPartsEqual(a, b lipapi.Part) bool {
	if a.Kind != lipapi.PartReasoning || b.Kind != lipapi.PartReasoning {
		return false
	}
	if a.Reasoning == nil || b.Reasoning == nil {
		return a.Reasoning == nil && b.Reasoning == nil
	}
	ra, rb := a.Reasoning, b.Reasoning
	if lipapi.NormalizeReasoningDialect(ra.Dialect) != lipapi.NormalizeReasoningDialect(rb.Dialect) {
		return false
	}
	if ra.Text != rb.Text || ra.Signature != rb.Signature {
		return false
	}
	return bytes.Equal(ra.Opaque, rb.Opaque)
}

func validateArtifacts(artifacts []TurnArtifact) error {
	for i, a := range artifacts {
		if a.ReasoningBytes < 0 {
			return fmt.Errorf("%s: artifact[%d] has negative ReasoningBytes", ID, i)
		}
		if a.ID == "" {
			return fmt.Errorf("%s: artifact[%d] missing id", ID, i)
		}
		for j, p := range a.Reasoning {
			if p.BeforeNonReasoningPart < 0 {
				return fmt.Errorf("%s: artifact[%d].Reasoning[%d] invalid placement", ID, i, j)
			}
			if p.Part.Kind != lipapi.PartReasoning || p.Part.Reasoning == nil {
				return fmt.Errorf("%s: artifact[%d].Reasoning[%d] invalid part", ID, i, j)
			}
		}
	}
	return nil
}
