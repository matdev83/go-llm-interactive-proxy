package conformance

import (
	"errors"
	"fmt"
	"strings"
)

type FeatureID string

const (
	FeatureJSONText             FeatureID = "json_text"
	FeatureSSEText              FeatureID = "sse_text"
	FeatureInstructionsRoles    FeatureID = "instructions_roles"
	FeatureHistory              FeatureID = "history"
	FeatureTools                FeatureID = "tools"
	FeatureMultimodal           FeatureID = "multimodal"
	FeatureAssistantMedia       FeatureID = "assistant_media"
	FeatureUsageErrors          FeatureID = "usage_errors"
	FeatureReasoningReplay      FeatureID = "reasoning_replay"
	FeatureAssistantPhase       FeatureID = "assistant_phase"
	FeatureItemReferences       FeatureID = "item_references"
	FeatureContinuation         FeatureID = "continuation"
	FeatureCompaction           FeatureID = "compaction"
	FeatureExtensions           FeatureID = "extensions"
	FeatureCancellation         FeatureID = "cancellation_backpressure"
	FeatureFailover             FeatureID = "failover"
	FeatureNoRetryVisibleOutput FeatureID = "no_retry_after_visible_output"
)

type CompatibilityOutcome string

const (
	OutcomeLossless        CompatibilityOutcome = "lossless"
	OutcomeProjection      CompatibilityOutcome = "documented_deterministic_projection"
	OutcomeRejectBeforeNet CompatibilityOutcome = "rejected_before_network"
	OutcomeOutOfScope      CompatibilityOutcome = "out_of_scope"
	OutcomePlanned         CompatibilityOutcome = "planned"      // Prohibited in release-ready entries
	OutcomeUnclassified    CompatibilityOutcome = "unclassified" // Prohibited in release-ready entries
)

type FeatureEvidence struct {
	Outcome       CompatibilityOutcome `json:"outcome"`
	ScenarioIDs   []string             `json:"scenario_ids"`
	TestArtifacts []string             `json:"test_artifacts"`
	Rationale     string               `json:"rationale"`
}

func (fe FeatureEvidence) ValidateReleaseReady() error {
	switch fe.Outcome {
	case OutcomeLossless, OutcomeProjection, OutcomeRejectBeforeNet, OutcomeOutOfScope:
		// Valid outcomes
	case OutcomePlanned:
		return errors.New("outcome 'planned' is not permitted for release-ready evidence")
	case OutcomeUnclassified:
		return errors.New("outcome 'unclassified' is not permitted for release-ready evidence")
	case "":
		return errors.New("outcome cannot be empty")
	default:
		return fmt.Errorf("unknown compatibility outcome: %q", fe.Outcome)
	}

	if fe.Outcome == OutcomeOutOfScope {
		if strings.TrimSpace(fe.Rationale) == "" {
			return errors.New("out_of_scope feature evidence must provide an explicit non-empty rationale")
		}
	} else {
		if len(fe.ScenarioIDs) == 0 {
			return errors.New("release-ready feature evidence must link at least one scenario ID")
		}
		if len(fe.TestArtifacts) == 0 {
			return errors.New("release-ready feature evidence must link at least one test artifact")
		}
		for _, sID := range fe.ScenarioIDs {
			if strings.TrimSpace(sID) == "" {
				return errors.New("scenario ID cannot be empty string")
			}
		}
		for _, art := range fe.TestArtifacts {
			if strings.TrimSpace(art) == "" {
				return errors.New("test artifact path cannot be empty string")
			}
		}
	}
	return nil
}
