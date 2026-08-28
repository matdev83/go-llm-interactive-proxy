package diag

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func freezeBundleForDiagTest(b lipfeature.FeatureBundle) lipfeature.FrozenPlaneSet {
	cs := lipfeature.NewContributionSet()
	if len(b.AttemptTransforms) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, "test", b.AttemptTransforms)
	}
	if len(b.StreamObserverFactories) > 0 {
		_ = lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, "test", b.StreamObserverFactories)
	}
	return cs.Freeze()
}

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
	at := []request.AttemptTransform{
		nil,
		invAttemptTransform{id: "keep", ord: 1},
		nil,
	}
	so := []response.StreamObserverFactory{
		nil,
		invStreamObserverFactory{id: "keep", ord: 1},
		nil,
	}

	atOccs := lipfeature.PlaneAttemptTransforms.MaterializeOccupants(at)
	if len(atOccs) != 1 || atOccs[0].Label != "attempt_transform:keep" {
		t.Fatalf("unexpected at occupants: %+v", atOccs)
	}

	soOccs := lipfeature.PlaneStreamObserverFactories.MaterializeOccupants(so)
	if len(soOccs) != 1 || soOccs[0].Label != "stream_observer:keep" {
		t.Fatalf("unexpected so occupants: %+v", soOccs)
	}

	proj := []lipfeature.DiagnosticPlaneProjection{
		{
			PlaneID:   lipfeature.PlaneAttemptTransforms.ID,
			StageID:   lipfeature.PlaneAttemptTransforms.Diagnostics.StageID,
			Order:     lipfeature.PlaneAttemptTransforms.Diagnostics.Order,
			Occupants: atOccs,
		},
		{
			PlaneID:   lipfeature.PlaneStreamObserverFactories.ID,
			StageID:   lipfeature.PlaneStreamObserverFactories.Diagnostics.StageID,
			Order:     lipfeature.PlaneStreamObserverFactories.Diagnostics.Order,
			Occupants: soOccs,
		},
	}
	occ, _, err := reduceDiagnosticProjections(proj)
	if err != nil {
		t.Fatalf("reduceDiagnosticProjections err: %v", err)
	}
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
		FeaturePlanes: freezeBundleForDiagTest(b),
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

func TestReduceDiagnosticProjections_DisposableProbePlane(t *testing.T) {
	t.Parallel()

	projections := []lipfeature.DiagnosticPlaneProjection{
		{
			PlaneID:       "disposable_probe",
			StageID:       extensions.StageToolEventReaction,
			CoalesceGroup: "probe_group",
			Order:         85,
			Occupants: []lipfeature.DiagnosticOccupant{
				{Label: "probe:alpha"},
			},
			Privileges: lipfeature.PrivilegeProjection{
				Flags: []string{lipfeature.PrivilegeAuxiliaryRequests},
			},
		},
		{
			PlaneID:       "tool_policies",
			StageID:       extensions.StageToolEventReaction,
			CoalesceGroup: "probe_group",
			Order:         85,
			Occupants: []lipfeature.DiagnosticOccupant{
				{Label: "tool_policy:beta"},
			},
		},
	}

	stageOcc, privs, err := reduceDiagnosticProjections(projections)
	require.NoError(t, err)
	require.Len(t, stageOcc, 1)
	assert.Equal(t, extensions.StageToolEventReaction, stageOcc[0].StageID)
	assert.Equal(t, 2, stageOcc[0].Count)
	assert.Equal(t, []string{"probe:alpha", "tool_policy:beta"}, stageOcc[0].HandlerIDs)
	assert.True(t, privs.AuxiliaryRequests)
	assert.False(t, privs.RawCapture)
	assert.False(t, privs.CompletionGate)
	assert.False(t, privs.AuthProvider)
}

func TestReduceDiagnosticProjections_UnknownPrivilegeFlagError(t *testing.T) {
	t.Parallel()

	projections := []lipfeature.DiagnosticPlaneProjection{
		{
			PlaneID: "bad_priv_plane",
			StageID: extensions.StagePreRequest,
			Order:   10,
			Privileges: lipfeature.PrivilegeProjection{
				Flags: []string{"unknown_priv_flag_xyz"},
			},
		},
	}

	_, _, err := reduceDiagnosticProjections(projections)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown privilege flag "unknown_priv_flag_xyz"`)
}

type mixedTestRegistry struct {
	bundles map[string]lipfeature.FeatureBundle
}

func (r *mixedTestRegistry) BuildFeatureBundle(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
	if b, ok := r.bundles[factoryKey]; ok {
		return b, nil
	}
	return lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}, nil
}

func TestBuildInventoryExtensions_ValidMixedBundleSortingAndNils(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "feat-mixed", Kind: "mixed", Enabled: true},
			},
		},
	}

	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SubmitHooks: []sdkhooks.SubmitHook{
			nil,
			charStubSubmitHook{id: "sub-z", ord: 20},
			charStubSubmitHook{id: "sub-a", ord: 10},
			nil,
		},
		ToolCallPolicies: []toolpolicy.Policy{
			nil,
			charStubToolPolicy{id: "pol-b", ord: 10},
			charStubToolPolicy{id: "pol-a", ord: 10},
			nil,
		},
		SessionOpeners: []session.Opener{
			nil,
			charStubSessionOpener{id: "opener-1"},
			nil,
		},
		AttemptTransforms: []request.AttemptTransform{
			charStubAttemptTransform{id: "att-z", ord: 20},
			charStubAttemptTransform{id: "att-a", ord: 10},
		},
		StreamObserverFactories: []response.StreamObserverFactory{
			charStubStreamObserverFactory{id: "so-z", ord: 20},
			charStubStreamObserverFactory{id: "so-a", ord: 10},
		},
	}

	reg := &mixedTestRegistry{
		bundles: map[string]lipfeature.FeatureBundle{
			"mixed": b,
		},
	}

	extras := &InventoryExtras{
		Reg:           reg,
		Registrations: config.RegistrationsFromConfig(cfg),
	}

	ext := buildInventoryExtensions(t.Context(), cfg, extras)
	require.Len(t, ext.Features, 1)
	f0 := ext.Features[0]
	require.Empty(t, f0.BundleError)
	require.NotEmpty(t, f0.StageOccupancy)

	// Verify sorting and nil filtering
	for _, occ := range f0.StageOccupancy {
		switch occ.StageID {
		case extensions.StageSubmit:
			assert.Equal(t, []string{"sub-a", "sub-z"}, occ.HandlerIDs)
		case extensions.StageToolEventReaction:
			assert.Equal(t, []string{"tool_policy:pol-a", "tool_policy:pol-b"}, occ.HandlerIDs)
		case extensions.StageSessionOpen:
			assert.Equal(t, []string{"opener:opener-1"}, occ.HandlerIDs)
		case extensions.StageCandidateAttemptTransform:
			assert.Equal(t, []string{"attempt_transform:att-a", "attempt_transform:att-z"}, occ.HandlerIDs)
		case extensions.StageFinalStreamObservation:
			assert.Equal(t, []string{"stream_observer:so-a", "stream_observer:so-z"}, occ.HandlerIDs)
		}
	}

	assert.True(t, ext.GenericPorts.AttemptTransformOccupied)
	assert.Equal(t, 2, ext.GenericPorts.AttemptTransformHandlers)
	assert.True(t, ext.GenericPorts.FinalStreamObservationOccupied)
	assert.Equal(t, 2, ext.GenericPorts.FinalStreamObservationHandlers)
}

func TestBuildInventoryExtensions_InvalidAttemptOrStreamBundleValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		bundle    lipfeature.FeatureBundle
		wantError string
	}{
		{
			name: "invalid attempt",
			bundle: lipfeature.FeatureBundle{
				SchemaVersion:     lipfeature.SchemaVersionV1,
				AttemptTransforms: []request.AttemptTransform{nil},
			},
			wantError: "feature: FeatureBundle: AttemptTransforms[0] must not be nil",
		},
		{
			name: "invalid stream observer",
			bundle: lipfeature.FeatureBundle{
				SchemaVersion:           lipfeature.SchemaVersionV1,
				StreamObserverFactories: []response.StreamObserverFactory{nil},
			},
			wantError: "feature: FeatureBundle: StreamObserverFactories[0] must not be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{
				Plugins: config.PluginsConfig{
					Features: []config.PluginConfig{
						{ID: "feat-invalid", Kind: "invalid-kind", Enabled: true},
					},
				},
			}

			reg := &mixedTestRegistry{
				bundles: map[string]lipfeature.FeatureBundle{
					"invalid-kind": tt.bundle,
				},
			}

			extras := &InventoryExtras{
				Reg:           reg,
				Registrations: config.RegistrationsFromConfig(cfg),
			}

			ext := buildInventoryExtensions(t.Context(), cfg, extras)
			require.Len(t, ext.Features, 1)
			f0 := ext.Features[0]
			require.Equal(t, tt.wantError, f0.BundleError)
			require.NotNil(t, f0.StageOccupancy)
			require.Len(t, f0.StageOccupancy, 0)
			require.Equal(t, InventoryPrivileges{}, f0.Privileges)
			require.Equal(t, InventoryGenericPorts{}, ext.GenericPorts)
			require.False(t, ext.GenericPorts.AttemptTransformOccupied)
			require.Equal(t, 0, ext.GenericPorts.AttemptTransformHandlers)
			require.False(t, ext.GenericPorts.FinalStreamObservationOccupied)
			require.Equal(t, 0, ext.GenericPorts.FinalStreamObservationHandlers)
		})
	}
}

type diagDummyLTHandler struct {
	id  string
	ord int
}

func (h diagDummyLTHandler) ID() string                      { return h.id }
func (h diagDummyLTHandler) Order() int                      { return h.ord }
func (diagDummyLTHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (diagDummyLTHandler) Match(context.Context, lipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{Claimed: false}, nil
}
func (diagDummyLTHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

func TestPlaneLocalTurnHandlers_OrderingAndMaterialize(t *testing.T) {
	t.Parallel()

	assert.Equal(t, lipfeature.StageIDPreRequest, lipfeature.PlaneLocalTurnHandlers.Diagnostics.StageID)
	assert.Equal(t, "", lipfeature.PlaneLocalTurnHandlers.Diagnostics.CoalesceGroup)

	var typedNil *diagDummyLTHandler
	handlers := []localturn.Handler{
		diagDummyLTHandler{id: "lt-z", ord: 20},
		nil,
		diagDummyLTHandler{id: "lt-a", ord: 10},
		typedNil,
		diagDummyLTHandler{id: "lt-m", ord: 10},
	}

	require.NotNil(t, lipfeature.PlaneLocalTurnHandlers.Diagnostics.Materialize)
	occupants := lipfeature.PlaneLocalTurnHandlers.Diagnostics.Materialize(handlers)
	require.Len(t, occupants, 3)
	assert.Equal(t, "local_turn:lt-a", occupants[0].Label)
	assert.Equal(t, "local_turn:lt-m", occupants[1].Label)
	assert.Equal(t, "local_turn:lt-z", occupants[2].Label)

	jsonBytes, err := json.Marshal(occupants)
	require.NoError(t, err)
	expectedJSON := `[{"Label":"local_turn:lt-a","PluginID":"","Privileges":null},{"Label":"local_turn:lt-m","PluginID":"","Privileges":null},{"Label":"local_turn:lt-z","PluginID":"","Privileges":null}]`
	assert.Equal(t, expectedJSON, string(jsonBytes))
}
