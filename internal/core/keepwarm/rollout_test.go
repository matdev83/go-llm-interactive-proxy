package keepwarm

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

func TestUnprovenProviderRolloutRequiresIndependentEvidence(t *testing.T) {
	providers := []string{"codex-subscription", "openai-minimum-residency", "gemini-implicit", "deepseek", "xai", "mistral", "openrouter", "zai", "aggregator"}
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			gate := EvidenceGate{SafeControl: true, AffinityPreserved: true, ForegroundIsolationProven: true}
			if gate.ActiveRenewalSupported() {
				t.Fatal("cache-effect evidence was omitted from active-renewal gate")
			}
		})
	}
}

func TestProviderNeutralSchedulerDoesNotInventExpiryForUnprovenLifetimes(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.HeuristicOverrides = nil
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	for _, lifecycle := range []promptcache.LifecycleKind{promptcache.LifecycleMinimumResidency, promptcache.LifecycleBestEffort, promptcache.LifecycleUnknown} {
		o := testObservation(now, lifecycle, 0)
		o.Timing.ExpiresAt = nil
		result := armTestTarget(t, m, &fixedResultController{}, o)
		if result.Armed {
			t.Fatalf("lifecycle %q acquired an invented schedule: %+v", lifecycle, result)
		}
	}
}

func TestOnlyExplicitHeuristicCanScheduleWithoutExpiry(t *testing.T) {
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.HeuristicOverrides = []HeuristicOverride{{BackendInstance: "backend", CanonicalModel: "model", Interval: time.Minute}}
	m, err := NewManager(cfg, &testClock{now: now}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	o := testObservation(now, promptcache.LifecycleBestEffort, 0)
	result := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: []lipapi.ToolEvent{{Kind: lipapi.ToolEventFinished, Category: lipapi.ToolCategoryOSCommand}}, Observations: []promptcache.Observation{o}, BackendInstanceID: "backend", CanonicalModelID: "model", Controller: &fixedResultController{}})
	if !result.Armed {
		t.Fatalf("explicit operator heuristic was not honored: %+v", result)
	}
}
