package conformance

// OpenResponses backend compatibility column (spec Phase 8, Task 8.4).
//
// The column classifies the five bundled frontend/client families →
// OpenResponses-compatible backend cells with linked feature evidence. Every
// cell feature is lossless, documented_deterministic_projection,
// rejected_before_network, or out_of_scope (with rationale). No cell may be
// planned, unclassified, or silently unlinked.
//
// The backend column consumes legacy message-authority calls (OpenAI Chat,
// OpenAI Responses, Anthropic, Gemini) and item-authority calls (OpenResponses)
// through the single explicit legacy→ordered-items projector (or the pinned
// item wire), so no pairwise translator exists. Rejections assert zero
// reference-backend requests.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	testkitopenresponses "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/openresponses"
)

// OpenResponsesBackendColumnFrontendIDs returns the deterministic authoritative
// list of the five Task 8.4 column cells (frontend family → OpenResponses backend).
func OpenResponsesBackendColumnFrontendIDs() []string {
	return []string{
		FrontendOpenAILegacy,
		FrontendOpenAIResponses,
		FrontendAnthropic,
		FrontendGemini,
		FrontendOpenResponses,
	}
}

// ColumnScenarioPrefix is the shared scenario-ID prefix for column scenarios.
const ColumnScenarioPrefix = "openresponses-column"

// OpenResponsesBackendColumnScenarioIDs returns the deterministic scenario IDs a
// cell registers and links, derived from the executable scenario table
// (openresponses_backend_column_scenarios.go). Every feature evidence scenario
// ID must come from this set so the validator can cross-check registrations.
func OpenResponsesBackendColumnScenarioIDs(frontend string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, sc := range OpenResponsesBackendColumnScenarios() {
		if sc.Frontend != frontend {
			continue
		}
		if _, ok := seen[sc.ScenarioID]; ok {
			continue
		}
		seen[sc.ScenarioID] = struct{}{}
		out = append(out, sc.ScenarioID)
	}
	return out
}

// OpenResponsesBackendColumnRequiredFeatures is the feature set every column
// cell must classify (mirrors the repository matrix feature vocabulary).
func OpenResponsesBackendColumnRequiredFeatures() []FeatureID {
	return []FeatureID{
		FeatureJSONText,
		FeatureSSEText,
		FeatureInstructionsRoles,
		FeatureHistory,
		FeatureTools,
		FeatureMultimodal,
		FeatureAssistantMedia,
		FeatureUsageErrors,
		FeatureReasoningReplay,
		FeatureAssistantPhase,
		FeatureItemReferences,
		FeatureContinuation,
		FeatureCompaction,
		FeatureExtensions,
		FeatureCancellation,
		FeatureFailover,
		FeatureNoRetryVisibleOutput,
	}
}

// OpenResponsesBackendColumnCell is one OpenResponses-backend column cell with
// its complete feature evidence.
type OpenResponsesBackendColumnCell struct {
	Frontend string
	Features map[FeatureID]FeatureEvidence
}

// columnScenarioID builds the scenario ID for a cell frontend and feature suffix.
func columnScenarioID(frontend, suffix string) string {
	return ColumnScenarioPrefix + "-" + frontend + "-" + suffix
}

// columnArtifacts lists the repository test sources that exercise the column cell.
var columnArtifacts = []string{
	"internal/testkit/conformance/openresponses_backend_column_conformance_test.go",
	"internal/testkit/conformance/openresponses_backend_column_evidence_test.go",
}

// columnArtifactRef points evidence at the column artifact set.
func columnArtifactRef() []string {
	return append([]string(nil), columnArtifacts...)
}

func columnLossless(frontend string, feat FeatureID, rationale string) FeatureEvidence {
	return FeatureEvidence{
		Outcome:       OutcomeLossless,
		ScenarioIDs:   columnScenarioIDsByFeature(frontend, feat),
		TestArtifacts: columnArtifactRef(),
		Rationale:     rationale,
	}
}

func columnProjection(frontend string, feat FeatureID, rationale string) FeatureEvidence {
	return FeatureEvidence{
		Outcome:       OutcomeProjection,
		ScenarioIDs:   columnScenarioIDsByFeature(frontend, feat),
		TestArtifacts: columnArtifactRef(),
		Rationale:     rationale,
	}
}

func columnReject(frontend string, feat FeatureID, rationale string) FeatureEvidence {
	return FeatureEvidence{
		Outcome:       OutcomeRejectBeforeNet,
		ScenarioIDs:   columnScenarioIDsByFeature(frontend, feat),
		TestArtifacts: columnArtifactRef(),
		Rationale:     rationale,
	}
}

func columnOutOfScope(rationale string) FeatureEvidence {
	return FeatureEvidence{
		Outcome:   OutcomeOutOfScope,
		Rationale: rationale,
	}
}

// OpenResponsesBackendColumn returns the authoritative feature classification
// for the five OpenResponses-backend column cells. Outcomes are evidence-backed
// by the harness cell tests; rejections assert zero reference-backend requests.
func OpenResponsesBackendColumn() []OpenResponsesBackendColumnCell {
	out := make([]OpenResponsesBackendColumnCell, 0, len(OpenResponsesBackendColumnFrontendIDs()))
	for _, frontend := range OpenResponsesBackendColumnFrontendIDs() {
		out = append(out, openResponsesBackendColumnCellFor(frontend))
	}
	return out
}

func openResponsesBackendColumnCellFor(frontend string) OpenResponsesBackendColumnCell {
	cell := OpenResponsesBackendColumnCell{Frontend: frontend, Features: map[FeatureID]FeatureEvidence{}}

	textOutcome := OutcomeLossless
	textRationale := "Portable text and instructions project through the explicit legacy→ordered-items projector with exact ordering."
	if frontend == FrontendOpenResponses {
		textRationale = "Item-authority calls map to the pinned wire with exact ordering."
	}
	text := func(feat FeatureID, rationale string) FeatureEvidence {
		return FeatureEvidence{
			Outcome:       textOutcome,
			ScenarioIDs:   columnScenarioIDsByFeature(frontend, feat),
			TestArtifacts: columnArtifactRef(),
			Rationale:     rationale,
		}
	}

	cell.Features[FeatureJSONText] = text(FeatureJSONText, textRationale)
	cell.Features[FeatureSSEText] = text(FeatureSSEText,
		"Incremental SSE text reaches the client with a single terminal event over the same canonical stream.")
	cell.Features[FeatureInstructionsRoles] = text(FeatureInstructionsRoles, textRationale)
	cell.Features[FeatureHistory] = text(FeatureHistory,
		"Ordered history is preserved through the legacy→ordered-items projection and the pinned wire.")

	// Tools / multimodal.
	switch frontend {
	case FrontendOpenResponses:
		cell.Features[FeatureTools] = columnLossless(frontend, FeatureTools, "Semantics survive the legacy→ordered-items projection (or pinned item wire) unchanged.")
		cell.Features[FeatureMultimodal] = columnProjection(frontend, FeatureMultimodal, "Image input maps to the pinned wire's portable image representation.")
	default:
		cell.Features[FeatureTools] = columnProjection(frontend, FeatureTools, "Tools and results project to ordered function_call / function_call_output items.")
		cell.Features[FeatureMultimodal] = columnProjection(frontend, FeatureMultimodal, "Image input projects to an ordered image item in the portable trajectory.")
	}

	cell.Features[FeatureAssistantMedia] = columnOutOfScope("The OpenResponses backend emits no assistant media reference output events and the OpenResponses frontend has no EventAssistantImageRef/EventAssistantFileRef output mapping, so assistant media reference output has no surface in the column configuration.")

	cell.Features[FeatureUsageErrors] = text(FeatureUsageErrors,
		"Usage is surfaced on the response resource when reported and upstream errors map to stable client-visible error envelopes.")

	// Continuation is positive only for the OpenResponses frontend cell (the
	// proxy-owned continuation surface). Every legacy column frontend exposes no
	// client-facing previous-response surface, so continuation is honestly
	// out_of_scope there with an exact rationale and never links a scenario.
	if frontend == FrontendOpenResponses {
		cell.Features[FeatureContinuation] = columnLossless(frontend, FeatureContinuation,
			"Proxy-owned continuation materializes the canonical history and re-executes through the same projector; the executable continuation scenario asserts the second create with previous_response_id round-trips and re-executes upstream exactly once.")
	} else {
		cell.Features[FeatureContinuation] = columnOutOfScope(columnContinuationOutOfScopeRationale(frontend))
	}
	cell.Features[FeatureReasoningReplay] = columnReject(frontend, FeatureReasoningReplay,
		"No column cell declares a compatible provider-bound reasoning replay dialect; reasoning replay is rejected before network with zero requests.")
	cell.Features[FeatureAssistantPhase] = columnReject(frontend, FeatureAssistantPhase,
		"Assistant phase on input items is rejected before network with zero requests.")
	cell.Features[FeatureItemReferences] = columnReject(frontend, FeatureItemReferences,
		"No column cell declares a compatible item-reference dialect; item references are rejected before network with zero requests.")
	cell.Features[FeatureCompaction] = columnReject(frontend, FeatureCompaction,
		"Compaction input is rejected before network with zero requests.")
	if frontend == FrontendOpenResponses {
		ev := columnLossless(frontend, FeatureCompaction,
			"The generic OpenResponses backend declares the compaction operation/capability; compaction input round-trips losslessly.")
		cell.Features[FeatureCompaction] = ev
	}
	cell.Features[FeatureExtensions] = columnReject(frontend, FeatureExtensions,
		"No column cell declares a compatible extension type; opaque extensions are rejected before network with zero requests.")

	// Cancellation, pre-output failover, and post-visible no-retry each link
	// their own executable scenario (never the generic usage-commitment proof).
	// The scenarios assert the request counts: a canceled client stops upstream
	// work with no second attempt, failover reaches the candidate, and an
	// abrupt mid-stream death after the first visible output leaves the
	// candidate untouched.
	cell.Features[FeatureCancellation] = columnLossless(frontend, FeatureCancellation,
		"Cancellation/backpressure surface through the transport: a canceled client context stops upstream work with no second attempt and no failover (executable cancellation scenario asserts the candidate origin receives zero requests).")
	cell.Features[FeatureFailover] = columnLossless(frontend, FeatureFailover,
		"Pre-output failover retains the complete requirement set and re-executes through the same canonical path (executable failover scenario with a failing primary and a succeeding candidate asserts both origin counts).")
	cell.Features[FeatureNoRetryVisibleOutput] = columnLossless(frontend, FeatureNoRetryVisibleOutput,
		"Single-terminal commitment is preserved; after the first visible output an abrupt upstream termination does not trigger retry or failover (executable no-retry scenario asserts the candidate origin receives zero requests).")
	return cell
}

// columnContinuationOutOfScopeRationale returns the exact approved rationale for
// the out_of_scope continuation classification of a legacy column frontend.
func columnContinuationOutOfScopeRationale(frontend string) string {
	switch frontend {
	case FrontendOpenAIResponses:
		return "The openai-responses frontend recognizes but does not materialize previous_response_id, so proxy-owned continuation has no client-facing surface in this column cell (the OpenResponses continuation surface lives in the OpenResponses frontend row/column)."
	case FrontendOpenAILegacy:
		return "The openai-legacy chat API has no parent/previous-response concept, so proxy-owned continuation has no client-facing surface for this column cell."
	case FrontendAnthropic:
		return "The Anthropic Messages API has no parent/previous-response concept, so proxy-owned continuation has no client-facing surface for this column cell."
	case FrontendGemini:
		return "The Gemini generateContent API has no parent/previous-response concept, so proxy-owned continuation has no client-facing surface for this column cell."
	default:
		return "This column frontend exposes no parent/previous-response concept, so proxy-owned continuation has no client-facing surface."
	}
}

// ValidateOpenResponsesBackendColumn checks that every column cell classifies
// every required feature with release-ready, artifact-linked evidence. It
// returns a non-nil error describing the first violation so release gates fail
// closed.
func ValidateOpenResponsesBackendColumn(moduleRoot string) error {
	cells := OpenResponsesBackendColumn()
	required := OpenResponsesBackendColumnRequiredFeatures()
	for _, cell := range cells {
		for _, feat := range required {
			ev, ok := cell.Features[feat]
			if !ok {
				return fmt.Errorf("column cell %s: missing evidence for feature %q", cell.Frontend, feat)
			}
			if err := ev.ValidateReleaseReady(); err != nil {
				return fmt.Errorf("column cell %s feature %q: %w", cell.Frontend, feat, err)
			}
			if ev.Outcome == OutcomeOutOfScope {
				continue
			}
			for _, art := range ev.TestArtifacts {
				if !columnArtifactExists(moduleRoot, art) {
					return fmt.Errorf("column cell %s feature %q: linked test artifact not found: %s", cell.Frontend, feat, art)
				}
			}
			for _, sid := range ev.ScenarioIDs {
				if !strings.HasPrefix(sid, ColumnScenarioPrefix) {
					return fmt.Errorf("column cell %s feature %q: scenario ID %q does not use the column prefix %q", cell.Frontend, feat, sid, ColumnScenarioPrefix)
				}
			}
		}
	}
	return nil
}

func columnArtifactExists(moduleRoot, rel string) bool {
	if strings.TrimSpace(rel) == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(moduleRoot, filepath.FromSlash(rel)))
	return err == nil
}

// RegisterOpenResponsesBackendColumnScenarios registers every column scenario ID
// on reg so evidence scenario IDs are verifiable against the registry.
func RegisterOpenResponsesBackendColumnScenarios(reg *testkitopenresponses.ScenarioRegistry) error {
	for _, frontend := range OpenResponsesBackendColumnFrontendIDs() {
		for _, sid := range OpenResponsesBackendColumnScenarioIDs(frontend) {
			if err := reg.Register(testkitopenresponses.ScenarioDescriptor{
				ID:          sid,
				Kind:        testkitopenresponses.ScenarioKindJSONText,
				Description: "OpenResponses backend column cell " + frontend + " scenario " + sid,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
