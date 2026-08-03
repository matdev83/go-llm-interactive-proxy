package conformance_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/conformance"
	testkitopenresponses "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/openresponses"
)

// TestOpenResponsesBackendColumn_EvidenceReleaseReady is the Task 8.4 column
// evidence validator. It fails when any of the five column cells has a missing,
// planned, unclassified, or unlinked feature outcome, or links a scenario ID
// that is not registered or a test artifact that does not exist on disk.
func TestOpenResponsesBackendColumn_EvidenceReleaseReady(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	if err := conformance.ValidateOpenResponsesBackendColumn(root); err != nil {
		t.Fatalf("OpenResponses backend column evidence is not release-ready: %v", err)
	}
}

// TestOpenResponsesBackendColumn_FiveCellsNoMoreNoLess pins the Task 8.4 column
// to exactly the five target frontend families: legacy OpenAI Chat, OpenAI
// Responses, Anthropic, Gemini/Vertex, and OpenResponses. It must not silently
// grow (Task 8.5 owns the authoritative matrix expansion).
func TestOpenResponsesBackendColumn_FiveCellsNoMoreNoLess(t *testing.T) {
	t.Parallel()
	got := conformance.OpenResponsesBackendColumnFrontendIDs()
	want := []string{
		conformance.FrontendOpenAILegacy,
		conformance.FrontendOpenAIResponses,
		conformance.FrontendAnthropic,
		conformance.FrontendGemini,
		conformance.FrontendOpenResponses,
	}
	if len(got) != 5 {
		t.Fatalf("column frontend count = %d, want exactly 5 (Task 8.4)", len(got))
	}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("column frontend[%d] = %q, want %q", i, got[i], id)
		}
	}
}

// TestOpenResponsesBackendColumn_ScenarioRegistration proves every evidence
// scenario ID is registered in the scenario registry (no unverifiable links).
func TestOpenResponsesBackendColumn_ScenarioRegistration(t *testing.T) {
	t.Parallel()
	reg := testkitopenresponses.NewScenarioRegistry()
	if err := conformance.RegisterOpenResponsesBackendColumnScenarios(reg); err != nil {
		t.Fatalf("register column scenarios: %v", err)
	}
	for _, cell := range conformance.OpenResponsesBackendColumn() {
		for feat, ev := range cell.Features {
			for _, sid := range ev.ScenarioIDs {
				if _, ok := reg.Get(sid); !ok {
					t.Fatalf("column cell %s feature %q links unregistered scenario %q", cell.Frontend, feat, sid)
				}
			}
		}
	}
}
