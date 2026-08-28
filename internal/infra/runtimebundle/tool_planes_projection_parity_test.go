package runtimebundle

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stubs for Tool Planes Testing ---

type toolTestFilter struct {
	id     string
	ord    int
	mode   sdkhooks.FailureMode
	err    error
	events *[]string
	mu     *sync.Mutex
}

func (f toolTestFilter) ID() string                        { return f.id }
func (f toolTestFilter) Order() int                        { return f.ord }
func (f toolTestFilter) FailureMode() sdkhooks.FailureMode { return f.mode }
func (f toolTestFilter) Handle(ctx context.Context, call *lipapi.Call, meta toolcatalog.CatalogMeta, svc toolcatalog.Services) error {
	if f.mu != nil && f.events != nil {
		f.mu.Lock()
		*f.events = append(*f.events, f.id)
		f.mu.Unlock()
	}
	return f.err
}

type toolTestPolicy struct {
	id       string
	ord      int
	mode     sdkhooks.FailureMode
	decision toolpolicy.Decision
	err      error
	events   *[]string
	mu       *sync.Mutex
}

func (p toolTestPolicy) ID() string                        { return p.id }
func (p toolTestPolicy) Order() int                        { return p.ord }
func (p toolTestPolicy) FailureMode() sdkhooks.FailureMode { return p.mode }
func (p toolTestPolicy) Handle(ctx context.Context, ev lipapi.ToolEvent, meta toolpolicy.Meta, svc toolpolicy.Services) (toolpolicy.Decision, error) {
	if p.mu != nil && p.events != nil {
		p.mu.Lock()
		*p.events = append(*p.events, p.id)
		p.mu.Unlock()
	}
	if p.err != nil {
		return toolpolicy.DecisionDeny, p.err
	}
	return p.decision, nil
}

type toolTestReactor struct {
	id  string
	ord int
}

func (r toolTestReactor) ID() string { return r.id }
func (r toolTestReactor) Order() int { return r.ord }
func (toolTestReactor) HandleToolEvent(context.Context, lipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

type toolTestFinalizer struct {
	id  string
	ord int
}

func (f toolTestFinalizer) ID() string { return f.id }
func (f toolTestFinalizer) Order() int { return f.ord }
func (toolTestFinalizer) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

// --- Parity, Ordering, and Snapshot Projection Tests ---

func TestToolPlanesProjection_ParityWithFrozenAndRegistrationOrder(t *testing.T) {
	t.Parallel()

	var events []string
	var mu sync.Mutex

	b1 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolCatalogFilters: []toolcatalog.Filter{
			toolTestFilter{id: "filter-b1-20", ord: 20, events: &events, mu: &mu},
			toolTestFilter{id: "filter-b1-10", ord: 10, events: &events, mu: &mu},
		},
		ToolCallPolicies: []toolpolicy.Policy{
			toolTestPolicy{id: "pol-b1-20", ord: 20, decision: toolpolicy.DecisionAllow, events: &events, mu: &mu},
			toolTestPolicy{id: "pol-b1-10", ord: 10, decision: toolpolicy.DecisionAllow, events: &events, mu: &mu},
		},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolCatalogFilters: []toolcatalog.Filter{
			toolTestFilter{id: "filter-b2-5", ord: 5, events: &events, mu: &mu},
		},
		ToolCallPolicies: []toolpolicy.Policy{
			toolTestPolicy{id: "pol-b2-5", ord: 5, decision: toolpolicy.DecisionAllow, events: &events, mu: &mu},
		},
	}

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	// 1. Verify Frozen plane contents preserve registration order
	frozenFilters := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCatalogFilters)
	require.Len(t, frozenFilters, 3)
	assert.Equal(t, "filter-b1-20", frozenFilters[0].ID())
	assert.Equal(t, "filter-b1-10", frozenFilters[1].ID())
	assert.Equal(t, "filter-b2-5", frozenFilters[2].ID())

	frozenPolicies := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallPolicies)
	require.Len(t, frozenPolicies, 3)
	assert.Equal(t, "pol-b1-20", frozenPolicies[0].ID())
	assert.Equal(t, "pol-b1-10", frozenPolicies[1].ID())
	assert.Equal(t, "pol-b2-5", frozenPolicies[2].ID())

	// 2. Build runtime snapshot from FeaturePlanes directly
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{
		FeaturePlanes: gen.Frozen,
	}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
	require.NotNil(t, snap)

	// 3. Snapshot ToolCatalogFilters returns defensive copy of raw filters
	snapFilters := snap.ToolCatalogFilters()
	require.Len(t, snapFilters, 3)
	assert.Equal(t, "filter-b1-20", snapFilters[0].ID())
	assert.Equal(t, "filter-b1-10", snapFilters[1].ID())
	assert.Equal(t, "filter-b2-5", snapFilters[2].ID())

	// 4. Snapshot ToolCallPolicies returns sorted defensive copy (MaterializeSorted order: 5, 10, 20)
	snapPolicies := snap.ToolCallPolicies()
	require.Len(t, snapPolicies, 3)
	assert.Equal(t, "pol-b2-5", snapPolicies[0].ID())
	assert.Equal(t, "pol-b1-10", snapPolicies[1].ID())
	assert.Equal(t, "pol-b1-20", snapPolicies[2].ID())

	// 5. ToolCallPoliciesExecution returns backing slice in MaterializeSorted order
	execPolicies := snap.ToolCallPoliciesExecution()
	require.Len(t, execPolicies, 3)
	assert.Equal(t, "pol-b2-5", execPolicies[0].ID())
	assert.Equal(t, "pol-b1-10", execPolicies[1].ID())
	assert.Equal(t, "pol-b1-20", execPolicies[2].ID())
}

func TestToolPlanesProjection_NilAndEmptySlicePreservation(t *testing.T) {
	t.Parallel()

	t.Run("nil_contributions", func(t *testing.T) {
		t.Parallel()
		b := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
		}
		gen, err := featurebundle.MergeBundlesGenerated(b)
		require.NoError(t, err)

		filters := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCatalogFilters)
		assert.Nil(t, filters)

		policies := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallPolicies)
		assert.Nil(t, policies)

		opts := &BuildOptions{
			FeaturePlanes: gen.Frozen,
		}
		snap := buildRuntimeSnapshot(nil, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
		require.NotNil(t, snap)
		assert.Empty(t, snap.ToolCatalogFilters())
		assert.Empty(t, snap.ToolCallPolicies())
		assert.Empty(t, snap.ToolCallPoliciesExecution())
	})

	t.Run("explicitly_empty_contributions", func(t *testing.T) {
		t.Parallel()
		cs := lipfeature.NewContributionSet()
		err := lipfeature.Contribute(cs, lipfeature.PlaneToolCatalogFilters, "plugin-1", []toolcatalog.Filter{})
		require.NoError(t, err)
		err = lipfeature.Contribute(cs, lipfeature.PlaneToolCallPolicies, "plugin-1", []toolpolicy.Policy{})
		require.NoError(t, err)

		frozen := cs.Freeze()
		filters := lipfeature.Get(frozen, lipfeature.PlaneToolCatalogFilters)
		assert.NotNil(t, filters)
		assert.Empty(t, filters)

		policies := lipfeature.Get(frozen, lipfeature.PlaneToolCallPolicies)
		assert.NotNil(t, policies)
		assert.Empty(t, policies)

		opts := &BuildOptions{
			FeaturePlanes: frozen,
		}
		snap := buildRuntimeSnapshot(nil, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
		require.NotNil(t, snap)
		assert.NotNil(t, snap.ToolCatalogFilters())
		assert.Empty(t, snap.ToolCatalogFilters())
		assert.Nil(t, snap.ToolCallPolicies())
		assert.Empty(t, snap.ToolCallPolicies())
	})
}

func TestToolPlanesProjection_DefensiveCopyingAndBackingSlice(t *testing.T) {
	t.Parallel()

	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolCatalogFilters: []toolcatalog.Filter{
			toolTestFilter{id: "f1", ord: 1},
		},
		ToolCallPolicies: []toolpolicy.Policy{
			toolTestPolicy{id: "p1", ord: 1, decision: toolpolicy.DecisionAllow},
		},
	}

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)

	opts := &BuildOptions{
		FeaturePlanes: gen.Frozen,
	}
	snap := buildRuntimeSnapshot(nil, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
	require.NotNil(t, snap)

	// 1. ToolCatalogFilters defensive copy: mutating copy does not affect subsequent calls
	fCopy1 := snap.ToolCatalogFilters()
	require.Len(t, fCopy1, 1)
	fCopy1[0] = toolTestFilter{id: "mutated"}

	fCopy2 := snap.ToolCatalogFilters()
	require.Len(t, fCopy2, 1)
	assert.Equal(t, "f1", fCopy2[0].ID())

	// 2. ToolCallPolicies defensive copy: mutating copy does not affect subsequent calls
	pCopy1 := snap.ToolCallPolicies()
	require.Len(t, pCopy1, 1)
	pCopy1[0] = toolTestPolicy{id: "mutated"}

	pCopy2 := snap.ToolCallPolicies()
	require.Len(t, pCopy2, 1)
	assert.Equal(t, "p1", pCopy2[0].ID())

	// 3. ToolCallPoliciesExecution returns stable backing slice
	pExec1 := snap.ToolCallPoliciesExecution()
	pExec2 := snap.ToolCallPoliciesExecution()
	require.Len(t, pExec1, 1)
	assert.Equal(t, &pExec1[0], &pExec2[0], "ToolCallPoliciesExecution must return the exact same backing slice without allocation")
}

func TestToolPlanesProjection_CompileGenerationCandidateOverlay(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	require.NoError(t, reg.RegisterFeature("tool-feature", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			ToolCatalogFilters: []toolcatalog.Filter{
				toolTestFilter{id: "base-filter", ord: 20},
			},
			ToolCallPolicies: []toolpolicy.Policy{
				toolTestPolicy{id: "base-policy", ord: 20, decision: toolpolicy.DecisionAllow},
			},
		}, nil
	}))

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "tool-feature")

	// 1. Base generation compilation without candidate overlay
	baseGen, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = baseGen.Close() })

	baseBundle, ok := baseGen.(*GenerationBundle)
	require.True(t, ok)
	baseSnap := baseBundle.execution.executor.RuntimeSnapshot
	require.Len(t, baseSnap.ToolCatalogFilters(), 1)
	assert.Equal(t, "base-filter", baseSnap.ToolCatalogFilters()[0].ID())
	require.Len(t, baseSnap.ToolCallPolicies(), 1)
	assert.Equal(t, "base-policy", baseSnap.ToolCallPolicies()[0].ID())

	// 2. Candidate overlay contributing additional filters and policies via FeaturePlanes
	candCS := lipfeature.NewContributionSet()
	err = lipfeature.Contribute(candCS, lipfeature.PlaneToolCatalogFilters, "candidate-plugin", []toolcatalog.Filter{
		toolTestFilter{id: "cand-filter", ord: 10},
	})
	require.NoError(t, err)
	err = lipfeature.Contribute(candCS, lipfeature.PlaneToolCallPolicies, "candidate-plugin", []toolpolicy.Policy{
		toolTestPolicy{id: "cand-policy", ord: 10, decision: toolpolicy.DecisionAllow},
	})
	require.NoError(t, err)

	candGen, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candCS.Freeze(),
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = candGen.Close() })

	candBundle, ok := candGen.(*GenerationBundle)
	require.True(t, ok)
	candSnap := candBundle.execution.executor.RuntimeSnapshot

	// Catalog filters: base then candidate in registration order
	require.Len(t, candSnap.ToolCatalogFilters(), 2)
	assert.Equal(t, "base-filter", candSnap.ToolCatalogFilters()[0].ID())
	assert.Equal(t, "cand-filter", candSnap.ToolCatalogFilters()[1].ID())

	// Policies: MaterializeSorted order (10 before 20)
	require.Len(t, candSnap.ToolCallPolicies(), 2)
	assert.Equal(t, "cand-policy", candSnap.ToolCallPolicies()[0].ID())
	assert.Equal(t, "base-policy", candSnap.ToolCallPolicies()[1].ID())
}

func TestToolPlanesProjection_StageCoalescingAndDiagnostics(t *testing.T) {
	t.Parallel()

	// Feature with ToolCatalogFilters, ToolCallPolicies, ToolCallFinalizers, and ToolReactors
	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolCatalogFilters: []toolcatalog.Filter{
			toolTestFilter{id: "filter-cat-2", ord: 20},
			toolTestFilter{id: "filter-cat-1", ord: 10},
			nil, // nil filtering
		},
		ToolCallPolicies: []toolpolicy.Policy{
			toolTestPolicy{id: "pol-2", ord: 20, decision: toolpolicy.DecisionAllow},
			nil, // nil filtering
			toolTestPolicy{id: "pol-1", ord: 10, decision: toolpolicy.DecisionAllow},
		},
		ToolCallFinalizers: []toolcall.Finalizer{
			toolTestFinalizer{id: "fin-2", ord: 20},
			toolTestFinalizer{id: "fin-1", ord: 10},
		},
		ToolReactors: []sdkhooks.ToolReactor{
			toolTestReactor{id: "reac-2", ord: 20},
			toolTestReactor{id: "reac-1", ord: 10},
		},
	}

	reg := &featurebundleStubRegistry{bundle: b}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "feat-tool", Kind: "feat-tool", Enabled: true},
			},
		},
	}
	extras := &diag.InventoryExtras{
		Reg:           reg,
		Registrations: config.RegistrationsFromConfig(cfg),
	}

	snapshot, err := diag.InventorySnapshotForConfig(t.Context(), cfg, extras)
	require.NoError(t, err)

	require.Len(t, snapshot.Extensions.Features, 1)
	feat := snapshot.Extensions.Features[0]
	assert.Equal(t, "feat-tool", feat.InstanceID)

	// Verify Privilege projection: ToolCatalogFilters non-empty triggers AuxiliaryRequests
	assert.True(t, feat.Privileges.AuxiliaryRequests)
	assert.False(t, feat.Privileges.RawCapture)
	assert.False(t, feat.Privileges.CompletionGate)
	assert.False(t, feat.Privileges.AuthProvider)

	// Stage occupancy:
	// 1. StageToolCatalog: MaterializeSorted order (10 then 20) with "tool_catalog:" prefix
	var catalogOcc *diag.InventoryStageOccupancy
	var reactionOcc *diag.InventoryStageOccupancy
	for i := range feat.StageOccupancy {
		if feat.StageOccupancy[i].StageID == extensions.StageToolCatalog {
			catalogOcc = &feat.StageOccupancy[i]
		}
		if feat.StageOccupancy[i].StageID == extensions.StageToolEventReaction {
			reactionOcc = &feat.StageOccupancy[i]
		}
	}

	require.NotNil(t, catalogOcc, "missing StageToolCatalog occupancy")
	assert.Equal(t, 2, catalogOcc.Count)
	assert.Equal(t, []string{"tool_catalog:filter-cat-1", "tool_catalog:filter-cat-2"}, catalogOcc.HandlerIDs)

	// 2. StageToolEventReaction: coalesces ToolCallPolicies + ToolCallFinalizers + ToolReactors
	require.NotNil(t, reactionOcc, "missing StageToolEventReaction occupancy")
	assert.Equal(t, 6, reactionOcc.Count)
	assert.Equal(t, []string{
		"tool_policy:pol-1",
		"tool_policy:pol-2",
		"tool_finalizer:fin-1",
		"tool_finalizer:fin-2",
		"reac-1",
		"reac-2",
	}, reactionOcc.HandlerIDs)
}

func TestToolPlanesProjection_FinalizersAndBufferReduction_ParityAndReduction(t *testing.T) {
	t.Parallel()

	b1 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolCallFinalizers: []toolcall.Finalizer{
			toolTestFinalizer{id: "fin-b1-20", ord: 20},
			toolTestFinalizer{id: "fin-b1-10", ord: 10},
		},
		ToolCallFinalizationMaxArgsBytes: 4096,
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolCallFinalizers: []toolcall.Finalizer{
			toolTestFinalizer{id: "fin-b2-5", ord: 5},
		},
		ToolCallFinalizationMaxArgsBytes: 1024,
	}
	b3 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolCallFinalizers: []toolcall.Finalizer{
			toolTestFinalizer{id: "fin-b3-30", ord: 30},
		},
		ToolCallFinalizationMaxArgsBytes: 8192,
	}
	b4 := lipfeature.FeatureBundle{
		SchemaVersion:                    lipfeature.SchemaVersionV1,
		ToolCallFinalizationMaxArgsBytes: 0, // 0 is unset
	}

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2, b3, b4)
	require.NoError(t, err)

	// 1. Verify Frozen plane contents preserve registration order
	frozenFinalizers := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizers)
	require.Len(t, frozenFinalizers, 4)
	assert.Equal(t, "fin-b1-20", frozenFinalizers[0].ID())
	assert.Equal(t, "fin-b1-10", frozenFinalizers[1].ID())
	assert.Equal(t, "fin-b2-5", frozenFinalizers[2].ID())
	assert.Equal(t, "fin-b3-30", frozenFinalizers[3].ID())

	// 2. Buffer cap is min of positives (min(4096, 1024, 8192) = 1024; 0 is unset)
	frozenMaxArgs := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	assert.Equal(t, 1024, frozenMaxArgs)

	// 3. Build runtime snapshot from FeaturePlanes directly
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{
		FeaturePlanes: gen.Frozen,
	}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
	require.NotNil(t, snap)

	// 4. Snapshot ToolCallFinalizers returns sorted defensive copy (MaterializeSorted order: 5, 10, 20, 30)
	snapFinalizers := snap.ToolCallFinalizers()
	require.Len(t, snapFinalizers, 4)
	assert.Equal(t, "fin-b2-5", snapFinalizers[0].ID())
	assert.Equal(t, "fin-b1-10", snapFinalizers[1].ID())
	assert.Equal(t, "fin-b1-20", snapFinalizers[2].ID())
	assert.Equal(t, "fin-b3-30", snapFinalizers[3].ID())

	// 5. ToolCallFinalizersExecution returns backing slice in MaterializeSorted order
	execFinalizers := snap.ToolCallFinalizersExecution()
	require.Len(t, execFinalizers, 4)
	assert.Equal(t, "fin-b2-5", execFinalizers[0].ID())
	assert.Equal(t, "fin-b1-10", execFinalizers[1].ID())
	assert.Equal(t, "fin-b1-20", execFinalizers[2].ID())
	assert.Equal(t, "fin-b3-30", execFinalizers[3].ID())
}

func TestToolPlanesProjection_FinalizersNilAndEmptySlicePreservation(t *testing.T) {
	t.Parallel()

	t.Run("nil_and_empty_finalizers", func(t *testing.T) {
		t.Parallel()
		b := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
		}
		gen, err := featurebundle.MergeBundlesGenerated(b)
		require.NoError(t, err)

		finalizers := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizers)
		assert.Nil(t, finalizers)

		maxArgs := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes)
		assert.Equal(t, 0, maxArgs)

		opts := &BuildOptions{
			FeaturePlanes: gen.Frozen,
		}
		snap := buildRuntimeSnapshot(nil, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
		require.NotNil(t, snap)
		assert.Empty(t, snap.ToolCallFinalizers())
		assert.Empty(t, snap.ToolCallFinalizersExecution())
	})

	t.Run("explicitly_empty_finalizers", func(t *testing.T) {
		t.Parallel()
		cs := lipfeature.NewContributionSet()
		err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizers, "plugin-1", []toolcall.Finalizer{})
		require.NoError(t, err)

		frozen := cs.Freeze()
		finalizers := lipfeature.Get(frozen, lipfeature.PlaneToolCallFinalizers)
		assert.NotNil(t, finalizers)
		assert.Empty(t, finalizers)

		opts := &BuildOptions{
			FeaturePlanes: frozen,
		}
		snap := buildRuntimeSnapshot(nil, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
		require.NotNil(t, snap)
		assert.Nil(t, snap.ToolCallFinalizers())
		assert.Empty(t, snap.ToolCallFinalizers())
		assert.Empty(t, snap.ToolCallFinalizersExecution())
	})

	t.Run("nil_elements_filtered_in_snapshot", func(t *testing.T) {
		t.Parallel()
		cs := lipfeature.NewContributionSet()
		err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizers, "plugin-1", []toolcall.Finalizer{
			toolTestFinalizer{id: "f1", ord: 10},
			nil,
			toolTestFinalizer{id: "f2", ord: 5},
		})
		require.NoError(t, err)

		frozen := cs.Freeze()
		rawFins := lipfeature.Get(frozen, lipfeature.PlaneToolCallFinalizers)
		require.Len(t, rawFins, 3)
		assert.Nil(t, rawFins[1])

		opts := &BuildOptions{
			FeaturePlanes: frozen,
		}
		snap := buildRuntimeSnapshot(nil, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
		require.NotNil(t, snap)
		snapFins := snap.ToolCallFinalizers()
		require.Len(t, snapFins, 2)
		assert.Equal(t, "f2", snapFins[0].ID())
		assert.Equal(t, "f1", snapFins[1].ID())
	})
}

func TestToolPlanesProjection_FinalizersDefensiveCopyingAndBackingSlice(t *testing.T) {
	t.Parallel()

	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		ToolCallFinalizers: []toolcall.Finalizer{
			toolTestFinalizer{id: "fin-1", ord: 1},
		},
	}

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)

	opts := &BuildOptions{
		FeaturePlanes: gen.Frozen,
	}
	snap := buildRuntimeSnapshot(nil, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
	require.NotNil(t, snap)

	// 1. ToolCallFinalizers defensive copy
	fCopy1 := snap.ToolCallFinalizers()
	require.Len(t, fCopy1, 1)
	fCopy1[0] = toolTestFinalizer{id: "mutated"}

	fCopy2 := snap.ToolCallFinalizers()
	require.Len(t, fCopy2, 1)
	assert.Equal(t, "fin-1", fCopy2[0].ID())

	// 2. ToolCallFinalizersExecution returns stable backing slice
	fExec1 := snap.ToolCallFinalizersExecution()
	fExec2 := snap.ToolCallFinalizersExecution()
	require.Len(t, fExec1, 1)
	assert.Equal(t, &fExec1[0], &fExec2[0], "ToolCallFinalizersExecution must return exact same backing slice without allocation")
}

func TestToolPlanesProjection_FinalizersAndBufferReduction_CompileGenerationCandidateOverlay(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	require.NoError(t, reg.RegisterFeature("tool-feature", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			ToolCallFinalizers: []toolcall.Finalizer{
				toolTestFinalizer{id: "base-fin", ord: 20},
			},
			ToolCallFinalizationMaxArgsBytes: 4096,
		}, nil
	}))

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "tool-feature")

	// 1. Base generation compilation without candidate overlay
	baseGen, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = baseGen.Close() })

	baseBundle, ok := baseGen.(*GenerationBundle)
	require.True(t, ok)
	baseSnap := baseBundle.execution.executor.RuntimeSnapshot
	require.Len(t, baseSnap.ToolCallFinalizers(), 1)
	assert.Equal(t, "base-fin", baseSnap.ToolCallFinalizers()[0].ID())
	assert.Equal(t, 4096, baseBundle.execution.executor.ToolCallFinalizationMaxArgsBytes)

	// 2. Candidate overlay contributing smaller buffer cap (min-reduction) and additional finalizer
	candCS := lipfeature.NewContributionSet()
	err = lipfeature.Contribute(candCS, lipfeature.PlaneToolCallFinalizers, "candidate-plugin", []toolcall.Finalizer{
		toolTestFinalizer{id: "cand-fin", ord: 10},
	})
	require.NoError(t, err)
	err = lipfeature.Contribute(candCS, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "candidate-plugin", 1024)
	require.NoError(t, err)

	candGen, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candCS.Freeze(),
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = candGen.Close() })

	candBundle, ok := candGen.(*GenerationBundle)
	require.True(t, ok)
	candSnap := candBundle.execution.executor.RuntimeSnapshot

	// Finalizers: MaterializeSorted order (10 before 20)
	require.Len(t, candSnap.ToolCallFinalizers(), 2)
	assert.Equal(t, "cand-fin", candSnap.ToolCallFinalizers()[0].ID())
	assert.Equal(t, "base-fin", candSnap.ToolCallFinalizers()[1].ID())

	// Buffer reduction: min(4096, 1024) = 1024
	assert.Equal(t, 1024, candBundle.execution.executor.ToolCallFinalizationMaxArgsBytes)

	// 3. Candidate overlay with LARGER buffer cap: min-reduction preserves smaller base cap (4096 vs 8192)
	candCS2 := lipfeature.NewContributionSet()
	err = lipfeature.Contribute(candCS2, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "candidate-plugin", 8192)
	require.NoError(t, err)

	candGen2, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candCS2.Freeze(),
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = candGen2.Close() })

	candBundle2, ok := candGen2.(*GenerationBundle)
	require.True(t, ok)
	assert.Equal(t, 4096, candBundle2.execution.executor.ToolCallFinalizationMaxArgsBytes, "candidate larger cap must not overwrite smaller base cap (min-reduction)")
}

func TestToolPlanesProjection_BufferReduction_ExecutorClampBoundaries(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t)

	tests := []struct {
		name       string
		contribute int
		wantCap    int
	}{
		{"unset_zero_defaults_to_zero_in_executor_and_clamps_to_64kb_in_assembler", 0, 0},
		{"positive_in_bounds_preserves_value", 1024, 1024},
		{"larger_positive_preserves_value", 128 * 1024, 128 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			candCS := lipfeature.NewContributionSet()
			if tt.contribute > 0 {
				require.NoError(t, lipfeature.Contribute(candCS, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "cand", tt.contribute))
			}

			gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
				Process:   ps,
				Candidate: cand,
				CandidateOpts: &BuildOptions{
					FeaturePlanes: candCS.Freeze(),
				},
				Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
				},
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = gen.Close() })

			bundle, ok := gen.(*GenerationBundle)
			require.True(t, ok)
			assert.Equal(t, tt.wantCap, bundle.execution.executor.ToolCallFinalizationMaxArgsBytes)
		})
	}
}

func TestToolPlanesProjection_CandidateRollbackOnInvalidFinalizerContribution(t *testing.T) {
	t.Parallel()

	// 1. Direct validation failure on negative buffer cap
	candCS := lipfeature.NewContributionSet()
	err := lipfeature.Contribute(candCS, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "cand", -50)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be >= 0")

	// 2. Candidate rollback when compose step fails during CompileGeneration with candidate planes
	reg := obsTestFactoryCatalog(t)
	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t)
	validCandCS := lipfeature.NewContributionSet()
	require.NoError(t, lipfeature.Contribute(validCandCS, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, "cand", 2048))

	composeErr := errors.New("compose intentional failure")
	_, err = CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: validCandCS.Freeze(),
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return nil, composeErr
		},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, composeErr)
}

type featurebundleStubRegistry struct {
	bundle lipfeature.FeatureBundle
}

func (r *featurebundleStubRegistry) BuildFeatureBundle(factoryKey string, _ yaml.Node) (lipfeature.FeatureBundle, error) {
	return r.bundle, nil
}
