package featurebundle_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
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

type trackingSubmitHook struct {
	id     string
	order  int
	mode   sdkhooks.FailureMode
	log    *[]string
	mu     *sync.Mutex
	panics bool
}

func (h trackingSubmitHook) ID() string                        { return h.id }
func (h trackingSubmitHook) Order() int                        { return h.order }
func (h trackingSubmitHook) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h trackingSubmitHook) Handle(ctx context.Context, call *lipapi.Call, meta *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	if h.mu != nil && h.log != nil {
		h.mu.Lock()
		*h.log = append(*h.log, h.id)
		h.mu.Unlock()
	}
	if h.panics {
		panic(h.id + " boom")
	}
	return sdkhooks.SubmitDecision{}, nil
}

type trackingRequestPartHook struct {
	id     string
	order  int
	mode   sdkhooks.FailureMode
	log    *[]string
	mu     *sync.Mutex
	panics bool
}

func (h trackingRequestPartHook) ID() string                        { return h.id }
func (h trackingRequestPartHook) Order() int                        { return h.order }
func (h trackingRequestPartHook) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h trackingRequestPartHook) HandleRequestParts(ctx context.Context, call *lipapi.Call, meta sdkhooks.PartMeta) error {
	if h.mu != nil && h.log != nil {
		h.mu.Lock()
		*h.log = append(*h.log, h.id)
		h.mu.Unlock()
	}
	if h.panics {
		panic(h.id + " boom")
	}
	return nil
}

type trackingResponsePartHook struct {
	id     string
	order  int
	mode   sdkhooks.FailureMode
	log    *[]string
	mu     *sync.Mutex
	panics bool
}

func (h trackingResponsePartHook) ID() string                        { return h.id }
func (h trackingResponsePartHook) Order() int                        { return h.order }
func (h trackingResponsePartHook) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h trackingResponsePartHook) HandleEvent(ctx context.Context, ev *lipapi.Event, meta sdkhooks.PartMeta) error {
	if h.mu != nil && h.log != nil {
		h.mu.Lock()
		*h.log = append(*h.log, h.id)
		h.mu.Unlock()
	}
	if h.panics {
		panic(h.id + " boom")
	}
	return nil
}

func TestPlaneParity_HookBus_ThreeHookFamiliesGeneratedEndToEnd(t *testing.T) {
	t.Parallel()

	makeBundles := func(execLog *[]string, mu *sync.Mutex) (lipfeature.FeatureBundle, lipfeature.FeatureBundle, lipfeature.FeatureBundle) {
		b1 := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			SubmitHooks: []sdkhooks.SubmitHook{
				trackingSubmitHook{id: "sub-b1-10", order: 10, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
				trackingSubmitHook{id: "sub-b1-5", order: 5, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
			},
			RequestPartHooks: []sdkhooks.RequestPartHook{
				trackingRequestPartHook{id: "reqpart-b1-2", order: 2, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
			},
			ResponsePartHooks: []sdkhooks.ResponsePartHook{
				trackingResponsePartHook{id: "resppart-b1-20", order: 20, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
			},
		}

		b2 := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			SubmitHooks: []sdkhooks.SubmitHook{
				trackingSubmitHook{id: "sub-b2-5", order: 5, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
				trackingSubmitHook{id: "sub-b2-1", order: 1, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
			},
			RequestPartHooks: []sdkhooks.RequestPartHook{
				trackingRequestPartHook{id: "reqpart-b2-1", order: 1, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
			},
			ResponsePartHooks: []sdkhooks.ResponsePartHook{
				trackingResponsePartHook{id: "resppart-b2-10", order: 10, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
			},
		}

		b3 := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			SubmitHooks: []sdkhooks.SubmitHook{
				trackingSubmitHook{id: "sub-b3-1", order: 1, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
			},
			RequestPartHooks: []sdkhooks.RequestPartHook{
				trackingRequestPartHook{id: "reqpart-b3-0", order: 0, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
			},
			ResponsePartHooks: []sdkhooks.ResponsePartHook{
				trackingResponsePartHook{id: "resppart-b3-5", order: 5, mode: sdkhooks.FailOpen, log: execLog, mu: mu},
			},
		}
		return b1, b2, b3
	}

	t.Run("dual_path_parity_and_registration_order", func(t *testing.T) {
		t.Parallel()

		var execLog []string
		var mu sync.Mutex
		b1, b2, b3 := makeBundles(&execLog, &mu)

		planeparity.AssertDualPathParity(t, b1, b2, b3)

		gen, err := featurebundle.MergeBundlesGenerated(b1, b2, b3)
		require.NoError(t, err)

		// 1. SubmitHooks registration order: [b1-10, b1-5, b2-5, b2-1, b3-1]
		submitHooks := lipfeature.Get(gen.Frozen, lipfeature.PlaneSubmitHooks)
		require.Len(t, submitHooks, 5)
		require.Equal(t, "sub-b1-10", submitHooks[0].ID())
		require.Equal(t, "sub-b1-5", submitHooks[1].ID())
		require.Equal(t, "sub-b2-5", submitHooks[2].ID())
		require.Equal(t, "sub-b2-1", submitHooks[3].ID())
		require.Equal(t, "sub-b3-1", submitHooks[4].ID())

		// 2. RequestPartHooks registration order: [b1-2, b2-1, b3-0]
		reqPartHooks := lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestPartHooks)
		require.Len(t, reqPartHooks, 3)
		require.Equal(t, "reqpart-b1-2", reqPartHooks[0].ID())
		require.Equal(t, "reqpart-b2-1", reqPartHooks[1].ID())
		require.Equal(t, "reqpart-b3-0", reqPartHooks[2].ID())

		// 3. ResponsePartHooks registration order: [b1-20, b2-10, b3-5]
		respPartHooks := lipfeature.Get(gen.Frozen, lipfeature.PlaneResponsePartHooks)
		require.Len(t, respPartHooks, 3)
		require.Equal(t, "resppart-b1-20", respPartHooks[0].ID())
		require.Equal(t, "resppart-b2-10", respPartHooks[1].ID())
		require.Equal(t, "resppart-b3-5", respPartHooks[2].ID())
	})

	t.Run("hook_bus_stable_sorting_and_execution", func(t *testing.T) {
		t.Parallel()

		var execLog []string
		var mu sync.Mutex
		b1, b2, b3 := makeBundles(&execLog, &mu)

		gen, err := featurebundle.MergeBundlesGenerated(b1, b2, b3)
		require.NoError(t, err)

		bus := corehooks.New(corehooks.Config{
			SubmitHooks:       lipfeature.Get(gen.Frozen, lipfeature.PlaneSubmitHooks),
			RequestPartHooks:  lipfeature.Get(gen.Frozen, lipfeature.PlaneRequestPartHooks),
			ResponsePartHooks: lipfeature.Get(gen.Frozen, lipfeature.PlaneResponsePartHooks),
		})

		ctx := context.Background()
		call := &lipapi.Call{
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		}

		// Submit hooks: sorted by Order asc, ID asc, regIdx asc
		// Expected: sub-b2-1 (order 1), sub-b3-1 (order 1), sub-b1-5 (order 5), sub-b2-5 (order 5), sub-b1-10 (order 10)
		execLog = nil
		err = bus.RunSubmit(ctx, call, &sdkhooks.SubmitMeta{Annotations: map[string]string{}})
		require.NoError(t, err)
		require.Equal(t, []string{"sub-b2-1", "sub-b3-1", "sub-b1-5", "sub-b2-5", "sub-b1-10"}, execLog)

		// Request part hooks: sorted by Order asc, ID asc, regIdx asc
		// Expected: reqpart-b3-0 (order 0), reqpart-b2-1 (order 1), reqpart-b1-2 (order 2)
		execLog = nil
		err = bus.RunRequestPartHooks(ctx, call, sdkhooks.PartMeta{})
		require.NoError(t, err)
		require.Equal(t, []string{"reqpart-b3-0", "reqpart-b2-1", "reqpart-b1-2"}, execLog)

		// Response part hooks: sorted by Order asc, ID asc, regIdx asc
		// Expected: resppart-b3-5 (order 5), resppart-b2-10 (order 10), resppart-b1-20 (order 20)
		execLog = nil
		ev := &lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "chunk"}
		err = bus.RunResponsePartHooks(ctx, ev, sdkhooks.PartMeta{})
		require.NoError(t, err)
		require.Equal(t, []string{"resppart-b3-5", "resppart-b2-10", "resppart-b1-20"}, execLog)
	})

	t.Run("panic_isolation_behavior", func(t *testing.T) {
		t.Parallel()

		var execLog []string
		var mu sync.Mutex

		bPanicFailOpen := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			SubmitHooks: []sdkhooks.SubmitHook{
				trackingSubmitHook{id: "panic-open", order: 1, mode: sdkhooks.FailOpen, log: &execLog, mu: &mu, panics: true},
			},
		}
		bFollower := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			SubmitHooks: []sdkhooks.SubmitHook{
				trackingSubmitHook{id: "follower", order: 2, mode: sdkhooks.FailOpen, log: &execLog, mu: &mu},
			},
		}

		gen, err := featurebundle.MergeBundlesGenerated(bPanicFailOpen, bFollower)
		require.NoError(t, err)

		bus := corehooks.New(corehooks.Config{
			SubmitHooks: lipfeature.Get(gen.Frozen, lipfeature.PlaneSubmitHooks),
		})
		call := &lipapi.Call{
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("test")}}},
		}
		err = bus.RunSubmit(context.Background(), call, &sdkhooks.SubmitMeta{Annotations: map[string]string{}})
		require.NoError(t, err)
		require.Equal(t, []string{"panic-open", "follower"}, execLog, "follower hook must run after fail-open panic in generated hook pipeline")

		// Fail closed panic surfaces *safety.PanicError
		bPanicFailClosed := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			RequestPartHooks: []sdkhooks.RequestPartHook{
				trackingRequestPartHook{id: "panic-closed", order: 1, mode: sdkhooks.FailClosed, panics: true},
			},
		}
		genClosed, err := featurebundle.MergeBundlesGenerated(bPanicFailClosed)
		require.NoError(t, err)

		busClosed := corehooks.New(corehooks.Config{
			RequestPartHooks: lipfeature.Get(genClosed.Frozen, lipfeature.PlaneRequestPartHooks),
		})
		err = busClosed.RunRequestPartHooks(context.Background(), call, sdkhooks.PartMeta{})
		require.Error(t, err)
		var pe *safety.PanicError
		require.ErrorAs(t, err, &pe)
	})

	t.Run("backing_array_isolation", func(t *testing.T) {
		t.Parallel()

		var execLog []string
		var mu sync.Mutex
		b1, b2, _ := makeBundles(&execLog, &mu)

		gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
		require.NoError(t, err)

		h1 := lipfeature.Get(gen.Frozen, lipfeature.PlaneSubmitHooks)
		require.Len(t, h1, 4)

		// Mutate slice elements in h1
		h1[0] = trackingSubmitHook{id: "mutated", order: 999}

		// Subsequent Get must return untampered slice
		h2 := lipfeature.Get(gen.Frozen, lipfeature.PlaneSubmitHooks)
		require.Equal(t, "sub-b1-10", h2[0].ID(), "frozen set backing array must be isolated from caller mutations")
	})
}

type trackingToolReactor struct {
	id       string
	order    int
	decision sdkhooks.ToolDecision
	mutateFn func(ev lipapi.ToolEvent) lipapi.ToolEvent
	err      error
	panics   bool
	log      *[]string
	mu       *sync.Mutex
}

func (r trackingToolReactor) ID() string { return r.id }
func (r trackingToolReactor) Order() int { return r.order }
func (r trackingToolReactor) HandleToolEvent(ctx context.Context, ev lipapi.ToolEvent, meta sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	if r.mu != nil && r.log != nil {
		r.mu.Lock()
		*r.log = append(*r.log, r.id)
		r.mu.Unlock()
	}
	if r.panics {
		panic(r.id + " boom")
	}
	if r.err != nil {
		return sdkhooks.ToolPass, ev, r.err
	}
	outEv := ev
	if r.mutateFn != nil {
		outEv = r.mutateFn(ev)
	}
	dec := r.decision
	if dec == 0 {
		dec = sdkhooks.ToolPass
	}
	return dec, outEv, nil
}

func TestPlaneParity_HookBus_ToolReactorsGeneratedEndToEnd(t *testing.T) {
	t.Parallel()

	makeBundles := func(execLog *[]string, mu *sync.Mutex) (lipfeature.FeatureBundle, lipfeature.FeatureBundle, lipfeature.FeatureBundle) {
		b1 := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			ToolReactors: []sdkhooks.ToolReactor{
				trackingToolReactor{id: "reactor-b1-10", order: 10, log: execLog, mu: mu},
				trackingToolReactor{id: "reactor-b1-5", order: 5, log: execLog, mu: mu},
			},
		}

		b2 := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			ToolReactors: []sdkhooks.ToolReactor{
				trackingToolReactor{id: "reactor-b2-5", order: 5, log: execLog, mu: mu},
				trackingToolReactor{id: "reactor-b2-1", order: 1, log: execLog, mu: mu},
			},
		}

		b3 := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			ToolReactors: []sdkhooks.ToolReactor{
				trackingToolReactor{id: "reactor-b3-1", order: 1, log: execLog, mu: mu},
			},
		}
		return b1, b2, b3
	}

	t.Run("dual_path_parity_and_registration_order", func(t *testing.T) {
		t.Parallel()

		var execLog []string
		var mu sync.Mutex
		b1, b2, b3 := makeBundles(&execLog, &mu)

		planeparity.AssertDualPathParity(t, b1, b2, b3)

		gen, err := featurebundle.MergeBundlesGenerated(b1, b2, b3)
		require.NoError(t, err)

		reactors := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolReactors)
		require.Len(t, reactors, 5)
		require.Equal(t, "reactor-b1-10", reactors[0].ID())
		require.Equal(t, "reactor-b1-5", reactors[1].ID())
		require.Equal(t, "reactor-b2-5", reactors[2].ID())
		require.Equal(t, "reactor-b2-1", reactors[3].ID())
		require.Equal(t, "reactor-b3-1", reactors[4].ID())
	})

	t.Run("hook_bus_stable_sorting_and_execution", func(t *testing.T) {
		t.Parallel()

		var execLog []string
		var mu sync.Mutex
		b1, b2, b3 := makeBundles(&execLog, &mu)

		legacyReactors := append(append(append([]sdkhooks.ToolReactor(nil), b1.ToolReactors...), b2.ToolReactors...), b3.ToolReactors...)

		gen, err := featurebundle.MergeBundlesGenerated(b1, b2, b3)
		require.NoError(t, err)

		type evidenceEntry struct {
			providerID    string
			decision      sdkhooks.ToolDecision
			err           error
			validationErr error
		}
		var legacyEvidence, genEvidence []evidenceEntry
		var evMu sync.Mutex
		recordEvidence := func(dest *[]evidenceEntry) corehooks.ToolReactorEvidenceFunc {
			return func(_ context.Context, providerID string, dec sdkhooks.ToolDecision, err error, validationErr error) {
				evMu.Lock()
				defer evMu.Unlock()
				*dest = append(*dest, evidenceEntry{providerID: providerID, decision: dec, err: err, validationErr: validationErr})
			}
		}

		legacyBus := corehooks.New(corehooks.Config{
			ToolReactors: legacyReactors,
		})
		bus := corehooks.New(corehooks.Config{
			ToolReactors: lipfeature.Get(gen.Frozen, lipfeature.PlaneToolReactors),
		})

		ctxLegacy := corehooks.WithToolReactorEvidence(context.Background(), recordEvidence(&legacyEvidence))
		ctxGen := corehooks.WithToolReactorEvidence(context.Background(), recordEvidence(&genEvidence))
		ev := lipapi.ToolEvent{
			Kind:       lipapi.ToolEventStarted,
			ToolCallID: "call-1",
			ToolName:   "search",
		}

		// Tool reactors: sorted by Order asc, ID asc, regIdx asc
		// Expected: reactor-b2-1 (order 1), reactor-b3-1 (order 1), reactor-b1-5 (order 5), reactor-b2-5 (order 5), reactor-b1-10 (order 10)
		execLog = nil
		outLegacy := legacyBus.ApplyToolReactors(ctxLegacy, ev, sdkhooks.ToolMeta{})
		execLogLegacy := append([]string(nil), execLog...)

		execLog = nil
		out := bus.ApplyToolReactors(ctxGen, ev, sdkhooks.ToolMeta{})
		require.NoError(t, out.Err)
		require.True(t, out.Emit)
		require.Equal(t, ev, out.Event)
		require.Equal(t, outLegacy, out)
		require.Equal(t, []string{"reactor-b2-1", "reactor-b3-1", "reactor-b1-5", "reactor-b2-5", "reactor-b1-10"}, execLog)
		require.Equal(t, execLogLegacy, execLog)
		require.Equal(t, legacyEvidence, genEvidence)
		require.Len(t, genEvidence, 5)
		for i, id := range []string{"reactor-b2-1", "reactor-b3-1", "reactor-b1-5", "reactor-b2-5", "reactor-b1-10"} {
			require.Equal(t, id, genEvidence[i].providerID)
			require.Equal(t, sdkhooks.ToolPass, genEvidence[i].decision)
			require.NoError(t, genEvidence[i].err)
			require.NoError(t, genEvidence[i].validationErr)
		}
	})

	t.Run("panic_and_error_policy_behavior", func(t *testing.T) {
		t.Parallel()

		var execLog []string
		var mu sync.Mutex

		bPanic := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			ToolReactors: []sdkhooks.ToolReactor{
				trackingToolReactor{id: "reactor-panic", order: 1, log: &execLog, mu: &mu, panics: true},
			},
		}
		bFollower := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			ToolReactors: []sdkhooks.ToolReactor{
				trackingToolReactor{id: "reactor-follower", order: 2, log: &execLog, mu: &mu},
			},
		}

		legacyReactors := append(append([]sdkhooks.ToolReactor(nil), bPanic.ToolReactors...), bFollower.ToolReactors...)

		gen, err := featurebundle.MergeBundlesGenerated(bPanic, bFollower)
		require.NoError(t, err)

		type evidenceEntry struct {
			providerID    string
			decision      sdkhooks.ToolDecision
			hasPanicErr   bool
			validationErr error
		}
		var legacyEv, genEv []evidenceEntry
		var evMu sync.Mutex
		recordEvidence := func(dest *[]evidenceEntry) corehooks.ToolReactorEvidenceFunc {
			return func(_ context.Context, providerID string, dec sdkhooks.ToolDecision, err error, validationErr error) {
				evMu.Lock()
				defer evMu.Unlock()
				var pe *safety.PanicError
				*dest = append(*dest, evidenceEntry{
					providerID:    providerID,
					decision:      dec,
					hasPanicErr:   errors.As(err, &pe),
					validationErr: validationErr,
				})
			}
		}

		ev := lipapi.ToolEvent{
			Kind:       lipapi.ToolEventStarted,
			ToolCallID: "call-1",
			ToolName:   "search",
		}

		// 1. FailOpen policy (default/unspecified): follower still runs, no error returned
		execLog = nil
		legacyEv, genEv = nil, nil
		ctxLegacy := corehooks.WithToolReactorEvidence(context.Background(), recordEvidence(&legacyEv))
		ctxGen := corehooks.WithToolReactorEvidence(context.Background(), recordEvidence(&genEv))

		busFailOpenLegacy := corehooks.New(corehooks.Config{
			ToolReactors:           legacyReactors,
			ToolReactorErrorPolicy: sdkhooks.ToolReactorErrorsFailOpen,
		})
		busFailOpen := corehooks.New(corehooks.Config{
			ToolReactors:           lipfeature.Get(gen.Frozen, lipfeature.PlaneToolReactors),
			ToolReactorErrorPolicy: sdkhooks.ToolReactorErrorsFailOpen,
		})
		outOpenLegacy := busFailOpenLegacy.ApplyToolReactors(ctxLegacy, ev, sdkhooks.ToolMeta{})
		execLogLegacy := append([]string(nil), execLog...)

		execLog = nil
		outOpen := busFailOpen.ApplyToolReactors(ctxGen, ev, sdkhooks.ToolMeta{})
		require.NoError(t, outOpen.Err)
		require.True(t, outOpen.Emit)
		require.Equal(t, outOpenLegacy, outOpen)
		require.Equal(t, []string{"reactor-panic", "reactor-follower"}, execLog, "follower must run after fail-open panic")
		require.Equal(t, execLogLegacy, execLog)
		require.Equal(t, legacyEv, genEv)
		require.Len(t, genEv, 2)
		require.Equal(t, "reactor-panic", genEv[0].providerID)
		require.True(t, genEv[0].hasPanicErr)
		require.Equal(t, "reactor-follower", genEv[1].providerID)
		require.False(t, genEv[1].hasPanicErr)

		// 2. FailClosed policy: panic surfaces as *safety.PanicError, follower does NOT run
		execLog = nil
		legacyEv, genEv = nil, nil
		ctxLegacy = corehooks.WithToolReactorEvidence(context.Background(), recordEvidence(&legacyEv))
		ctxGen = corehooks.WithToolReactorEvidence(context.Background(), recordEvidence(&genEv))

		busFailClosedLegacy := corehooks.New(corehooks.Config{
			ToolReactors:           legacyReactors,
			ToolReactorErrorPolicy: sdkhooks.ToolReactorErrorsFailClosed,
		})
		busFailClosed := corehooks.New(corehooks.Config{
			ToolReactors:           lipfeature.Get(gen.Frozen, lipfeature.PlaneToolReactors),
			ToolReactorErrorPolicy: sdkhooks.ToolReactorErrorsFailClosed,
		})
		outClosedLegacy := busFailClosedLegacy.ApplyToolReactors(ctxLegacy, ev, sdkhooks.ToolMeta{})
		execLogLegacy = append([]string(nil), execLog...)

		execLog = nil
		outClosed := busFailClosed.ApplyToolReactors(ctxGen, ev, sdkhooks.ToolMeta{})
		require.Error(t, outClosed.Err)
		require.Error(t, outClosedLegacy.Err)
		require.Equal(t, outClosedLegacy.Emit, outClosed.Emit)
		var pe *safety.PanicError
		require.ErrorAs(t, outClosed.Err, &pe)
		var peLegacy *safety.PanicError
		require.ErrorAs(t, outClosedLegacy.Err, &peLegacy)
		require.Equal(t, []string{"reactor-panic"}, execLog, "follower must not run after fail-closed panic")
		require.Equal(t, execLogLegacy, execLog)
		require.Equal(t, legacyEv, genEv)
		require.Len(t, genEv, 1)
		require.Equal(t, "reactor-panic", genEv[0].providerID)
		require.True(t, genEv[0].hasPanicErr)

		// 3. SwallowEvent policy: panic swallows event (Emit=false), follower does NOT run, no error
		execLog = nil
		legacyEv, genEv = nil, nil
		ctxLegacy = corehooks.WithToolReactorEvidence(context.Background(), recordEvidence(&legacyEv))
		ctxGen = corehooks.WithToolReactorEvidence(context.Background(), recordEvidence(&genEv))

		busSwallowLegacy := corehooks.New(corehooks.Config{
			ToolReactors:           legacyReactors,
			ToolReactorErrorPolicy: sdkhooks.ToolReactorErrorsSwallowEvent,
		})
		busSwallow := corehooks.New(corehooks.Config{
			ToolReactors:           lipfeature.Get(gen.Frozen, lipfeature.PlaneToolReactors),
			ToolReactorErrorPolicy: sdkhooks.ToolReactorErrorsSwallowEvent,
		})
		outSwallowLegacy := busSwallowLegacy.ApplyToolReactors(ctxLegacy, ev, sdkhooks.ToolMeta{})
		execLogLegacy = append([]string(nil), execLog...)

		execLog = nil
		outSwallow := busSwallow.ApplyToolReactors(ctxGen, ev, sdkhooks.ToolMeta{})
		require.NoError(t, outSwallow.Err)
		require.False(t, outSwallow.Emit, "swallow policy must drop event on panic")
		require.Equal(t, outSwallowLegacy, outSwallow)
		require.Equal(t, []string{"reactor-panic"}, execLog)
		require.Equal(t, execLogLegacy, execLog)
		require.Equal(t, legacyEv, genEv)
		require.Len(t, genEv, 1)
		require.Equal(t, "reactor-panic", genEv[0].providerID)
		require.True(t, genEv[0].hasPanicErr)
	})

	t.Run("evidence_semantics_and_decision_parity", func(t *testing.T) {
		t.Parallel()

		type evidenceEntry struct {
			providerID    string
			decision      sdkhooks.ToolDecision
			hasErr        bool
			hasValidation bool
		}

		b1 := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			ToolReactors: []sdkhooks.ToolReactor{
				// Order 1: valid rewrite
				trackingToolReactor{
					id:       "reactor-rewrite",
					order:    1,
					decision: sdkhooks.ToolRewrite,
					mutateFn: func(ev lipapi.ToolEvent) lipapi.ToolEvent {
						ev.ArgsDelta = `{"query":"modified"}`
						return ev
					},
				},
			},
		}

		b2 := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			ToolReactors: []sdkhooks.ToolReactor{
				// Order 2: invalid rewrite (fails validation because ToolCallID is cleared)
				trackingToolReactor{
					id:       "reactor-invalid-rewrite",
					order:    2,
					decision: sdkhooks.ToolRewrite,
					mutateFn: func(ev lipapi.ToolEvent) lipapi.ToolEvent {
						ev.ToolCallID = "" // invalid: empty tool call id
						return ev
					},
				},
				// Order 3: reactor returning error
				trackingToolReactor{
					id:    "reactor-err",
					order: 3,
					err:   errors.New("reactor failure"),
				},
			},
		}

		b3 := lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			ToolReactors: []sdkhooks.ToolReactor{
				// Order 4: reactor pass
				trackingToolReactor{
					id:       "reactor-pass",
					order:    4,
					decision: sdkhooks.ToolPass,
				},
			},
		}

		legacyReactors := append(append(append([]sdkhooks.ToolReactor(nil), b1.ToolReactors...), b2.ToolReactors...), b3.ToolReactors...)

		gen, err := featurebundle.MergeBundlesGenerated(b1, b2, b3)
		require.NoError(t, err)

		var legacyEvEntries, genEvEntries []evidenceEntry
		var mu sync.Mutex

		record := func(dest *[]evidenceEntry) corehooks.ToolReactorEvidenceFunc {
			return func(_ context.Context, providerID string, dec sdkhooks.ToolDecision, err error, vErr error) {
				mu.Lock()
				defer mu.Unlock()
				*dest = append(*dest, evidenceEntry{
					providerID:    providerID,
					decision:      dec,
					hasErr:        err != nil,
					hasValidation: vErr != nil,
				})
			}
		}

		legacyBus := corehooks.New(corehooks.Config{
			ToolReactors:           legacyReactors,
			ToolReactorErrorPolicy: sdkhooks.ToolReactorErrorsFailOpen,
		})
		genBus := corehooks.New(corehooks.Config{
			ToolReactors:           lipfeature.Get(gen.Frozen, lipfeature.PlaneToolReactors),
			ToolReactorErrorPolicy: sdkhooks.ToolReactorErrorsFailOpen,
		})

		ctxLegacy := corehooks.WithToolReactorEvidence(context.Background(), record(&legacyEvEntries))
		ctxGen := corehooks.WithToolReactorEvidence(context.Background(), record(&genEvEntries))

		inputEvent := lipapi.ToolEvent{
			Kind:       lipapi.ToolEventArgsDelta,
			ToolCallID: "call-42",
			ToolName:   "search",
			ArgsDelta:  `{"query":"initial"}`,
		}

		outLegacy := legacyBus.ApplyToolReactors(ctxLegacy, inputEvent, sdkhooks.ToolMeta{})
		outGen := genBus.ApplyToolReactors(ctxGen, inputEvent, sdkhooks.ToolMeta{})

		// Dual-path parity checks
		require.Equal(t, outLegacy, outGen, "output from ApplyToolReactors must match across legacy and generated paths")
		require.Equal(t, legacyEvEntries, genEvEntries, "evidence records must match across legacy and generated paths")

		// Observable evidence assertions
		require.Len(t, genEvEntries, 4)

		// 1. reactor-rewrite: ToolRewrite, no err, no validationErr
		require.Equal(t, "reactor-rewrite", genEvEntries[0].providerID)
		require.Equal(t, sdkhooks.ToolRewrite, genEvEntries[0].decision)
		require.False(t, genEvEntries[0].hasErr)
		require.False(t, genEvEntries[0].hasValidation)

		// 2. reactor-invalid-rewrite: ToolRewrite, no err, HAS validationErr
		require.Equal(t, "reactor-invalid-rewrite", genEvEntries[1].providerID)
		require.Equal(t, sdkhooks.ToolRewrite, genEvEntries[1].decision)
		require.False(t, genEvEntries[1].hasErr)
		require.True(t, genEvEntries[1].hasValidation, "invalid rewrite must record validation error in evidence")

		// 3. reactor-err: HAS err, no validationErr
		require.Equal(t, "reactor-err", genEvEntries[2].providerID)
		require.True(t, genEvEntries[2].hasErr, "failing reactor must record error in evidence")
		require.False(t, genEvEntries[2].hasValidation)

		// 4. reactor-pass: ToolPass, no err, no validationErr
		require.Equal(t, "reactor-pass", genEvEntries[3].providerID)
		require.Equal(t, sdkhooks.ToolPass, genEvEntries[3].decision)
		require.False(t, genEvEntries[3].hasErr)
		require.False(t, genEvEntries[3].hasValidation)

		// Final event retained rewrite from reactor 1 (since reactor 2 invalid rewrite was rejected in fail-open)
		require.True(t, outGen.Emit)
		require.Equal(t, `{"query":"modified"}`, outGen.Event.ArgsDelta)
		require.Equal(t, "call-42", outGen.Event.ToolCallID)
	})

	t.Run("backing_array_isolation", func(t *testing.T) {
		t.Parallel()

		var execLog []string
		var mu sync.Mutex
		b1, b2, _ := makeBundles(&execLog, &mu)

		gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
		require.NoError(t, err)

		r1 := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolReactors)
		require.Len(t, r1, 4)

		// Mutate slice elements in r1
		r1[0] = trackingToolReactor{id: "mutated", order: 999}

		// Subsequent Get must return untampered slice
		r2 := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolReactors)
		require.Equal(t, "reactor-b1-10", r2[0].ID(), "frozen set backing array must be isolated from caller mutations")
	})
}
