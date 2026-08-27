package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stubs for Request Transform Families ---

type stubReqTransform struct {
	id     string
	ord    int
	events *[]string
	mu     *sync.Mutex
	mutate func(*lipapi.Call)
}

func (t stubReqTransform) ID() string                        { return t.id }
func (t stubReqTransform) Order() int                        { return t.ord }
func (t stubReqTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (t stubReqTransform) Handle(ctx context.Context, call *lipapi.Call, meta request.RequestMeta, svc request.Services) error {
	if t.mu != nil && t.events != nil {
		t.mu.Lock()
		*t.events = append(*t.events, t.id)
		t.mu.Unlock()
	}
	if t.mutate != nil {
		t.mutate(call)
	}
	return nil
}

type stubPreReqHandler struct {
	id     string
	ord    int
	events *[]string
	mu     *sync.Mutex
	deny   bool
}

func (h stubPreReqHandler) ID() string                        { return h.id }
func (h stubPreReqHandler) Order() int                        { return h.ord }
func (h stubPreReqHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (h stubPreReqHandler) Handle(ctx context.Context, call *lipapi.Call, meta prerequest.Meta, svc prerequest.Services) (prerequest.Decision, error) {
	if h.mu != nil && h.events != nil {
		h.mu.Lock()
		*h.events = append(*h.events, h.id)
		h.mu.Unlock()
	}
	if h.deny {
		return prerequest.Deny("denied by " + h.id), nil
	}
	return prerequest.Allow(), nil
}

type stubAttemptTransform struct {
	id     string
	ord    int
	events *[]string
	mu     *sync.Mutex
	mutate func(*lipapi.Call)
}

func (a stubAttemptTransform) ID() string                        { return a.id }
func (a stubAttemptTransform) Order() int                        { return a.ord }
func (a stubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (a stubAttemptTransform) HandleAttempt(ctx context.Context, call *lipapi.Call, meta request.AttemptMeta, svc request.Services) (request.AttemptDecision, error) {
	if a.mu != nil && a.events != nil {
		a.mu.Lock()
		*a.events = append(*a.events, a.id)
		a.mu.Unlock()
	}
	if a.mutate != nil {
		a.mutate(call)
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

// --- Parity and Snapshot Projection Tests ---

func TestRequestTransformsProjection_ParityWithFrozenAndRegistrationOrder(t *testing.T) {
	t.Parallel()

	var events []string
	var mu sync.Mutex

	b1 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		RequestTransforms: []request.Transform{
			stubReqTransform{id: "rt-1", ord: 10, events: &events, mu: &mu},
			stubReqTransform{id: "rt-2", ord: 20, events: &events, mu: &mu},
		},
		PreRequestHandlers: []prerequest.Handler{
			stubPreReqHandler{id: "pr-1", ord: 10, events: &events, mu: &mu},
		},
		AttemptTransforms: []request.AttemptTransform{
			stubAttemptTransform{id: "at-1", ord: 10, events: &events, mu: &mu},
		},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		RequestTransforms: []request.Transform{
			stubReqTransform{id: "rt-3", ord: 5, events: &events, mu: &mu},
		},
		PreRequestHandlers: []prerequest.Handler{
			stubPreReqHandler{id: "pr-2", ord: 5, events: &events, mu: &mu},
		},
		AttemptTransforms: []request.AttemptTransform{
			stubAttemptTransform{id: "at-2", ord: 5, events: &events, mu: &mu},
		},
	}

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	// Verify Frozen plane contents preserve registration order
	frozenRT := lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestTransforms)
	require.Len(t, frozenRT, 3)
	assert.Equal(t, "rt-1", frozenRT[0].ID())
	assert.Equal(t, "rt-2", frozenRT[1].ID())
	assert.Equal(t, "rt-3", frozenRT[2].ID())

	frozenPR := lipfeature.Get(gen.Frozen, lipfeature.PlanePreRequestHandlers)
	require.Len(t, frozenPR, 2)
	assert.Equal(t, "pr-1", frozenPR[0].ID())
	assert.Equal(t, "pr-2", frozenPR[1].ID())

	frozenAT := lipfeature.Get(gen.Frozen, lipfeature.PlaneAttemptTransforms)
	require.Len(t, frozenAT, 2)
	assert.Equal(t, "at-1", frozenAT[0].ID())
	assert.Equal(t, "at-2", frozenAT[1].ID())

	// Build runtime snapshot from FeaturePlanes directly
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{
		FeaturePlanes: gen.Frozen,
	}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	// Snapshot methods return defensive copies preserving order
	snapRT := snap.RequestTransforms()
	require.Len(t, snapRT, 3)
	assert.Equal(t, "rt-1", snapRT[0].ID())
	assert.Equal(t, "rt-2", snapRT[1].ID())
	assert.Equal(t, "rt-3", snapRT[2].ID())

	// PreRequestHandlers are materialized/sorted in snapshot
	snapPR := snap.PreRequestHandlers()
	require.Len(t, snapPR, 2)
	// MaterializeSorted sorts by Order then ID: pr-2 (ord 5) before pr-1 (ord 10)
	assert.Equal(t, "pr-2", snapPR[0].ID())
	assert.Equal(t, "pr-1", snapPR[1].ID())

	// AttemptTransforms are materialized/sorted in snapshot
	snapAT := snap.AttemptTransforms()
	require.Len(t, snapAT, 2)
	assert.Equal(t, "at-2", snapAT[0].ID())
	assert.Equal(t, "at-1", snapAT[1].ID())
}

func TestRequestTransformsProjection_NilVsEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("zero_value_bundles_produce_nil_planes", func(t *testing.T) {
		t.Parallel()
		gen, err := featurebundle.MergeBundlesGenerated(lipfeature.FeatureBundle{}, lipfeature.FeatureBundle{})
		require.NoError(t, err)

		assert.Nil(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestTransforms))
		assert.Nil(t, lipfeature.Get(gen.Frozen, lipfeature.PlanePreRequestHandlers))
		assert.Nil(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneAttemptTransforms))

		bus := hooks.New(hooks.Config{})
		opts := &BuildOptions{FeaturePlanes: gen.Frozen}
		snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

		assert.Nil(t, snap.RequestTransforms())
		assert.Nil(t, snap.PreRequestHandlers())
		assert.Nil(t, snap.AttemptTransforms())
	})

	t.Run("explicitly_empty_slices_normalize_properly", func(t *testing.T) {
		t.Parallel()
		b := lipfeature.FeatureBundle{
			SchemaVersion:      lipfeature.SchemaVersionV1,
			RequestTransforms:  []request.Transform{},
			PreRequestHandlers: []prerequest.Handler{},
			AttemptTransforms:  []request.AttemptTransform{},
		}
		gen, err := featurebundle.MergeBundlesGenerated(b)
		require.NoError(t, err)

		assert.Nil(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestTransforms))
		assert.Nil(t, lipfeature.Get(gen.Frozen, lipfeature.PlanePreRequestHandlers))
		assert.Nil(t, lipfeature.Get(gen.Frozen, lipfeature.PlaneAttemptTransforms))

		bus := hooks.New(hooks.Config{})
		opts := &BuildOptions{FeaturePlanes: gen.Frozen}
		snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

		assert.Nil(t, snap.RequestTransforms())
		assert.Nil(t, snap.PreRequestHandlers())
		assert.Nil(t, snap.AttemptTransforms())
	})
}

func TestRequestTransformsProjection_BackingArrayIsolation(t *testing.T) {
	t.Parallel()

	origRT := []request.Transform{stubReqTransform{id: "rt-orig"}}
	origPR := []prerequest.Handler{stubPreReqHandler{id: "pr-orig"}}
	origAT := []request.AttemptTransform{stubAttemptTransform{id: "at-orig"}}

	b := lipfeature.FeatureBundle{
		SchemaVersion:      lipfeature.SchemaVersionV1,
		RequestTransforms:  origRT,
		PreRequestHandlers: origPR,
		AttemptTransforms:  origAT,
	}

	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)

	// Mutate original slice backing array
	origRT[0] = stubReqTransform{id: "rt-mutated"}
	origPR[0] = stubPreReqHandler{id: "pr-mutated"}
	origAT[0] = stubAttemptTransform{id: "at-mutated"}

	// Frozen plane must retain original value
	assert.Equal(t, "rt-orig", lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestTransforms)[0].ID())
	assert.Equal(t, "pr-orig", lipfeature.Get(gen.Frozen, lipfeature.PlanePreRequestHandlers)[0].ID())
	assert.Equal(t, "at-orig", lipfeature.Get(gen.Frozen, lipfeature.PlaneAttemptTransforms)[0].ID())

	// Snapshot defensive copy isolation
	bus := hooks.New(hooks.Config{})
	opts := &BuildOptions{FeaturePlanes: gen.Frozen}
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)

	snapRT1 := snap.RequestTransforms()
	snapRT1[0] = stubReqTransform{id: "rt-snap-mutated"}
	snapRT2 := snap.RequestTransforms()
	assert.Equal(t, "rt-orig", snapRT2[0].ID())
}

func TestRequestTransformsProjection_RequestTransformsTypedNilRejection(t *testing.T) {
	t.Parallel()

	b := lipfeature.FeatureBundle{
		SchemaVersion:     lipfeature.SchemaVersionV1,
		RequestTransforms: []request.Transform{nil},
	}

	_, err := featurebundle.MergeBundlesGenerated(b)
	require.Error(t, err, "nil RequestTransforms element must fail MergeBundlesGenerated")
	var attrErr *lipfeature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, lipfeature.PlaneRequestTransforms.ID, attrErr.PlaneID)
}

func TestRequestTransformsProjection_PreRequestHandlersTypedNilRejection(t *testing.T) {
	t.Parallel()

	b := lipfeature.FeatureBundle{
		SchemaVersion:      lipfeature.SchemaVersionV1,
		PreRequestHandlers: []prerequest.Handler{nil},
	}

	_, err := featurebundle.MergeBundlesGenerated(b)
	require.Error(t, err, "nil PreRequestHandlers element must fail MergeBundlesGenerated")
	var attrErr *lipfeature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, lipfeature.PlanePreRequestHandlers.ID, attrErr.PlaneID)
}

func TestRequestTransformsProjection_AttemptTransformsTypedNilRejection(t *testing.T) {
	t.Parallel()

	b := lipfeature.FeatureBundle{
		SchemaVersion:     lipfeature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{nil},
	}

	_, err := featurebundle.MergeBundlesGenerated(b)
	require.Error(t, err, "nil AttemptTransforms element must fail MergeBundlesGenerated")
	var attrErr *lipfeature.AttributedError
	require.ErrorAs(t, err, &attrErr)
	assert.Equal(t, lipfeature.PlaneAttemptTransforms.ID, attrErr.PlaneID)
}

func TestRequestTransformsProjection_TransactionalBinderReplacement(t *testing.T) {
	t.Parallel()

	b := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{
			stubAttemptTransform{id: "at-initial-1"},
			stubAttemptTransform{id: "at-initial-2"},
		},
	}
	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)

	// Replace at-initial-1 with new implementation
	replacement := []request.AttemptTransform{
		stubAttemptTransform{id: "at-initial-1"},
	}
	updated, err := gen.BindAttemptTransforms("plugin-replacer", replacement)
	require.NoError(t, err)

	frozenAT := lipfeature.Get(updated.Frozen, lipfeature.PlaneAttemptTransforms)
	require.Len(t, frozenAT, 2)
	assert.Equal(t, "at-initial-2", frozenAT[0].ID())
	assert.Equal(t, "at-initial-1", frozenAT[1].ID())

	// Failed replacement (e.g. nil element) leaves candidate unmodified
	_, failErr := updated.BindAttemptTransforms("plugin-bad", []request.AttemptTransform{nil})
	require.Error(t, failErr)

	// Original updated candidate was not mutated
	frozenAfterFail := lipfeature.Get(updated.Frozen, lipfeature.PlaneAttemptTransforms)
	require.Len(t, frozenAfterFail, 2)
	assert.Equal(t, "at-initial-2", frozenAfterFail[0].ID())
	assert.Equal(t, "at-initial-1", frozenAfterFail[1].ID())
}

// --- End-to-End CompileGeneration and Request Execution Tests ---

func TestCompileGeneration_RequestTransformsExecution(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	var executedTransforms []string
	var executedPreReqs []string
	var executedAttempts []string
	var mu sync.Mutex

	// Register feature 1: request transform and pre-request handler
	require.NoError(t, reg.RegisterFeature("test-shaping-1", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			RequestTransforms: []request.Transform{
				stubReqTransform{
					id:     "tx-prefix",
					ord:    1,
					events: &executedTransforms,
					mu:     &mu,
					mutate: func(c *lipapi.Call) {
						if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
							c.Messages[0].Parts[0].Text = "[PREFIX] " + c.Messages[0].Parts[0].Text
						}
					},
				},
			},
			PreRequestHandlers: []prerequest.Handler{
				stubPreReqHandler{id: "pr-check", ord: 1, events: &executedPreReqs, mu: &mu},
			},
		}, nil
	}))

	// Register feature 2: attempt transform
	require.NoError(t, reg.RegisterFeature("test-shaping-2", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			AttemptTransforms: []request.AttemptTransform{
				stubAttemptTransform{
					id:     "at-modify",
					ord:    1,
					events: &executedAttempts,
					mu:     &mu,
					mutate: func(c *lipapi.Call) {
						if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
							c.Messages[0].Parts[0].Text = c.Messages[0].Parts[0].Text + " [ATTEMPT]"
						}
					},
				},
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

	cand := obsTestCandidateConfig(t, "test-shaping-1", "test-shaping-2")

	var gotMu sync.Mutex
	var capturedCall lipapi.Call
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
				gotMu.Lock()
				capturedCall = call
				gotMu.Unlock()
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
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hello world"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := lipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	assert.Equal(t, int64(1), capturedCallsCount.Load())

	// Verify execution order and mutations
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, executedTransforms, "tx-prefix")
	assert.Contains(t, executedPreReqs, "pr-check")
	assert.Contains(t, executedAttempts, "at-modify")

	// Verify text received both prefix transform and attempt transform
	gotMu.Lock()
	defer gotMu.Unlock()
	require.Len(t, capturedCall.Messages, 1)
	require.Len(t, capturedCall.Messages[0].Parts, 1)
	assert.Equal(t, "[PREFIX] hello world [ATTEMPT]", capturedCall.Messages[0].Parts[0].Text)
}

func TestCompileGeneration_DisabledFeatureContributesNothing(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	require.NoError(t, reg.RegisterFeature("test-disabled-shaping", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			RequestTransforms: []request.Transform{
				stubReqTransform{id: "disabled-tx"},
			},
			PreRequestHandlers: []prerequest.Handler{
				stubPreReqHandler{id: "disabled-pr"},
			},
			AttemptTransforms: []request.AttemptTransform{
				stubAttemptTransform{id: "disabled-at"},
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

	// Feature registered in factory catalog but NOT enabled in candidate features
	cand := obsTestCandidateConfig(t)

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

	snap := ex.RuntimeSnapshot
	require.NotNil(t, snap)

	assert.Nil(t, snap.RequestTransforms())
	assert.Nil(t, snap.PreRequestHandlers())
	assert.Nil(t, snap.AttemptTransforms())
}

func TestCompileGeneration_CandidateFeaturePlanesOverlayShaping(t *testing.T) {
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

	var executedTransforms []string
	var executedPreReqs []string
	var executedAttempts []string
	var mu sync.Mutex

	candBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		RequestTransforms: []request.Transform{
			stubReqTransform{
				id:     "cand-tx-prefix",
				ord:    1,
				events: &executedTransforms,
				mu:     &mu,
				mutate: func(c *lipapi.Call) {
					if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
						c.Messages[0].Parts[0].Text = "[CAND_PREFIX] " + c.Messages[0].Parts[0].Text
					}
				},
			},
		},
		PreRequestHandlers: []prerequest.Handler{
			stubPreReqHandler{id: "cand-pr-check", ord: 1, events: &executedPreReqs, mu: &mu},
		},
		AttemptTransforms: []request.AttemptTransform{
			stubAttemptTransform{
				id:     "cand-at-modify",
				ord:    1,
				events: &executedAttempts,
				mu:     &mu,
				mutate: func(c *lipapi.Call) {
					if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
						c.Messages[0].Parts[0].Text = c.Messages[0].Parts[0].Text + " [CAND_ATTEMPT]"
					}
				},
			},
		},
	}
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	cand := obsTestCandidateConfig(t)

	var gotMu sync.Mutex
	var capturedCall lipapi.Call
	var capturedCallsCount atomic.Int64

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
				gotMu.Lock()
				capturedCall = call
				gotMu.Unlock()
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
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "candidate input"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := lipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	assert.Equal(t, int64(1), capturedCallsCount.Load())

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, executedTransforms, "cand-tx-prefix")
	assert.Contains(t, executedPreReqs, "cand-pr-check")
	assert.Contains(t, executedAttempts, "cand-at-modify")

	gotMu.Lock()
	defer gotMu.Unlock()
	require.Len(t, capturedCall.Messages, 1)
	require.Len(t, capturedCall.Messages[0].Parts, 1)
	assert.Equal(t, "[CAND_PREFIX] candidate input [CAND_ATTEMPT]", capturedCall.Messages[0].Parts[0].Text)
}

func TestCompileGeneration_CandidateFeaturePlanes_UnrelatedPlanesIgnored(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	var (
		rtCalled bool
		atCalled bool
		mu       sync.Mutex
	)

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
	// - RequestTransforms
	// - PreRequestHandlers
	// - AttemptTransforms
	// AND populated UNRELATED planes that must be ignored for this wave:
	// - TrafficObservers
	// - UsageObservers
	// - SubmitHooks
	// - StreamObserverFactories
	// - TerminalDecisionProvider
	candBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		RequestTransforms: []request.Transform{
			stubReqTransform{
				id:  "cand-rt",
				ord: 1,
				mutate: func(c *lipapi.Call) {
					mu.Lock()
					rtCalled = true
					mu.Unlock()
				},
			},
		},
		PreRequestHandlers: []prerequest.Handler{
			stubPreReqHandler{
				id:  "cand-pr",
				ord: 1,
			},
		},
		AttemptTransforms: []request.AttemptTransform{
			stubAttemptTransform{
				id:  "cand-at",
				ord: 1,
				mutate: func(c *lipapi.Call) {
					mu.Lock()
					atCalled = true
					mu.Unlock()
				},
			},
		},
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{
				id:     "unrelated-cand-to",
				events: &[]string{},
				mu:     &mu,
			},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{
				id:     "unrelated-cand-uo",
				events: &[]string{},
				mu:     &mu,
			},
		},
		SubmitHooks: []sdkhooks.SubmitHook{
			stubSubmitHook{
				id:    "unrelated-cand-submit",
				order: 1,
			},
		},
		StreamObserverFactories: []response.StreamObserverFactory{
			stubStreamObsFactory{
				id: "unrelated-cand-so",
			},
		},
		TerminalDecisionProvider: charTerminalProvider{
			id: "unrelated-cand-terminal",
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
			if in.Frontends.TrafficPorts.Obs != nil {
				_ = in.Frontends.TrafficPorts.Obs.OnObservation(ctx, traffic.Observation{Leg: traffic.LegCTP})
			}
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
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "negative candidate test"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := lipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	mu.Lock()
	defer mu.Unlock()

	// Candidate request-shaping planes were executed
	assert.True(t, rtCalled, "candidate request transform must run")
	assert.True(t, atCalled, "candidate attempt transform must run")

	// Unrelated candidate planes were ignored
	snap := ex.RuntimeSnapshot
	require.NotNil(t, snap)
	assert.Equal(t, traffic.NoopObserver{}, snap.TrafficObserver(), "unrelated candidate traffic observer must be ignored")
	assert.Equal(t, usage.NoopObserver{}, snap.UsageObserver(), "unrelated candidate usage observer must be ignored")
	assert.Empty(t, snap.StreamObserverFactories(), "unrelated candidate stream observer factory must be ignored")
	assert.Nil(t, snap.TerminalDecisionProvider(), "unrelated candidate terminal decision provider must be ignored")
}

func TestCompileGeneration_CandidateFeaturePlanes_ReasoningPreservationAttemptReplacement(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	egressRef := "egress-cand-reasoning-replace"
	node := reasoningYAMLForTypedNil(t, egressRef)

	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
		return fixedRunnerForTypedNil{id: "reasoning-cand-runner"}
	}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheduler.Close() })

	prod := ProductionOptions{
		ReasoningCompression: ReasoningCompressionOptions{
			EgressPolicies: map[string]reasoningpreservation.EgressPolicy{
				egressRef: charEgressPolicy{version: "v1"},
			},
			MatcherResolver: charMatcherResolver{},
		},
	}

	cfg := obsTestProcessConfig()
	cfg.Plugins.Features = []config.PluginConfig{
		{ID: reasoningpreservation.ID, Enabled: true, Config: node},
	}
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:           cfg,
		Log:           testkit.DiscardLogger(),
		Opts:          &BuildOptions{PluginRegistry: reg, Production: prod},
		BackgroundAux: scheduler,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	var executionSequence []string
	var mu sync.Mutex

	reasoningXformID := reasoningpreservation.ID + "-transform"

	// Candidate overlay: contributes cand-at-1, a stub attempt transform with matching reasoning ID, and cand-at-2
	candBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		RequestTransforms: []request.Transform{
			stubReqTransform{
				id:     "cand-rt",
				ord:    1,
				events: &executionSequence,
				mu:     &mu,
			},
		},
		AttemptTransforms: []request.AttemptTransform{
			stubAttemptTransform{
				id:     "cand-at-survivor-1",
				ord:    -10,
				events: &executionSequence,
				mu:     &mu,
			},
			stubAttemptTransform{
				id:     reasoningXformID,
				ord:    0,
				events: &executionSequence,
				mu:     &mu,
				mutate: func(c *lipapi.Call) {
					if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
						c.Messages[0].Parts[0].Text += " [CAND_RAW_STUB]"
					}
				},
			},
			stubAttemptTransform{
				id:     "cand-at-survivor-2",
				ord:    10,
				events: &executionSequence,
				mu:     &mu,
			},
		},
	}
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	cand := obsTestCandidateConfig(t)
	cand.Plugins.Features = []config.PluginConfig{
		{ID: reasoningpreservation.ID, Enabled: true, Config: node},
	}

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

	var gotMu sync.Mutex
	var capturedCall lipapi.Call

	ex.Backends = map[string]execbackend.Backend{
		"stub-backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				gotMu.Lock()
				capturedCall = call
				gotMu.Unlock()
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	snap := ex.RuntimeSnapshot
	require.NotNil(t, snap)
	xforms := snap.AttemptTransforms()
	require.Len(t, xforms, 3)
	assert.Equal(t, "cand-at-survivor-1", xforms[0].ID())
	assert.Equal(t, reasoningXformID, xforms[1].ID())
	assert.Equal(t, "cand-at-survivor-2", xforms[2].ID())

	inputCall := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "reasoning test"}}},
		},
	}

	stream, execErr := ex.Execute(context.Background(), inputCall)
	require.NoError(t, execErr)
	require.NotNil(t, stream)

	_, collectErr := lipapi.Collect(context.Background(), stream)
	require.NoError(t, collectErr)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, executionSequence, 3)
	assert.Equal(t, "cand-rt", executionSequence[0])
	assert.Equal(t, "cand-at-survivor-1", executionSequence[1])
	assert.Equal(t, "cand-at-survivor-2", executionSequence[2])

	gotMu.Lock()
	defer gotMu.Unlock()
	assert.NotContains(t, capturedCall.Messages[0].Parts[0].Text, "[CAND_RAW_STUB]", "candidate stub transform must be replaced by bound generation binder")
}

func TestCompileGeneration_PreRequestDenyDecisionEvidence(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)

	var txCalled, atCalled, backendCalled bool
	var mu sync.Mutex

	require.NoError(t, reg.RegisterFeature("test-deny-feature", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			PreRequestHandlers: []prerequest.Handler{
				stubPreReqHandler{id: "pr-deny-gate", ord: 1, deny: true},
			},
			RequestTransforms: []request.Transform{
				stubReqTransform{
					id:  "tx-should-not-run",
					ord: 1,
					mutate: func(c *lipapi.Call) {
						mu.Lock()
						txCalled = true
						mu.Unlock()
					},
				},
			},
			AttemptTransforms: []request.AttemptTransform{
				stubAttemptTransform{
					id:  "at-should-not-run",
					ord: 1,
					mutate: func(c *lipapi.Call) {
						mu.Lock()
						atCalled = true
						mu.Unlock()
					},
				},
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

	cand := obsTestCandidateConfig(t, "test-deny-feature")

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
				mu.Lock()
				backendCalled = true
				mu.Unlock()
				return nil, nil
			},
		},
	}

	inputCall := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub-backend:default"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "should deny"}}},
		},
	}

	_, execErr := ex.Execute(context.Background(), inputCall)
	require.Error(t, execErr, "pre-request deny must fail execution")
	assert.Contains(t, execErr.Error(), "denied by pr-deny-gate")

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, txCalled, "request transform runs before pre-request stage")
	assert.False(t, atCalled, "attempt transform must not run when pre-request denies")
	assert.False(t, backendCalled, "backend must not run when pre-request denies")
}
