package conformance

import (
	"testing"
)

// TestOpenResponsesFrontendRow_ScenarioTableIsExecutable proves the row evidence
// scenario IDs are exactly the executable table entries (no metadata-only
// scenario IDs): the table is non-empty, every entry references a known row
// cell and feature, and every scenario ID is distinct per cell.
func TestOpenResponsesFrontendRow_ScenarioTableIsExecutable(t *testing.T) {
	t.Parallel()
	table := OpenResponsesFrontendRowScenarios()
	if len(table) == 0 {
		t.Fatal("row scenario table is empty")
	}
	backends := map[string]struct{}{}
	for _, id := range OpenResponsesFrontendRowBackendIDs() {
		backends[id] = struct{}{}
	}
	for _, sc := range table {
		if _, ok := backends[sc.Backend]; !ok {
			t.Fatalf("row scenario %s references unknown backend %q", sc.ScenarioID, sc.Backend)
		}
		if _, ok := OpenResponsesFrontendRowFeatureSuffixes()[sc.Feature]; !ok {
			t.Fatalf("row scenario %s references feature %q without a suffix", sc.ScenarioID, sc.Feature)
		}
		if sc.ScenarioID == "" {
			t.Fatalf("row scenario for backend %s feature %q has an empty scenario ID", sc.Backend, sc.Feature)
		}
	}
	// Evidence scenario IDs must be exactly the table-derived set.
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		cell := openResponsesFrontendRowCellFor(backend)
		for feat, ev := range cell.Features {
			for _, sid := range ev.ScenarioIDs {
				found := false
				for _, sc := range table {
					if sc.ScenarioID == sid {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("row cell %s feature %q links scenario %q outside the executable table", backend, feat, sid)
				}
			}
		}
	}
}

// TestOpenResponsesBackendColumn_ScenarioTableIsExecutable proves the column
// evidence scenario IDs are exactly the executable table entries (no
// metadata-only scenario IDs): the table is non-empty, every entry references a
// known column cell and feature, and continuation entries exist only where the
// proxy-owned continuation surface is viable.
func TestOpenResponsesBackendColumn_ScenarioTableIsExecutable(t *testing.T) {
	t.Parallel()
	table := OpenResponsesBackendColumnScenarios()
	if len(table) == 0 {
		t.Fatal("column scenario table is empty")
	}
	frontends := map[string]struct{}{}
	for _, id := range OpenResponsesBackendColumnFrontendIDs() {
		frontends[id] = struct{}{}
	}
	for _, sc := range table {
		if _, ok := frontends[sc.Frontend]; !ok {
			t.Fatalf("column scenario %s references unknown frontend %q", sc.ScenarioID, sc.Frontend)
		}
		if _, ok := OpenResponsesBackendColumnFeatureSuffixes()[sc.Feature]; !ok {
			t.Fatalf("column scenario %s references feature %q without a suffix", sc.ScenarioID, sc.Feature)
		}
		if sc.ScenarioID == "" {
			t.Fatalf("column scenario for frontend %s feature %q has an empty scenario ID", sc.Frontend, sc.Feature)
		}
	}
	for _, frontend := range OpenResponsesBackendColumnFrontendIDs() {
		cell := openResponsesBackendColumnCellFor(frontend)
		linked := map[string]struct{}{}
		for feat, ev := range cell.Features {
			for _, sid := range ev.ScenarioIDs {
				found := false
				for _, sc := range table {
					if sc.ScenarioID == sid {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("column cell %s feature %q links scenario %q outside the executable table", frontend, feat, sid)
				}
				linked[sid] = struct{}{}
			}
		}
		// Every table entry for a cell must be linked by its evidence.
		for _, sc := range table {
			if sc.Frontend != frontend {
				continue
			}
			if _, ok := linked[sc.ScenarioID]; !ok {
				t.Fatalf("column cell %s executable scenario %q (feature %q) is not linked by any evidence feature", frontend, sc.ScenarioID, sc.Feature)
			}
		}
	}
}

// TestOpenResponsesFrontendRow_ContinuationHonestClassification pins the honest
// row classification: continuation is positive for every cell whose backend can
// replay the materialized trajectory (proxy-owned), and the ACP v1 prompt-turn
// subset honestly rejects the materialized trajectory before any network
// request. No row cell may reuse a generic json-text proof.
func TestOpenResponsesFrontendRow_ContinuationHonestClassification(t *testing.T) {
	t.Parallel()
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		cell := openResponsesFrontendRowCellFor(backend)
		ev, ok := cell.Features[FeatureContinuation]
		if !ok {
			t.Fatalf("row cell %s has no continuation evidence", backend)
		}
		if backend == BackendACP {
			if ev.Outcome != OutcomeRejectBeforeNet {
				t.Fatalf("row cell %s continuation = %q, want rejected_before_network (materialized ACP reasoning trajectory cannot be replayed)", backend, ev.Outcome)
			}
			if len(ev.ScenarioIDs) == 0 {
				t.Fatalf("row cell %s continuation reject links no executable scenario", backend)
			}
			for _, sid := range ev.ScenarioIDs {
				if !hasSuffix(sid, "-continuation") {
					t.Fatalf("row cell %s continuation reject links generic scenario %q, want the executable -continuation scenario", backend, sid)
				}
			}
			continue
		}
		if ev.Outcome == OutcomeOutOfScope || ev.Outcome == OutcomeRejectBeforeNet {
			t.Fatalf("row cell %s continuation = %q, want positive (proxy-owned continuation)", backend, ev.Outcome)
		}
		for _, sid := range ev.ScenarioIDs {
			if !hasSuffix(sid, "-continuation") {
				t.Fatalf("row cell %s continuation links generic scenario %q, want the executable -continuation scenario", backend, sid)
			}
		}
	}
}

// TestOpenResponsesBackendColumn_ContinuationHonestClassification pins the
// honest column classification: only the OpenResponses frontend cell claims
// positive continuation; every legacy column frontend classifies continuation
// out_of_scope with an exact rationale and no scenario link.
func TestOpenResponsesBackendColumn_ContinuationHonestClassification(t *testing.T) {
	t.Parallel()
	for _, frontend := range OpenResponsesBackendColumnFrontendIDs() {
		cell := openResponsesBackendColumnCellFor(frontend)
		ev, ok := cell.Features[FeatureContinuation]
		if !ok {
			t.Fatalf("column cell %s has no continuation evidence", frontend)
		}
		if frontend == FrontendOpenResponses {
			if ev.Outcome == OutcomeOutOfScope {
				t.Fatalf("column cell %s continuation = out_of_scope, want positive (proxy-owned continuation)", frontend)
			}
			for _, sid := range ev.ScenarioIDs {
				if !hasSuffix(sid, "-continuation") {
					t.Fatalf("column cell %s continuation links generic scenario %q, want the executable -continuation scenario", frontend, sid)
				}
			}
			continue
		}
		if ev.Outcome != OutcomeOutOfScope {
			t.Fatalf("column cell %s continuation = %q, want out_of_scope (no client-facing previous-response surface)", frontend, ev.Outcome)
		}
		if len(ev.ScenarioIDs) != 0 {
			t.Fatalf("column cell %s continuation out_of_scope links scenario IDs %v", frontend, ev.ScenarioIDs)
		}
		if ev.Rationale == "" {
			t.Fatalf("column cell %s continuation out_of_scope has an empty rationale", frontend)
		}
	}
}

// TestOpenResponsesRowColumn_CompactionNamingOutcomeAgreement pins the
// compaction scenario-ID naming to the evidence outcome: the OpenResponses
// backend cell in the row and the OpenResponses frontend cell in the column are
// the documented positive compaction exceptions and must link the positive
// "-compaction" scenario; every reject cell must link the "-compaction-reject"
// scenario. Scenario-ID naming and outcome must never disagree.
func TestOpenResponsesRowColumn_CompactionNamingOutcomeAgreement(t *testing.T) {
	t.Parallel()
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		cell := openResponsesFrontendRowCellFor(backend)
		ev, ok := cell.Features[FeatureCompaction]
		if !ok {
			t.Fatalf("row cell %s has no compaction evidence", backend)
		}
		if backend == BackendOpenResponses {
			if ev.Outcome != OutcomeLossless {
				t.Fatalf("row cell %s compaction = %q, want lossless (generic backend declares the compaction capability)", backend, ev.Outcome)
			}
			for _, sid := range ev.ScenarioIDs {
				if !hasSuffix(sid, "-compaction") {
					t.Fatalf("row cell %s compaction links generic scenario %q, want the positive -compaction scenario", backend, sid)
				}
				if hasSuffix(sid, "-compaction-reject") {
					t.Fatalf("row cell %s compaction links reject scenario %q for a positive outcome", backend, sid)
				}
			}
			continue
		}
		if ev.Outcome != OutcomeRejectBeforeNet {
			t.Fatalf("row cell %s compaction = %q, want rejected_before_network", backend, ev.Outcome)
		}
		for _, sid := range ev.ScenarioIDs {
			if !hasSuffix(sid, "-compaction-reject") {
				t.Fatalf("row cell %s compaction links scenario %q, want the -compaction-reject scenario", backend, sid)
			}
		}
	}
	for _, frontend := range OpenResponsesBackendColumnFrontendIDs() {
		cell := openResponsesBackendColumnCellFor(frontend)
		ev, ok := cell.Features[FeatureCompaction]
		if !ok {
			t.Fatalf("column cell %s has no compaction evidence", frontend)
		}
		if frontend == FrontendOpenResponses {
			if ev.Outcome != OutcomeLossless {
				t.Fatalf("column cell %s compaction = %q, want lossless (generic backend declares the compaction capability)", frontend, ev.Outcome)
			}
			for _, sid := range ev.ScenarioIDs {
				if !hasSuffix(sid, "-compaction") {
					t.Fatalf("column cell %s compaction links generic scenario %q, want the positive -compaction scenario", frontend, sid)
				}
				if hasSuffix(sid, "-compaction-reject") {
					t.Fatalf("column cell %s compaction links reject scenario %q for a positive outcome", frontend, sid)
				}
			}
			continue
		}
		if ev.Outcome != OutcomeRejectBeforeNet {
			t.Fatalf("column cell %s compaction = %q, want rejected_before_network", frontend, ev.Outcome)
		}
		for _, sid := range ev.ScenarioIDs {
			if !hasSuffix(sid, "-compaction-reject") {
				t.Fatalf("column cell %s compaction links scenario %q, want the -compaction-reject scenario", frontend, sid)
			}
		}
	}
}

// TestOpenResponsesRowColumn_TransportEvidenceLinksExecutableScenarios pins the
// F3 repair: continuation, cancellation, failover, and no-retry evidence in the
// row and column never point at the generic json-text or usage-commitment
// scenarios; they link the dedicated executable -continuation, -cancellation,
// -failover, and -no-retry scenarios instead.
func TestOpenResponsesRowColumn_TransportEvidenceLinksExecutableScenarios(t *testing.T) {
	t.Parallel()
	check := func(cellName string, ev FeatureEvidence, wantSuffix string) {
		t.Helper()
		if ev.Outcome == OutcomeOutOfScope {
			if wantSuffix == "continuation" {
				return
			}
			t.Fatalf("%s links %q out_of_scope but transport feature requires an executable scenario", cellName, wantSuffix)
		}
		if len(ev.ScenarioIDs) == 0 {
			t.Fatalf("%s %q has no scenario IDs", cellName, wantSuffix)
		}
		for _, sid := range ev.ScenarioIDs {
			if !hasSuffix(sid, "-"+wantSuffix) {
				t.Fatalf("%s %q links generic scenario %q, want the executable -%s scenario", cellName, wantSuffix, sid, wantSuffix)
			}
		}
	}
	for _, backend := range OpenResponsesFrontendRowBackendIDs() {
		cell := openResponsesFrontendRowCellFor(backend)
		for feat, suffix := range map[FeatureID]string{
			FeatureContinuation:         "continuation",
			FeatureCancellation:         "cancellation",
			FeatureFailover:             "failover",
			FeatureNoRetryVisibleOutput: "no-retry",
		} {
			check("row "+backend, cell.Features[feat], suffix)
		}
	}
	for _, frontend := range OpenResponsesBackendColumnFrontendIDs() {
		cell := openResponsesBackendColumnCellFor(frontend)
		for feat, suffix := range map[FeatureID]string{
			FeatureContinuation:         "continuation",
			FeatureCancellation:         "cancellation",
			FeatureFailover:             "failover",
			FeatureNoRetryVisibleOutput: "no-retry",
		} {
			check("column "+frontend, cell.Features[feat], suffix)
		}
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
