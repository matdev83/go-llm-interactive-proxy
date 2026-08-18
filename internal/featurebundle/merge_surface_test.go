package featurebundle

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// --- Test helpers (minimal no-op implementations for each interface) ---

type testSubmitHook struct{ tag string }

func (h testSubmitHook) ID() string                      { return h.tag }
func (testSubmitHook) Order() int                        { return 0 }
func (testSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testSubmitHook) Handle(_ context.Context, _ *lipapi.Call, _ *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type testRequestPartHook struct{ tag string }

func (h testRequestPartHook) ID() string                      { return h.tag }
func (testRequestPartHook) Order() int                        { return 0 }
func (testRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testRequestPartHook) HandleRequestParts(_ context.Context, _ *lipapi.Call, _ sdkhooks.PartMeta) error {
	return nil
}

type testResponsePartHook struct{ tag string }

func (h testResponsePartHook) ID() string                      { return h.tag }
func (testResponsePartHook) Order() int                        { return 0 }
func (testResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testResponsePartHook) HandleEvent(_ context.Context, _ *lipapi.Event, _ sdkhooks.PartMeta) error {
	return nil
}

type testToolReactor struct{ tag string }

func (h testToolReactor) ID() string { return h.tag }
func (testToolReactor) Order() int   { return 0 }
func (testToolReactor) HandleToolEvent(_ context.Context, _ lipapi.ToolEvent, _ sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

type testOpener struct{ tag string }

func (h testOpener) ID() string { return h.tag }
func (testOpener) Open(_ context.Context, _ session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type testResolver struct{ tag string }

func (testResolver) Resolve(_ context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{ProjectRoot: "/tmp"}, nil
}

type testCatalogFilter struct{ tag string }

func (h testCatalogFilter) ID() string                      { return h.tag }
func (testCatalogFilter) Order() int                        { return 0 }
func (testCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testCatalogFilter) Handle(_ context.Context, _ *lipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	return nil
}

type testPolicy struct{ tag string }

func (h testPolicy) ID() string                      { return h.tag }
func (testPolicy) Order() int                        { return 0 }
func (testPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testPolicy) Handle(_ context.Context, _ lipapi.ToolEvent, _ toolpolicy.Meta, _ toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type testFinalizer struct{ tag string }

func (h testFinalizer) ID() string { return h.tag }
func (testFinalizer) Order() int   { return 0 }
func (testFinalizer) Finalize(context.Context, toolcall.CompletedCall, lipapi.ToolDef, []lipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type testTransform struct{ tag string }

func (h testTransform) ID() string                      { return h.tag }
func (testTransform) Order() int                        { return 0 }
func (testTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testTransform) Handle(_ context.Context, _ *lipapi.Call, _ request.RequestMeta, _ request.Services) error {
	return nil
}

type testPreReq struct{ tag string }

func (h testPreReq) ID() string                      { return h.tag }
func (testPreReq) Order() int                        { return 0 }
func (testPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testPreReq) Handle(_ context.Context, _ *lipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type testRouteHint struct{ tag string }

func (h testRouteHint) ID() string                      { return h.tag }
func (testRouteHint) Order() int                        { return 0 }
func (testRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testRouteHint) Hint(_ context.Context, _ routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type testCompGate struct{ tag string }

func (h testCompGate) ID() string                      { return h.tag }
func (testCompGate) Order() int                        { return 0 }
func (testCompGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testCompGate) Handle(_ context.Context, _ completion.Meta, _ completion.Buffered, _ completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type testAttemptTransform struct{ tag string }

func (h testAttemptTransform) ID() string                      { return h.tag }
func (testAttemptTransform) Order() int                        { return 0 }
func (testAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (testAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type testStreamObserverFactory struct{ tag string }

func (h testStreamObserverFactory) ID() string                      { return h.tag }
func (testStreamObserverFactory) Order() int                        { return 0 }
func (testStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type testTrafficObs struct{ tag string }

func (testTrafficObs) OnObservation(_ context.Context, _ traffic.Observation) error { return nil }

type testUsageObs struct{ tag string }

func (testUsageObs) OnUsage(_ context.Context, _ usage.Event) error { return nil }

type testCompactionObs struct{ tag string }

func (testCompactionObs) OnCompaction(_ context.Context, _ compaction.Event) error { return nil }

type testRawSink struct{ tag string }

func (testRawSink) WriteRaw(_ context.Context, _ traffic.Leg, _ traffic.CaptureMeta, _ []byte) error {
	return nil
}

type testRedactor struct{ tag string }

func (r testRedactor) ID() string { return r.tag }
func (testRedactor) Redact(_ context.Context, _ traffic.Leg, _ traffic.CaptureMeta, body []byte) ([]byte, error) {
	return body, nil
}

type testSecretGuard struct{ tag string }

func (g testSecretGuard) ID() string                         { return g.tag }
func (testSecretGuard) Order() int                           { return 0 }
func (testSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (testSecretGuard) Evaluate(context.Context, *lipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

// --- Tests ---

func TestMergeBundles_empty(t *testing.T) {
	t.Parallel()
	m := MergeBundles()
	if len(m.SubmitHooks) != 0 || len(m.SessionOpeners) != 0 || len(m.TrafficObservers) != 0 {
		t.Fatalf("MergeBundles() with no args should be empty: %+v", m)
	}
}

func TestMergedFeatureSurfaceAppend_concatenatesAllFields(t *testing.T) {
	t.Parallel()
	b1 := lipfeature.FeatureBundle{
		SchemaVersion:           lipfeature.SchemaVersionV1,
		SubmitHooks:             []sdkhooks.SubmitHook{testSubmitHook{tag: "s1"}},
		RequestPartHooks:        []sdkhooks.RequestPartHook{testRequestPartHook{tag: "r1"}},
		ResponsePartHooks:       []sdkhooks.ResponsePartHook{testResponsePartHook{tag: "rp1"}},
		ToolReactors:            []sdkhooks.ToolReactor{testToolReactor{tag: "tr1"}},
		SessionOpeners:          []session.Opener{testOpener{tag: "o1"}},
		WorkspaceResolvers:      []lipworkspace.Resolver{testResolver{tag: "w1"}},
		ToolCatalogFilters:      []toolcatalog.Filter{testCatalogFilter{tag: "c1"}},
		ToolCallPolicies:        []toolpolicy.Policy{testPolicy{tag: "p1"}},
		ToolCallFinalizers:      []toolcall.Finalizer{testFinalizer{tag: "f1"}},
		RequestTransforms:       []request.Transform{testTransform{tag: "rt1"}},
		PreRequestHandlers:      []prerequest.Handler{testPreReq{tag: "pr1"}},
		RouteHintProviders:      []routehint.Provider{testRouteHint{tag: "rh1"}},
		CompletionGates:         []completion.Gate{testCompGate{tag: "cg1"}},
		AttemptTransforms:       []request.AttemptTransform{testAttemptTransform{tag: "at1"}},
		StreamObserverFactories: []response.StreamObserverFactory{testStreamObserverFactory{tag: "so1"}},
		TrafficObservers:        []traffic.Observer{testTrafficObs{tag: "to1"}},
		UsageObservers:          []usage.Observer{testUsageObs{tag: "uo1"}},
		RawCaptureSinks:         []traffic.RawCaptureSink{testRawSink{tag: "rs1"}},
		TrafficRedactors:        []traffic.Redactor{testRedactor{tag: "red1"}},
		CompactionObservers:     []compaction.Observer{testCompactionObs{tag: "co1"}},
		SecretGuards:            []secretguard.Guard{testSecretGuard{tag: "sg1"}},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion:           lipfeature.SchemaVersionV1,
		SubmitHooks:             []sdkhooks.SubmitHook{testSubmitHook{tag: "s2"}},
		RequestPartHooks:        []sdkhooks.RequestPartHook{testRequestPartHook{tag: "r2"}},
		ResponsePartHooks:       []sdkhooks.ResponsePartHook{testResponsePartHook{tag: "rp2"}},
		ToolReactors:            []sdkhooks.ToolReactor{testToolReactor{tag: "tr2"}},
		SessionOpeners:          []session.Opener{testOpener{tag: "o2"}},
		WorkspaceResolvers:      []lipworkspace.Resolver{testResolver{tag: "w2"}},
		ToolCatalogFilters:      []toolcatalog.Filter{testCatalogFilter{tag: "c2"}},
		ToolCallPolicies:        []toolpolicy.Policy{testPolicy{tag: "p2"}},
		ToolCallFinalizers:      []toolcall.Finalizer{testFinalizer{tag: "f2"}},
		RequestTransforms:       []request.Transform{testTransform{tag: "rt2"}},
		PreRequestHandlers:      []prerequest.Handler{testPreReq{tag: "pr2"}},
		RouteHintProviders:      []routehint.Provider{testRouteHint{tag: "rh2"}},
		CompletionGates:         []completion.Gate{testCompGate{tag: "cg2"}},
		AttemptTransforms:       []request.AttemptTransform{testAttemptTransform{tag: "at2"}},
		StreamObserverFactories: []response.StreamObserverFactory{testStreamObserverFactory{tag: "so2"}},
		TrafficObservers:        []traffic.Observer{testTrafficObs{tag: "to2"}},
		UsageObservers:          []usage.Observer{testUsageObs{tag: "uo2"}},
		RawCaptureSinks:         []traffic.RawCaptureSink{testRawSink{tag: "rs2"}},
		TrafficRedactors:        []traffic.Redactor{testRedactor{tag: "red2"}},
		CompactionObservers:     []compaction.Observer{testCompactionObs{tag: "co2"}},
		SecretGuards:            []secretguard.Guard{testSecretGuard{tag: "sg2"}},
	}
	m := MergeBundles(b1, b2)

	checks := []struct {
		name string
		got  int
	}{
		{"SubmitHooks", len(m.SubmitHooks)},
		{"RequestPartHooks", len(m.RequestPartHooks)},
		{"ResponsePartHooks", len(m.ResponsePartHooks)},
		{"ToolReactors", len(m.ToolReactors)},
		{"SessionOpeners", len(m.SessionOpeners)},
		{"WorkspaceResolvers", len(m.WorkspaceResolvers)},
		{"ToolCatalogFilters", len(m.ToolCatalogFilters)},
		{"ToolCallPolicies", len(m.ToolCallPolicies)},
		{"ToolCallFinalizers", len(m.ToolCallFinalizers)},
		{"RequestTransforms", len(m.RequestTransforms)},
		{"PreRequestHandlers", len(m.PreRequestHandlers)},
		{"RouteHintProviders", len(m.RouteHintProviders)},
		{"CompletionGates", len(m.CompletionGates)},
		{"AttemptTransforms", len(m.AttemptTransforms)},
		{"StreamObserverFactories", len(m.StreamObserverFactories)},
		{"TrafficObservers", len(m.TrafficObservers)},
		{"UsageObservers", len(m.UsageObservers)},
		{"RawCaptureSinks", len(m.RawCaptureSinks)},
		{"TrafficRedactors", len(m.TrafficRedactors)},
		{"CompactionObservers", len(m.CompactionObservers)},
		{"SecretGuards", len(m.SecretGuards)},
	}
	for _, c := range checks {
		if c.got != 2 {
			t.Fatalf("%s: got %d want 2", c.name, c.got)
		}
	}
	for i, want := range []string{"co1", "co2"} {
		observer, ok := m.CompactionObservers[i].(testCompactionObs)
		if !ok || observer.tag != want {
			t.Fatalf("CompactionObservers[%d]=%T/%q want %q", i, m.CompactionObservers[i], observer.tag, want)
		}
	}
}

func TestMergeBundles_ToolCallFinalizationMaxArgsBytesMin(t *testing.T) {
	t.Parallel()
	m := MergeBundles(
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 4096},
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 1024},
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 0},
	)
	if m.ToolCallFinalizationMaxArgsBytes != 1024 {
		t.Fatalf("got %d want 1024 (min of positives)", m.ToolCallFinalizationMaxArgsBytes)
	}
}

func TestMergeBundles_ToolCallFinalizationMaxArgsBytesIgnoresNonPositive(t *testing.T) {
	t.Parallel()
	// Merge keeps min of positives only; Validate rejects negatives on FeatureBundle.
	// Non-positive values are not contributions at the merge surface.
	m := MergeBundles(
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 2048},
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: -1},
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 0},
	)
	if m.ToolCallFinalizationMaxArgsBytes != 2048 {
		t.Fatalf("got %d want 2048 (negatives/zero ignored at merge)", m.ToolCallFinalizationMaxArgsBytes)
	}
}

func TestMergeBundles_preservesBundleOrderAcrossSlices(t *testing.T) {
	t.Parallel()
	b1 := lipfeature.FeatureBundle{
		SchemaVersion:  lipfeature.SchemaVersionV1,
		UsageObservers: []usage.Observer{testUsageObs{tag: "first"}},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion:  lipfeature.SchemaVersionV1,
		UsageObservers: []usage.Observer{testUsageObs{tag: "second"}},
	}
	b3 := lipfeature.FeatureBundle{
		SchemaVersion:  lipfeature.SchemaVersionV1,
		UsageObservers: []usage.Observer{testUsageObs{tag: "third"}},
	}
	m := MergeBundles(b1, b2, b3)
	if len(m.UsageObservers) != 3 {
		t.Fatalf("UsageObservers: got %d want 3", len(m.UsageObservers))
	}
	u0, ok := m.UsageObservers[0].(testUsageObs)
	if !ok || u0.tag != "first" {
		t.Fatalf("first usage observer: %#v", m.UsageObservers[0])
	}
	u1, ok := m.UsageObservers[1].(testUsageObs)
	if !ok || u1.tag != "second" {
		t.Fatalf("second usage observer: %#v", m.UsageObservers[1])
	}
	u2, ok := m.UsageObservers[2].(testUsageObs)
	if !ok || u2.tag != "third" {
		t.Fatalf("third usage observer: %#v", m.UsageObservers[2])
	}
}
