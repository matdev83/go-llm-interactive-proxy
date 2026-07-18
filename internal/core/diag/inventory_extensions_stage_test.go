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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
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

type invSecretGuard struct {
	id  string
	ord int
}

func (g invSecretGuard) ID() string                         { return g.id }
func (g invSecretGuard) Order() int                         { return g.ord }
func (invSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (invSecretGuard) Evaluate(context.Context, *lipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type invAttemptTransform struct {
	id  string
	ord int
}

func (t invAttemptTransform) ID() string                      { return t.id }
func (t invAttemptTransform) Order() int                      { return t.ord }
func (invAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (invAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type invStreamObserverFactory struct {
	id  string
	ord int
}

func (f invStreamObserverFactory) ID() string                      { return f.id }
func (f invStreamObserverFactory) Order() int                      { return f.ord }
func (invStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (invStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

func TestStageOccupancyFromBundle_nilAttemptAndStreamEntriesSkippedWithoutPanic(t *testing.T) {
	t.Parallel()
	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{
			nil,
			invAttemptTransform{id: "keep", ord: 1},
			nil,
		},
		StreamObserverFactories: []response.StreamObserverFactory{
			nil,
			invStreamObserverFactory{id: "keep", ord: 1},
			nil,
		},
	}
	occ := stageOccupancyFromBundle(b)
	var atOcc, soOcc *InventoryStageOccupancy
	for i := range occ {
		switch occ[i].StageID {
		case extensions.StageCandidateAttemptTransform:
			atOcc = &occ[i]
		case extensions.StageFinalStreamObservation:
			soOcc = &occ[i]
		}
	}
	if atOcc == nil || len(atOcc.HandlerIDs) != 1 || atOcc.HandlerIDs[0] != "attempt_transform:keep" {
		t.Fatalf("attempt occupancy=%#v", atOcc)
	}
	if soOcc == nil || len(soOcc.HandlerIDs) != 1 || soOcc.HandlerIDs[0] != "stream_observer:keep" {
		t.Fatalf("stream occupancy=%#v", soOcc)
	}
}

func TestStageOccupancyFromBundle_attemptTransformsAndStreamObservers(t *testing.T) {
	t.Parallel()
	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{
			invAttemptTransform{id: "z", ord: 2},
			invAttemptTransform{id: "a", ord: 1},
		},
		StreamObserverFactories: []response.StreamObserverFactory{
			invStreamObserverFactory{id: "z", ord: 2},
			invStreamObserverFactory{id: "a", ord: 1},
		},
	}
	occ := stageOccupancyFromBundle(b)
	var atOcc, soOcc *InventoryStageOccupancy
	for i := range occ {
		switch occ[i].StageID {
		case extensions.StageCandidateAttemptTransform:
			atOcc = &occ[i]
		case extensions.StageFinalStreamObservation:
			soOcc = &occ[i]
		}
	}
	if atOcc == nil {
		t.Fatal("missing candidate_attempt_transform occupancy")
	}
	wantAT := []string{"attempt_transform:a", "attempt_transform:z"}
	if len(atOcc.HandlerIDs) != len(wantAT) {
		t.Fatalf("attempt HandlerIDs=%#v", atOcc.HandlerIDs)
	}
	for i := range wantAT {
		if atOcc.HandlerIDs[i] != wantAT[i] {
			t.Fatalf("attempt idx %d want %q got %#v", i, wantAT[i], atOcc.HandlerIDs)
		}
	}
	if soOcc == nil {
		t.Fatal("missing final_stream_observation occupancy")
	}
	wantSO := []string{"stream_observer:a", "stream_observer:z"}
	if len(soOcc.HandlerIDs) != len(wantSO) {
		t.Fatalf("observer HandlerIDs=%#v", soOcc.HandlerIDs)
	}
	for i := range wantSO {
		if soOcc.HandlerIDs[i] != wantSO[i] {
			t.Fatalf("observer idx %d want %q got %#v", i, wantSO[i], soOcc.HandlerIDs)
		}
	}
	snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		AttemptTransforms:       b.AttemptTransforms,
		StreamObserverFactories: b.StreamObserverFactories,
	})
	if got := snap.AttemptTransforms(); len(got) != 2 || got[0].ID() != "a" || got[1].ID() != "z" {
		t.Fatalf("snapshot AttemptTransforms sort mismatch: %#v", got)
	}
	if got := snap.StreamObserverFactories(); len(got) != 2 || got[0].ID() != "a" || got[1].ID() != "z" {
		t.Fatalf("snapshot StreamObserverFactories sort mismatch: %#v", got)
	}
}

func TestStageOccupancyFromBundle_secretGuardsSortedWithPrefix(t *testing.T) {
	t.Parallel()
	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SecretGuards: []secretguard.Guard{
			invSecretGuard{id: "z", ord: 2},
			invSecretGuard{id: "a", ord: 1},
			invSecretGuard{id: "b", ord: 1},
		},
	}
	occ := stageOccupancyFromBundle(b)
	var guardOcc *InventoryStageOccupancy
	for i := range occ {
		if occ[i].StageID == extensions.StageSecretGuard {
			guardOcc = &occ[i]
			break
		}
	}
	if guardOcc == nil {
		t.Fatal("missing secret_guard occupancy")
		return
	}
	want := []string{"secret_guard:a", "secret_guard:b", "secret_guard:z"}
	if len(guardOcc.HandlerIDs) != len(want) {
		t.Fatalf("got %#v", guardOcc.HandlerIDs)
	}
	for i := range want {
		if guardOcc.HandlerIDs[i] != want[i] {
			t.Fatalf("idx %d want %q got %#v", i, want[i], guardOcc.HandlerIDs)
		}
	}
	if guardOcc.Count != len(want) {
		t.Fatalf("count %d want %d", guardOcc.Count, len(want))
	}
}
