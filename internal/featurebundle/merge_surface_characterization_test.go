package featurebundle

import (
	"context"
	"reflect"
	"testing"

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
)

// --- Characterization stubs (tag-carrying minimal implementations) ---

type charSubmitHook struct{ tag string }

func (h charSubmitHook) ID() string                      { return h.tag }
func (charSubmitHook) Order() int                        { return 0 }
func (charSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charSubmitHook) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type charRequestPartHook struct{ tag string }

func (h charRequestPartHook) ID() string                      { return h.tag }
func (charRequestPartHook) Order() int                        { return 0 }
func (charRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charRequestPartHook) HandleRequestParts(context.Context, *lipapi.Call, sdkhooks.PartMeta) error {
	return nil
}

type charResponsePartHook struct{ tag string }

func (h charResponsePartHook) ID() string                      { return h.tag }
func (charResponsePartHook) Order() int                        { return 0 }
func (charResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charResponsePartHook) HandleEvent(context.Context, *lipapi.Event, sdkhooks.PartMeta) error {
	return nil
}

type charToolReactor struct{ tag string }

func (h charToolReactor) ID() string { return h.tag }
func (charToolReactor) Order() int   { return 0 }
func (charToolReactor) HandleToolEvent(context.Context, lipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

type charLifecycle struct{ tag string }

func (charLifecycle) Start(context.Context) error { return nil }
func (charLifecycle) Stop(context.Context) error  { return nil }

type charOpener struct{ tag string }

func (h charOpener) ID() string { return h.tag }
func (charOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type charResolver struct{ tag string }

func (charResolver) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{ProjectRoot: "/tmp"}, nil
}

type charCatalogFilter struct{ tag string }

func (h charCatalogFilter) ID() string                      { return h.tag }
func (charCatalogFilter) Order() int                        { return 0 }
func (charCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charCatalogFilter) Handle(context.Context, *lipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type charPolicy struct{ tag string }

func (h charPolicy) ID() string                      { return h.tag }
func (charPolicy) Order() int                        { return 0 }
func (charPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPolicy) Handle(context.Context, lipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type charFinalizer struct{ tag string }

func (h charFinalizer) ID() string { return h.tag }
func (charFinalizer) Order() int   { return 0 }
func (charFinalizer) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type charTransform struct{ tag string }

func (h charTransform) ID() string                      { return h.tag }
func (charTransform) Order() int                        { return 0 }
func (charTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charTransform) Handle(context.Context, *lipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type charPreReq struct{ tag string }

func (h charPreReq) ID() string                      { return h.tag }
func (charPreReq) Order() int                        { return 0 }
func (charPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPreReq) Handle(context.Context, *lipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type charRouteHint struct{ tag string }

func (h charRouteHint) ID() string                      { return h.tag }
func (charRouteHint) Order() int                        { return 0 }
func (charRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charRouteHint) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type charCompGate struct{ tag string }

func (h charCompGate) ID() string                      { return h.tag }
func (charCompGate) Order() int                        { return 0 }
func (charCompGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charCompGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type charAttemptTransform struct{ tag string }

func (h charAttemptTransform) ID() string                      { return h.tag }
func (charAttemptTransform) Order() int                        { return 0 }
func (charAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (charAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type charStreamObserverFactory struct{ tag string }

func (h charStreamObserverFactory) ID() string                      { return h.tag }
func (charStreamObserverFactory) Order() int                        { return 0 }
func (charStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type charTrafficObs struct{ tag string }

func (charTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type charUsageObs struct{ tag string }

func (charUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

type charRawSink struct{ tag string }

func (charRawSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return nil
}

type charRedactor struct{ tag string }

func (h charRedactor) ID() string { return h.tag }

func (charRedactor) Redact(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type charCompactionObs struct{ tag string }

func (charCompactionObs) OnCompaction(context.Context, compaction.Event) error { return nil }

type charCompactionPreserver struct{ tag string }

func (p charCompactionPreserver) ID() string { return p.tag }

func (charCompactionPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charCompactionPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charCompactionPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type charSecretGuard struct{ tag string }

func (g charSecretGuard) ID() string                         { return g.tag }
func (charSecretGuard) Order() int                           { return 0 }
func (charSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (charSecretGuard) Evaluate(context.Context, *lipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type charLocalTurnHandler struct{ tag string }

func (h charLocalTurnHandler) ID() string                      { return h.tag }
func (charLocalTurnHandler) Order() int                        { return 0 }
func (charLocalTurnHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (charLocalTurnHandler) Match(context.Context, lipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (charLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

type charTerminalProvider struct{ tag string }

func (p charTerminalProvider) ID() string { return p.tag }

func (charTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

// --- Helpers ---

func charTags[T any](xs []T, tag func(T) string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, tag(x))
	}
	return out
}

// charBundle contributes two tagged elements on every ordered plane so both
// bundle order and within-bundle order are observable after a merge.
func charBundle(prefix string) lipfeature.FeatureBundle {
	return lipfeature.FeatureBundle{
		SchemaVersion:            lipfeature.SchemaVersionV1,
		SubmitHooks:              []sdkhooks.SubmitHook{charSubmitHook{tag: prefix + "-a"}, charSubmitHook{tag: prefix + "-b"}},
		RequestPartHooks:         []sdkhooks.RequestPartHook{charRequestPartHook{tag: prefix + "-a"}, charRequestPartHook{tag: prefix + "-b"}},
		ResponsePartHooks:        []sdkhooks.ResponsePartHook{charResponsePartHook{tag: prefix + "-a"}, charResponsePartHook{tag: prefix + "-b"}},
		ToolReactors:             []sdkhooks.ToolReactor{charToolReactor{tag: prefix + "-a"}, charToolReactor{tag: prefix + "-b"}},
		Lifecycles:               []lipplugin.Lifecycle{charLifecycle{tag: prefix + "-a"}, charLifecycle{tag: prefix + "-b"}},
		SessionOpeners:           []session.Opener{charOpener{tag: prefix + "-a"}, charOpener{tag: prefix + "-b"}},
		WorkspaceResolvers:       []lipworkspace.Resolver{charResolver{tag: prefix + "-a"}, charResolver{tag: prefix + "-b"}},
		ToolCatalogFilters:       []toolcatalog.Filter{charCatalogFilter{tag: prefix + "-a"}, charCatalogFilter{tag: prefix + "-b"}},
		ToolCallPolicies:         []toolpolicy.Policy{charPolicy{tag: prefix + "-a"}, charPolicy{tag: prefix + "-b"}},
		ToolCallFinalizers:       []toolcall.Finalizer{charFinalizer{tag: prefix + "-a"}, charFinalizer{tag: prefix + "-b"}},
		RequestTransforms:        []request.Transform{charTransform{tag: prefix + "-a"}, charTransform{tag: prefix + "-b"}},
		PreRequestHandlers:       []prerequest.Handler{charPreReq{tag: prefix + "-a"}, charPreReq{tag: prefix + "-b"}},
		RouteHintProviders:       []routehint.Provider{charRouteHint{tag: prefix + "-a"}, charRouteHint{tag: prefix + "-b"}},
		CompletionGates:          []completion.Gate{charCompGate{tag: prefix + "-a"}, charCompGate{tag: prefix + "-b"}},
		AttemptTransforms:        []request.AttemptTransform{charAttemptTransform{tag: prefix + "-a"}, charAttemptTransform{tag: prefix + "-b"}},
		StreamObserverFactories:  []response.StreamObserverFactory{charStreamObserverFactory{tag: prefix + "-a"}, charStreamObserverFactory{tag: prefix + "-b"}},
		TrafficObservers:         []traffic.Observer{charTrafficObs{tag: prefix + "-a"}, charTrafficObs{tag: prefix + "-b"}},
		UsageObservers:           []usage.Observer{charUsageObs{tag: prefix + "-a"}, charUsageObs{tag: prefix + "-b"}},
		RawCaptureSinks:          []traffic.RawCaptureSink{charRawSink{tag: prefix + "-a"}, charRawSink{tag: prefix + "-b"}},
		TrafficRedactors:         []traffic.Redactor{charRedactor{tag: prefix + "-a"}, charRedactor{tag: prefix + "-b"}},
		CompactionObservers:      []compaction.Observer{charCompactionObs{tag: prefix + "-a"}, charCompactionObs{tag: prefix + "-b"}},
		CompactionPreservers:     []compaction.Preserver{charCompactionPreserver{tag: prefix + "-a"}, charCompactionPreserver{tag: prefix + "-b"}},
		SecretGuards:             []secretguard.Guard{charSecretGuard{tag: prefix + "-a"}, charSecretGuard{tag: prefix + "-b"}},
		LocalTurnHandlers:        []localturn.Handler{charLocalTurnHandler{tag: prefix + "-a"}, charLocalTurnHandler{tag: prefix + "-b"}},
		TerminalDecisionProvider: charTerminalProvider{tag: prefix + "-provider"},
	}
}

// charOrderedBundle contributes on every ordered plane without touching the
// exclusive terminal-decision slot.
func charOrderedBundle(prefix string) lipfeature.FeatureBundle {
	b := charBundle(prefix)
	b.TerminalDecisionProvider = nil
	return b
}

func assertAllSliceFieldsNil(t *testing.T, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.Type.Kind() != reflect.Slice {
			continue
		}
		fv := rv.Field(i)
		require.True(t, fv.IsNil(), "%s: want nil slice, got len=%d", field.Name, fv.Len())
	}
}

// --- Tests ---

// Pins requirement 1.1: contributions concatenate across every ordered plane in
// bundle (registration) order with within-bundle order preserved and no
// reordering; zero-length contributions are inert. Lifecycles ride the same
// ordered concatenation at the merge layer even though they leave through the
// side channel at transport time.
func TestMergeBundlesChecked_orderedConcatenationAcrossAllPlanes(t *testing.T) {
	t.Parallel()

	emptyD := lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}
	merged, err := MergeBundlesChecked(charOrderedBundle("A"), charOrderedBundle("B"), emptyD, charOrderedBundle("C"))
	require.NoError(t, err)

	want := []string{"A-a", "A-b", "B-a", "B-b", "C-a", "C-b"}
	tests := []struct {
		name string
		got  func(MergedFeatureSurface) []string
	}{
		{"Lifecycles", func(m MergedFeatureSurface) []string {
			return charTags(m.Lifecycles, func(l lipplugin.Lifecycle) string {
				if lc, ok := l.(charLifecycle); ok {
					return lc.tag
				}
				return ""
			})
		}},
		{"CompactionObservers", func(m MergedFeatureSurface) []string {
			return charTags(m.CompactionObservers, func(o compaction.Observer) string {
				if co, ok := o.(charCompactionObs); ok {
					return co.tag
				}
				return ""
			})
		}},
		{"CompactionPreservers", func(m MergedFeatureSurface) []string {
			return charTags(m.CompactionPreservers, func(p compaction.Preserver) string {
				if cp, ok := p.(charCompactionPreserver); ok {
					return cp.tag
				}
				return ""
			})
		}},
		{"SecretGuards", func(m MergedFeatureSurface) []string {
			return charTags(m.SecretGuards, func(g secretguard.Guard) string {
				if sg, ok := g.(charSecretGuard); ok {
					return sg.tag
				}
				return ""
			})
		}},
		{"LocalTurnHandlers", func(m MergedFeatureSurface) []string {
			return charTags(m.LocalTurnHandlers, func(h localturn.Handler) string {
				if lh, ok := h.(charLocalTurnHandler); ok {
					return lh.tag
				}
				return ""
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, want, tt.got(merged))
		})
	}

	// Exclusive slot: a later bundle's provider lands after ordered slice
	// contributions; the single contribution wins the one slot.
	provider := charTerminalProvider{tag: "C-provider"}
	final, err := MergeBundlesChecked(charOrderedBundle("A"), charOrderedBundle("B"),
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, TerminalDecisionProvider: provider})
	require.NoError(t, err)
	require.Equal(t, provider, final.TerminalDecisionProvider)
}

// Pins actual nil-vs-empty semantics of the legacy merge: nothing materializes
// a slice unless a real contribution lands; explicitly-empty (non-nil) bundle
// slices do not survive as non-nil merged slices.
func TestMergeBundlesChecked_nilVsEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("no_bundles_keeps_all_slice_fields_nil", func(t *testing.T) {
		t.Parallel()
		merged, err := MergeBundlesChecked()
		require.NoError(t, err)
		assertAllSliceFieldsNil(t, merged)
	})

	t.Run("zero_value_bundles_keep_all_slice_fields_nil", func(t *testing.T) {
		t.Parallel()
		merged, err := MergeBundlesChecked(lipfeature.FeatureBundle{}, lipfeature.FeatureBundle{})
		require.NoError(t, err)
		assertAllSliceFieldsNil(t, merged)
	})

	t.Run("explicitly_empty_bundle_slices_do_not_materialize", func(t *testing.T) {
		t.Parallel()
		bundle := lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			SubmitHooks:              []sdkhooks.SubmitHook{},
			RequestPartHooks:         []sdkhooks.RequestPartHook{},
			ResponsePartHooks:        []sdkhooks.ResponsePartHook{},
			ToolReactors:             []sdkhooks.ToolReactor{},
			Lifecycles:               []lipplugin.Lifecycle{},
			SessionOpeners:           []session.Opener{},
			WorkspaceResolvers:       []lipworkspace.Resolver{},
			ToolCatalogFilters:       []toolcatalog.Filter{},
			ToolCallPolicies:         []toolpolicy.Policy{},
			ToolCallFinalizers:       []toolcall.Finalizer{},
			RequestTransforms:        []request.Transform{},
			PreRequestHandlers:       []prerequest.Handler{},
			RouteHintProviders:       []routehint.Provider{},
			CompletionGates:          []completion.Gate{},
			AttemptTransforms:        []request.AttemptTransform{},
			StreamObserverFactories:  []response.StreamObserverFactory{},
			CompactionObservers:      []compaction.Observer{},
			CompactionPreservers:     []compaction.Preserver{},
			SecretGuards:             []secretguard.Guard{},
			LocalTurnHandlers:        []localturn.Handler{},
			TerminalDecisionProvider: nil,
		}
		merged, err := MergeBundlesChecked(bundle)
		require.NoError(t, err)
		assertAllSliceFieldsNil(t, merged)
	})

	t.Run("populated_contributions_materialize_non_nil_slices", func(t *testing.T) {
		t.Parallel()
		merged, err := MergeBundlesChecked(charBundle("A"))
		require.NoError(t, err)
		require.Len(t, merged.CompactionObservers, 2)
		require.Len(t, merged.Lifecycles, 2)
		require.False(t, reflect.ValueOf(merged.CompactionObservers).IsNil())
	})
}

// Pins validate-before-mutate: a rejected contribution leaves the accumulated
// receiver byte-for-byte unchanged, so lifecycles/hooks already merged are
// neither lost nor reordered by a failing Append.
func TestMergedFeatureSurface_Append_failureLeavesReceiverUnchanged(t *testing.T) {
	t.Parallel()

	m, err := MergeBundlesChecked(charBundle("A"))
	require.NoError(t, err)
	snapshot := m

	err = m.Append(charBundle("B"))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrTerminalDecisionProviderConflict)
	require.Contains(t, err.Error(), "A-provider")
	require.Contains(t, err.Error(), "B-provider")
	require.Equal(t, snapshot, m)
}

// Pins fail-closed candidate discard: when a later bundle conflicts, the whole
// accumulated candidate (including earlier lifecycles) is discarded rather
// than returned partially merged.
func TestMergeBundlesChecked_conflictDiscardsAccumulatedCandidate(t *testing.T) {
	t.Parallel()

	merged, err := MergeBundlesChecked(
		charBundle("A"),
		charBundle("B"),
	)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrTerminalDecisionProviderConflict)
	require.Equal(t, MergedFeatureSurface{}, merged)
}
