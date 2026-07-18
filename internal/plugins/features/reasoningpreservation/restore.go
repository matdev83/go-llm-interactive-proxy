package reasoningpreservation

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type RestoreResult struct {
	Mutated       bool
	RestoredCount int
	RestoredBytes int
	Exclude       bool
	ReasonCode    string
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
	if in.Call == nil {
		return RestoreResult{}, fmt.Errorf("%s: call is required", ID)
	}
	artifacts := cloneArtifacts(in.Artifacts)
	if in.Action == ActionObserve {
		if err := validateArtifacts(artifacts); err != nil {
			return RestoreResult{ReasonCode: "state_error"}, nil
		}
		if _, err := ClassifyAssistantTurns(in.Call.Messages, artifacts); err != nil {
			return RestoreResult{ReasonCode: "state_error"}, nil
		}
		return RestoreResult{}, nil
	}
	if err := validateArtifacts(artifacts); err != nil {
		return applyStateErrorPolicy(in.OnStateError)
	}
	classified, err := ClassifyAssistantTurns(in.Call.Messages, artifacts)
	if err != nil {
		return applyStateErrorPolicy(in.OnStateError)
	}
	if !in.Eligible {
		return RestoreResult{}, nil
	}
	if in.Action != ActionRestore {
		return RestoreResult{}, fmt.Errorf("%s: unknown action %q", ID, in.Action)
	}

	byID := make(map[string]TurnArtifact, len(artifacts))
	for _, a := range artifacts {
		byID[a.ID] = a
	}

	type pending struct {
		msgIndex int
		art      TurnArtifact
	}
	var toRestore []pending
	assistantIdx := 0
	for i, m := range in.Call.Messages {
		if m.Role != lipapi.RoleAssistant {
			continue
		}
		if assistantIdx >= len(classified) {
			return applyStateErrorPolicy(in.OnStateError)
		}
		c := classified[assistantIdx]
		assistantIdx++
		if c.Classification != ClassMissing {
			continue
		}
		art, ok := byID[c.ArtifactID]
		if !ok {
			return applyStateErrorPolicy(in.OnStateError)
		}
		toRestore = append(toRestore, pending{msgIndex: i, art: art})
	}
	if len(toRestore) == 0 {
		return RestoreResult{}, nil
	}

	supported := dialectSet(in.ReplaySupport.Dialects)
	for _, p := range toRestore {
		for _, pr := range p.art.Reasoning {
			d := lipapi.NormalizeReasoningDialect(pr.Part.Reasoning.Dialect)
			if _, ok := supported[d]; !ok {
				return applyUnrepresentablePolicy(in.OnUnrepresentable)
			}
		}
	}

	work := lipapi.CloneCall(*in.Call)
	restoredCount := 0
	restoredBytes := 0
	for _, p := range toRestore {
		msg := work.Messages[p.msgIndex]
		newParts, bytes, err := insertPlacements(msg.Parts, p.art.Reasoning)
		if err != nil {
			return applyStateErrorPolicy(in.OnStateError)
		}
		work.Messages[p.msgIndex].Parts = newParts
		restoredCount++
		restoredBytes += bytes
	}
	if err := work.Validate(); err != nil {
		return applyStateErrorPolicy(in.OnStateError)
	}
	in.Call.Messages = work.Messages
	return RestoreResult{
		Mutated:       true,
		RestoredCount: restoredCount,
		RestoredBytes: restoredBytes,
	}, nil
}

func applyUnrepresentablePolicy(policy string) (RestoreResult, error) {
	switch policy {
	case PolicyReject, "":
		return RestoreResult{Exclude: true, ReasonCode: "unrepresentable_replay"}, nil
	case PolicyLogSkip:
		return RestoreResult{ReasonCode: "unrepresentable"}, nil
	default:
		return RestoreResult{}, fmt.Errorf("%s: unknown on_unrepresentable policy", ID)
	}
}

func applyStateErrorPolicy(policy string) (RestoreResult, error) {
	switch policy {
	case PolicyReject:
		return RestoreResult{Exclude: true, ReasonCode: "state_error"}, nil
	case PolicyLogSkip, "":
		return RestoreResult{ReasonCode: "state_error"}, nil
	default:
		return RestoreResult{}, fmt.Errorf("%s: unknown on_state_error policy", ID)
	}
}

func dialectSet(dialects []lipapi.ReasoningDialect) map[lipapi.ReasoningDialect]struct{} {
	out := make(map[lipapi.ReasoningDialect]struct{}, len(dialects))
	for _, d := range lipapi.NormalizeReasoningDialects(dialects) {
		out[d] = struct{}{}
	}
	return out
}

func insertPlacements(parts []lipapi.Part, placements []PlacedReasoning) ([]lipapi.Part, int, error) {
	nonReasoning := make([]lipapi.Part, 0, len(parts))
	for _, p := range parts {
		if p.Kind == lipapi.PartReasoning {
			continue
		}
		nonReasoning = append(nonReasoning, clonePart(p))
	}
	n := len(nonReasoning)
	byIndex := make(map[int][]lipapi.Part)
	bytes := 0
	for _, pr := range placements {
		if pr.BeforeNonReasoningPart < 0 || pr.BeforeNonReasoningPart > n {
			return nil, 0, fmt.Errorf("%s: placement out of range", ID)
		}
		if pr.Part.Kind != lipapi.PartReasoning || pr.Part.Reasoning == nil {
			return nil, 0, fmt.Errorf("%s: invalid placed reasoning", ID)
		}
		cp := clonePart(pr.Part)
		byIndex[pr.BeforeNonReasoningPart] = append(byIndex[pr.BeforeNonReasoningPart], cp)
		bytes += lipapi.ReasoningPayloadBytes(cp.Reasoning)
	}
	out := make([]lipapi.Part, 0, len(nonReasoning)+len(placements))
	for i := 0; i <= n; i++ {
		out = append(out, byIndex[i]...)
		if i < n {
			out = append(out, nonReasoning[i])
		}
	}
	return out, bytes, nil
}
