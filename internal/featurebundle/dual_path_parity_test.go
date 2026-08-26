package featurebundle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/planeparity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stubs for Parity Testing ---

type paritySubmitHook struct{ tag string }

func (h paritySubmitHook) ID() string                      { return h.tag }
func (paritySubmitHook) Order() int                        { return 0 }
func (paritySubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (paritySubmitHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type parityRequestPartHook struct{ tag string }

func (h parityRequestPartHook) ID() string                      { return h.tag }
func (parityRequestPartHook) Order() int                        { return 0 }
func (parityRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (parityRequestPartHook) HandleRequestParts(context.Context, *lipapi.Call, sdkhooks.PartMeta) error {
	return nil
}

type parityResponsePartHook struct{ tag string }

func (h parityResponsePartHook) ID() string                      { return h.tag }
func (parityResponsePartHook) Order() int                        { return 0 }
func (parityResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (parityResponsePartHook) HandleEvent(context.Context, *lipapi.Event, sdkhooks.PartMeta) error {
	return nil
}

type parityToolReactor struct{ tag string }

func (h parityToolReactor) ID() string { return h.tag }
func (parityToolReactor) Order() int   { return 0 }
func (parityToolReactor) HandleToolEvent(context.Context, lipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

type parityLifecycle struct{ tag string }

func (parityLifecycle) Start(context.Context) error { return nil }
func (parityLifecycle) Stop(context.Context) error  { return nil }

type parityOpener struct{ tag string }

func (h parityOpener) ID() string { return h.tag }
func (parityOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type parityResolver struct{ tag string }

func (parityResolver) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{ProjectRoot: "/tmp"}, nil
}

type parityCatalogFilter struct{ tag string }

func (h parityCatalogFilter) ID() string                      { return h.tag }
func (parityCatalogFilter) Order() int                        { return 0 }
func (parityCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (parityCatalogFilter) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type parityPolicy struct{ tag string }

func (h parityPolicy) ID() string                      { return h.tag }
func (parityPolicy) Order() int                        { return 0 }
func (parityPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (parityPolicy) Handle(context.Context, lipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type parityFinalizer struct{ tag string }

func (h parityFinalizer) ID() string { return h.tag }
func (parityFinalizer) Order() int   { return 0 }
func (parityFinalizer) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type parityTransform struct{ tag string }

func (h parityTransform) ID() string                      { return h.tag }
func (parityTransform) Order() int                        { return 0 }
func (parityTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (parityTransform) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type parityPreReq struct{ tag string }

func (h parityPreReq) ID() string                      { return h.tag }
func (parityPreReq) Order() int                        { return 0 }
func (parityPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (parityPreReq) Handle(context.Context, *lipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type parityRouteHint struct{ tag string }

func (h parityRouteHint) ID() string                      { return h.tag }
func (parityRouteHint) Order() int                        { return 0 }
func (parityRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (parityRouteHint) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type parityCompGate struct{ tag string }

func (h parityCompGate) ID() string                      { return h.tag }
func (parityCompGate) Order() int                        { return 0 }
func (parityCompGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (parityCompGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type parityAttemptTransform struct{ tag string }

func (h parityAttemptTransform) ID() string                      { return h.tag }
func (parityAttemptTransform) Order() int                        { return 0 }
func (parityAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (parityAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type parityStreamObserverFactory struct{ tag string }

func (h parityStreamObserverFactory) ID() string                      { return h.tag }
func (parityStreamObserverFactory) Order() int                        { return 0 }
func (parityStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (parityStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type parityTrafficObs struct{ tag string }

func (parityTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type parityUsageObs struct{ tag string }

func (parityUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

type parityRawSink struct{ tag string }

func (parityRawSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return nil
}

type parityRedactor struct{ tag string }

func (h parityRedactor) ID() string { return h.tag }
func (parityRedactor) Redact(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type parityCompactionObs struct{ tag string }

func (parityCompactionObs) OnCompaction(context.Context, compaction.Event) error { return nil }

type parityCompactionPreserver struct{ tag string }

func (p parityCompactionPreserver) ID() string { return p.tag }

func (parityCompactionPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (parityCompactionPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (parityCompactionPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type paritySecretGuard struct{ tag string }

func (g paritySecretGuard) ID() string                         { return g.tag }
func (paritySecretGuard) Order() int                           { return 0 }
func (paritySecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (paritySecretGuard) Evaluate(context.Context, *lipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type parityLocalTurnHandler struct{ tag string }

func (h parityLocalTurnHandler) ID() string                      { return h.tag }
func (parityLocalTurnHandler) Order() int                        { return 0 }
func (parityLocalTurnHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (parityLocalTurnHandler) Match(context.Context, lipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (parityLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

type parityTerminalProvider struct{ tag string }

func (p parityTerminalProvider) ID() string { return p.tag }
func (parityTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type parityBadIDTerminalProvider struct{ badID string }

func (p parityBadIDTerminalProvider) ID() string { return p.badID }
func (parityBadIDTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type parityPanicTerminalProvider struct{}

func (parityPanicTerminalProvider) ID() string { panic("provider boom") }
func (parityPanicTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

// makeParityBundle generates a populated feature bundle with tagged items across all planes.
func makeParityBundle(prefix string, includeTerminalProvider bool) lipfeature.FeatureBundle {
	b := lipfeature.FeatureBundle{
		SchemaVersion:                    lipfeature.SchemaVersionV1,
		SubmitHooks:                      []sdkhooks.SubmitHook{paritySubmitHook{tag: prefix + "-sub-1"}, paritySubmitHook{tag: prefix + "-sub-2"}},
		RequestPartHooks:                 []sdkhooks.RequestPartHook{parityRequestPartHook{tag: prefix + "-reqpart-1"}, parityRequestPartHook{tag: prefix + "-reqpart-2"}},
		ResponsePartHooks:                []sdkhooks.ResponsePartHook{parityResponsePartHook{tag: prefix + "-resppart-1"}, parityResponsePartHook{tag: prefix + "-resppart-2"}},
		ToolReactors:                     []sdkhooks.ToolReactor{parityToolReactor{tag: prefix + "-reactor-1"}, parityToolReactor{tag: prefix + "-reactor-2"}},
		Lifecycles:                       []lipplugin.Lifecycle{parityLifecycle{tag: prefix + "-lifecycle-1"}, parityLifecycle{tag: prefix + "-lifecycle-2"}},
		SessionOpeners:                   []session.Opener{parityOpener{tag: prefix + "-opener-1"}, parityOpener{tag: prefix + "-opener-2"}},
		WorkspaceResolvers:               []lipworkspace.Resolver{parityResolver{tag: prefix + "-resolver-1"}, parityResolver{tag: prefix + "-resolver-2"}},
		ToolCatalogFilters:               []toolcatalog.Filter{parityCatalogFilter{tag: prefix + "-filter-1"}, parityCatalogFilter{tag: prefix + "-filter-2"}},
		ToolCallPolicies:                 []toolpolicy.Policy{parityPolicy{tag: prefix + "-policy-1"}, parityPolicy{tag: prefix + "-policy-2"}},
		ToolCallFinalizers:               []toolcall.Finalizer{parityFinalizer{tag: prefix + "-finalizer-1"}, parityFinalizer{tag: prefix + "-finalizer-2"}},
		ToolCallFinalizationMaxArgsBytes: 4096,
		RequestTransforms:                []request.Transform{parityTransform{tag: prefix + "-transform-1"}, parityTransform{tag: prefix + "-transform-2"}},
		PreRequestHandlers:               []prerequest.Handler{parityPreReq{tag: prefix + "-prereq-1"}, parityPreReq{tag: prefix + "-prereq-2"}},
		RouteHintProviders:               []routehint.Provider{parityRouteHint{tag: prefix + "-hint-1"}, parityRouteHint{tag: prefix + "-hint-2"}},
		CompletionGates:                  []completion.Gate{parityCompGate{tag: prefix + "-gate-1"}, parityCompGate{tag: prefix + "-gate-2"}},
		AttemptTransforms:                []request.AttemptTransform{parityAttemptTransform{tag: prefix + "-attempt-1"}, parityAttemptTransform{tag: prefix + "-attempt-2"}},
		StreamObserverFactories:          []response.StreamObserverFactory{parityStreamObserverFactory{tag: prefix + "-streamobs-1"}, parityStreamObserverFactory{tag: prefix + "-streamobs-2"}},
		TrafficObservers:                 []traffic.Observer{parityTrafficObs{tag: prefix + "-trafficobs-1"}, parityTrafficObs{tag: prefix + "-trafficobs-2"}},
		UsageObservers:                   []usage.Observer{parityUsageObs{tag: prefix + "-usageobs-1"}, parityUsageObs{tag: prefix + "-usageobs-2"}},
		RawCaptureSinks:                  []traffic.RawCaptureSink{parityRawSink{tag: prefix + "-rawsink-1"}, parityRawSink{tag: prefix + "-rawsink-2"}},
		TrafficRedactors:                 []traffic.Redactor{parityRedactor{tag: prefix + "-redactor-1"}, parityRedactor{tag: prefix + "-redactor-2"}},
		CompactionObservers:              []compaction.Observer{parityCompactionObs{tag: prefix + "-compactobs-1"}, parityCompactionObs{tag: prefix + "-compactobs-2"}},
		CompactionPreservers:             []compaction.Preserver{parityCompactionPreserver{tag: prefix + "-compactpres-1"}, parityCompactionPreserver{tag: prefix + "-compactpres-2"}},
		SecretGuards:                     []secretguard.Guard{paritySecretGuard{tag: prefix + "-secret-1"}, paritySecretGuard{tag: prefix + "-secret-2"}},
		LocalTurnHandlers:                []localturn.Handler{parityLocalTurnHandler{tag: prefix + "-turn-1"}, parityLocalTurnHandler{tag: prefix + "-turn-2"}},
	}
	if includeTerminalProvider {
		b.TerminalDecisionProvider = parityTerminalProvider{tag: prefix + "-termprov"}
	}
	return b
}

// --- Parity Test Suite ---

func TestPlaneParity_DualPathOrderedConcatenation(t *testing.T) {
	t.Parallel()

	bA := makeParityBundle("A", false)
	bB := makeParityBundle("B", false)
	bC := makeParityBundle("C", false)

	planeparity.AssertDualPathParity(t, bA, bB, bC)
}

func TestPlaneParity_DualPathNilVsEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("nil_bundles", func(t *testing.T) {
		t.Parallel()
		planeparity.AssertDualPathParity(t)
	})

	t.Run("zero_value_bundles", func(t *testing.T) {
		t.Parallel()
		planeparity.AssertDualPathParity(t, lipfeature.FeatureBundle{}, lipfeature.FeatureBundle{})
	})

	t.Run("explicitly_empty_slices", func(t *testing.T) {
		t.Parallel()
		emptyBundle := lipfeature.FeatureBundle{
			SchemaVersion:           lipfeature.SchemaVersionV1,
			SubmitHooks:             []sdkhooks.SubmitHook{},
			RequestPartHooks:        []sdkhooks.RequestPartHook{},
			ResponsePartHooks:       []sdkhooks.ResponsePartHook{},
			ToolReactors:            []sdkhooks.ToolReactor{},
			Lifecycles:              []lipplugin.Lifecycle{},
			SessionOpeners:          []session.Opener{},
			WorkspaceResolvers:      []lipworkspace.Resolver{},
			ToolCatalogFilters:      []toolcatalog.Filter{},
			ToolCallPolicies:        []toolpolicy.Policy{},
			ToolCallFinalizers:      []toolcall.Finalizer{},
			RequestTransforms:       []request.Transform{},
			PreRequestHandlers:      []prerequest.Handler{},
			RouteHintProviders:      []routehint.Provider{},
			CompletionGates:         []completion.Gate{},
			AttemptTransforms:       []request.AttemptTransform{},
			StreamObserverFactories: []response.StreamObserverFactory{},
			TrafficObservers:        []traffic.Observer{},
			UsageObservers:          []usage.Observer{},
			RawCaptureSinks:         []traffic.RawCaptureSink{},
			TrafficRedactors:        []traffic.Redactor{},
			CompactionObservers:     []compaction.Observer{},
			CompactionPreservers:    []compaction.Preserver{},
			SecretGuards:            []secretguard.Guard{},
			LocalTurnHandlers:       []localturn.Handler{},
		}
		planeparity.AssertDualPathParity(t, emptyBundle)
	})
}

func TestPlaneParity_DualPathScalarMinReduction(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		bundles []lipfeature.FeatureBundle
	}{
		{
			name: "decreasing",
			bundles: []lipfeature.FeatureBundle{
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 8192},
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 4096},
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 1024},
			},
		},
		{
			name: "increasing",
			bundles: []lipfeature.FeatureBundle{
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 1024},
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 4096},
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 8192},
			},
		},
		{
			name: "equal_idempotence",
			bundles: []lipfeature.FeatureBundle{
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 2048},
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 2048},
			},
		},
		{
			name: "interspersed_zeros_and_negatives",
			bundles: []lipfeature.FeatureBundle{
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 0},
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 4096},
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: -1},
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 1024},
				{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 0},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			planeparity.AssertDualPathParity(t, tc.bundles...)
		})
	}
}

func TestPlaneParity_DualPathExclusiveSlot(t *testing.T) {
	t.Parallel()

	provA := parityTerminalProvider{tag: "alg.provider.a"}
	provB := parityTerminalProvider{tag: "alg.provider.b"}

	t.Run("single_provider", func(t *testing.T) {
		t.Parallel()
		b := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			SubmitHooks:              []sdkhooks.SubmitHook{paritySubmitHook{tag: "h1"}},
			TerminalDecisionProvider: provA,
		}
		planeparity.AssertDualPathParity(t, b)
	})

	t.Run("distinct_providers_conflict", func(t *testing.T) {
		t.Parallel()
		b1 := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			TerminalDecisionProvider: provA,
		}
		b2 := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			TerminalDecisionProvider: provB,
		}
		planeparity.AssertDualPathParity(t, b1, b2)
	})

	t.Run("same_provider_recontribution_conflict", func(t *testing.T) {
		t.Parallel()
		b1 := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			TerminalDecisionProvider: provA,
		}
		b2 := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			TerminalDecisionProvider: provA,
		}
		planeparity.AssertDualPathParity(t, b1, b2)
	})
}

func TestPlaneParity_DualPathInvalidContributions(t *testing.T) {
	t.Parallel()

	invalidCases := []struct {
		name   string
		bundle lipfeature.FeatureBundle
	}{
		{
			name: "empty_provider_id",
			bundle: lipfeature.FeatureBundle{
				SchemaVersion:            lipfeature.SchemaVersionV1,
				TerminalDecisionProvider: parityBadIDTerminalProvider{badID: ""},
			},
		},
		{
			name: "invalid_utf8_provider_id",
			bundle: lipfeature.FeatureBundle{
				SchemaVersion:            lipfeature.SchemaVersionV1,
				TerminalDecisionProvider: parityBadIDTerminalProvider{badID: "\xff\xfe\xfd"},
			},
		},
		{
			name: "excessive_provider_id",
			bundle: lipfeature.FeatureBundle{
				SchemaVersion:            lipfeature.SchemaVersionV1,
				TerminalDecisionProvider: parityBadIDTerminalProvider{badID: strings.Repeat("x", terminaldecision.MaxProviderIDBytes+1)},
			},
		},
		{
			name: "panicking_provider",
			bundle: lipfeature.FeatureBundle{
				SchemaVersion:            lipfeature.SchemaVersionV1,
				TerminalDecisionProvider: parityPanicTerminalProvider{},
			},
		},
		{
			name: "typed_nil_provider",
			bundle: lipfeature.FeatureBundle{
				SchemaVersion:            lipfeature.SchemaVersionV1,
				TerminalDecisionProvider: (*parityTerminalProvider)(nil),
			},
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			planeparity.AssertDualPathParity(t, tc.bundle)
		})
	}
}

func TestPlaneParity_DualPathFailBeforeMutate(t *testing.T) {
	t.Parallel()

	provA := parityTerminalProvider{tag: "alg.provider.a"}
	provB := parityTerminalProvider{tag: "alg.provider.b"}

	bGood := makeParityBundle("Good", false)
	bGood.TerminalDecisionProvider = provA

	bConflicting := makeParityBundle("Bad", false)
	bConflicting.TerminalDecisionProvider = provB

	// 1. Dual-path parity on multi-bundle conflict
	planeparity.AssertDualPathParity(t, bGood, bConflicting)

	// 2. Direct verification that generated merge produces empty result on error
	res, err := featurebundle.MergeBundlesGenerated(bGood, bConflicting)
	require.Error(t, err)
	require.Equal(t, featurebundle.GeneratedMergeSurface{}, res, "candidate must be discarded on conflict")
	require.ErrorIs(t, err, lipfeature.ErrExclusiveConflict)
}

func TestPlaneParity_DualPathLifecycleSideChannel(t *testing.T) {
	t.Parallel()

	bA := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		Lifecycles:    []lipplugin.Lifecycle{parityLifecycle{tag: "life-A1"}, parityLifecycle{tag: "life-A2"}},
	}
	bB := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		Lifecycles:    []lipplugin.Lifecycle{parityLifecycle{tag: "life-B1"}},
	}
	bC := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		Lifecycles:    []lipplugin.Lifecycle{parityLifecycle{tag: "life-C1"}, parityLifecycle{tag: "life-C2"}},
	}

	planeparity.AssertDualPathParity(t, bA, bB, bC)
}

func TestPlaneParity_DualPathInvalidSliceItems(t *testing.T) {
	t.Parallel()

	t.Run("nil_attempt_transform", func(t *testing.T) {
		t.Parallel()
		b := lipfeature.FeatureBundle{
			SchemaVersion:     lipfeature.SchemaVersionV1,
			AttemptTransforms: []request.AttemptTransform{nil},
		}
		_, err := featurebundle.MergeBundlesGenerated(b)
		require.Error(t, err)
		require.ErrorIs(t, err, lipfeature.ErrInvalidContribution)
	})

	t.Run("nil_local_turn_handler", func(t *testing.T) {
		t.Parallel()
		b := lipfeature.FeatureBundle{
			SchemaVersion:     lipfeature.SchemaVersionV1,
			LocalTurnHandlers: []localturn.Handler{nil},
		}
		_, err := featurebundle.MergeBundlesGenerated(b)
		require.Error(t, err)
		require.ErrorIs(t, err, lipfeature.ErrInvalidContribution)
	})

	t.Run("nil_compaction_preserver", func(t *testing.T) {
		t.Parallel()
		b := lipfeature.FeatureBundle{
			SchemaVersion:        lipfeature.SchemaVersionV1,
			CompactionPreservers: []compaction.Preserver{nil},
		}
		_, err := featurebundle.MergeBundlesGenerated(b)
		require.Error(t, err)
		require.ErrorIs(t, err, lipfeature.ErrInvalidContribution)
	})

	t.Run("nil_stream_observer_factory", func(t *testing.T) {
		t.Parallel()
		b := lipfeature.FeatureBundle{
			SchemaVersion:           lipfeature.SchemaVersionV1,
			StreamObserverFactories: []response.StreamObserverFactory{nil},
		}
		_, err := featurebundle.MergeBundlesGenerated(b)
		require.Error(t, err)
		require.ErrorIs(t, err, lipfeature.ErrInvalidContribution)
	})
}

func TestPlaneParity_DualPathMergeFeatureSurfaceWithRegistry(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()

	err := reg.RegisterFeature("feat-alpha", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion:                    lipfeature.SchemaVersionV1,
			SubmitHooks:                      []sdkhooks.SubmitHook{paritySubmitHook{tag: "alpha-sub"}},
			ToolCallFinalizationMaxArgsBytes: 4096,
			Lifecycles:                       []lipplugin.Lifecycle{parityLifecycle{tag: "alpha-life"}},
		}, nil
	})
	require.NoError(t, err)

	err = reg.RegisterFeature("feat-beta", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion:                    lipfeature.SchemaVersionV1,
			SubmitHooks:                      []sdkhooks.SubmitHook{paritySubmitHook{tag: "beta-sub"}},
			ToolCallFinalizationMaxArgsBytes: 2048,
			TerminalDecisionProvider:         parityTerminalProvider{tag: "beta-provider"},
			Lifecycles:                       []lipplugin.Lifecycle{parityLifecycle{tag: "beta-life"}},
		}, nil
	})
	require.NoError(t, err)

	err = reg.RegisterFeature("feat-gamma-disabled", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			SubmitHooks:   []sdkhooks.SubmitHook{paritySubmitHook{tag: "gamma-sub"}},
		}, nil
	})
	require.NoError(t, err)

	registrations := []lipsdk.Registration{
		{
			Kind:    lipsdk.PluginKindFeature,
			ID:      "feat-alpha",
			Enabled: true,
		},
		{
			Kind:    lipsdk.PluginKindFeature,
			ID:      "feat-gamma-disabled",
			Enabled: false,
		},
		{
			Kind:    lipsdk.PluginKindFeature,
			ID:      "feat-beta",
			Enabled: true,
		},
	}

	legacy, errLegacy := featurebundle.MergeFeatureSurface(reg, registrations)
	require.NoError(t, errLegacy)

	gen, errGen := featurebundle.MergeFeatureSurfaceGenerated(reg, registrations)
	require.NoError(t, errGen)

	planeparity.AssertMergedSurfacesEqual(t, legacy, gen)

	viaGen, errViaGen := featurebundle.MergeFeatureSurfaceViaGenerated(reg, registrations)
	require.NoError(t, errViaGen)
	require.Equal(t, legacy, viaGen)
}
