package runtimebundle

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stubs for Route Hints and Completion Gates ---

type stubRouteHintProvider struct {
	id     string
	ord    int
	mode   sdkhooks.FailureMode
	hints  []string
	err    error
	events *[]string
	mu     *sync.Mutex
}

func (p stubRouteHintProvider) ID() string                        { return p.id }
func (p stubRouteHintProvider) Order() int                        { return p.ord }
func (p stubRouteHintProvider) FailureMode() sdkhooks.FailureMode { return p.mode }

func (p stubRouteHintProvider) Hint(ctx context.Context, meta routehint.Input) (routehint.Result, error) {
	if p.mu != nil && p.events != nil {
		p.mu.Lock()
		*p.events = append(*p.events, p.id)
		p.mu.Unlock()
	}
	if p.err != nil {
		return routehint.Result{}, p.err
	}
	return routehint.Result{PreferredCandidateKeys: p.hints}, nil
}

type stubCompletionGate struct {
	id      string
	ord     int
	mode    sdkhooks.FailureMode
	outcome completion.Outcome
	err     error
	events  *[]string
	mu      *sync.Mutex
}

func (g stubCompletionGate) ID() string                        { return g.id }
func (g stubCompletionGate) Order() int                        { return g.ord }
func (g stubCompletionGate) FailureMode() sdkhooks.FailureMode { return g.mode }

func (g stubCompletionGate) Handle(ctx context.Context, meta completion.Meta, buf completion.Buffered, svc completion.Services) (completion.Outcome, error) {
	if g.mu != nil && g.events != nil {
		g.mu.Lock()
		*g.events = append(*g.events, g.id)
		g.mu.Unlock()
	}
	if g.err != nil {
		return completion.Outcome{}, g.err
	}
	if g.outcome.Kind == 0 && len(g.outcome.Events) == 0 && g.outcome.Err == nil {
		return completion.PassOriginalOutcome(), nil
	}
	return g.outcome, nil
}

// --- Parity and Snapshot Projection Tests ---

func TestGatesAndRouteHintsProjection_ParityWithFrozenAndRegistrationOrder(t *testing.T) {
	t.Parallel()

	var events []string
	var mu sync.Mutex

	b1 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		RouteHintProviders: []routehint.Provider{
			stubRouteHintProvider{id: "rh-1", ord: 10, hints: []string{"cand-b"}, events: &events, mu: &mu},
			stubRouteHintProvider{id: "rh-2", ord: 20, hints: []string{"cand-c"}, events: &events, mu: &mu},
		},
		CompletionGates: []completion.Gate{
			stubCompletionGate{id: "cg-1", ord: 10, events: &events, mu: &mu},
		},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		RouteHintProviders: []routehint.Provider{
			stubRouteHintProvider{id: "rh-3", ord: 5, hints: []string{"cand-a"}, events: &events, mu: &mu},
		},
		CompletionGates: []completion.Gate{
			stubCompletionGate{id: "cg-2", ord: 5, events: &events, mu: &mu},
		},
	}

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	// Verify Frozen plane contents preserve registration order
	frozenRH := lipfeature.Get(gen.Frozen, lipfeature.PlaneRouteHintProviders)
	require.Len(t, frozenRH, 3)
	assert.Equal(t, "rh-1", frozenRH[0].ID())
	assert.Equal(t, "rh-2", frozenRH[1].ID())
	assert.Equal(t, "rh-3", frozenRH[2].ID())

	frozenCG := lipfeature.Get(gen.Frozen, lipfeature.PlaneCompletionGates)
	require.Len(t, frozenCG, 2)
	assert.Equal(t, "cg-1", frozenCG[0].ID())
	assert.Equal(t, "cg-2", frozenCG[1].ID())

	// Build runtime snapshot from FeaturePlanes directly
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{
		FeaturePlanes: gen.Frozen,
	}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	// Snapshot methods return defensive copies preserving registration order
	snapRH := snap.RouteHintProviders()
	require.Len(t, snapRH, 3)
	assert.Equal(t, "rh-1", snapRH[0].ID())
	assert.Equal(t, "rh-2", snapRH[1].ID())
	assert.Equal(t, "rh-3", snapRH[2].ID())

	snapCG := snap.CompletionGates()
	require.Len(t, snapCG, 2)
	assert.Equal(t, "cg-1", snapCG[0].ID())
	assert.Equal(t, "cg-2", snapCG[1].ID())
}

func TestGatesAndRouteHintsProjection_NilVsEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("zero_value_bundles_produce_nil_planes", func(t *testing.T) {
		t.Parallel()
		gen, err := featurebundle.MergeBundlesGenerated(lipfeature.FeatureBundle{}, lipfeature.FeatureBundle{})
		require.NoError(t, err)

		assert.Nil(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneRouteHintProviders))
		assert.Nil(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneCompletionGates))

		bus := hooks.New(hooks.Config{})
		opts := &BuildOptions{FeaturePlanes: gen.Frozen}
		snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

		assert.Nil(t, snap.RouteHintProviders())
		assert.Nil(t, snap.CompletionGates())

		// Seam view from context returns non-nil emptyCompletionGates when snapshot has nil gates
		ctx := extensions.WithRequestRuntimeSnapshot(context.Background(), snap)
		seamGates := extensions.CompletionGatesFromContext(ctx, nil)
		assert.NotNil(t, seamGates)
		assert.Empty(t, seamGates)
	})

	t.Run("explicit_empty_completion_gates_preserves_non_nil_empty", func(t *testing.T) {
		t.Parallel()
		b := lipfeature.FeatureBundle{
			SchemaVersion:      lipfeature.SchemaVersionV1,
			CompletionGates:    []completion.Gate{},
			RouteHintProviders: []routehint.Provider{},
		}
		gen, err := featurebundle.MergeBundlesGenerated(b)
		require.NoError(t, err)

		// PlaneCompletionGates preserves explicit non-nil empty slice
		gotGates := lipfeature.Get(gen.Frozen, lipfeature.PlaneCompletionGates)
		assert.NotNil(t, gotGates, "PlaneCompletionGates must preserve explicit non-nil empty slice")
		assert.Empty(t, gotGates)

		bus := hooks.New(hooks.Config{})
		opts := &BuildOptions{FeaturePlanes: gen.Frozen}
		snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

		snapGates := snap.CompletionGates()
		assert.NotNil(t, snapGates, "Snapshot CompletionGates must preserve explicit non-nil empty slice")
		assert.Empty(t, snapGates)
	})
}

func TestGatesAndRouteHintsProjection_BackingArrayIsolation(t *testing.T) {
	t.Parallel()

	origRH := []routehint.Provider{stubRouteHintProvider{id: "rh-orig"}}
	origCG := []completion.Gate{stubCompletionGate{id: "cg-orig"}}

	b := lipfeature.FeatureBundle{
		SchemaVersion:      lipfeature.SchemaVersionV1,
		RouteHintProviders: origRH,
		CompletionGates:    origCG,
	}

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)

	// Mutate original slice backing array
	origRH[0] = stubRouteHintProvider{id: "rh-mutated"}
	origCG[0] = stubCompletionGate{id: "cg-mutated"}

	// Frozen plane must retain original value
	assert.Equal(t, "rh-orig", lipfeature.Get(gen.Frozen, lipfeature.PlaneRouteHintProviders)[0].ID())
	assert.Equal(t, "cg-orig", lipfeature.Get(gen.Frozen, lipfeature.PlaneCompletionGates)[0].ID())

	// Snapshot defensive copy isolation
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{FeaturePlanes: gen.Frozen}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	snapRH1 := snap.RouteHintProviders()
	snapRH1[0] = stubRouteHintProvider{id: "rh-snap-mutated"}
	snapRH2 := snap.RouteHintProviders()
	assert.Equal(t, "rh-orig", snapRH2[0].ID())

	snapCG1 := snap.CompletionGates()
	snapCG1[0] = stubCompletionGate{id: "cg-snap-mutated"}
	snapCG2 := snap.CompletionGates()
	assert.Equal(t, "cg-orig", snapCG2[0].ID())
}

func TestGatesAndRouteHintsProjection_SeamViewsSourceCompatibility(t *testing.T) {
	t.Parallel()

	cg := []completion.Gate{stubCompletionGate{id: "cg-seam"}}
	b := lipfeature.FeatureBundle{
		SchemaVersion:   lipfeature.SchemaVersionV1,
		CompletionGates: cg,
	}
	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)

	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{FeaturePlanes: gen.Frozen}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	// 1. Direct from context with snapshot
	ctxWithSnap := extensions.WithRequestRuntimeSnapshot(context.Background(), snap)
	gates := extensions.CompletionGatesFromContext(ctxWithSnap, nil)
	require.Len(t, gates, 1)
	assert.Equal(t, "cg-seam", gates[0].ID())

	// 2. Fallback when context has no snapshot
	fallbackGates := extensions.CompletionGatesFromContext(context.Background(), snap)
	require.Len(t, fallbackGates, 1)
	assert.Equal(t, "cg-seam", fallbackGates[0].ID())

	// 3. Fallback nil and context nil returns empty non-nil slice
	emptyGates := extensions.CompletionGatesFromContext(context.Background(), nil)
	assert.NotNil(t, emptyGates)
	assert.Empty(t, emptyGates)
}

// --- End-to-End CompileGeneration and Execution Tests ---

func TestCompileGeneration_GatesAndRouteHintsExecution(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	var executedRouteHints []string
	var executedGates []string
	var mu sync.Mutex

	// Register feature 1: route hint provider (returns preferred backend "stub-backend:preferred")
	require.NoError(t, reg.RegisterFeature("test-hints-1", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			RouteHintProviders: []routehint.Provider{
				stubRouteHintProvider{
					id:     "rh-preferred",
					ord:    1,
					hints:  []string{"stub-backend:preferred"},
					events: &executedRouteHints,
					mu:     &mu,
				},
			},
		}, nil
	}))

	// Register feature 2: completion gate (passes completion)
	require.NoError(t, reg.RegisterFeature("test-gates-1", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			CompletionGates: []completion.Gate{
				stubCompletionGate{
					id:     "cg-verify",
					ord:    1,
					events: &executedGates,
					mu:     &mu,
				},
			},
		}, nil
	}))

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	policyObs := &capturePolicyObserver{}

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
			Policy: PolicyOptions{
				PolicyObservers: []policydecision.Observer{policyObs},
			},
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "test-hints-1", "test-gates-1")

	var capturedCallsCount atomic.Int64

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok, "genRuntime must be *GenerationBundle")
	ex := bundle.execution.executor
	require.NotNil(t, ex)

	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				capturedCallsCount.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	inputCall := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "prompt text"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := lipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	assert.Equal(t, int64(1), capturedCallsCount.Load())

	// Verify both RouteHintProviders and CompletionGates executed
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, executedRouteHints, "rh-preferred")
	assert.Contains(t, executedGates, "cg-verify")

	// Verify policy decision evidence for route hint and completion gate
	records := policyObs.snapshot()
	var hasRouteHintRecord bool
	var hasCompletionGateRecord bool
	for _, r := range records {
		if r.Provider.Stage == lipfeature.StageIDRouteHinting && r.Provider.ID == "rh-preferred" {
			hasRouteHintRecord = true
		}
		if r.Provider.Stage == lipfeature.StageIDCompletionGating && r.Provider.ID == "cg-verify" {
			hasCompletionGateRecord = true
		}
	}
	assert.True(t, hasRouteHintRecord, "expected policy decision record for route hint provider")
	assert.True(t, hasCompletionGateRecord, "expected policy decision record for completion gate")
}

func TestCompileGeneration_RouteHintErrorEvidence_FailOpen(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	// Route hint provider that errors with FailOpen
	require.NoError(t, reg.RegisterFeature("test-failopen-hint", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			RouteHintProviders: []routehint.Provider{
				stubRouteHintProvider{
					id:   "rh-error-failopen",
					ord:  1,
					mode: sdkhooks.FailOpen,
					err:  errors.New("hint provider computation failed"),
				},
			},
		}, nil
	}))

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	policyObs := &capturePolicyObserver{}

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
			Policy: PolicyOptions{
				PolicyObservers: []policydecision.Observer{policyObs},
			},
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "test-failopen-hint")

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	ex := bundle.execution.executor

	var backendAttempts atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				backendAttempts.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	inputCall := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "prompt"}}},
		},
	}

	// Execution succeeds because route hint is FailOpen
	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := lipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	// Backend must be attempted exactly once
	assert.Equal(t, int32(1), backendAttempts.Load(), "backend must be attempted exactly once when fail-open route hint errors")

	// Error evidence was emitted
	records := policyObs.snapshot()
	var foundRecord *policydecision.Record
	for _, r := range records {
		if r.Provider.Stage == lipfeature.StageIDRouteHinting && r.Provider.ID == "rh-error-failopen" {
			rec := r
			foundRecord = &rec
			break
		}
	}
	require.NotNil(t, foundRecord, "expected error policy decision evidence for fail-open route hint error")
	assert.Equal(t, lipfeature.StageIDRouteHinting, foundRecord.Provider.Stage)
	assert.Equal(t, "rh-error-failopen", foundRecord.Provider.ID)
	assert.Equal(t, policydecision.CategoryFailure, foundRecord.ClientCategory)
	assert.Equal(t, policydecision.FailureBehaviorFailOpen, foundRecord.FailureBehavior)
	assert.False(t, foundRecord.BackendAttempted, "route hint stage runs pre-backend, so BackendAttempted must be false")
}

func TestCompileGeneration_CandidateFeaturePlanesOverlayGatesAndRouteHints(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	policyObs := &capturePolicyObserver{}

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
			Policy: PolicyOptions{
				PolicyObservers: []policydecision.Observer{policyObs},
			},
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	var executedRouteHints []string
	var executedGates []string
	var mu sync.Mutex

	candBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		RouteHintProviders: []routehint.Provider{
			stubRouteHintProvider{
				id:     "cand-rh-1",
				ord:    1,
				hints:  []string{"cand-route"},
				events: &executedRouteHints,
				mu:     &mu,
			},
		},
		CompletionGates: []completion.Gate{
			stubCompletionGate{
				id:     "cand-cg-1",
				ord:    1,
				events: &executedGates,
				mu:     &mu,
			},
		},
	}
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	cand := obsTestCandidateConfig(t)

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candGen.Frozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	ex := bundle.execution.executor
	require.NotNil(t, ex)

	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	inputCall := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "candidate input"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := lipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, executedRouteHints, "cand-rh-1")
	assert.Contains(t, executedGates, "cand-cg-1")
}

func TestCompileGeneration_CandidateFeaturePlanes_UnrelatedPlanesIgnoredWithGatesAndRouteHints(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	var mu sync.Mutex

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

	// Candidate overlay contains allowed candidate request planes:
	// - RouteHintProviders
	// - CompletionGates
	// AND populated UNRELATED planes that must be ignored for this wave:
	// - TrafficObservers (ignored)
	// - UsageObservers (ignored)
	candBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		RouteHintProviders: []routehint.Provider{
			stubRouteHintProvider{
				id:  "cand-rh-valid",
				ord: 1,
				events: func() *[]string {
					s := make([]string, 0)
					return &s
				}(),
				mu: &mu,
			},
		},
		CompletionGates: []completion.Gate{
			stubCompletionGate{
				id:  "cand-cg-valid",
				ord: 1,
				events: func() *[]string {
					s := make([]string, 0)
					return &s
				}(),
				mu: &mu,
			},
		},
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "unrelated-traffic-obs"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "unrelated-usage-obs"},
		},
	}
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	cand := obsTestCandidateConfig(t)

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candGen.Frozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	snap := bundle.execution.executor.RuntimeSnapshot
	require.NotNil(t, snap)

	// Allowed candidate planes are present
	assert.Len(t, snap.RouteHintProviders(), 1)
	assert.Equal(t, "cand-rh-valid", snap.RouteHintProviders()[0].ID())

	assert.Len(t, snap.CompletionGates(), 1)
	assert.Equal(t, "cand-cg-valid", snap.CompletionGates()[0].ID())

	// Unrelated planes in candidate overlay were filtered out and not applied
	assert.Equal(t, traffic.NoopObserver{}, snap.TrafficObserver(), "unrelated candidate traffic observer must be ignored")
	assert.Equal(t, usage.NoopObserver{}, snap.UsageObserver(), "unrelated candidate usage observer must be ignored")
}

func TestCompileGeneration_CandidateExplicitEmptyCompletionGates(t *testing.T) {
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

	candBundle := lipfeature.FeatureBundle{
		SchemaVersion:   lipfeature.SchemaVersionV1,
		CompletionGates: []completion.Gate{},
	}
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	cand := obsTestCandidateConfig(t)
	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candGen.Frozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	snap := bundle.execution.executor.RuntimeSnapshot
	require.NotNil(t, snap)

	gotGates := snap.CompletionGates()
	assert.NotNil(t, gotGates, "CandidateOpts.FeaturePlanes with generated storage must preserve non-nil empty completion gates")
	assert.Empty(t, gotGates)
}

func TestCompileGeneration_MultiRouteHintProvidersExecutionAndEvidenceOrder(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	var executedRouteHints []string
	var mu sync.Mutex

	// Register feature with deliberately unsorted route hint providers:
	// Registration order:
	// 1. rh-z: ord=20
	// 2. rh-b: ord=10
	// 3. rh-a-1: ord=10
	// 4. rh-a-2: ord=10
	// 5. rh-first: ord=5
	require.NoError(t, reg.RegisterFeature("test-hints-multi-1", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			RouteHintProviders: []routehint.Provider{
				stubRouteHintProvider{id: "rh-z", ord: 20, hints: []string{"cand-z"}, events: &executedRouteHints, mu: &mu},
				stubRouteHintProvider{id: "rh-b", ord: 10, hints: []string{"cand-b"}, events: &executedRouteHints, mu: &mu},
				stubRouteHintProvider{id: "rh-a-1", ord: 10, hints: []string{"cand-a1"}, events: &executedRouteHints, mu: &mu},
			},
		}, nil
	}))
	require.NoError(t, reg.RegisterFeature("test-hints-multi-2", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			RouteHintProviders: []routehint.Provider{
				stubRouteHintProvider{id: "rh-a-2", ord: 10, hints: []string{"cand-a2"}, events: &executedRouteHints, mu: &mu},
				stubRouteHintProvider{id: "rh-first", ord: 5, hints: []string{"cand-first"}, events: &executedRouteHints, mu: &mu},
			},
		}, nil
	}))

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	policyObs := &capturePolicyObserver{}

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
			Policy: PolicyOptions{
				PolicyObservers: []policydecision.Observer{policyObs},
			},
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "test-hints-multi-1", "test-hints-multi-2")

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	ex := bundle.execution.executor
	require.NotNil(t, ex)

	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	inputCall := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "multi hint input"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := lipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	// Materialized runtime order must sort by (Order, ID, RegistrationIndex):
	// 1. rh-first (ord 5)
	// 2. rh-a-1 (ord 10, ID "rh-a-1")
	// 3. rh-a-2 (ord 10, ID "rh-a-2")
	// 4. rh-b (ord 10, ID "rh-b")
	// 5. rh-z (ord 20, ID "rh-z")
	expectedOrder := []string{"rh-first", "rh-a-1", "rh-a-2", "rh-b", "rh-z"}

	mu.Lock()
	assert.Equal(t, expectedOrder, executedRouteHints, "route hint providers must be invoked in (Order, ID, regIdx) sorted order")
	mu.Unlock()

	// Evidence order must match execution order
	records := policyObs.snapshot()
	var evidenceOrder []string
	for _, r := range records {
		if r.Provider.Stage == lipfeature.StageIDRouteHinting {
			evidenceOrder = append(evidenceOrder, r.Provider.ID)
			assert.Equal(t, policydecision.OutcomeAllow, r.Outcome)
			assert.Equal(t, policydecision.CategoryObserved, r.ClientCategory)
			assert.Equal(t, policydecision.FailureBehaviorUnspecified, r.FailureBehavior)
		}
	}
	assert.Equal(t, expectedOrder, evidenceOrder, "route hint policy decision records must be emitted in exact materialized order")
}

func TestCompileGeneration_RouteHintErrorEvidence_FailClosed_RequestFailsBackendNotAttempted(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	// Route hint provider that errors with FailClosed
	require.NoError(t, reg.RegisterFeature("test-failclosed-hint", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			RouteHintProviders: []routehint.Provider{
				stubRouteHintProvider{
					id:   "rh-error-failclosed",
					ord:  1,
					mode: sdkhooks.FailClosed,
					err:  errors.New("strict route hint evaluation failed"),
				},
			},
		}, nil
	}))

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	policyObs := &capturePolicyObserver{}

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
			Policy: PolicyOptions{
				PolicyObservers: []policydecision.Observer{policyObs},
			},
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "test-failclosed-hint")

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	ex := bundle.execution.executor

	var backendAttempts atomic.Int64
	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				backendAttempts.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	inputCall := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "prompt"}}},
		},
	}

	// Execution must FAIL because route hint is FailClosed
	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.Error(t, execErr, "execution must fail when a fail-closed route hint errors")
	assert.Nil(t, stream)

	// Backend must NOT have been attempted
	assert.Equal(t, int64(0), backendAttempts.Load(), "backend must NOT be attempted when fail-closed route hint errors")

	// Exact error evidence must be emitted
	records := policyObs.snapshot()
	var foundRecord *policydecision.Record
	for _, r := range records {
		if r.Provider.Stage == lipfeature.StageIDRouteHinting && r.Provider.ID == "rh-error-failclosed" {
			rec := r
			foundRecord = &rec
			break
		}
	}
	require.NotNil(t, foundRecord, "expected policy decision record for fail-closed route hint error")
	assert.Equal(t, lipfeature.StageIDRouteHinting, foundRecord.Provider.Stage)
	assert.Equal(t, "rh-error-failclosed", foundRecord.Provider.ID)
	assert.Equal(t, policydecision.CategoryFailure, foundRecord.ClientCategory)
	assert.Equal(t, policydecision.FailureBehaviorFailClosed, foundRecord.FailureBehavior)
	assert.Equal(t, extensions.ReasonRouteHintFailure, foundRecord.ReasonCode)
}

type typedNilRouteHintProvider struct {
	id string
}

func (p *typedNilRouteHintProvider) ID() string {
	if p == nil {
		return "rh-nil"
	}
	return p.id
}
func (p *typedNilRouteHintProvider) Order() int                        { return 0 }
func (p *typedNilRouteHintProvider) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (p *typedNilRouteHintProvider) Hint(ctx context.Context, meta routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type typedNilCompletionGate struct {
	id string
}

func (g *typedNilCompletionGate) ID() string {
	if g == nil {
		return "cg-nil"
	}
	return g.id
}
func (g *typedNilCompletionGate) Order() int                        { return 0 }
func (g *typedNilCompletionGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (g *typedNilCompletionGate) Handle(ctx context.Context, meta completion.Meta, buf completion.Buffered, svc completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

func TestGatesAndRouteHints_LiteralNilAndBoxedTypedNilLegacyCharacterization(t *testing.T) {
	t.Parallel()

	var typedNilRH *typedNilRouteHintProvider
	var literalNilRH routehint.Provider
	validRH := stubRouteHintProvider{id: "rh-valid", ord: 10}

	var typedNilCG *typedNilCompletionGate
	var literalNilCG completion.Gate
	validCG := stubCompletionGate{id: "cg-valid", ord: 10}

	// 1. Bundle merging preserves literal nil and boxed typed-nil without error
	b := lipfeature.FeatureBundle{
		SchemaVersion:      lipfeature.SchemaVersionV1,
		RouteHintProviders: []routehint.Provider{literalNilRH, typedNilRH, validRH},
		CompletionGates:    []completion.Gate{literalNilCG, typedNilCG, validCG},
	}

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err, "contribute/merge must not reject literal nil or boxed typed nil for gates and route hints")

	gotRH := lipfeature.Get(gen.Frozen, lipfeature.PlaneRouteHintProviders)
	require.Len(t, gotRH, 3)
	assert.Nil(t, gotRH[0])
	assert.True(t, gotRH[1] != nil, "boxed typed-nil interface is non-nil interface holding nil concrete pointer")
	assert.Equal(t, "rh-nil", gotRH[1].ID())
	assert.Equal(t, "rh-valid", gotRH[2].ID())

	gotCG := lipfeature.Get(gen.Frozen, lipfeature.PlaneCompletionGates)
	require.Len(t, gotCG, 3)
	assert.Nil(t, gotCG[0])
	assert.True(t, gotCG[1] != nil, "boxed typed-nil interface is non-nil interface holding nil concrete pointer")
	assert.Equal(t, "cg-nil", gotCG[1].ID())
	assert.Equal(t, "cg-valid", gotCG[2].ID())

	// 2. Snapshot accessors return defensive copies with all 3 elements
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{FeaturePlanes: gen.Frozen}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	snapRH := snap.RouteHintProviders()
	require.Len(t, snapRH, 3)
	assert.Nil(t, snapRH[0])

	snapCG := snap.CompletionGates()
	require.Len(t, snapCG, 3)
	assert.Nil(t, snapCG[0])

	// 3. Diagnostics materializers safely skip nil entries
	diagRH := lipfeature.PlaneRouteHintProviders.Diagnostics.Materialize(gotRH)
	require.Len(t, diagRH, 2) // skips literal nil, retains typed nil and valid
	assert.Equal(t, "route_hint:rh-nil", diagRH[0].Label)
	assert.Equal(t, "route_hint:rh-valid", diagRH[1].Label)

	diagCG := lipfeature.PlaneCompletionGates.Diagnostics.Materialize(gotCG)
	require.Len(t, diagCG, 2)
	assert.Equal(t, "completion_gate:cg-nil", diagCG[0].Label)
	assert.Equal(t, "completion_gate:cg-valid", diagCG[1].Label)

	// 4. Seam views and stage runners handle slices containing nil safely
	ctx := extensions.WithRequestRuntimeSnapshot(context.Background(), snap)
	seamGates := extensions.CompletionGatesFromContext(ctx, nil)
	require.Len(t, seamGates, 3)

	// Route hint stage execution skips literal nil without panic
	call := &lipapi.Call{ID: "test-call"}
	hintIn := routehint.Input{TraceID: "test-trace", Call: call}
	hints, rerr := extensions.RunRouteHintStage(context.Background(), nil, []routehint.Provider{literalNilRH, validRH}, call, hintIn)
	require.NoError(t, rerr)
	assert.Empty(t, hints)

	// Completion gate chain execution skips literal nil without panic
	gateRes, gerr := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{literalNilCG, validCG}, completion.Meta{}, []lipapi.Event{{Kind: lipapi.EventResponseFinished}}, false, completion.Services{}, nil)
	require.NoError(t, gerr)
	assert.Len(t, gateRes.Events, 1)
}
