package conformance_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/conformance"
	testkitopenresponses "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/openresponses"
)

// TestOpenResponsesFrontendRow_EvidenceReleaseReady is the Task 8.3 row evidence
// validator. It fails when any of the nine row cells has a missing, planned,
// unclassified, or unlinked feature outcome, or links a scenario ID that is not
// registered or a test artifact that does not exist on disk.
func TestOpenResponsesFrontendRow_EvidenceReleaseReady(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	if err := conformance.ValidateOpenResponsesFrontendRow(root); err != nil {
		t.Fatalf("OpenResponses frontend row evidence is not release-ready: %v", err)
	}
}

// TestOpenResponsesFrontendRow_NineCellsNoMoreNoLess pins the Task 8.3 row to
// exactly the nine target cells: legacy OpenAI Chat, OpenAI Responses, ACP,
// Anthropic, Gemini/Vertex, Bedrock, OpenResponses-compatible, OpenRouter, and
// NVIDIA. It must not silently grow (Task 8.5 owns the authoritative matrix
// expansion).
func TestOpenResponsesFrontendRow_NineCellsNoMoreNoLess(t *testing.T) {
	t.Parallel()
	got := conformance.OpenResponsesFrontendRowBackendIDs()
	want := []string{
		conformance.BackendOpenAILegacy,
		conformance.BackendOpenAIResponses,
		conformance.BackendACP,
		conformance.BackendAnthropic,
		conformance.BackendGemini,
		conformance.BackendBedrock,
		conformance.BackendOpenResponses,
		conformance.BackendOpenRouter,
		conformance.BackendNVIDIA,
	}
	if len(got) != 9 {
		t.Fatalf("row backend count = %d, want exactly 9 (Task 8.3)", len(got))
	}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("row backend[%d] = %q, want %q", i, got[i], id)
		}
	}
}

// TestOpenResponsesFrontendRow_ScenarioRegistration proves every evidence
// scenario ID is registered in the scenario registry (no unverifiable links).
func TestOpenResponsesFrontendRow_ScenarioRegistration(t *testing.T) {
	t.Parallel()
	reg := testkitopenresponses.NewScenarioRegistry()
	if err := conformance.RegisterOpenResponsesFrontendRowScenarios(reg); err != nil {
		t.Fatalf("register row scenarios: %v", err)
	}
	for _, cell := range conformance.OpenResponsesFrontendRow() {
		for feat, ev := range cell.Features {
			for _, sid := range ev.ScenarioIDs {
				if _, ok := reg.Get(sid); !ok {
					t.Fatalf("row cell %s feature %q links unregistered scenario %q", cell.Backend, feat, sid)
				}
			}
		}
	}
}

// TestOpenResponsesFrontendRow_OptionalConnectorsStayOptional proves the row did
// not silently promote the OpenRouter/NVIDIA connector columns to essential
// status: they remain absent from the essential backend kinds and the connector
// executables are the evidence path (Task 8.5 owns any authoritative list
// expansion).
func TestOpenResponsesFrontendRow_OptionalConnectorsStayOptional(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	if err := conformance.AssertOpenRouterNVIDIAStayOptional(root); err != nil {
		t.Fatalf("optional connector columns must stay optional: %v", err)
	}
}
