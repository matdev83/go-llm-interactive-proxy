package runtimebundle

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	sdksg "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Minimal Typed-Nil Stubs for Parity Characterization ---

type charStubTerminalProvider struct {
	id    string
	panic bool
}

func (p *charStubTerminalProvider) ID() string {
	if p == nil {
		return ""
	}
	if p.panic {
		panic("boom-terminal-provider-id")
	}
	return p.id
}

func (p *charStubTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "stop"}, nil
}

type charStubLocalTurnHandler struct {
	id  string
	ord int
}

func (h *charStubLocalTurnHandler) ID() string {
	if h == nil {
		return ""
	}
	return h.id
}

func (h *charStubLocalTurnHandler) Order() int {
	if h == nil {
		return 0
	}
	return h.ord
}
func (h *charStubLocalTurnHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h *charStubLocalTurnHandler) Match(context.Context, lipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h *charStubLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

type charStubSubmitHook struct{ tag string }

func (h charStubSubmitHook) ID() string                      { return h.tag }
func (charStubSubmitHook) Order() int                        { return 0 }
func (charStubSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubSubmitHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type charStubRequestPartHook struct{ tag string }

func (h charStubRequestPartHook) ID() string                      { return h.tag }
func (charStubRequestPartHook) Order() int                        { return 0 }
func (charStubRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubRequestPartHook) HandleRequestParts(context.Context, *lipapi.Call, sdkhooks.PartMeta) error {
	return nil
}

type charStubResponsePartHook struct{ tag string }

func (h charStubResponsePartHook) ID() string                      { return h.tag }
func (charStubResponsePartHook) Order() int                        { return 0 }
func (charStubResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubResponsePartHook) HandleEvent(context.Context, *lipapi.Event, sdkhooks.PartMeta) error {
	return nil
}

type charStubToolReactor struct{ tag string }

func (h charStubToolReactor) ID() string { return h.tag }
func (charStubToolReactor) Order() int   { return 0 }
func (charStubToolReactor) HandleToolEvent(context.Context, lipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

type charStubLifecycle struct{ tag string }

func (charStubLifecycle) Start(context.Context) error { return nil }
func (charStubLifecycle) Stop(context.Context) error  { return nil }

type charStubOpener struct{ tag string }

func (h charStubOpener) ID() string { return h.tag }
func (charStubOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type charStubResolver struct{ tag string }

func (charStubResolver) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{ProjectRoot: "/tmp"}, nil
}

type charStubCatalogFilter struct{ tag string }

func (h charStubCatalogFilter) ID() string                      { return h.tag }
func (charStubCatalogFilter) Order() int                        { return 0 }
func (charStubCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubCatalogFilter) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type charStubPolicy struct{ tag string }

func (h *charStubPolicy) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}
func (h *charStubPolicy) Order() int                        { return 0 }
func (h *charStubPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h *charStubPolicy) Handle(context.Context, lipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type charStubFinalizer struct{ tag string }

func (h *charStubFinalizer) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}
func (h *charStubFinalizer) Order() int { return 0 }
func (h *charStubFinalizer) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type charStubTransform struct{ tag string }

func (h *charStubTransform) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}
func (h *charStubTransform) Order() int                        { return 0 }
func (h *charStubTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h *charStubTransform) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type charStubPreReq struct{ tag string }

func (h *charStubPreReq) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}
func (h *charStubPreReq) Order() int                        { return 0 }
func (h *charStubPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h *charStubPreReq) Handle(context.Context, *lipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type charStubRouteHint struct{ tag string }

func (h charStubRouteHint) ID() string                      { return h.tag }
func (charStubRouteHint) Order() int                        { return 0 }
func (charStubRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubRouteHint) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type charStubCompGate struct{ tag string }

func (h *charStubCompGate) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}
func (h *charStubCompGate) Order() int                        { return 0 }
func (h *charStubCompGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubCompGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type charStubAttemptTransform struct{ tag string }

func (h charStubAttemptTransform) ID() string                      { return h.tag }
func (charStubAttemptTransform) Order() int                        { return 0 }
func (charStubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (charStubAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type charStubStreamObserverFactory struct{ tag string }

func (h charStubStreamObserverFactory) ID() string                      { return h.tag }
func (charStubStreamObserverFactory) Order() int                        { return 0 }
func (charStubStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type charStubTrafficObs struct{ tag string }

func (charStubTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type charStubUsageObs struct{ tag string }

func (charStubUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

type charStubRawSink struct{ tag string }

func (charStubRawSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return nil
}

type charStubRedactor struct{ tag string }

func (h *charStubRedactor) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}

func (h *charStubRedactor) Redact(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type charStubCompactionObs struct{ tag string }

func (charStubCompactionObs) OnCompaction(context.Context, compaction.Event) error { return nil }

type charStubCompactionPreserver struct{ tag string }

func (p charStubCompactionPreserver) ID() string { return p.tag }
func (charStubCompactionPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charStubCompactionPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charStubCompactionPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type charStubSGGuard struct {
	id  string
	ord int
}

func (g *charStubSGGuard) ID() string {
	if g == nil {
		return ""
	}
	return g.id
}

func (g *charStubSGGuard) Order() int {
	if g == nil {
		return 0
	}
	return g.ord
}
func (g *charStubSGGuard) FailureMode() sdksg.FailureMode { return sdksg.FailClosed }
func (g *charStubSGGuard) Evaluate(context.Context, *lipapi.Call, sdksg.Meta, sdksg.Services) (sdksg.Decision, error) {
	return sdksg.Decision{Outcome: sdksg.OutcomePass}, nil
}

// --- Acceptance Criteria 3: Terminal-Decision Typed-Nil & Fail-Before-Mutate ---

// TestTerminalDecision_TypedNilFailBeforeMutateAndCompileGeneration pins Requirement 1.2, 1.4, 4.2:
// - FeatureBundle.Validate() returns ErrInvalidProvider on typed-nil provider.
// - MergedFeatureSurface.Append() returns ErrInvalidProvider on typed-nil provider.
// - Append fails BEFORE any receiver mutation: fields on MergedFeatureSurface remain identical.
// - Append validates receiver and incoming provider identity before checking conflict.
// - CompileGeneration rejects typed-nil provider fail-closed.
// - overlayExtensions carries typed-nil provider when dst is nil, which fails candidate compile.
func TestTerminalDecision_TypedNilFailBeforeMutateAndCompileGeneration(t *testing.T) {
	t.Parallel()

	var typedNilProvider *charStubTerminalProvider

	t.Run("feature_bundle_validate_rejects_typed_nil", func(t *testing.T) {
		t.Parallel()
		b := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			TerminalDecisionProvider: typedNilProvider,
		}
		err := b.Validate()
		require.Error(t, err)
		assert.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		assert.Contains(t, err.Error(), "TerminalDecisionProvider: terminaldecision: invalid provider")
	})

	t.Run("merged_feature_surface_append_rejects_typed_nil_and_fails_before_mutate", func(t *testing.T) {
		t.Parallel()
		var m featurebundle.MergedFeatureSurface
		// Seed receiver with some initial data
		m.SecretGuards = []sdksg.Guard{&charStubSGGuard{id: "initial-guard", ord: 1}}
		m.Lifecycles = []lipplugin.Lifecycle{charStubLifecycle{tag: "initial-lifecycle"}}

		snapshotBefore := m // value copy

		b := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			SecretGuards:             []sdksg.Guard{&charStubSGGuard{id: "incoming-guard", ord: 2}},
			TerminalDecisionProvider: typedNilProvider,
		}

		err := m.Append(b)
		require.Error(t, err)
		assert.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		assert.Equal(t, "featurebundle: contributed terminal-decision provider: terminaldecision: invalid provider", err.Error())

		// Fail-before-mutate assertion: receiver is unchanged
		assert.True(t, reflect.DeepEqual(snapshotBefore, m), "MergedFeatureSurface receiver must not be mutated on validation error")
		assert.Len(t, m.SecretGuards, 1)
		assert.Equal(t, "initial-guard", m.SecretGuards[0].ID())
	})

	t.Run("incoming_typed_nil_fails_identity_before_conflict_check", func(t *testing.T) {
		t.Parallel()
		var m featurebundle.MergedFeatureSurface
		m.TerminalDecisionProvider = &charStubTerminalProvider{id: "valid-active"}

		b := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			TerminalDecisionProvider: typedNilProvider,
		}

		err := m.Append(b)
		require.Error(t, err)
		// Identity error must win over conflict error
		assert.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		assert.False(t, errors.Is(err, featurebundle.ErrTerminalDecisionProviderConflict))
		assert.Equal(t, "featurebundle: contributed terminal-decision provider: terminaldecision: invalid provider", err.Error())
	})

	t.Run("receiver_with_typed_nil_fails_merged_validation", func(t *testing.T) {
		t.Parallel()
		var m featurebundle.MergedFeatureSurface
		m.TerminalDecisionProvider = typedNilProvider

		b := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			TerminalDecisionProvider: &charStubTerminalProvider{id: "valid-incoming"},
		}

		err := m.Append(b)
		require.Error(t, err)
		assert.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		assert.Equal(t, "featurebundle: merged terminal-decision provider: terminaldecision: invalid provider", err.Error())
	})

	t.Run("compile_generation_rejects_typed_nil_provider", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Routing:     config.RoutingConfig{MaxAttempts: 3},
			Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
			Server:      config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
			Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		}
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg:  cfg,
			Log:  slog.Default(),
			Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process: ps,
			CandidateOpts: &BuildOptions{
				Extensions: ExtensionsOptions{
					TerminalDecisionProvider: typedNilProvider,
				},
			},
			Compose: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
				return http.NotFoundHandler(), nil
			},
		})
		require.Error(t, err)
		assert.Nil(t, gen)
		assert.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		assert.Equal(t, "runtimebundle: terminal decision provider: terminaldecision: invalid provider", err.Error())
	})

	t.Run("overlay_extensions_carries_typed_nil_when_dst_nil", func(t *testing.T) {
		t.Parallel()
		dst := ExtensionsOptions{}
		src := ExtensionsOptions{TerminalDecisionProvider: typedNilProvider}
		overlayExtensions(&dst, src)
		// Typed nil is copied onto dst because dst was nil
		assert.Equal(t, typedNilProvider, dst.TerminalDecisionProvider)
		// But validation in CompileGeneration fails
		err := validateTerminalDecisionProvider(dst.TerminalDecisionProvider)
		require.Error(t, err)
		assert.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
	})
}

// --- Acceptance Criteria 4: Nil Policy for Ordered Interface-Valued Planes ---

// TestPlaneParity_OrderedInterfacePlanesNilPolicyCensus characterizes the exact nil policy
// across all 24 ordered interface-valued planes:
// 1. FeatureBundle.Validate():
//   - Exactly 4 planes reject nil interface elements (AttemptTransforms, StreamObserverFactories, CompactionPreservers, LocalTurnHandlers).
//   - The remaining 20 planes accept slices with nil interface elements without error.
//
// 2. MergedFeatureSurface.Append():
//   - All 24 planes append slices verbatim, preserving nil interface elements and relative slice positions.
//
// 3. extensionsFromMerged() and overlayExtensions():
//   - Nil interface elements are copied and appended without omission across all projected slice planes.
//
// 4. RequestRuntimeSnapshot:
//   - MaterializeSorted filters nil on: SecretGuards (filters untyped/typed nil), LocalTurnHandlers (filters untyped/typed nil), ToolCallFinalizers (filters untyped nil), TrafficRedactors (filters untyped nil).
//   - toolpolicy.MaterializeSorted sorts non-nils and preserves nils at the end.
//   - Cloned planes preserve nil elements (SessionOpeners, ToolCatalogFilters, RouteHintProviders, CompactionObservers, CompactionPreservers, RequestTransforms, CompletionGates).
//   - Strict planes panic if nil passed: AttemptTransforms, StreamObserverFactories.
func TestPlaneParity_OrderedInterfacePlanesNilPolicyCensus(t *testing.T) {
	t.Parallel()

	t.Run("feature_bundle_validation_nil_policy_census", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name      string
			bundle    lipfeature.FeatureBundle
			wantError bool
			errSubstr string
		}{
			{"SubmitHooks_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, SubmitHooks: []sdkhooks.SubmitHook{nil}}, false, ""},
			{"RequestPartHooks_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, RequestPartHooks: []sdkhooks.RequestPartHook{nil}}, false, ""},
			{"ResponsePartHooks_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ResponsePartHooks: []sdkhooks.ResponsePartHook{nil}}, false, ""},
			{"ToolReactors_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolReactors: []sdkhooks.ToolReactor{nil}}, false, ""},
			{"Lifecycles_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, Lifecycles: []lipplugin.Lifecycle{nil}}, false, ""},
			{"SessionOpeners_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, SessionOpeners: []session.Opener{nil}}, false, ""},
			{"WorkspaceResolvers_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, WorkspaceResolvers: []lipworkspace.Resolver{nil}}, false, ""},
			{"ToolCatalogFilters_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCatalogFilters: []toolcatalog.Filter{nil}}, false, ""},
			{"ToolCallPolicies_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallPolicies: []toolpolicy.Policy{nil}}, false, ""},
			{"ToolCallFinalizers_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizers: []toolcall.Finalizer{nil}}, false, ""},
			{"RequestTransforms_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, RequestTransforms: []request.Transform{nil}}, false, ""},
			{"PreRequestHandlers_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, PreRequestHandlers: []prerequest.Handler{nil}}, false, ""},
			{"RouteHintProviders_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, RouteHintProviders: []routehint.Provider{nil}}, false, ""},
			{"CompletionGates_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, CompletionGates: []completion.Gate{nil}}, false, ""},
			{"TrafficObservers_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, TrafficObservers: []traffic.Observer{nil}}, false, ""},
			{"UsageObservers_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, UsageObservers: []usage.Observer{nil}}, false, ""},
			{"RawCaptureSinks_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, RawCaptureSinks: []traffic.RawCaptureSink{nil}}, false, ""},
			{"TrafficRedactors_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, TrafficRedactors: []traffic.Redactor{nil}}, false, ""},
			{"CompactionObservers_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, CompactionObservers: []compaction.Observer{nil}}, false, ""},
			{"SecretGuards_accepts_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, SecretGuards: []sdksg.Guard{nil}}, false, ""},

			// Strictly rejecting planes:
			{"AttemptTransforms_rejects_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, AttemptTransforms: []request.AttemptTransform{nil}}, true, "AttemptTransforms[0] must not be nil"},
			{"StreamObserverFactories_rejects_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, StreamObserverFactories: []response.StreamObserverFactory{nil}}, true, "StreamObserverFactories[0] must not be nil"},
			{"CompactionPreservers_rejects_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, CompactionPreservers: []compaction.Preserver{nil}}, true, "CompactionPreservers[0] must not be nil"},
			{"LocalTurnHandlers_rejects_nil", lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, LocalTurnHandlers: []localturn.Handler{nil}}, true, "LocalTurnHandlers[0] must not be nil"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				err := tt.bundle.Validate()
				if tt.wantError {
					require.Error(t, err)
					assert.Contains(t, err.Error(), tt.errSubstr)
				} else {
					require.NoError(t, err)
				}
			})
		}
	})

	t.Run("merged_feature_surface_append_preserves_nil_elements_verbatim", func(t *testing.T) {
		t.Parallel()
		var m featurebundle.MergedFeatureSurface
		b := lipfeature.FeatureBundle{
			SchemaVersion:      lipfeature.SchemaVersionV1,
			SecretGuards:       []sdksg.Guard{nil, &charStubSGGuard{id: "sg1", ord: 1}, nil},
			RouteHintProviders: []routehint.Provider{charStubRouteHint{tag: "rh1"}, nil},
		}

		require.NoError(t, m.Append(b))
		require.Len(t, m.SecretGuards, 3)
		assert.Nil(t, m.SecretGuards[0])
		assert.NotNil(t, m.SecretGuards[1])
		assert.Nil(t, m.SecretGuards[2])

		// Extensions extraction and overlay also preserve verbatim on projected slice planes
		ext := extensionsFromMerged(m, featurebundle.GeneratedMergeSurface{}, nil)
		require.Len(t, ext.SecretGuards, 3)
		assert.Nil(t, ext.SecretGuards[0])

		dst := ExtensionsOptions{}
		overlayExtensions(&dst, ext)
		require.Len(t, dst.SecretGuards, 3)
		assert.Nil(t, dst.SecretGuards[0])
	})
}

// TestPlaneParity_SnapshotMaterializationNilAndTypedNilFiltering tests the behavior
// of NewRequestRuntimeSnapshot on planes with nil and typed-nil elements:
// - SecretGuards: MaterializeSorted filters untyped nil and typed nil guards.
// - LocalTurnHandlers: MaterializeSorted filters untyped nil and typed nil handlers.
// - ToolCallFinalizers: MaterializeSorted filters nil.
// - TrafficRedactors: MaterializeSortedRedactors filters nil.
// - ToolCallPolicies: MaterializeSorted preserves nil (sorted to end).
// - Cloned planes (SessionOpeners, ToolCatalogFilters, RouteHintProviders, CompactionObservers, CompactionPreservers, RequestTransforms, CompletionGates) preserve nil elements.
// - Strict planes panic on nil: AttemptTransforms, StreamObserverFactories.
func TestPlaneParity_SnapshotMaterializationNilAndTypedNilFiltering(t *testing.T) {
	t.Parallel()

	var typedNilSGGuard *charStubSGGuard
	var typedNilLTHandler *charStubLocalTurnHandler
	var typedNilFinalizer *charStubFinalizer
	var typedNilRedactor *charStubRedactor

	snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		SecretGuardPlane: extensions.SecretGuardPlane{
			Guards: []sdksg.Guard{
				nil,
				typedNilSGGuard,
				&charStubSGGuard{id: "sg-valid-2", ord: 20},
				&charStubSGGuard{id: "sg-valid-1", ord: 10},
				nil,
			},
		},
		LocalTurnHandlers: []localturn.Handler{
			nil,
			typedNilLTHandler,
			&charStubLocalTurnHandler{id: "lt-valid-2", ord: 20},
			&charStubLocalTurnHandler{id: "lt-valid-1", ord: 10},
		},
		ToolCallFinalizers: []toolcall.Finalizer{
			nil,
			typedNilFinalizer,
			&charStubFinalizer{tag: "tf-valid"},
		},
		TrafficRedactors: []traffic.Redactor{
			nil,
			typedNilRedactor,
			&charStubRedactor{tag: "tr-valid"},
		},
		ToolCallPolicies: []toolpolicy.Policy{
			&charStubPolicy{tag: "tp-valid"},
			nil,
		},
		PreRequestHandlers: []prerequest.Handler{
			&charStubPreReq{tag: "pr-valid-2"},
			&charStubPreReq{tag: "pr-valid-1"},
		},
		RequestTransforms: []request.Transform{
			nil,
			&charStubTransform{tag: "rt-valid"},
		},
		CompletionGates: []completion.Gate{
			nil,
			&charStubCompGate{tag: "cg-valid"},
		},
		SessionOpeners:     []session.Opener{charStubOpener{tag: "so-1"}, nil},
		ToolCatalogFilters: []toolcatalog.Filter{nil, charStubCatalogFilter{tag: "cf-1"}},
		RouteHintProviders: []routehint.Provider{charStubRouteHint{tag: "rh-1"}, nil},
	})

	// 1. Reflection-based filtering planes: filters BOTH untyped nil and typed nil
	sgExec := snap.SecretGuardExecutionPlane().Guards
	require.Len(t, sgExec, 2)
	assert.Equal(t, "sg-valid-1", sgExec[0].ID())
	assert.Equal(t, "sg-valid-2", sgExec[1].ID())

	ltExec := snap.LocalTurnHandlers()
	require.Len(t, ltExec, 2)
	assert.Equal(t, "lt-valid-1", ltExec[0].ID())
	assert.Equal(t, "lt-valid-2", ltExec[1].ID())

	// 2. Interface nil filtering planes: filters untyped nil (typed nil with nil-safe methods retained)
	tfExec := snap.ToolCallFinalizers()
	require.Len(t, tfExec, 2)
	assert.Nil(t, tfExec[0])
	assert.Equal(t, "tf-valid", tfExec[1].ID())

	trExec := snap.TrafficRedactors()
	require.Len(t, trExec, 2)
	assert.Nil(t, trExec[0])
	assert.Equal(t, "tr-valid", trExec[1].ID())

	// 2. toolpolicy MaterializeSorted preserves nil at the end
	tpExec := snap.ToolCallPolicies()
	require.Len(t, tpExec, 2)
	assert.Equal(t, "tp-valid", tpExec[0].ID())
	assert.Nil(t, tpExec[1])

	// 3. Direct clone planes: nil elements preserved verbatim
	rtExec := snap.RequestTransforms()
	require.Len(t, rtExec, 2)
	assert.Nil(t, rtExec[0])
	assert.NotNil(t, rtExec[1])

	cgExec := snap.CompletionGates()
	require.Len(t, cgExec, 2)
	assert.Nil(t, cgExec[0])
	assert.NotNil(t, cgExec[1])

	soSnap := snap.SessionOpeners()
	require.Len(t, soSnap, 2)
	assert.NotNil(t, soSnap[0])
	assert.Nil(t, soSnap[1])

	cfSnap := snap.ToolCatalogFilters()
	require.Len(t, cfSnap, 2)
	assert.Nil(t, cfSnap[0])
	assert.NotNil(t, cfSnap[1])

	rhSnap := snap.RouteHintProviders()
	require.Len(t, rhSnap, 2)
	assert.NotNil(t, rhSnap[0])
	assert.Nil(t, rhSnap[1])

	// 4. Strict planes panic on nil
	assert.Panics(t, func() {
		extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
			AttemptTransforms: []request.AttemptTransform{nil},
		})
	})
	assert.Panics(t, func() {
		extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
			StreamObserverFactories: []response.StreamObserverFactory{nil},
		})
	})
}

// TestPlaneParity_FailBeforeMutateOnInvalidInterfaceValues verifies that an invalid
// contribution (panicking provider, invalid identity, etc.) fails BEFORE modifying
// any field of the receiver MergedFeatureSurface.
func TestPlaneParity_FailBeforeMutateOnInvalidInterfaceValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		provider terminaldecision.Provider
		wantErr  string
	}{
		{
			name:     "typed_nil",
			provider: (*charStubTerminalProvider)(nil),
			wantErr:  "terminaldecision: invalid provider",
		},
		{
			name:     "empty_id",
			provider: &charStubTerminalProvider{id: ""},
			wantErr:  "provider identity is required",
		},
		{
			name:     "whitespace_id",
			provider: &charStubTerminalProvider{id: "   "},
			wantErr:  "provider identity is required",
		},
		{
			name:     "panicking_id",
			provider: &charStubTerminalProvider{panic: true},
			wantErr:  "provider identity unavailable",
		},
		{
			name:     "invalid_utf8_in_id",
			provider: &charStubTerminalProvider{id: "\xff\xfe\xfd"},
			wantErr:  "provider identity is not valid UTF-8",
		},
		{
			name:     "exceeds_max_bytes_in_id",
			provider: &charStubTerminalProvider{id: strings.Repeat("a", terminaldecision.MaxProviderIDBytes+1)},
			wantErr:  "provider identity exceeds 128 bytes",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var m featurebundle.MergedFeatureSurface
			m.SecretGuards = []sdksg.Guard{&charStubSGGuard{id: "guard-1", ord: 1}}

			snapBefore := m // value copy

			b := lipfeature.FeatureBundle{
				SchemaVersion:            lipfeature.SchemaVersionV1,
				SecretGuards:             []sdksg.Guard{&charStubSGGuard{id: "guard-2", ord: 2}},
				TerminalDecisionProvider: tc.provider,
			}

			err := m.Append(b)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.True(t, reflect.DeepEqual(snapBefore, m), "MergedFeatureSurface receiver must not be mutated on failure")
		})
	}
}
