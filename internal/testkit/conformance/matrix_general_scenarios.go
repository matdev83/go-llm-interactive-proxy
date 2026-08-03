package conformance

import (
	"fmt"
	"strings"

	testkitopenresponses "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/openresponses"
)

// Executable scenario table for the general matrix cells (the 4×8 = 32 cells
// owned by neither the OpenResponses frontend row nor the OpenResponses backend
// column; see GeneralMatrixCells in matrix.go).
//
// This table is the single source of truth for general-cell scenario IDs: the
// tagged integration test (matrix_general_conformance_test.go) executes every
// entry through a real deployment, and the evidence builder
// (matrix45GeneralFeatures) derives the exact same scenario IDs from this table.
// No general-cell scenario ID exists that is not an executable table entry.
//
// Features without an entry in GeneralMatrixFeatureSuffixes are classified
// out_of_scope in the evidence builder with an explicit approved rationale and
// never claim test evidence.

// GeneralMatrixFeatureSuffixes maps every feature that has an executable proof
// for general matrix cells to its scenario-ID suffix. Several features share one
// executable scenario (for example instructions/roles and history are proven by
// the json-text round trip), so they map to the same suffix.
func GeneralMatrixFeatureSuffixes() map[FeatureID]string {
	return map[FeatureID]string{
		FeatureJSONText:             "json-text",
		FeatureSSEText:              "sse-text",
		FeatureInstructionsRoles:    "json-text",
		FeatureHistory:              "json-text",
		FeatureTools:                "tools",
		FeatureMultimodal:           "multimodal",
		FeatureAssistantMedia:       "assistant-media",
		FeatureUsageErrors:          "usage-commitment",
		FeatureReasoningReplay:      "replay-reject",
		FeatureAssistantPhase:       "phase-reject",
		FeatureItemReferences:       "itemref-reject",
		FeatureCompaction:           "compaction-reject",
		FeatureExtensions:           "extension-reject",
		FeatureCancellation:         "cancellation",
		FeatureFailover:             "failover",
		FeatureNoRetryVisibleOutput: "no-retry",
	}
}

// GeneralMatrixScenario is one executable proof for one feature of one general
// matrix cell. ScenarioID is derived deterministically from the cell and the
// feature's suffix.
type GeneralMatrixScenario struct {
	Frontend   string
	Backend    string
	Feature    FeatureID
	ScenarioID string
}

// GeneralMatrixScenarios returns the authoritative executable scenario table
// for the 32 general cells. The integration test and the evidence builder both
// consume this exact table. Every general cell feature in
// GeneralMatrixFeatureSuffixes has an executable proof, so the table is the
// full suffix set per cell (no general-cell feature is out_of_scope).
func GeneralMatrixScenarios() []GeneralMatrixScenario {
	var out []GeneralMatrixScenario
	for _, cell := range GeneralMatrixCells() {
		for feat, suffix := range GeneralMatrixFeatureSuffixes() {
			out = append(out, GeneralMatrixScenario{
				Frontend:   cell.Frontend,
				Backend:    cell.Backend,
				Feature:    feat,
				ScenarioID: matrix45ScenarioID(cell.Frontend, cell.Backend, suffix),
			})
		}
	}
	return out
}

// GeneralMatrixScenarioIDs returns the deterministic scenario IDs a general
// matrix cell registers and links, derived from the executable scenario table.
func GeneralMatrixScenarioIDs(frontend, backend string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, sc := range GeneralMatrixScenarios() {
		if sc.Frontend != frontend || sc.Backend != backend {
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

// generalMatrixScenarioByID returns the table entry for a scenario ID, if any.
func generalMatrixScenarioByID(sid string) (GeneralMatrixScenario, bool) {
	for _, sc := range GeneralMatrixScenarios() {
		if sc.ScenarioID == sid {
			return sc, true
		}
	}
	return GeneralMatrixScenario{}, false
}

// matrix45GeneralScenarioIDsByFeature returns the scenario IDs a general cell
// links for one feature (normally exactly one); features without an executable
// proof return nil.
func matrix45GeneralScenarioIDsByFeature(frontend, backend string, feat FeatureID) []string {
	suffix, ok := GeneralMatrixFeatureSuffixes()[feat]
	if !ok {
		return nil
	}
	return []string{matrix45ScenarioID(frontend, backend, suffix)}
}

// matrix45GeneralFeatures classifies every required feature of one general
// matrix cell. Scenario IDs are derived from the executable scenario table; no
// feature claims evidence for a scenario that is not executed by the tagged
// integration test. Features with no executable transport surface are classified
// out_of_scope with an explicit approved rationale.
func matrix45GeneralFeatures(frontend, backend string) map[FeatureID]FeatureEvidence {
	native := frontend == backend
	cell := map[FeatureID]FeatureEvidence{}

	lossless := func(feat FeatureID, rationale string) FeatureEvidence {
		return FeatureEvidence{
			Outcome:       OutcomeLossless,
			ScenarioIDs:   matrix45GeneralScenarioIDsByFeature(frontend, backend, feat),
			TestArtifacts: matrix45ArtifactRef(),
			Rationale:     rationale,
		}
	}
	projection := func(feat FeatureID, rationale string) FeatureEvidence {
		return FeatureEvidence{
			Outcome:       OutcomeProjection,
			ScenarioIDs:   matrix45GeneralScenarioIDsByFeature(frontend, backend, feat),
			TestArtifacts: matrix45ArtifactRef(),
			Rationale:     rationale,
		}
	}
	reject := func(feat FeatureID, rationale string) FeatureEvidence {
		return FeatureEvidence{
			Outcome:       OutcomeRejectBeforeNet,
			ScenarioIDs:   matrix45GeneralScenarioIDsByFeature(frontend, backend, feat),
			TestArtifacts: matrix45ArtifactRef(),
			Rationale:     rationale,
		}
	}

	cell[FeatureJSONText] = lossless(FeatureJSONText,
		"Portable text survives the canonical trajectory and the target view with exact ordering (executable json-text round trip).")
	cell[FeatureSSEText] = lossless(FeatureSSEText,
		"Incremental SSE text reaches the client with a single terminal event over the same canonical stream (executable sse-text round trip).")
	cell[FeatureInstructionsRoles] = lossless(FeatureInstructionsRoles,
		"System/developer/instructions and roles survive the canonical trajectory and the target view (shared json-text round trip).")
	cell[FeatureHistory] = lossless(FeatureHistory,
		"Ordered multi-turn history is preserved through the canonical trajectory and the target view (shared json-text round trip).")

	switch {
	case backend == BackendACP:
		cell[FeatureTools] = reject(FeatureTools,
			"ACP v1 prompt-turn subset rejects tools before any network request; the executable tools scenario asserts zero upstream requests.")
		cell[FeatureMultimodal] = projection(FeatureMultimodal,
			"Image input projects to ACP resource prompt blocks (the executable multimodal scenario round-trips with upstream resource projection); unrepresentable forms (video/audio) are rejected before network.")
	case backend == BackendOpenRouter || backend == BackendNVIDIA:
		cell[FeatureTools] = reject(FeatureTools,
			"The OpenRouter/NVIDIA connector executables advertise streaming-only capabilities, so canonical tools are rejected before any network request by the host-adapter backend admission; the executable tools scenario asserts zero upstream requests.")
		cell[FeatureMultimodal] = reject(FeatureMultimodal,
			"The OpenRouter/NVIDIA connector executables advertise streaming-only capabilities (no vision), so multimodal input is rejected before any network request by the host-adapter backend admission; the executable multimodal scenario asserts zero upstream requests.")
	case native:
		cell[FeatureTools] = lossless(FeatureTools,
			"The native tool surface round-trips losslessly through the canonical trajectory (executable tools round trip).")
		cell[FeatureMultimodal] = lossless(FeatureMultimodal,
			"The native multimodal surface round-trips losslessly through the canonical trajectory (executable multimodal round trip).")
	default:
		cell[FeatureTools] = projection(FeatureTools,
			"Tools and results project to the target's documented tool surface (executable tools round trip).")
		cell[FeatureMultimodal] = projection(FeatureMultimodal,
			"Image input projects to the target's portable image representation (executable multimodal round trip).")
	}

	cell[FeatureAssistantMedia] = assistantMediaFeature(frontend, backend)

	cell[FeatureUsageErrors] = lossless(FeatureUsageErrors,
		"Usage is surfaced on the response resource when reported and upstream errors map to stable client-visible error envelopes (executable usage-commitment scenario).")

	cell[FeatureContinuation] = FeatureEvidence{
		Outcome:   OutcomeOutOfScope,
		Rationale: generalContinuationOutOfScopeRationale(frontend),
	}

	cell[FeatureReasoningReplay] = reject(FeatureReasoningReplay,
		"The frontend wire cannot deliver provider-bound reasoning replay material; unrepresentable replay forms are rejected before any network request (executable replay-reject scenario).")
	cell[FeatureAssistantPhase] = reject(FeatureAssistantPhase,
		"The frontend wire has no assistant-phase surface; unrepresentable phase forms are rejected before any network request (executable phase-reject scenario).")
	cell[FeatureItemReferences] = reject(FeatureItemReferences,
		"The frontend wire has no item-reference dialect; unrepresentable item references are rejected before any network request (executable itemref-reject scenario).")
	cell[FeatureCompaction] = reject(FeatureCompaction,
		"The general cell has no compaction capability surface; unrepresentable compaction input is rejected before any network request (executable compaction-reject scenario).")
	cell[FeatureExtensions] = reject(FeatureExtensions,
		"No general cell declares a compatible extension type; unrepresentable opaque extensions are rejected before any network request (executable extension-reject scenario).")

	cell[FeatureCancellation] = lossless(FeatureCancellation,
		"Cancellation/backpressure surface through the transport: a canceled client context stops upstream work with no second attempt and no retry after visible output (executable cancellation scenario).")
	cell[FeatureFailover] = lossless(FeatureFailover,
		"Pre-output failover retains the complete requirement set and re-executes through the same canonical path (executable failover scenario with a failing primary and a succeeding candidate). The ACP connector classifies transport/HTTP 5xx/rate-limit/protocol failures before canonical output as recoverable pre-output while terminal auth/validation stays terminal.")
	cell[FeatureNoRetryVisibleOutput] = lossless(FeatureNoRetryVisibleOutput,
		"Single-terminal commitment is preserved; after the first visible output an abrupt upstream termination does not trigger retry or failover (executable no-retry scenario asserts the candidate origin receives zero requests).")
	return cell
}

// assistantMediaOutcome classifies the assistant media output feature for one
// general matrix cell.
//
// Backends that emit canonical EventAssistantImageRef/EventAssistantFileRef
// events from their native wire (openai-responses, anthropic, gemini) are
// positive: the executable assistant-media scenario drives the origin to emit
// assistant media and asserts the actual client wire carries the media
// reference with exactly one upstream request. Same-wire cells are lossless;
// cross-protocol cells preserve the reference through the target wire
// (projection).
//
// Backends whose wire cannot represent assistant media output (openai-legacy
// chat, bedrock Converse, ACP, and the openrouter/nvidia connectors, whose
// stream decoder surfaces no assistant media reference output on either the
// chat-completions or the Responses wire) cannot support the feature. The
// executable scenario drives a canonical assistant-media-ref call through the
// cell and asserts the executor rejects it before any network request — the
// origin observes zero upstream requests.
func assistantMediaOutcome(frontend, backend string) (CompatibilityOutcome, string) {
	switch backend {
	case BackendOpenAILegacy:
		return OutcomeRejectBeforeNet, "The OpenAI chat-completions wire has no assistant media reference output surface; the executable assistant-media scenario drives a canonical assistant-media-ref call and asserts it is rejected before any network request (zero upstream requests)."
	case BackendBedrock:
		return OutcomeRejectBeforeNet, "The Bedrock Converse wire has no assistant media reference output surface in the bundled mapper; the executable assistant-media scenario drives a canonical assistant-media-ref call and asserts it is rejected before any network request (zero upstream requests)."
	case BackendACP:
		return OutcomeRejectBeforeNet, "The ACP v1 prompt-turn subset has no assistant media reference output surface; the executable assistant-media scenario drives a canonical assistant-media-ref call and asserts it is rejected before any network request (zero upstream requests)."
	case BackendOpenRouter, BackendNVIDIA:
		return OutcomeRejectBeforeNet, "The real " + backend + " connector's stream decoder surfaces no assistant media reference output on either the chat-completions or the Responses wire it selects per operation; the executable assistant-media scenario drives a canonical assistant-media-ref call and asserts it is rejected before any network request (zero upstream requests)."
	}
	native := frontend == backend
	if native {
		return OutcomeLossless, "The native " + frontend + " ↔ " + backend + " wire carries assistant media references exactly; the executable assistant-media round trip asserts the client wire media reference with a single upstream request."
	}
	return OutcomeProjection, "Assistant media references project to the " + backend + " target wire preserving semantics; the executable assistant-media round trip asserts the client wire media reference with a single upstream request."
}

// assistantMediaFeature returns the release-ready feature evidence for the
// assistant media output feature of one general matrix cell. The scenario ID is
// always derived from the executable table (suffix assistant-media), never
// metadata-only.
func assistantMediaFeature(frontend, backend string) FeatureEvidence {
	outcome, rationale := assistantMediaOutcome(frontend, backend)
	ev := FeatureEvidence{
		Outcome:       outcome,
		TestArtifacts: matrix45ArtifactRef(),
		Rationale:     rationale,
	}
	if outcome != OutcomeOutOfScope {
		ev.ScenarioIDs = matrix45GeneralScenarioIDsByFeature(frontend, backend, FeatureAssistantMedia)
	}
	return ev
}

// generalContinuationOutOfScopeRationale returns the exact approved rationale
// for the out_of_scope continuation classification of a general cell frontend.
func generalContinuationOutOfScopeRationale(frontend string) string {
	switch frontend {
	case FrontendOpenAIResponses:
		return "The openai-responses frontend recognizes but does not materialize previous_response_id; proxy-owned continuation has no client-facing surface in this general cell (the OpenResponses continuation surface lives in the excluded OpenResponses row/column)."
	case FrontendOpenAILegacy:
		return "The openai-legacy chat API has no parent/previous-response concept, so proxy-owned continuation has no client-facing surface for this cell."
	case FrontendAnthropic:
		return "The Anthropic Messages API has no parent/previous-response concept, so proxy-owned continuation has no client-facing surface for this cell."
	case FrontendGemini:
		return "The Gemini generateContent API has no parent/previous-response concept, so proxy-owned continuation has no client-facing surface for this cell."
	default:
		return "This general cell frontend exposes no parent/previous-response concept, so proxy-owned continuation has no client-facing surface."
	}
}

// matrix45ScenarioIDValid accepts scenario IDs from any authoritative registry
// that owns a cell: the matrix45 executable table for general cells, the row
// prefix for the OpenResponses frontend row, or the column prefix for the
// OpenResponses backend column. General-cell IDs must belong to the exact cell.
func matrix45ScenarioIDValid(frontend, backend, sid string) bool {
	if sc, ok := generalMatrixScenarioByID(sid); ok {
		return sc.Frontend == frontend && sc.Backend == backend
	}
	if frontend == FrontendOpenResponses && strings.HasPrefix(sid, RowScenarioPrefix) {
		return true
	}
	if backend == BackendOpenResponses && strings.HasPrefix(sid, ColumnScenarioPrefix) {
		return true
	}
	return false
}

// RegisterMatrix45Scenarios registers every scenario ID the 45-cell registry
// links (row, column, and general matrix cells) so evidence scenario IDs are
// verifiable against the registry.
func RegisterMatrix45Scenarios(reg *testkitopenresponses.ScenarioRegistry) error {
	if err := RegisterOpenResponsesFrontendRowScenarios(reg); err != nil {
		return err
	}
	if err := RegisterOpenResponsesBackendColumnScenarios(reg); err != nil {
		return err
	}
	for _, cell := range GeneralMatrixCells() {
		for _, sid := range GeneralMatrixScenarioIDs(cell.Frontend, cell.Backend) {
			if err := reg.Register(testkitopenresponses.ScenarioDescriptor{
				ID:          sid,
				Kind:        testkitopenresponses.ScenarioKindJSONText,
				Description: "matrix45 general cell " + cell.Frontend + " × " + cell.Backend + " scenario " + sid,
			}); err != nil {
				return fmt.Errorf("register general scenario %s: %w", sid, err)
			}
		}
	}
	return nil
}
