package conformance

// Executable scenario table for the five OpenResponses backend column cells
// (spec Phase 8, Task 8.4).
//
// This table is the single source of truth for column-cell scenario IDs: the
// tagged integration test (openresponses_backend_column_scenarios_test.go)
// executes every entry through a real deployment with the independent
// OpenResponses refbackend injected as the reference-provider origin, and the
// evidence builder (openResponsesBackendColumnCellFor) derives the exact same
// scenario IDs from this table. No column scenario ID exists that is not an
// executable table entry.
//
// Features without an entry in OpenResponsesBackendColumnFeatureSuffixes are
// classified out_of_scope in the evidence builder with an explicit approved
// rationale and never claim test evidence.
//
// Continuation is positive only for the OpenResponses frontend cell (the
// proxy-owned continuation surface); every legacy column frontend has no
// client-facing previous-response surface, so continuation has no executable
// entry there and is classified out_of_scope. Cancellation/backpressure,
// failover, and no-retry-after-visible-output each have their own executable
// scenario so evidence never points at the generic json-text or
// usage-commitment proof.

// OpenResponsesBackendColumnFeatureSuffixes maps every column feature that has
// an executable proof to its scenario-ID suffix. Several features share one
// executable scenario (instructions/roles and history are proven by the
// json-text round trip), so they map to the same suffix.
func OpenResponsesBackendColumnFeatureSuffixes() map[FeatureID]string {
	return map[FeatureID]string{
		FeatureJSONText:             "json-text",
		FeatureSSEText:              "sse-text",
		FeatureInstructionsRoles:    "json-text",
		FeatureHistory:              "json-text",
		FeatureTools:                "tools",
		FeatureMultimodal:           "multimodal",
		FeatureUsageErrors:          "usage-commitment",
		FeatureReasoningReplay:      "replay-reject",
		FeatureAssistantPhase:       "phase-reject",
		FeatureItemReferences:       "itemref-reject",
		FeatureCompaction:           "compaction-reject",
		FeatureExtensions:           "extension-reject",
		FeatureContinuation:         "continuation",
		FeatureCancellation:         "cancellation",
		FeatureFailover:             "failover",
		FeatureNoRetryVisibleOutput: "no-retry",
	}
}

// columnFeatureSuffix returns the scenario-ID suffix for one column feature of
// one cell. The OpenResponses frontend cell declares the compaction capability,
// so its compaction scenario uses the positive "compaction" suffix; every other
// column cell rejects compaction before network and keeps the
// "compaction-reject" suffix so scenario-ID naming agrees with the evidence
// outcome.
func columnFeatureSuffix(frontend string, feat FeatureID) string {
	if feat == FeatureCompaction && frontend == FrontendOpenResponses {
		return "compaction"
	}
	return OpenResponsesBackendColumnFeatureSuffixes()[feat]
}

// OpenResponsesBackendColumnScenario is one executable proof for one feature of
// one column cell. ScenarioID is derived deterministically from the cell
// frontend and the feature's suffix.
type OpenResponsesBackendColumnScenario struct {
	Frontend   string
	Feature    FeatureID
	ScenarioID string
}

// columnContinuationViable reports whether a column frontend has a proxy-owned
// continuation surface. Only the OpenResponses frontend does; legacy frontends
// expose no client-facing previous-response concept (the openai-responses
// frontend recognizes but does not materialize previous_response_id).
func columnContinuationViable(frontend string) bool {
	return frontend == FrontendOpenResponses
}

// OpenResponsesBackendColumnScenarios returns the authoritative executable
// scenario table for the five column cells. The integration test and the
// evidence builder both consume this exact table. Continuation entries exist
// only for cells with a proxy-owned continuation surface.
func OpenResponsesBackendColumnScenarios() []OpenResponsesBackendColumnScenario {
	var out []OpenResponsesBackendColumnScenario
	for _, frontend := range OpenResponsesBackendColumnFrontendIDs() {
		for feat := range OpenResponsesBackendColumnFeatureSuffixes() {
			if feat == FeatureContinuation && !columnContinuationViable(frontend) {
				continue
			}
			out = append(out, OpenResponsesBackendColumnScenario{
				Frontend:   frontend,
				Feature:    feat,
				ScenarioID: columnScenarioID(frontend, columnFeatureSuffix(frontend, feat)),
			})
		}
	}
	return out
}

// columnScenarioIDsByFeature returns the scenario IDs a column cell links for
// one feature (normally exactly one); features without an executable proof
// return nil so out_of_scope evidence never claims a scenario.
func columnScenarioIDsByFeature(frontend string, feat FeatureID) []string {
	_, ok := OpenResponsesBackendColumnFeatureSuffixes()[feat]
	if !ok {
		return nil
	}
	if feat == FeatureContinuation && !columnContinuationViable(frontend) {
		return nil
	}
	return []string{columnScenarioID(frontend, columnFeatureSuffix(frontend, feat))}
}
