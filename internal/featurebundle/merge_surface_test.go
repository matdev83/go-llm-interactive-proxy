package featurebundle

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

type testCompactionPreserver struct{ tag string }

func (p testCompactionPreserver) ID() string { return p.tag }

func (testCompactionPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (testCompactionPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (testCompactionPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

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

type testTerminalDecisionProvider struct{ tag string }

func (p testTerminalDecisionProvider) ID() string { return p.tag }

func (testTerminalDecisionProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

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
	if len(m.CompactionObservers) != 0 {
		t.Fatalf("MergeBundles() with no args should be empty: %+v", m)
	}
}

func TestMergeBundlesChecked_TerminalDecisionProviderZeroAndOne(t *testing.T) {
	t.Parallel()
	empty, err := MergeBundlesChecked()
	if err != nil {
		t.Fatalf("empty merge error: %v", err)
	}
	if empty.TerminalDecisionProvider != nil {
		t.Fatal("empty merge unexpectedly contributed provider")
	}

	provider := testTerminalDecisionProvider{tag: "provider.example"}
	merged, err := MergeBundlesChecked(lipfeature.FeatureBundle{
		SchemaVersion:            lipfeature.SchemaVersionV1,
		CompactionObservers:      []compaction.Observer{testCompactionObs{tag: "existing"}},
		TerminalDecisionProvider: provider,
	})
	if err != nil {
		t.Fatalf("one-provider merge error: %v", err)
	}
	if merged.TerminalDecisionProvider != provider {
		t.Fatalf("merged provider = %#v, want %#v", merged.TerminalDecisionProvider, provider)
	}
	obs, ok := merged.CompactionObservers[0].(testCompactionObs)
	if len(merged.CompactionObservers) != 1 || !ok || obs.tag != "existing" {
		t.Fatalf("existing fields changed during provider merge: %#v", merged.CompactionObservers)
	}
}

func TestMergeBundlesChecked_TerminalDecisionProviderConflictFailsBeforePublication(t *testing.T) {
	t.Parallel()
	first := testTerminalDecisionProvider{tag: "provider.first"}
	second := testTerminalDecisionProvider{tag: "provider.second"}
	merged, err := MergeBundlesChecked(
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, TerminalDecisionProvider: first},
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, TerminalDecisionProvider: second},
	)
	if err == nil {
		t.Fatal("duplicate providers were accepted")
	}
	if !errors.Is(err, ErrTerminalDecisionProviderConflict) {
		t.Fatalf("conflict error = %v, want ErrTerminalDecisionProviderConflict", err)
	}
	if !strings.Contains(err.Error(), first.ID()) || !strings.Contains(err.Error(), second.ID()) {
		t.Fatalf("conflict error = %q, want both bounded provider identities", err)
	}
	if merged.TerminalDecisionProvider != nil || len(merged.CompactionObservers) != 0 {
		t.Fatalf("candidate was published after conflict: %#v", merged)
	}
}

func TestMergeBundlesChecked_TerminalDecisionProviderRejectsInvalidProvider(t *testing.T) {
	t.Parallel()
	var typedNil *testTerminalDecisionProvider
	_, err := MergeBundlesChecked(lipfeature.FeatureBundle{
		SchemaVersion:            lipfeature.SchemaVersionV1,
		TerminalDecisionProvider: typedNil,
	})
	if !errors.Is(err, terminaldecision.ErrInvalidProvider) {
		t.Fatalf("typed-nil provider error = %v, want ErrInvalidProvider", err)
	}
}

func TestMergedFeatureSurfaceTerminalDecisionContributionIsSingular(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeFor[MergedFeatureSurface]().FieldByName("TerminalDecisionProvider")
	if !ok {
		t.Fatal("MergedFeatureSurface is missing TerminalDecisionProvider")
	}
	want := reflect.TypeFor[terminaldecision.Provider]()
	if field.Type != want {
		t.Fatalf("TerminalDecisionProvider type = %v, want %v", field.Type, want)
	}
}

func TestMergedFeatureSurfaceAppend_concatenatesAllFields(t *testing.T) {
	t.Parallel()
	b1 := lipfeature.FeatureBundle{
		SchemaVersion:        lipfeature.SchemaVersionV1,
		SubmitHooks:          []sdkhooks.SubmitHook{testSubmitHook{tag: "s1"}},
		RequestPartHooks:     []sdkhooks.RequestPartHook{testRequestPartHook{tag: "r1"}},
		ResponsePartHooks:    []sdkhooks.ResponsePartHook{testResponsePartHook{tag: "rp1"}},
		ToolReactors:         []sdkhooks.ToolReactor{testToolReactor{tag: "tr1"}},
		SessionOpeners:       []session.Opener{testOpener{tag: "o1"}},
		WorkspaceResolvers:   []lipworkspace.Resolver{testResolver{tag: "w1"}},
		ToolCatalogFilters:   []toolcatalog.Filter{testCatalogFilter{tag: "c1"}},
		ToolCallPolicies:     []toolpolicy.Policy{testPolicy{tag: "p1"}},
		ToolCallFinalizers:   []toolcall.Finalizer{testFinalizer{tag: "f1"}},
		RequestTransforms:    []request.Transform{testTransform{tag: "rt1"}},
		PreRequestHandlers:   []prerequest.Handler{testPreReq{tag: "pr1"}},
		RouteHintProviders:   []routehint.Provider{testRouteHint{tag: "rh1"}},
		CompletionGates:      []completion.Gate{testCompGate{tag: "cg1"}},
		AttemptTransforms:    []request.AttemptTransform{testAttemptTransform{tag: "at1"}},
		CompactionObservers:  []compaction.Observer{testCompactionObs{tag: "co1"}},
		CompactionPreservers: []compaction.Preserver{testCompactionPreserver{tag: "cp1"}},
		SecretGuards:         []secretguard.Guard{testSecretGuard{tag: "sg1"}},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion:        lipfeature.SchemaVersionV1,
		SubmitHooks:          []sdkhooks.SubmitHook{testSubmitHook{tag: "s2"}},
		RequestPartHooks:     []sdkhooks.RequestPartHook{testRequestPartHook{tag: "r2"}},
		ResponsePartHooks:    []sdkhooks.ResponsePartHook{testResponsePartHook{tag: "rp2"}},
		ToolReactors:         []sdkhooks.ToolReactor{testToolReactor{tag: "tr2"}},
		SessionOpeners:       []session.Opener{testOpener{tag: "o2"}},
		WorkspaceResolvers:   []lipworkspace.Resolver{testResolver{tag: "w2"}},
		ToolCatalogFilters:   []toolcatalog.Filter{testCatalogFilter{tag: "c2"}},
		ToolCallPolicies:     []toolpolicy.Policy{testPolicy{tag: "p2"}},
		ToolCallFinalizers:   []toolcall.Finalizer{testFinalizer{tag: "f2"}},
		RequestTransforms:    []request.Transform{testTransform{tag: "rt2"}},
		PreRequestHandlers:   []prerequest.Handler{testPreReq{tag: "pr2"}},
		RouteHintProviders:   []routehint.Provider{testRouteHint{tag: "rh2"}},
		CompletionGates:      []completion.Gate{testCompGate{tag: "cg2"}},
		AttemptTransforms:    []request.AttemptTransform{testAttemptTransform{tag: "at2"}},
		CompactionObservers:  []compaction.Observer{testCompactionObs{tag: "co2"}},
		CompactionPreservers: []compaction.Preserver{testCompactionPreserver{tag: "cp2"}},
		SecretGuards:         []secretguard.Guard{testSecretGuard{tag: "sg2"}},
	}
	m := MergeBundles(b1, b2)

	checks := []struct {
		name string
		got  int
	}{
		{"CompactionObservers", len(m.CompactionObservers)},
		{"CompactionPreservers", len(m.CompactionPreservers)},
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
	for i, want := range []string{"cp1", "cp2"} {
		preserver, ok := m.CompactionPreservers[i].(testCompactionPreserver)
		if !ok || preserver.tag != want {
			t.Fatalf("CompactionPreservers[%d]=%T/%q want %q", i, m.CompactionPreservers[i], preserver.tag, want)
		}
	}
}

func TestMergeBundlesGenerated_ToolCallFinalizationMaxArgsBytesMin(t *testing.T) {
	t.Parallel()
	gen, err := MergeBundlesGenerated(
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 4096},
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 1024},
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 0},
	)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated error: %v", err)
	}
	got := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if got != 1024 {
		t.Fatalf("got %d want 1024 (min of positives)", got)
	}
}

func TestMergeBundlesGenerated_ToolCallFinalizationMaxArgsBytesIgnoresNonPositive(t *testing.T) {
	t.Parallel()
	// Merge keeps min of positives only; Validate rejects negatives on FeatureBundle.
	// Non-positive values are not contributions at the merge surface.
	gen, err := MergeBundlesGenerated(
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 2048},
		lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1, ToolCallFinalizationMaxArgsBytes: 0},
	)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated error: %v", err)
	}
	got := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if got != 2048 {
		t.Fatalf("got %d want 2048 (zero ignored at merge)", got)
	}
}

func TestMergeBundles_preservesBundleOrderAcrossSlices(t *testing.T) {
	t.Parallel()
	b1 := lipfeature.FeatureBundle{
		SchemaVersion:       lipfeature.SchemaVersionV1,
		CompactionObservers: []compaction.Observer{testCompactionObs{tag: "first"}},
		ToolCallFinalizers:  []toolcall.Finalizer{testFinalizer{tag: "first"}},
	}
	b2 := lipfeature.FeatureBundle{
		SchemaVersion:       lipfeature.SchemaVersionV1,
		CompactionObservers: []compaction.Observer{testCompactionObs{tag: "second"}},
		ToolCallFinalizers:  []toolcall.Finalizer{testFinalizer{tag: "second"}},
	}
	b3 := lipfeature.FeatureBundle{
		SchemaVersion:       lipfeature.SchemaVersionV1,
		CompactionObservers: []compaction.Observer{testCompactionObs{tag: "third"}},
		ToolCallFinalizers:  []toolcall.Finalizer{testFinalizer{tag: "third"}},
	}
	m := MergeBundles(b1, b2, b3)
	if len(m.CompactionObservers) != 3 {
		t.Fatalf("CompactionObservers: got %d want 3", len(m.CompactionObservers))
	}

	gen, err := MergeBundlesGenerated(b1, b2, b3)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated error: %v", err)
	}
	fins := lipfeature.Get(gen.Frozen, lipfeature.PlaneToolCallFinalizers)
	if len(fins) != 3 {
		t.Fatalf("ToolCallFinalizers: got %d want 3", len(fins))
	}
	f0, ok := fins[0].(testFinalizer)
	if !ok || f0.tag != "first" {
		t.Fatalf("first finalizer: %#v", fins[0])
	}
	f1, ok := fins[1].(testFinalizer)
	if !ok || f1.tag != "second" {
		t.Fatalf("second finalizer: %#v", fins[1])
	}
	f2, ok := fins[2].(testFinalizer)
	if !ok || f2.tag != "third" {
		t.Fatalf("third finalizer: %#v", fins[2])
	}
}

func TestMergeFeatureSurfacesWithHost_ThreeSourceOrdering(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	err := reg.RegisterFeature("test-feature", func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion: lipfeature.SchemaVersionV1,
			TrafficObservers: []traffic.Observer{
				testTrafficObs{tag: "feat-to"},
			},
			UsageObservers: []usage.Observer{
				testUsageObs{tag: "feat-uo"},
			},
			RawCaptureSinks: []traffic.RawCaptureSink{
				testRawSink{tag: "feat-raw"},
			},
			TrafficRedactors: []traffic.Redactor{
				testRedactor{tag: "feat-red"},
			},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	regs := []lipsdk.Registration{
		{
			Kind:        lipsdk.PluginKindFeature,
			ID:          "inst-feature",
			FactoryKind: "test-feature",
			Enabled:     true,
		},
	}

	host := HostContributions{
		TrafficObservers: []traffic.Observer{
			testTrafficObs{tag: "host-to"},
		},
		UsageObservers: []usage.Observer{
			testUsageObs{tag: "host-uo"},
		},
	}

	candExtra := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		TrafficObservers: []traffic.Observer{
			testTrafficObs{tag: "cand-to"},
		},
		UsageObservers: []usage.Observer{
			testUsageObs{tag: "cand-uo"},
		},
		RawCaptureSinks: []traffic.RawCaptureSink{
			testRawSink{tag: "cand-raw"},
		},
		TrafficRedactors: []traffic.Redactor{
			testRedactor{tag: "cand-red"},
		},
	}

	_, gen, err := MergeFeatureSurfacesWithHost(reg, regs, host, candExtra)
	if err != nil {
		t.Fatalf("MergeFeatureSurfacesWithHost error: %v", err)
	}

	to := lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficObservers)
	if len(to) != 3 {
		t.Fatalf("TrafficObservers len = %d, want 3", len(to))
	}
	to0, ok0 := to[0].(testTrafficObs)
	to1, ok1 := to[1].(testTrafficObs)
	to2, ok2 := to[2].(testTrafficObs)
	if !ok0 || !ok1 || !ok2 || to0.tag != "feat-to" || to1.tag != "host-to" || to2.tag != "cand-to" {
		t.Fatalf("TrafficObservers order = %#v, want [feat-to, host-to, cand-to]", to)
	}

	uo := lipfeature.Get(gen.Frozen, lipfeature.PlaneUsageObservers)
	if len(uo) != 3 {
		t.Fatalf("UsageObservers len = %d, want 3", len(uo))
	}
	uo0, ok0 := uo[0].(testUsageObs)
	uo1, ok1 := uo[1].(testUsageObs)
	uo2, ok2 := uo[2].(testUsageObs)
	if !ok0 || !ok1 || !ok2 || uo0.tag != "feat-uo" || uo1.tag != "host-uo" || uo2.tag != "cand-uo" {
		t.Fatalf("UsageObservers order = %#v, want [feat-uo, host-uo, cand-uo]", uo)
	}

	raw := lipfeature.Get(gen.Frozen, lipfeature.PlaneRawCaptureSinks)
	if len(raw) != 2 {
		t.Fatalf("RawCaptureSinks len = %d, want 2", len(raw))
	}
	raw0, ok0 := raw[0].(testRawSink)
	raw1, ok1 := raw[1].(testRawSink)
	if !ok0 || !ok1 || raw0.tag != "feat-raw" || raw1.tag != "cand-raw" {
		t.Fatalf("RawCaptureSinks order = %#v, want [feat-raw, cand-raw]", raw)
	}

	red := lipfeature.Get(gen.Frozen, lipfeature.PlaneTrafficRedactors)
	if len(red) != 2 {
		t.Fatalf("TrafficRedactors len = %d, want 2", len(red))
	}
	if red[0].ID() != "feat-red" || red[1].ID() != "cand-red" {
		t.Fatalf("TrafficRedactors order = %#v, want [feat-red, cand-red]", red)
	}
}

func TestGeneratedMergeSurface_BindAttemptTransforms_ReplaceByIdentity(t *testing.T) {
	t.Parallel()

	initBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{
			testAttemptTransform{tag: "xform-1"},
			testAttemptTransform{tag: "reasoning-preservation-transform"},
			testAttemptTransform{tag: "xform-2"},
		},
	}

	gen, err := MergeBundlesGenerated(initBundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}

	xformsBefore := lipfeature.Get(gen.Frozen, lipfeature.PlaneAttemptTransforms)
	if len(xformsBefore) != 3 {
		t.Fatalf("xforms before len = %d, want 3", len(xformsBefore))
	}

	boundXform := testAttemptTransform{tag: "reasoning-preservation-transform"}
	updatedGen, err := gen.BindAttemptTransforms("reasoning-preservation", []request.AttemptTransform{boundXform})
	if err != nil {
		t.Fatalf("BindAttemptTransforms: %v", err)
	}

	xformsAfter := lipfeature.Get(updatedGen.Frozen, lipfeature.PlaneAttemptTransforms)
	if len(xformsAfter) != 3 {
		t.Fatalf("xforms after len = %d, want 3", len(xformsAfter))
	}
	if xformsAfter[0].ID() != "xform-1" || xformsAfter[1].ID() != "xform-2" || xformsAfter[2].ID() != "reasoning-preservation-transform" {
		t.Fatalf("unexpected replacement order: %v, %v, %v", xformsAfter[0].ID(), xformsAfter[1].ID(), xformsAfter[2].ID())
	}

	// Idempotent: rebinding doesn't duplicate
	reboundGen, err := updatedGen.BindAttemptTransforms("reasoning-preservation", []request.AttemptTransform{boundXform})
	if err != nil {
		t.Fatalf("rebound: %v", err)
	}
	xformsRebound := lipfeature.Get(reboundGen.Frozen, lipfeature.PlaneAttemptTransforms)
	if len(xformsRebound) != 3 {
		t.Fatalf("xforms rebound len = %d, want 3", len(xformsRebound))
	}
	if xformsRebound[0].ID() != "xform-1" || xformsRebound[1].ID() != "xform-2" || xformsRebound[2].ID() != "reasoning-preservation-transform" {
		t.Fatalf("unexpected rebound order: %v, %v, %v", xformsRebound[0].ID(), xformsRebound[1].ID(), xformsRebound[2].ID())
	}
}

func TestGeneratedMergeSurface_BindAttemptTransforms_NilReject(t *testing.T) {
	t.Parallel()

	initBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{
			testAttemptTransform{tag: "xform-1"},
		},
	}

	gen, err := MergeBundlesGenerated(initBundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}

	// Rejects slice containing nil element
	_, err = gen.BindAttemptTransforms("reasoning-preservation", []request.AttemptTransform{nil})
	if err == nil {
		t.Fatalf("expected error for nil transform element, got nil")
	}
	if !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("error = %v, want 'must not be nil'", err)
	}
}

func TestGeneratedMergeSurface_AllPlanesPreserved_AcrossBindOperations(t *testing.T) {
	t.Parallel()

	initBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		SubmitHooks: []sdkhooks.SubmitHook{
			testSubmitHook{tag: "hook-1"},
		},
		SessionOpeners: []session.Opener{
			testOpener{tag: "opener-1"},
		},
		ToolCatalogFilters: []toolcatalog.Filter{
			testCatalogFilter{tag: "cat-1"},
		},
		ToolCallFinalizationMaxArgsBytes: 1024,
		RequestTransforms: []request.Transform{
			testTransform{tag: "req-1"},
		},
		AttemptTransforms: []request.AttemptTransform{
			testAttemptTransform{tag: "xform-1"},
		},
		StreamObserverFactories: []response.StreamObserverFactory{
			testStreamObserverFactory{tag: "obs-1"},
		},
		TrafficObservers: []traffic.Observer{
			testTrafficObs{tag: "to-1"},
		},
		CompactionPreservers: []compaction.Preserver{
			testCompactionPreserver{tag: "cp-1"},
		},
		SecretGuards: []secretguard.Guard{
			testSecretGuard{tag: "guard-1"},
		},
		TerminalDecisionProvider: testTerminalDecisionProvider{tag: "term-1"},
	}

	gen, err := MergeBundlesGenerated(initBundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}

	// 1. Test binding when starting from Frozen only (set == nil)
	fromFrozenOnly := GeneratedMergeSurface{
		Frozen:     gen.Frozen,
		Lifecycles: gen.Lifecycles,
	}

	updated, err := fromFrozenOnly.BindAttemptTransforms("binder-1", []request.AttemptTransform{
		testAttemptTransform{tag: "xform-bound"},
	})
	if err != nil {
		t.Fatalf("BindAttemptTransforms on fromFrozenOnly: %v", err)
	}

	// Verify all planes are preserved
	assert.Len(t, lipfeature.Get(updated.Frozen, lipfeature.PlaneSubmitHooks), 1)
	assert.Len(t, lipfeature.Get(updated.Frozen, lipfeature.PlaneSessionOpeners), 1)
	assert.Len(t, lipfeature.Get(updated.Frozen, lipfeature.PlaneToolCatalogFilters), 1)
	assert.Equal(t, 1024, lipfeature.Get(updated.Frozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.Len(t, lipfeature.Get(updated.Frozen, lipfeature.PlaneRequestTransforms), 1)
	assert.Len(t, lipfeature.Get(updated.Frozen, lipfeature.PlaneStreamObserverFactories), 1)
	assert.Len(t, lipfeature.Get(updated.Frozen, lipfeature.PlaneTrafficObservers), 1)
	assert.Len(t, lipfeature.Get(updated.Frozen, lipfeature.PlaneCompactionPreservers), 1)
	assert.Len(t, lipfeature.Get(updated.Frozen, lipfeature.PlaneSecretGuards), 1)
	assert.NotNil(t, lipfeature.Get(updated.Frozen, lipfeature.PlaneTerminalDecisionProvider))

	xforms := lipfeature.Get(updated.Frozen, lipfeature.PlaneAttemptTransforms)
	assert.Len(t, xforms, 2)
}

func TestGeneratedMergeSurface_SecondOperationFailureTransaction(t *testing.T) {
	t.Parallel()

	initBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{
			testAttemptTransform{tag: "xform-1"},
		},
		StreamObserverFactories: []response.StreamObserverFactory{
			testStreamObserverFactory{tag: "obs-1"},
		},
	}

	g0, err := MergeBundlesGenerated(initBundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}

	// Operation 1 succeeds
	g1, err := g0.BindAttemptTransforms("binder", []request.AttemptTransform{
		testAttemptTransform{tag: "xform-bound"},
	})
	if err != nil {
		t.Fatalf("BindAttemptTransforms failed: %v", err)
	}
	require.Len(t, lipfeature.Get(g1.Frozen, lipfeature.PlaneAttemptTransforms), 2)

	// Operation 2 fails (nil factory in slice under NilReject)
	g2, err := g1.BindStreamObserverFactories("binder", []response.StreamObserverFactory{nil})
	require.Error(t, err)
	assert.True(t, g2.Frozen.IsZero())

	// g1 MUST BE COMPLETELY UNCHANGED
	xformsG1 := lipfeature.Get(g1.Frozen, lipfeature.PlaneAttemptTransforms)
	require.Len(t, xformsG1, 2)
	assert.Equal(t, "xform-1", xformsG1[0].ID())
	assert.Equal(t, "xform-bound", xformsG1[1].ID())

	obsG1 := lipfeature.Get(g1.Frozen, lipfeature.PlaneStreamObserverFactories)
	require.Len(t, obsG1, 1)
	assert.Equal(t, "obs-1", obsG1[0].ID())

	// g0 MUST ALSO BE COMPLETELY UNCHANGED
	xformsG0 := lipfeature.Get(g0.Frozen, lipfeature.PlaneAttemptTransforms)
	require.Len(t, xformsG0, 1)
	assert.Equal(t, "xform-1", xformsG0[0].ID())
}

func TestGeneratedMergeSurface_BindCompactionPreservers(t *testing.T) {
	t.Parallel()

	initBundle := lipfeature.FeatureBundle{
		SchemaVersion: lipfeature.SchemaVersionV1,
		CompactionPreservers: []compaction.Preserver{
			testCompactionPreserver{tag: "pres-1"},
			testCompactionPreserver{tag: "pres-replace"},
		},
	}

	gen, err := MergeBundlesGenerated(initBundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}

	updated, err := gen.BindCompactionPreservers("pres-binder", []compaction.Preserver{
		testCompactionPreserver{tag: "pres-replace"},
	})
	if err != nil {
		t.Fatalf("BindCompactionPreservers failed: %v", err)
	}

	pres := lipfeature.Get(updated.Frozen, lipfeature.PlaneCompactionPreservers)
	require.Len(t, pres, 2)
	assert.Equal(t, "pres-1", pres[0].ID())
	assert.Equal(t, "pres-replace", pres[1].ID())
}
