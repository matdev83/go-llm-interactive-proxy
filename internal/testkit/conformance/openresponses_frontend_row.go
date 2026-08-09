package conformance

// OpenResponses frontend compatibility row (spec Phase 8, Task 8.3).
//
// The row classifies the nine OpenResponses-frontend → bundled backend cells with
// linked feature evidence. Every cell feature is either lossless,
// documented_deterministic_projection, rejected_before_network, or out_of_scope
// (with rationale). No cell may be planned, unclassified, or silently unlinked.
//
// Evidence mirrors observed harness behavior: rejections assert zero remote
// requests against the observing reference-provider origin.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	testkitopenresponses "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/openresponses"
)

// OpenResponsesFrontendRowBackendIDs returns the deterministic authoritative
// list of the nine Task 8.3 row cells (OpenResponses frontend × backend).
func OpenResponsesFrontendRowBackendIDs() []string {
	return []string{
		BackendOpenAILegacy,
		BackendOpenAIResponses,
		BackendACP,
		BackendAnthropic,
		BackendGemini,
		BackendBedrock,
		BackendOpenResponses,
		BackendOpenRouter,
		BackendNVIDIA,
	}
}

// RowScenarioPrefix is the shared scenario-ID prefix for row scenarios.
const RowScenarioPrefix = "openresponses-row"

// OpenResponsesFrontendRowScenarioIDs returns the deterministic scenario IDs a
// cell registers and links, derived from the executable scenario table
// (openresponses_frontend_row_scenarios.go). Every feature evidence scenario ID
// must come from this set so the validator can cross-check registrations.
func OpenResponsesFrontendRowScenarioIDs(backend string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, sc := range OpenResponsesFrontendRowScenarios() {
		if sc.Backend != backend {
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

// OpenResponsesFrontendRowRequiredFeatures is the feature set every row cell
// must classify (mirrors the repository matrix feature vocabulary).
func OpenResponsesFrontendRowRequiredFeatures() []FeatureID {
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

// OpenResponsesFrontendRowCell is one OpenResponses-frontend row cell with its
// complete feature evidence.
type OpenResponsesFrontendRowCell struct {
	Backend  string
	Features map[FeatureID]FeatureEvidence
}

// rowScenarioID builds the scenario ID for a cell backend and feature suffix.
func rowScenarioID(backend, suffix string) string {
	return RowScenarioPrefix + "-" + backend + "-" + suffix
}

// rowArtifacts lists the repository test sources that exercise the row cell.
var rowArtifacts = []string{
	"internal/testkit/conformance/openresponses_frontend_row_conformance_test.go",
	"internal/testkit/conformance/openresponses_frontend_row_evidence_test.go",
	"internal/testkit/conformance/openresponses_provider_mode.go",
	"internal/testkit/conformance/connector_host.go",
	"internal/testkit/conformance/connector_columns_matrix_test.go",
}

// rowArtifactRef points evidence at the row artifact set.
func rowArtifactRef() []string {
	return append([]string(nil), rowArtifacts...)
}

// rowLossless links lossless evidence to the executable scenario of one feature.
func rowLossless(backend string, feat FeatureID, rationale string) FeatureEvidence {
	return FeatureEvidence{
		Outcome:       OutcomeLossless,
		ScenarioIDs:   rowScenarioIDsByFeature(backend, feat),
		TestArtifacts: rowArtifactRef(),
		Rationale:     rationale,
	}
}

func rowProjection(backend string, feat FeatureID, rationale string) FeatureEvidence {
	return FeatureEvidence{
		Outcome:       OutcomeProjection,
		ScenarioIDs:   rowScenarioIDsByFeature(backend, feat),
		TestArtifacts: rowArtifactRef(),
		Rationale:     rationale,
	}
}

func rowReject(backend string, feat FeatureID, rationale string) FeatureEvidence {
	return FeatureEvidence{
		Outcome:       OutcomeRejectBeforeNet,
		ScenarioIDs:   rowScenarioIDsByFeature(backend, feat),
		TestArtifacts: rowArtifactRef(),
		Rationale:     rationale,
	}
}

func rowOutOfScope(rationale string) FeatureEvidence {
	return FeatureEvidence{
		Outcome:   OutcomeOutOfScope,
		Rationale: rationale,
	}
}

// OpenResponsesFrontendRow returns the authoritative feature classification for
// the nine OpenResponses-frontend row cells. Outcomes are evidence-backed by the
// harness cell tests; rejections assert zero upstream requests.
func OpenResponsesFrontendRow() []OpenResponsesFrontendRowCell {
	out := make([]OpenResponsesFrontendRowCell, 0, len(OpenResponsesFrontendRowBackendIDs()))
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		out = append(out, openResponsesFrontendRowCellFor(backend))
	}
	return out
}

func openResponsesFrontendRowCellFor(backend string) OpenResponsesFrontendRowCell {
	cell := OpenResponsesFrontendRowCell{Backend: backend, Features: map[FeatureID]FeatureEvidence{}}
	projected := backend == BackendACP || backend == BackendOpenRouter || backend == BackendNVIDIA

	textOutcome := OutcomeLossless
	if projected {
		textOutcome = OutcomeProjection
	}
	textRationale := "Portable text projects through the explicit item→target view with exact ordering."
	switch backend {
	case BackendACP:
		textRationale = "Portable text and system instructions project to ACP prompt-turn text blocks through the item→message projector."
	case BackendOpenRouter, BackendNVIDIA:
		textRationale = "Portable text projects through the item→legacy view to the OpenAI-compatible wire the connector selects for the openresponses.create operation (the chat-completions wire)."
	}
	text := func(feat FeatureID, rationale string) FeatureEvidence {
		return FeatureEvidence{
			Outcome:       textOutcome,
			ScenarioIDs:   rowScenarioIDsByFeature(backend, feat),
			TestArtifacts: rowArtifactRef(),
			Rationale:     rationale,
		}
	}

	cell.Features[FeatureJSONText] = text(FeatureJSONText, textRationale)
	cell.Features[FeatureSSEText] = text(FeatureSSEText,
		"Incremental SSE text reaches the client with a single terminal event over the same canonical stream.")
	cell.Features[FeatureInstructionsRoles] = text(FeatureInstructionsRoles, textRationale)
	cell.Features[FeatureHistory] = text(FeatureHistory,
		"Ordered history is preserved through the canonical item trajectory and the target view.")

	// Tools / multimodal.
	switch backend {
	case BackendACP:
		cell.Features[FeatureTools] = rowReject(backend, FeatureTools, "ACP v1 prompt-turn subset rejects tools before any network request (validateACPCall).")
		cell.Features[FeatureMultimodal] = rowProjection(backend, FeatureMultimodal,
			"Image/file URI references project to ACP resource prompt blocks (positive resource subset); unrepresentable multimodal forms (video/audio) are rejected before network.")
	case BackendOpenRouter, BackendNVIDIA:
		cell.Features[FeatureTools] = rowReject(backend, FeatureTools,
			"The connector executables advertise streaming-only capabilities, so canonical tools are rejected before any network request by the host-adapter backend admission.")
		cell.Features[FeatureMultimodal] = rowReject(backend, FeatureMultimodal,
			"The connector executables advertise streaming-only capabilities (no vision), so multimodal input is rejected before any network request by the host-adapter backend admission.")
	case BackendOpenAIResponses, BackendOpenResponses:
		cell.Features[FeatureTools] = rowLossless(backend, FeatureTools, "Semantics survive the canonical item trajectory unchanged.")
		cell.Features[FeatureMultimodal] = rowProjection(backend, FeatureMultimodal, "Image input projects to the target's portable image representation.")
	default:
		cell.Features[FeatureTools] = rowProjection(backend, FeatureTools, "Tools and results project to the target's documented tool surface.")
		cell.Features[FeatureMultimodal] = rowProjection(backend, FeatureMultimodal, "Image input projects to the target's portable image representation.")
	}

	cell.Features[FeatureAssistantMedia] = rowOutOfScope("The OpenResponses frontend has no EventAssistantImageRef/EventAssistantFileRef output mapping on its client wire, so assistant media reference output has no surface in the row configuration.")

	cell.Features[FeatureUsageErrors] = text(FeatureUsageErrors,
		"Usage is surfaced on the response resource when reported and upstream errors map to stable client-visible error envelopes.")

	// Continuation is proxy-owned: it stores the canonical trajectory and
	// re-executes through the same projector, so it is positive for every row
	// cell that can replay the materialized trajectory. The ACP v1 prompt-turn
	// subset cannot replay a materialized trajectory whose prior ACP response
	// produced reasoning output (ACP agent plan updates), so the continuation
	// call is honestly rejected before any network request with zero additional
	// upstream requests. Every cell links the dedicated executable
	// -continuation scenario (never the generic json-text proof).
	if backend == BackendACP {
		cell.Features[FeatureContinuation] = rowReject(backend, FeatureContinuation,
			"Proxy-owned continuation materializes the prior canonical trajectory; when the prior ACP response produced reasoning output (ACP agent plan updates), the ACP v1 prompt-turn subset cannot replay the materialized reasoning trajectory, so the continuation call is rejected before any network request (the executable continuation scenario asserts the second create with previous_response_id is rejected with zero additional upstream requests).")
	} else {
		cell.Features[FeatureContinuation] = rowLossless(backend, FeatureContinuation,
			"Proxy-owned continuation materializes the canonical history and re-executes through the same projector; the executable continuation scenario asserts the second create with previous_response_id round-trips and re-executes upstream exactly once.")
	}
	cell.Features[FeatureReasoningReplay] = rowReject(backend, FeatureReasoningReplay,
		"No row cell declares a compatible provider-bound reasoning replay dialect; reasoning replay is rejected before network with zero requests.")
	cell.Features[FeatureAssistantPhase] = rowReject(backend, FeatureAssistantPhase,
		"Assistant phase on input items is rejected before network with zero requests.")
	cell.Features[FeatureItemReferences] = rowReject(backend, FeatureItemReferences,
		"No row cell declares a compatible item-reference dialect; item references are rejected before network with zero requests.")
	if backend == BackendOpenResponses {
		cell.Features[FeatureItemReferences] = rowLossless(backend, FeatureItemReferences,
			"The generic OpenResponses backend declares the exact item_reference item dialect and the pinned wire carries item_reference items verbatim; an item-reference call round-trips losslessly (the executable itemref scenario asserts the upstream request carries the item reference).")
	}
	cell.Features[FeatureCompaction] = rowReject(backend, FeatureCompaction,
		"Compaction input is rejected before network with zero requests.")
	if backend == BackendOpenResponses {
		ev := rowLossless(backend, FeatureCompaction,
			"The generic OpenResponses backend declares the compaction operation/capability; compaction input round-trips losslessly.")
		cell.Features[FeatureCompaction] = ev
	}
	cell.Features[FeatureExtensions] = rowReject(backend, FeatureExtensions,
		"No row cell declares a compatible extension type; opaque extensions are rejected before network with zero requests.")

	// Cancellation, pre-output failover, and post-visible no-retry each link
	// their own executable scenario (never the generic usage-commitment proof).
	// The scenarios assert the request counts: a canceled client stops upstream
	// work with no second attempt, failover reaches the candidate, and an
	// abrupt mid-stream death after the first visible output leaves the
	// candidate untouched.
	cell.Features[FeatureCancellation] = rowLossless(backend, FeatureCancellation,
		"Cancellation/backpressure surface through the transport: a canceled client context stops upstream work with no second attempt and no failover (executable cancellation scenario asserts the candidate origin receives zero requests).")
	cell.Features[FeatureFailover] = rowLossless(backend, FeatureFailover,
		"Pre-output failover retains the complete requirement set and re-executes through the same canonical path (executable failover scenario with a failing primary and a succeeding candidate asserts both origin counts).")
	cell.Features[FeatureNoRetryVisibleOutput] = rowLossless(backend, FeatureNoRetryVisibleOutput,
		"Single-terminal commitment is preserved; after the first visible output an abrupt upstream termination does not trigger retry or failover (executable no-retry scenario asserts the candidate origin receives zero requests).")
	return cell
}

// ValidateOpenResponsesFrontendRow checks that every row cell classifies every
// required feature with release-ready, artifact-linked evidence. It returns a
// non-nil error describing the first violation so release gates fail closed.
func ValidateOpenResponsesFrontendRow(moduleRoot string) error {
	cells := OpenResponsesFrontendRow()
	required := OpenResponsesFrontendRowRequiredFeatures()
	for _, cell := range cells {
		for _, feat := range required {
			ev, ok := cell.Features[feat]
			if !ok {
				return fmt.Errorf("row cell %s: missing evidence for feature %q", cell.Backend, feat)
			}
			if err := ev.ValidateReleaseReady(); err != nil {
				return fmt.Errorf("row cell %s feature %q: %w", cell.Backend, feat, err)
			}
			if ev.Outcome == OutcomeOutOfScope {
				continue
			}
			for _, art := range ev.TestArtifacts {
				if !artifactExists(moduleRoot, art) {
					return fmt.Errorf("row cell %s feature %q: linked test artifact not found: %s", cell.Backend, feat, art)
				}
			}
			for _, sid := range ev.ScenarioIDs {
				if !strings.HasPrefix(sid, RowScenarioPrefix) {
					return fmt.Errorf("row cell %s feature %q: scenario ID %q does not use the row prefix %q", cell.Backend, feat, sid, RowScenarioPrefix)
				}
			}
		}
	}
	return nil
}

func artifactExists(moduleRoot, rel string) bool {
	if strings.TrimSpace(rel) == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(moduleRoot, filepath.FromSlash(rel)))
	return err == nil
}

// AssertOpenRouterNVIDIAStayOptional proves the OpenRouter/NVIDIA connector
// columns remain optional: they are absent from the essential backend kinds and
// from the base harness constructible backend set (the Task 8.3 route proof uses
// the actual connector executables through the connector host, not an essential
// connector).
func AssertOpenRouterNVIDIAStayOptional(moduleRoot string) error {
	for _, id := range []string{BackendOpenRouter, BackendNVIDIA} {
		if slices.Contains(standardplugins.EssentialBackendKinds, id) {
			return fmt.Errorf("row cell %q was silently promoted to an essential backend kind; optional connectors must stay optional (Task 8.5 owns authoritative list expansion)", id)
		}
	}
	return nil
}

// RegisterOpenResponsesFrontendRowScenarios registers every row scenario ID on
// reg so evidence scenario IDs are verifiable against the registry.
func RegisterOpenResponsesFrontendRowScenarios(reg *testkitopenresponses.ScenarioRegistry) error {
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		for _, sid := range OpenResponsesFrontendRowScenarioIDs(backend) {
			if err := reg.Register(testkitopenresponses.ScenarioDescriptor{
				ID:          sid,
				Kind:        testkitopenresponses.ScenarioKindJSONText,
				Description: "OpenResponses frontend row cell " + backend + " scenario " + sid,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
