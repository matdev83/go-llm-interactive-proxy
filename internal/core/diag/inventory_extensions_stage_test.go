package diag

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"gopkg.in/yaml.v3"
)

type invPol struct {
	id  string
	ord int
}

func (p invPol) ID() string                        { return p.id }
func (p invPol) Order() int                        { return p.ord }
func (p invPol) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (p invPol) Handle(context.Context, lipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type invTR struct {
	id string
}

func (t invTR) ID() string { return t.id }

func (invTR) Order() int { return 0 }

func (invTR) HandleToolEvent(context.Context, lipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

func TestStageOccupancyFromBundle_toolPoliciesSortedBeforeReactorsStablePrefixes(t *testing.T) {
	t.Parallel()
	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolCallPolicies: []toolpolicy.Policy{
			invPol{id: "z", ord: 2},
			invPol{id: "a", ord: 1},
		},
		ToolReactors: []sdkhooks.ToolReactor{invTR{id: "react"}},
	}
	occ := stageOccupancyFromBundle(b)
	var reaction *InventoryStageOccupancy
	for i := range occ {
		if occ[i].StageID == extensions.StageToolEventReaction {
			reaction = &occ[i]
			break
		}
	}
	if reaction == nil {
		t.Fatal("missing tool_event_reaction occupancy")
		return
	}
	want := []string{"tool_policy:a", "tool_policy:z", "react"}
	if len(reaction.HandlerIDs) != len(want) {
		t.Fatalf("got %#v", reaction.HandlerIDs)
	}
	for i := range want {
		if reaction.HandlerIDs[i] != want[i] {
			t.Fatalf("idx %d want %q got %#v", i, want[i], reaction.HandlerIDs)
		}
	}
}

type stubTrafficObs struct{}

func (stubTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type stubUsageObs struct{}

func (stubUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

func TestStageOccupancyFromBundle_trafficObservationTrafficAndUsageObserverIndices(t *testing.T) {
	t.Parallel()
	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{},
			nil,
			stubTrafficObs{},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{},
			stubUsageObs{},
		},
	}
	occ := stageOccupancyFromBundle(b)
	var trafficOcc *InventoryStageOccupancy
	for i := range occ {
		if occ[i].StageID == extensions.StageTrafficObservation {
			trafficOcc = &occ[i]
			break
		}
	}
	if trafficOcc == nil {
		t.Fatal("missing traffic_observation occupancy")
		return
	}
	want := []string{"traffic_observer:0", "traffic_observer:2", "usage_observer:0", "usage_observer:1"}
	if len(trafficOcc.HandlerIDs) != len(want) {
		t.Fatalf("got %#v", trafficOcc.HandlerIDs)
	}
	for i := range want {
		if trafficOcc.HandlerIDs[i] != want[i] {
			t.Fatalf("idx %d want %q got %#v", i, want[i], trafficOcc.HandlerIDs)
		}
	}
}

type polOnlyRegistry struct{}

func (polOnlyRegistry) BuildFeatureBundle(string, yaml.Node) (lipfeature.FeatureBundle, error) {
	return lipfeature.FeatureBundle{
		SchemaVersion:    lipfeature.SchemaVersionV1,
		ToolCallPolicies: []toolpolicy.Policy{invPol{id: "solo-pol", ord: 0}},
	}, nil
}

func TestBuildInventoryExtensions_toolPoliciesWithoutTransformsLeavesAuxiliaryRequestsFalse(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "feat-pol-only", Enabled: true}},
		},
	}
	ext := buildInventoryExtensions(context.Background(), cfg, &InventoryExtras{
		Reg: polOnlyRegistry{},
		Registrations: []lipsdk.Registration{
			{Kind: lipsdk.PluginKindFeature, ID: "feat-pol-only", Enabled: true, FactoryKind: "any"},
		},
	})
	if len(ext.Features) != 1 {
		t.Fatalf("features %d", len(ext.Features))
	}
	f0 := ext.Features[0]
	if f0.BundleError != "" {
		t.Fatalf("bundle_error %s", f0.BundleError)
	}
	if f0.Privileges.AuxiliaryRequests {
		t.Fatal("tool-call policies alone must not imply auxiliary_requests privilege")
	}
	var sawPol bool
	for _, occ := range f0.StageOccupancy {
		if occ.StageID != extensions.StageToolEventReaction {
			continue
		}
		for _, hid := range occ.HandlerIDs {
			if hid == "tool_policy:solo-pol" {
				sawPol = true
			}
		}
	}
	if !sawPol {
		t.Fatalf("missing tool_policy inventory tag in occupancy %#v", f0.StageOccupancy)
	}
}
