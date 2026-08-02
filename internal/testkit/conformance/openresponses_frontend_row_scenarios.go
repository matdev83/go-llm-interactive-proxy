package conformance

// Executable scenario table for the nine OpenResponses frontend row cells (spec
// Phase 8, Task 8.3).
//
// This table is the single source of truth for row-cell scenario IDs: the
// tagged integration test (openresponses_frontend_row_scenarios_test.go)
// executes every entry through a real deployment, and the evidence builder
// (openResponsesFrontendRowCellFor) derives the exact same scenario IDs from
// this table. No row scenario ID exists that is not an executable table entry.
//
// Features without an entry in OpenResponsesFrontendRowFeatureSuffixes are
// classified out_of_scope in the evidence builder with an explicit approved
// rationale and never claim test evidence.
//
// Continuation, cancellation/backpressure, failover, and
// no-retry-after-visible-output each have their own executable scenario so
// evidence never points at the generic json-text or usage-commitment proof.

// OpenResponsesFrontendRowFeatureSuffixes maps every row feature that has an
// executable proof to its scenario-ID suffix. Several features share one
// executable scenario (instructions/roles and history are proven by the
// json-text round trip), so they map to the same suffix.
func OpenResponsesFrontendRowFeatureSuffixes() map[FeatureID]string {
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

// OpenResponsesFrontendRowScenario is one executable proof for one feature of
// one row cell. ScenarioID is derived deterministically from the cell backend
// and the feature's suffix.
type OpenResponsesFrontendRowScenario struct {
	Backend    string
	Feature    FeatureID
	ScenarioID string
}

// OpenResponsesFrontendRowScenarios returns the authoritative executable
// scenario table for the nine row cells. The integration test and the evidence
// builder both consume this exact table.
func OpenResponsesFrontendRowScenarios() []OpenResponsesFrontendRowScenario {
	var out []OpenResponsesFrontendRowScenario
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		for feat, suffix := range OpenResponsesFrontendRowFeatureSuffixes() {
			out = append(out, OpenResponsesFrontendRowScenario{
				Backend:    backend,
				Feature:    feat,
				ScenarioID: rowScenarioID(backend, suffix),
			})
		}
	}
	return out
}

// rowScenarioIDsByFeature returns the scenario IDs a row cell links for one
// feature (normally exactly one); features without an executable proof return
// nil so out_of_scope evidence never claims a scenario.
func rowScenarioIDsByFeature(backend string, feat FeatureID) []string {
	suffix, ok := OpenResponsesFrontendRowFeatureSuffixes()[feat]
	if !ok {
		return nil
	}
	return []string{rowScenarioID(backend, suffix)}
}
