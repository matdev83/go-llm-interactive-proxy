package feature_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type stubLife struct{}

func (stubLife) Start(context.Context) error { return nil }
func (stubLife) Stop(context.Context) error  { return nil }

type stubSubmit struct {
	id  string
	ord int
}

func (s stubSubmit) ID() string                        { return s.id }
func (s stubSubmit) Order() int                        { return s.ord }
func (s stubSubmit) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubSubmit) Handle(context.Context, *lipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type stubTool struct {
	id  string
	ord int
}

func (s stubTool) ID() string { return s.id }
func (s stubTool) Order() int { return s.ord }
func (s stubTool) HandleToolEvent(context.Context, lipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, lipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, lipapi.ToolEvent{}, nil
}

// mergeFeatureBundles mirrors composition-root merge semantics used by the registry
// (concatenate slices in registration order) for contract tests only.
func mergeFeatureBundles(a, b feature.FeatureBundle) feature.FeatureBundle {
	out := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
	}
	if a.SchemaVersion != 0 {
		out.SchemaVersion = a.SchemaVersion
	} else if b.SchemaVersion != 0 {
		out.SchemaVersion = b.SchemaVersion
	} else {
		out.SchemaVersion = feature.SchemaVersionV1
	}
	out.SubmitHooks = append(append([]sdkhooks.SubmitHook(nil), a.SubmitHooks...), b.SubmitHooks...)
	out.RequestPartHooks = append(append([]sdkhooks.RequestPartHook(nil), a.RequestPartHooks...), b.RequestPartHooks...)
	out.ResponsePartHooks = append(append([]sdkhooks.ResponsePartHook(nil), a.ResponsePartHooks...), b.ResponsePartHooks...)
	out.ToolReactors = append(append([]sdkhooks.ToolReactor(nil), a.ToolReactors...), b.ToolReactors...)
	out.Lifecycles = append(append([]lipplugin.Lifecycle(nil), a.Lifecycles...), b.Lifecycles...)
	out.SessionOpeners = append(append([]session.Opener(nil), a.SessionOpeners...), b.SessionOpeners...)
	out.WorkspaceResolvers = append(append([]workspace.Resolver(nil), a.WorkspaceResolvers...), b.WorkspaceResolvers...)
	out.ToolCatalogFilters = append(append([]toolcatalog.Filter(nil), a.ToolCatalogFilters...), b.ToolCatalogFilters...)
	out.ToolCallPolicies = append(append([]toolpolicy.Policy(nil), a.ToolCallPolicies...), b.ToolCallPolicies...)
	out.ToolCallFinalizers = append(append([]toolcall.Finalizer(nil), a.ToolCallFinalizers...), b.ToolCallFinalizers...)
	out.RequestTransforms = append(append([]request.Transform(nil), a.RequestTransforms...), b.RequestTransforms...)
	out.PreRequestHandlers = append(append([]prerequest.Handler(nil), a.PreRequestHandlers...), b.PreRequestHandlers...)
	out.RouteHintProviders = slices.Concat(a.RouteHintProviders, b.RouteHintProviders)
	out.CompletionGates = append(append([]completion.Gate(nil), a.CompletionGates...), b.CompletionGates...)
	out.TrafficObservers = append(append([]traffic.Observer(nil), a.TrafficObservers...), b.TrafficObservers...)
	out.UsageObservers = append(append([]usage.Observer(nil), a.UsageObservers...), b.UsageObservers...)
	out.RawCaptureSinks = append(append([]traffic.RawCaptureSink(nil), a.RawCaptureSinks...), b.RawCaptureSinks...)
	out.TrafficRedactors = append(append([]traffic.Redactor(nil), a.TrafficRedactors...), b.TrafficRedactors...)
	out.CompactionPreservers = append(append([]compaction.Preserver(nil), a.CompactionPreservers...), b.CompactionPreservers...)
	out.SecretGuards = append(append([]secretguard.Guard(nil), a.SecretGuards...), b.SecretGuards...)
	return out
}

type stubSecretGuard struct {
	id  string
	ord int
}

type stubPreserver struct{ id string }

func (p stubPreserver) ID() string { return p.id }

func (stubPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (stubPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (stubPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (s stubSecretGuard) ID() string                           { return s.id }
func (s stubSecretGuard) Order() int                           { return s.ord }
func (s stubSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (s stubSecretGuard) Evaluate(context.Context, *lipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type stubRequestPartHook struct {
	id  string
	ord int
}

func (s stubRequestPartHook) ID() string                        { return s.id }
func (s stubRequestPartHook) Order() int                        { return s.ord }
func (s stubRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubRequestPartHook) HandleRequestParts(ctx context.Context, call *lipapi.Call, meta sdkhooks.PartMeta) error {
	return nil
}

type stubResponsePartHook struct {
	id  string
	ord int
}

func (s stubResponsePartHook) ID() string                        { return s.id }
func (s stubResponsePartHook) Order() int                        { return s.ord }
func (s stubResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubResponsePartHook) HandleEvent(ctx context.Context, ev *lipapi.Event, meta sdkhooks.PartMeta) error {
	return nil
}

type stubToolFilter struct{ id string }

func (f stubToolFilter) ID() string                        { return f.id }
func (f stubToolFilter) Order() int                        { return 0 }
func (f stubToolFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubToolFilter) Handle(ctx context.Context, call *lipapi.Call, meta toolcatalog.CatalogMeta, svc toolcatalog.Services) error {
	return nil
}

type stubToolPolicy struct{ id string }

func (p stubToolPolicy) ID() string                      { return p.id }
func (stubToolPolicy) Order() int                        { return 0 }
func (stubToolPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubToolPolicy) Handle(ctx context.Context, event lipapi.ToolEvent, meta toolpolicy.Meta, svc toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type stubToolFinalizer struct{ id string }

func (f stubToolFinalizer) ID() string { return f.id }
func (stubToolFinalizer) Order() int   { return 0 }
func (stubToolFinalizer) Finalize(ctx context.Context, call toolcall.CompletedCall, tool lipapi.ToolDef, catalog []lipapi.ToolDef, meta toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type stubTransform struct{ id string }

func (t stubTransform) ID() string                      { return t.id }
func (stubTransform) Order() int                        { return 0 }
func (stubTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubTransform) Handle(ctx context.Context, call *lipapi.Call, meta request.RequestMeta, svc request.Services) error {
	return nil
}

type stubPreRequestHandler struct{ id string }

func (h stubPreRequestHandler) ID() string                      { return h.id }
func (stubPreRequestHandler) Order() int                        { return 0 }
func (stubPreRequestHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubPreRequestHandler) Handle(ctx context.Context, call *lipapi.Call, meta prerequest.Meta, svc prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type stubRouteHint struct{ id string }

func (r stubRouteHint) ID() string                      { return r.id }
func (stubRouteHint) Order() int                        { return 0 }
func (stubRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubRouteHint) Hint(ctx context.Context, in routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type stubCompletionGate struct{ id string }

func (g stubCompletionGate) ID() string                      { return g.id }
func (stubCompletionGate) Order() int                        { return 0 }
func (stubCompletionGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubCompletionGate) Handle(ctx context.Context, meta completion.Meta, buf completion.Buffered, svc completion.Services) (completion.Outcome, error) {
	return completion.Outcome{}, nil
}

type stubTrafficObserver struct{}

func (stubTrafficObserver) OnObservation(ctx context.Context, ev traffic.Observation) error {
	return nil
}

type stubUsageObserver struct{}

func (stubUsageObserver) OnUsage(ctx context.Context, ev usage.Event) error { return nil }

type stubRawCaptureSink struct{}

func (stubRawCaptureSink) WriteRaw(ctx context.Context, leg traffic.Leg, meta traffic.CaptureMeta, payload []byte) error {
	return nil
}

type stubTrafficRedactor struct{ id string }

func (r stubTrafficRedactor) ID() string { return r.id }
func (stubTrafficRedactor) Redact(ctx context.Context, leg traffic.Leg, meta traffic.CaptureMeta, body []byte) ([]byte, error) {
	return body, nil
}

type stubCompactionObserver struct{}

func (stubCompactionObserver) OnCompaction(context.Context, compaction.Event) error { return nil }

type stubLocalTurnHandler struct{ id string }

func (h stubLocalTurnHandler) ID() string                       { return h.id }
func (stubLocalTurnHandler) Order() int                         { return 0 }
func (stubLocalTurnHandler) FailureMode() localturn.FailureMode { return localturn.FailOpen }
func (stubLocalTurnHandler) Match(ctx context.Context, call lipapi.Call, meta localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}
func (stubLocalTurnHandler) Handle(ctx context.Context, input localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "ok"}, nil
}

func TestEmptyFeatureBundle(t *testing.T) {
	t.Parallel()
	var b feature.FeatureBundle
	if b.SchemaVersion != 0 {
		t.Fatalf("zero SchemaVersion: %d", b.SchemaVersion)
	}
	if b.SubmitHooks != nil || b.RequestPartHooks != nil || b.ResponsePartHooks != nil || b.ToolReactors != nil {
		t.Fatal("expected all hook slices nil on zero value")
	}
	if b.Lifecycles != nil {
		t.Fatal("expected Lifecycles nil")
	}
	if b.SessionOpeners != nil || b.WorkspaceResolvers != nil {
		t.Fatal("expected session/workspace slices nil on zero value")
	}
	if b.ToolCatalogFilters != nil || b.RequestTransforms != nil || b.PreRequestHandlers != nil || b.RouteHintProviders != nil {
		t.Fatal("expected catalog/transform/pre-request/route-hint slices nil on zero value")
	}
	if b.AttemptTransforms != nil {
		t.Fatal("expected AttemptTransforms nil on zero value")
	}
	if b.StreamObserverFactories != nil {
		t.Fatal("expected StreamObserverFactories nil on zero value")
	}
	if b.CompletionGates != nil {
		t.Fatal("expected CompletionGates nil on zero value")
	}
	if b.TrafficObservers != nil || b.RawCaptureSinks != nil || b.TrafficRedactors != nil {
		t.Fatal("expected traffic slices nil on zero value")
	}
	if b.ToolCallPolicies != nil || b.ToolCallFinalizers != nil || b.UsageObservers != nil {
		t.Fatal("expected tool policy/finalizer and usage observer slices nil on zero value")
	}
	if b.SecretGuards != nil {
		t.Fatal("expected SecretGuards nil on zero value")
	}
	if b.CompactionPreservers != nil {
		t.Fatal("expected CompactionPreservers nil on zero value")
	}
}

type terminalDecisionProvider struct{ id string }

func (p terminalDecisionProvider) ID() string { return p.id }

func (terminalDecisionProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type terminalDecisionPtrProvider struct{}

func (*terminalDecisionPtrProvider) ID() string { return "provider.example" }

func (*terminalDecisionPtrProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type terminalDecisionPanicProvider struct{}

func (*terminalDecisionPanicProvider) ID() string { panic("unbounded provider detail") }

func (*terminalDecisionPanicProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}

func TestFeatureBundle_ValidateTerminalDecisionProvider(t *testing.T) {
	t.Parallel()
	valid := feature.FeatureBundle{
		SchemaVersion:            feature.SchemaVersionV1,
		TerminalDecisionProvider: terminalDecisionProvider{id: "provider.example"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid provider rejected: %v", err)
	}

	cases := map[string]terminaldecision.Provider{
		"nil": nil,
		"typed nil": func() terminaldecision.Provider {
			var p *terminalDecisionPtrProvider
			return p
		}(),
		"blank id":     terminalDecisionProvider{id: "   "},
		"invalid utf8": terminalDecisionProvider{id: string([]byte{0xff})},
		"oversized id": terminalDecisionProvider{id: strings.Repeat("p", terminaldecision.MaxProviderIDBytes+1)},
		"panicking id": &terminalDecisionPanicProvider{},
	}
	for name, provider := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, TerminalDecisionProvider: provider}
			err := b.Validate()
			if name == "nil" {
				if err != nil {
					t.Fatalf("nil provider changed zero-provider validation: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() accepted invalid provider")
			}
			if !errors.Is(err, terminaldecision.ErrInvalidProvider) {
				t.Fatalf("Validate() error = %v, want ErrInvalidProvider", err)
			}
			if strings.Contains(err.Error(), "unbounded provider detail") {
				t.Fatal("provider panic detail leaked into validation error")
			}
		})
	}
}

func TestFeatureBundleTerminalDecisionContributionIsSingular(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeFor[feature.FeatureBundle]().FieldByName("TerminalDecisionProvider")
	if !ok {
		t.Fatal("FeatureBundle is missing TerminalDecisionProvider")
	}
	want := reflect.TypeFor[terminaldecision.Provider]()
	if field.Type != want {
		t.Fatalf("TerminalDecisionProvider type = %v, want %v", field.Type, want)
	}
}

func TestFeatureBundle_ValidateRejectsNilCompactionPreserver(t *testing.T) {
	t.Parallel()
	b := feature.FeatureBundle{
		SchemaVersion:        feature.SchemaVersionV1,
		CompactionPreservers: []compaction.Preserver{nil},
	}
	if err := b.Validate(); err == nil {
		t.Fatal("expected nil CompactionPreserver validation error")
	}
}

func TestFeatureBundlePreservesHooksAndLifecycles(t *testing.T) {
	t.Parallel()
	h1 := stubSubmit{id: "a", ord: 1}
	h2 := stubSubmit{id: "b", ord: 2}
	b := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		SubmitHooks:   []sdkhooks.SubmitHook{h1, h2},
		Lifecycles:    []lipplugin.Lifecycle{stubLife{}, stubLife{}},
	}
	if len(b.SubmitHooks) != 2 {
		t.Fatalf("submit hooks: %d", len(b.SubmitHooks))
	}
	if len(b.Lifecycles) != 2 {
		t.Fatalf("lifecycles: %d", len(b.Lifecycles))
	}
}

func TestFeatureBundle_Validate_emptyAndV1(t *testing.T) {
	t.Parallel()
	if err := (feature.FeatureBundle{}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1}).Validate(); err != nil {
		t.Fatal(err)
	}
	bad := feature.FeatureBundle{
		SubmitHooks: []sdkhooks.SubmitHook{stubSubmit{id: "x", ord: 0}},
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for hooks with schema version 0")
	}
	fixed := bad
	fixed.SchemaVersion = feature.SchemaVersionV1
	if err := fixed.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureBundle_Validate_maxArgsBytesOnlyRequiresSchemaV1(t *testing.T) {
	t.Parallel()
	maxOnly := feature.FeatureBundle{ToolCallFinalizationMaxArgsBytes: 1024}
	if err := maxOnly.Validate(); err == nil {
		t.Fatal("expected error for max-args-only bundle with schema version 0")
	}
	ok := feature.FeatureBundle{
		SchemaVersion:                    feature.SchemaVersionV1,
		ToolCallFinalizationMaxArgsBytes: 1024,
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureBundle_Validate_negativeMaxArgsBytes(t *testing.T) {
	t.Parallel()
	bad := feature.FeatureBundle{
		SchemaVersion:                    feature.SchemaVersionV1,
		ToolCallFinalizationMaxArgsBytes: -1,
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for negative ToolCallFinalizationMaxArgsBytes")
	}
	unset := feature.FeatureBundle{
		ToolCallFinalizationMaxArgsBytes: -8,
	}
	if err := unset.Validate(); err == nil {
		t.Fatal("expected error for negative ToolCallFinalizationMaxArgsBytes even with schema version 0")
	}
}

func TestMergeFeatureBundlesAbsentChainsStayAbsent(t *testing.T) {
	t.Parallel()
	submitOnly := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		SubmitHooks:   []sdkhooks.SubmitHook{stubSubmit{id: "s", ord: 0}},
	}
	toolOnly := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		ToolReactors:  []sdkhooks.ToolReactor{stubTool{id: "t", ord: 0}},
	}
	merged := mergeFeatureBundles(submitOnly, toolOnly)
	if len(merged.SubmitHooks) != 1 {
		t.Fatalf("submit: %d", len(merged.SubmitHooks))
	}
	if merged.RequestPartHooks != nil {
		t.Fatalf("expected nil RequestPartHooks, got len=%d", len(merged.RequestPartHooks))
	}
	if merged.ResponsePartHooks != nil {
		t.Fatalf("expected nil ResponsePartHooks")
	}
	if len(merged.ToolReactors) != 1 {
		t.Fatalf("tool reactors: %d", len(merged.ToolReactors))
	}
}

type stubOpen struct{ id string }

func (s stubOpen) ID() string { return s.id }
func (stubOpen) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type stubWS struct{}

func (stubWS) Resolve(context.Context) (workspace.WorkspaceView, error) {
	return workspace.WorkspaceView{}, nil
}

func TestFeatureBundle_SecretGuards_orderCloneAndValidate(t *testing.T) {
	t.Parallel()
	bad := feature.FeatureBundle{SecretGuards: []secretguard.Guard{stubSecretGuard{id: "sg-a", ord: 2}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected schema error for SecretGuards without SchemaVersionV1")
	}
	a := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		SecretGuards:  []secretguard.Guard{stubSecretGuard{id: "sg-a", ord: 2}},
	}
	b := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		SecretGuards:  []secretguard.Guard{stubSecretGuard{id: "sg-b", ord: 1}},
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	merged := mergeFeatureBundles(a, b)
	if len(merged.SecretGuards) != 2 {
		t.Fatalf("merged SecretGuards: %d", len(merged.SecretGuards))
	}
	if merged.SecretGuards[0].ID() != "sg-a" || merged.SecretGuards[1].ID() != "sg-b" {
		t.Fatalf("merge order not preserved: %q then %q", merged.SecretGuards[0].ID(), merged.SecretGuards[1].ID())
	}
	// Clone semantics: mutating the source slice after merge must not affect the merged copy.
	a.SecretGuards[0] = stubSecretGuard{id: "mutated", ord: 99}
	if merged.SecretGuards[0].ID() != "sg-a" {
		t.Fatal("merged SecretGuards must be a cloned slice")
	}
}

func TestFeatureBundle_Validate_sessionWorkspaceRequiresSchemaV1(t *testing.T) {
	t.Parallel()
	bad := feature.FeatureBundle{SessionOpeners: []session.Opener{stubOpen{id: "x"}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected schema error")
	}
	ok := feature.FeatureBundle{
		SchemaVersion:  feature.SchemaVersionV1,
		SessionOpeners: []session.Opener{stubOpen{id: "x"}},
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	wsOnly := feature.FeatureBundle{
		SchemaVersion:      feature.SchemaVersionV1,
		WorkspaceResolvers: []workspace.Resolver{stubWS{}},
	}
	if err := wsOnly.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBundleFromPlanes_isolation(t *testing.T) {
	t.Parallel()

	// 1. SchemaVersion is SchemaVersionV1
	cs := feature.NewContributionSet()
	submitHook := stubSubmit{id: "hook-1", ord: 10}
	if err := feature.Contribute(cs, feature.PlaneSubmitHooks, "feat-1", []sdkhooks.SubmitHook{submitHook}); err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	frozen := cs.Freeze()

	life1 := stubLife{}
	inputLifecycles := []lipplugin.Lifecycle{life1}
	bundle := feature.BundleFromPlanes(frozen, inputLifecycles)

	if bundle.SchemaVersion != feature.SchemaVersionV1 {
		t.Fatalf("SchemaVersion = %d, want %d", bundle.SchemaVersion, feature.SchemaVersionV1)
	}

	// 2. Lifecycle isolation and nil vs empty preservation
	if len(bundle.Lifecycles) != 1 {
		t.Fatalf("Lifecycles len = %d, want 1", len(bundle.Lifecycles))
	}
	inputLifecycles[0] = nil
	if bundle.Lifecycles[0] == nil {
		t.Fatal("mutating source lifecycles affected bundle.Lifecycles")
	}

	nilLifeBundle := feature.BundleFromPlanes(frozen, nil)
	if nilLifeBundle.Lifecycles != nil {
		t.Fatal("expected nil Lifecycles preserved")
	}

	emptyLifeBundle := feature.BundleFromPlanes(frozen, []lipplugin.Lifecycle{})
	if emptyLifeBundle.Lifecycles == nil {
		t.Fatal("expected non-nil empty Lifecycles preserved")
	}
	if len(emptyLifeBundle.Lifecycles) != 0 {
		t.Fatalf("expected 0 lifecycles, got %d", len(emptyLifeBundle.Lifecycles))
	}

	// 3. FrozenPlaneSet isolation
	hooksFromBundle := feature.Get(bundle.PlaneSet, feature.PlaneSubmitHooks)
	if len(hooksFromBundle) != 1 || hooksFromBundle[0].ID() != "hook-1" {
		t.Fatalf("unexpected hooks from bundle: %v", hooksFromBundle)
	}
}

func TestFeatureBundle_Validate_OldNewLifecycleEmpty(t *testing.T) {
	t.Parallel()

	// Empty bundle: SchemaVersion 0 and 1 are valid, other is rejected
	if err := (feature.FeatureBundle{}).Validate(); err != nil {
		t.Fatalf("empty bundle with SchemaVersion 0 rejected: %v", err)
	}
	if err := (feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1}).Validate(); err != nil {
		t.Fatalf("empty bundle with SchemaVersionV1 rejected: %v", err)
	}
	if err := (feature.FeatureBundle{SchemaVersion: 2}).Validate(); err == nil {
		t.Fatal("empty bundle with SchemaVersion 2 accepted")
	}

	// Lifecycle-only bundle: requires SchemaVersionV1
	lifeBad := feature.FeatureBundle{Lifecycles: []lipplugin.Lifecycle{stubLife{}}}
	if err := lifeBad.Validate(); err == nil {
		t.Fatal("lifecycle-only bundle with SchemaVersion 0 accepted")
	}
	lifeOk := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		Lifecycles:    []lipplugin.Lifecycle{stubLife{}},
	}
	if err := lifeOk.Validate(); err != nil {
		t.Fatalf("lifecycle-only bundle with SchemaVersionV1 rejected: %v", err)
	}

	// Old-only bundle: requires SchemaVersionV1
	oldBad := feature.FeatureBundle{
		SubmitHooks: []sdkhooks.SubmitHook{stubSubmit{id: "h", ord: 1}},
	}
	if err := oldBad.Validate(); err == nil {
		t.Fatal("old-only bundle with SchemaVersion 0 accepted")
	}
	oldOk := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		SubmitHooks:   []sdkhooks.SubmitHook{stubSubmit{id: "h", ord: 1}},
	}
	if err := oldOk.Validate(); err != nil {
		t.Fatalf("old-only bundle with SchemaVersionV1 rejected: %v", err)
	}

	// New-only bundle (PlaneSet): requires SchemaVersionV1
	cs := feature.NewContributionSet()
	if err := feature.Contribute(cs, feature.PlaneSubmitHooks, "feat", []sdkhooks.SubmitHook{stubSubmit{id: "h", ord: 1}}); err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	frozen := cs.Freeze()

	newBad := feature.FeatureBundle{
		PlaneSet: frozen,
	}
	if err := newBad.Validate(); err == nil {
		t.Fatal("new-only bundle with SchemaVersion 0 accepted")
	}
	newOk := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		PlaneSet:      frozen,
	}
	if err := newOk.Validate(); err != nil {
		t.Fatalf("new-only bundle with SchemaVersionV1 rejected: %v", err)
	}
}

type legacyFieldEntry struct {
	name      string
	withEmpty func(b *feature.FeatureBundle)
	withVal   func(b *feature.FeatureBundle)
}

var expectedLegacyFields = []string{
	"SubmitHooks",
	"RequestPartHooks",
	"ResponsePartHooks",
	"ToolReactors",
	"SessionOpeners",
	"WorkspaceResolvers",
	"ToolCatalogFilters",
	"ToolCallPolicies",
	"ToolCallFinalizers",
	"ToolCallFinalizationMaxArgsBytes",
	"RequestTransforms",
	"PreRequestHandlers",
	"RouteHintProviders",
	"CompletionGates",
	"AttemptTransforms",
	"StreamObserverFactories",
	"TrafficObservers",
	"UsageObservers",
	"RawCaptureSinks",
	"TrafficRedactors",
	"CompactionObservers",
	"CompactionPreservers",
	"SecretGuards",
	"LocalTurnHandlers",
	"TerminalDecisionProvider",
}

func legacyFieldTable() []legacyFieldEntry {
	return []legacyFieldEntry{
		{
			name:      "SubmitHooks",
			withEmpty: func(b *feature.FeatureBundle) { b.SubmitHooks = []sdkhooks.SubmitHook{} },
			withVal:   func(b *feature.FeatureBundle) { b.SubmitHooks = []sdkhooks.SubmitHook{stubSubmit{id: "s1", ord: 1}} },
		},
		{
			name:      "RequestPartHooks",
			withEmpty: func(b *feature.FeatureBundle) { b.RequestPartHooks = []sdkhooks.RequestPartHook{} },
			withVal: func(b *feature.FeatureBundle) {
				b.RequestPartHooks = []sdkhooks.RequestPartHook{stubRequestPartHook{id: "rp1"}}
			},
		},
		{
			name:      "ResponsePartHooks",
			withEmpty: func(b *feature.FeatureBundle) { b.ResponsePartHooks = []sdkhooks.ResponsePartHook{} },
			withVal: func(b *feature.FeatureBundle) {
				b.ResponsePartHooks = []sdkhooks.ResponsePartHook{stubResponsePartHook{id: "resp1"}}
			},
		},
		{
			name:      "ToolReactors",
			withEmpty: func(b *feature.FeatureBundle) { b.ToolReactors = []sdkhooks.ToolReactor{} },
			withVal:   func(b *feature.FeatureBundle) { b.ToolReactors = []sdkhooks.ToolReactor{stubTool{id: "t1"}} },
		},
		{
			name:      "SessionOpeners",
			withEmpty: func(b *feature.FeatureBundle) { b.SessionOpeners = []session.Opener{} },
			withVal:   func(b *feature.FeatureBundle) { b.SessionOpeners = []session.Opener{stubOpen{id: "so1"}} },
		},
		{
			name:      "WorkspaceResolvers",
			withEmpty: func(b *feature.FeatureBundle) { b.WorkspaceResolvers = []workspace.Resolver{} },
			withVal:   func(b *feature.FeatureBundle) { b.WorkspaceResolvers = []workspace.Resolver{stubWS{}} },
		},
		{
			name:      "ToolCatalogFilters",
			withEmpty: func(b *feature.FeatureBundle) { b.ToolCatalogFilters = []toolcatalog.Filter{} },
			withVal:   func(b *feature.FeatureBundle) { b.ToolCatalogFilters = []toolcatalog.Filter{stubToolFilter{id: "tf1"}} },
		},
		{
			name:      "ToolCallPolicies",
			withEmpty: func(b *feature.FeatureBundle) { b.ToolCallPolicies = []toolpolicy.Policy{} },
			withVal:   func(b *feature.FeatureBundle) { b.ToolCallPolicies = []toolpolicy.Policy{stubToolPolicy{id: "tp1"}} },
		},
		{
			name:      "ToolCallFinalizers",
			withEmpty: func(b *feature.FeatureBundle) { b.ToolCallFinalizers = []toolcall.Finalizer{} },
			withVal: func(b *feature.FeatureBundle) {
				b.ToolCallFinalizers = []toolcall.Finalizer{stubToolFinalizer{id: "tfin1"}}
			},
		},
		{
			name:      "ToolCallFinalizationMaxArgsBytes",
			withEmpty: nil,
			withVal:   func(b *feature.FeatureBundle) { b.ToolCallFinalizationMaxArgsBytes = 1024 },
		},
		{
			name:      "RequestTransforms",
			withEmpty: func(b *feature.FeatureBundle) { b.RequestTransforms = []request.Transform{} },
			withVal:   func(b *feature.FeatureBundle) { b.RequestTransforms = []request.Transform{stubTransform{id: "rt1"}} },
		},
		{
			name:      "PreRequestHandlers",
			withEmpty: func(b *feature.FeatureBundle) { b.PreRequestHandlers = []prerequest.Handler{} },
			withVal: func(b *feature.FeatureBundle) {
				b.PreRequestHandlers = []prerequest.Handler{stubPreRequestHandler{id: "prh1"}}
			},
		},
		{
			name:      "RouteHintProviders",
			withEmpty: func(b *feature.FeatureBundle) { b.RouteHintProviders = []routehint.Provider{} },
			withVal:   func(b *feature.FeatureBundle) { b.RouteHintProviders = []routehint.Provider{stubRouteHint{id: "rh1"}} },
		},
		{
			name:      "CompletionGates",
			withEmpty: func(b *feature.FeatureBundle) { b.CompletionGates = []completion.Gate{} },
			withVal:   func(b *feature.FeatureBundle) { b.CompletionGates = []completion.Gate{stubCompletionGate{id: "cg1"}} },
		},
		{
			name:      "AttemptTransforms",
			withEmpty: func(b *feature.FeatureBundle) { b.AttemptTransforms = []request.AttemptTransform{} },
			withVal: func(b *feature.FeatureBundle) {
				b.AttemptTransforms = []request.AttemptTransform{stubAttemptTransform{id: "at1"}}
			},
		},
		{
			name:      "StreamObserverFactories",
			withEmpty: func(b *feature.FeatureBundle) { b.StreamObserverFactories = []response.StreamObserverFactory{} },
			withVal: func(b *feature.FeatureBundle) {
				b.StreamObserverFactories = []response.StreamObserverFactory{stubStreamObserverFactory{id: "sof1"}}
			},
		},
		{
			name:      "TrafficObservers",
			withEmpty: func(b *feature.FeatureBundle) { b.TrafficObservers = []traffic.Observer{} },
			withVal:   func(b *feature.FeatureBundle) { b.TrafficObservers = []traffic.Observer{stubTrafficObserver{}} },
		},
		{
			name:      "UsageObservers",
			withEmpty: func(b *feature.FeatureBundle) { b.UsageObservers = []usage.Observer{} },
			withVal:   func(b *feature.FeatureBundle) { b.UsageObservers = []usage.Observer{stubUsageObserver{}} },
		},
		{
			name:      "RawCaptureSinks",
			withEmpty: func(b *feature.FeatureBundle) { b.RawCaptureSinks = []traffic.RawCaptureSink{} },
			withVal:   func(b *feature.FeatureBundle) { b.RawCaptureSinks = []traffic.RawCaptureSink{stubRawCaptureSink{}} },
		},
		{
			name:      "TrafficRedactors",
			withEmpty: func(b *feature.FeatureBundle) { b.TrafficRedactors = []traffic.Redactor{} },
			withVal: func(b *feature.FeatureBundle) {
				b.TrafficRedactors = []traffic.Redactor{stubTrafficRedactor{id: "tr1"}}
			},
		},
		{
			name:      "CompactionObservers",
			withEmpty: func(b *feature.FeatureBundle) { b.CompactionObservers = []compaction.Observer{} },
			withVal: func(b *feature.FeatureBundle) {
				b.CompactionObservers = []compaction.Observer{stubCompactionObserver{}}
			},
		},
		{
			name:      "CompactionPreservers",
			withEmpty: func(b *feature.FeatureBundle) { b.CompactionPreservers = []compaction.Preserver{} },
			withVal: func(b *feature.FeatureBundle) {
				b.CompactionPreservers = []compaction.Preserver{stubPreserver{id: "cp1"}}
			},
		},
		{
			name:      "SecretGuards",
			withEmpty: func(b *feature.FeatureBundle) { b.SecretGuards = []secretguard.Guard{} },
			withVal:   func(b *feature.FeatureBundle) { b.SecretGuards = []secretguard.Guard{stubSecretGuard{id: "sg1"}} },
		},
		{
			name:      "LocalTurnHandlers",
			withEmpty: func(b *feature.FeatureBundle) { b.LocalTurnHandlers = []localturn.Handler{} },
			withVal: func(b *feature.FeatureBundle) {
				b.LocalTurnHandlers = []localturn.Handler{stubLocalTurnHandler{id: "lt.1"}}
			},
		},
		{
			name:      "TerminalDecisionProvider",
			withEmpty: nil,
			withVal:   func(b *feature.FeatureBundle) { b.TerminalDecisionProvider = terminalDecisionProvider{id: "term.1"} },
		},
	}
}

func TestFeatureBundle_PinnedExpectedLegacyFields(t *testing.T) {
	t.Parallel()
	bundleType := reflect.TypeFor[feature.FeatureBundle]()
	var actualFields []string
	for i := 0; i < bundleType.NumField(); i++ {
		name := bundleType.Field(i).Name
		if name == "SchemaVersion" || name == "PlaneSet" || name == "Lifecycles" {
			continue
		}
		actualFields = append(actualFields, name)
	}
	slices.Sort(actualFields)
	sortedExpected := slices.Clone(expectedLegacyFields)
	slices.Sort(sortedExpected)
	if !slices.Equal(actualFields, sortedExpected) {
		t.Fatalf("FeatureBundle legacy fields mismatch:\n got: %v\nwant: %v", actualFields, sortedExpected)
	}

	table := legacyFieldTable()
	if len(table) != len(expectedLegacyFields) {
		t.Fatalf("legacyFieldTable len = %d, want %d", len(table), len(expectedLegacyFields))
	}
	for _, entry := range table {
		if !slices.Contains(expectedLegacyFields, entry.name) {
			t.Fatalf("legacyFieldTable has unknown field %q", entry.name)
		}
	}
}

func TestFeatureBundle_Validate_DualTransportRejection(t *testing.T) {
	t.Parallel()

	cs := feature.NewContributionSet()
	if err := feature.Contribute(cs, feature.PlaneSubmitHooks, "feat", []sdkhooks.SubmitHook{stubSubmit{id: "h", ord: 1}}); err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	frozen := cs.Freeze()

	const wantErr = "feature: FeatureBundle: PlaneSet cannot be combined with deprecated named plane fields"

	table := legacyFieldTable()
	for _, entry := range table {
		entry := entry
		t.Run("Populated_"+entry.name, func(t *testing.T) {
			t.Parallel()
			b := feature.FeatureBundle{
				SchemaVersion: feature.SchemaVersionV1,
				PlaneSet:      frozen,
			}
			entry.withVal(&b)
			err := b.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted dual transport with populated %s", entry.name)
			}
			if err.Error() != wantErr {
				t.Fatalf("Validate() error = %q, want %q", err.Error(), wantErr)
			}
		})

		if entry.withEmpty != nil {
			t.Run("ExplicitEmpty_"+entry.name, func(t *testing.T) {
				t.Parallel()
				b := feature.FeatureBundle{
					SchemaVersion: feature.SchemaVersionV1,
					PlaneSet:      frozen,
				}
				entry.withEmpty(&b)
				err := b.Validate()
				if err == nil {
					t.Fatalf("Validate() accepted dual transport with explicit-empty %s", entry.name)
				}
				if err.Error() != wantErr {
					t.Fatalf("Validate() error = %q, want %q", err.Error(), wantErr)
				}
			})
		}
	}

	// PlaneSet with Lifecycles is valid (lifecycles are not a plane and may coexist with PlaneSet)
	withLife := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		PlaneSet:      frozen,
		Lifecycles:    []lipplugin.Lifecycle{stubLife{}},
	}
	if err := withLife.Validate(); err != nil {
		t.Fatalf("PlaneSet with Lifecycles rejected: %v", err)
	}
}

func TestFeatureBundle_Validate_OldOnly_ExplicitEmpty_AllSliceFields(t *testing.T) {
	t.Parallel()

	table := legacyFieldTable()
	for _, entry := range table {
		if entry.withEmpty == nil {
			continue
		}
		entry := entry
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			// SchemaVersion 0 (unset) is valid for empty bundle with explicit empty slice
			b0 := feature.FeatureBundle{SchemaVersion: 0}
			entry.withEmpty(&b0)
			if err := b0.Validate(); err != nil {
				t.Fatalf("SchemaVersion 0 rejected for explicit-empty %s: %v", entry.name, err)
			}

			// SchemaVersionV1 is valid for empty bundle with explicit empty slice
			b1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1}
			entry.withEmpty(&b1)
			if err := b1.Validate(); err != nil {
				t.Fatalf("SchemaVersionV1 rejected for explicit-empty %s: %v", entry.name, err)
			}

			// Invalid SchemaVersion (e.g. 2) must be rejected with exact empty error
			b2 := feature.FeatureBundle{SchemaVersion: 2}
			entry.withEmpty(&b2)
			err2 := b2.Validate()
			if err2 == nil {
				t.Fatalf("invalid schema version 2 accepted for explicit-empty %s", entry.name)
			}
			const wantErrSubstr = "invalid schema version 2 for empty bundle"
			if !strings.Contains(err2.Error(), wantErrSubstr) {
				t.Fatalf("error = %q, want substring %q", err2.Error(), wantErrSubstr)
			}
		})
	}
}

type callCountingTerminalProvider struct {
	id    string
	calls *int
}

func (c callCountingTerminalProvider) ID() string {
	if c.calls != nil {
		*c.calls++
	}
	return c.id
}

func (callCountingTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}

type panicAfterFreezeTerminalProvider struct {
	id          string
	shouldPanic *bool
}

func (p panicAfterFreezeTerminalProvider) ID() string {
	if p.shouldPanic != nil && *p.shouldPanic {
		panic("provider ID() invoked unexpectedly after freeze")
	}
	return p.id
}

func (panicAfterFreezeTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}

func TestFeatureBundle_Validate_FrozenExclusiveIdentity(t *testing.T) {
	t.Parallel()

	t.Run("provider ID call count unchanged during Validate", func(t *testing.T) {
		t.Parallel()
		calls := 0
		var prov terminaldecision.Provider = callCountingTerminalProvider{id: "prov.callcount", calls: &calls}

		cs := feature.NewContributionSet()
		if err := feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "prov.callcount", prov); err != nil {
			t.Fatalf("Contribute: %v", err)
		}
		frozen := cs.Freeze()
		callsAfterFreeze := calls
		if callsAfterFreeze == 0 {
			t.Fatal("expected provider ID to be called during contribution/freeze")
		}

		b := feature.FeatureBundle{
			SchemaVersion: feature.SchemaVersionV1,
			PlaneSet:      frozen,
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("b.Validate() failed: %v", err)
		}
		if calls != callsAfterFreeze {
			t.Fatalf("provider ID calls changed during b.Validate(): before=%d, after=%d", callsAfterFreeze, calls)
		}

		if err := frozen.Validate(); err != nil {
			t.Fatalf("frozen.Validate() failed: %v", err)
		}
		if calls != callsAfterFreeze {
			t.Fatalf("provider ID calls changed during frozen.Validate(): before=%d, after=%d", callsAfterFreeze, calls)
		}
	})

	t.Run("provider can panic after freeze and bundle validates from cache", func(t *testing.T) {
		t.Parallel()
		shouldPanic := false
		var prov terminaldecision.Provider = panicAfterFreezeTerminalProvider{id: "prov.panic", shouldPanic: &shouldPanic}

		cs := feature.NewContributionSet()
		if err := feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "prov.panic", prov); err != nil {
			t.Fatalf("Contribute: %v", err)
		}
		frozen := cs.Freeze()

		// Enable panics
		shouldPanic = true

		b := feature.FeatureBundle{
			SchemaVersion: feature.SchemaVersionV1,
			PlaneSet:      frozen,
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("b.Validate() failed with panicking provider: %v", err)
		}
		if err := frozen.Validate(); err != nil {
			t.Fatalf("frozen.Validate() failed with panicking provider: %v", err)
		}
	})

	t.Run("missing cached identity fails", func(t *testing.T) {
		t.Parallel()
		prov := terminalDecisionProvider{id: "term.missing"}

		// Missing identity (hasID false, id empty)
		f1 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(prov, "", false)
		b1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f1}
		err1 := b1.Validate()
		if err1 == nil {
			t.Fatal("b.Validate() accepted missing cached identity (hasID=false, id=\"\")")
		}
		if !strings.Contains(err1.Error(), "missing cached identity") {
			t.Fatalf("error = %q, want substring 'missing cached identity'", err1.Error())
		}

		// Present ID but hasID false
		f2 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(prov, "term.missing", false)
		b2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f2}
		err2 := b2.Validate()
		if err2 == nil {
			t.Fatal("b.Validate() accepted missing cached identity (hasID=false, id non-empty)")
		}
		if !strings.Contains(err2.Error(), "missing cached identity") {
			t.Fatalf("error = %q, want substring 'missing cached identity'", err2.Error())
		}

		// hasID true but ID empty
		f3 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(prov, "", true)
		b3 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f3}
		err3 := b3.Validate()
		if err3 == nil {
			t.Fatal("b.Validate() accepted missing cached identity (hasID=true, id=\"\")")
		}
		if !strings.Contains(err3.Error(), "missing cached identity") {
			t.Fatalf("error = %q, want substring 'missing cached identity'", err3.Error())
		}
	})

	t.Run("invalid cached ID fails exact terminal sentinel", func(t *testing.T) {
		t.Parallel()
		prov := terminalDecisionProvider{id: "term.invalid"}

		invalidIDs := map[string]string{
			"blank id":     "   ",
			"invalid utf8": string([]byte{0xff}),
			"oversized id": strings.Repeat("x", terminaldecision.MaxProviderIDBytes+1),
		}

		for name, invID := range invalidIDs {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(prov, invID, true)
				b := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f}
				err := b.Validate()
				if err == nil {
					t.Fatalf("b.Validate() accepted invalid cached ID %q", invID)
				}
				if !errors.Is(err, terminaldecision.ErrInvalidProvider) {
					t.Fatalf("b.Validate() error = %v, want ErrInvalidProvider", err)
				}
				if errFrozen := f.Validate(); !errors.Is(errFrozen, terminaldecision.ErrInvalidProvider) {
					t.Fatalf("f.Validate() error = %v, want ErrInvalidProvider", errFrozen)
				}
			})
		}
	})

	t.Run("cached identity with absent provider fails", func(t *testing.T) {
		t.Parallel()

		// Provider nil, but hasID true and ID set
		f1 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(nil, "valid.provider.id", true)
		b1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f1}
		err1 := b1.Validate()
		if err1 == nil {
			t.Fatal("b.Validate() accepted cached identity with nil provider")
		}
		if !strings.Contains(err1.Error(), "malformed metadata without value") {
			t.Fatalf("error = %q, want substring 'malformed metadata without value'", err1.Error())
		}

		// Provider nil, but hasID true and ID empty
		f2 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(nil, "", true)
		b2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f2}
		err2 := b2.Validate()
		if err2 == nil {
			t.Fatal("b.Validate() accepted hasID=true with nil provider")
		}
		if !strings.Contains(err2.Error(), "malformed metadata without value") {
			t.Fatalf("error = %q, want substring 'malformed metadata without value'", err2.Error())
		}

		// Provider nil, but hasID false and ID non-empty
		f3 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(nil, "valid.provider.id", false)
		b3 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f3}
		err3 := b3.Validate()
		if err3 == nil {
			t.Fatal("b.Validate() accepted ID non-empty with nil provider")
		}
		if !strings.Contains(err3.Error(), "malformed metadata without value") {
			t.Fatalf("error = %q, want substring 'malformed metadata without value'", err3.Error())
		}
	})

	t.Run("valid cached identity succeeds", func(t *testing.T) {
		t.Parallel()
		prov := terminalDecisionProvider{id: "provider.valid.cached"}
		f := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(prov, "provider.valid.cached", true)
		b := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f}
		if err := b.Validate(); err != nil {
			t.Fatalf("b.Validate() failed for valid cached identity: %v", err)
		}
		if err := f.Validate(); err != nil {
			t.Fatalf("f.Validate() failed for valid cached identity: %v", err)
		}
	})

	t.Run("no mutation during Validate", func(t *testing.T) {
		t.Parallel()
		cs := feature.NewContributionSet()
		var prov terminaldecision.Provider = terminalDecisionProvider{id: "term.nomutate"}
		if err := feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "term.nomutate", prov); err != nil {
			t.Fatalf("Contribute: %v", err)
		}
		frozen := cs.Freeze()

		b := feature.FeatureBundle{
			SchemaVersion: feature.SchemaVersionV1,
			PlaneSet:      frozen,
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("b.Validate(): %v", err)
		}

		gotProv := feature.Get(b.PlaneSet, feature.PlaneTerminalDecisionProvider)
		if gotProv == nil || gotProv.ID() != "term.nomutate" {
			t.Fatalf("PlaneTerminalDecisionProvider mutated: %v", gotProv)
		}
		id, ok := feature.FrozenIdentity(b.PlaneSet, feature.PlaneTerminalDecisionProvider)
		if !ok || id != "term.nomutate" {
			t.Fatalf("FrozenIdentity mutated: (%q, %v)", id, ok)
		}
	})
}

func TestFrozenPlaneSet_ReplayTo_allPlanesAndAtomicFailure(t *testing.T) {
	t.Parallel()

	// 1. Build a populated FrozenPlaneSet with candidate, non-candidate, scalar, exclusive, and explicit-empty
	cs := feature.NewContributionSet()
	submitHook := stubSubmit{id: "submit-1", ord: 1}
	if err := feature.Contribute(cs, feature.PlaneSubmitHooks, "feat-1", []sdkhooks.SubmitHook{submitHook}); err != nil {
		t.Fatalf("Contribute SubmitHooks: %v", err)
	}
	// Non-candidate plane: tool reactors
	toolReactor := stubTool{id: "tool-1", ord: 2}
	if err := feature.Contribute(cs, feature.PlaneToolReactors, "feat-1", []sdkhooks.ToolReactor{toolReactor}); err != nil {
		t.Fatalf("Contribute ToolReactors: %v", err)
	}
	// Scalar plane
	if err := feature.Contribute(cs, feature.PlaneToolCallFinalizationMaxArgsBytes, "feat-1", 4096); err != nil {
		t.Fatalf("Contribute MaxArgsBytes: %v", err)
	}
	// Exclusive plane
	var termProv terminaldecision.Provider = terminalDecisionProvider{id: "term-provider-1"}
	if err := feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "term-provider-1", termProv); err != nil {
		t.Fatalf("Contribute TerminalDecisionProvider: %v", err)
	}
	// Explicit non-nil empty slice
	if err := feature.Contribute(cs, feature.PlaneSessionOpeners, "feat-1", []session.Opener{}); err != nil {
		t.Fatalf("Contribute SessionOpeners: %v", err)
	}

	frozen := cs.Freeze()

	// 2. Replay all planes into a clean destination
	dst := feature.NewContributionSet()
	if err := frozen.ReplayTo(dst, "replay-plugin"); err != nil {
		t.Fatalf("ReplayTo: %v", err)
	}

	// Verify all planes replayed
	dstFrozen := dst.Freeze()
	sh := feature.Get(dstFrozen, feature.PlaneSubmitHooks)
	if len(sh) != 1 || sh[0].ID() != "submit-1" {
		t.Fatalf("replayed SubmitHooks mismatch: %v", sh)
	}
	tr := feature.Get(dstFrozen, feature.PlaneToolReactors)
	if len(tr) != 1 || tr[0].ID() != "tool-1" {
		t.Fatalf("replayed ToolReactors mismatch: %v", tr)
	}
	capBytes := feature.Get(dstFrozen, feature.PlaneToolCallFinalizationMaxArgsBytes)
	if capBytes != 4096 {
		t.Fatalf("replayed MaxArgsBytes = %d, want 4096", capBytes)
	}
	tp := feature.Get(dstFrozen, feature.PlaneTerminalDecisionProvider)
	if tp == nil || tp.ID() != "term-provider-1" {
		t.Fatalf("replayed TerminalDecisionProvider mismatch: %v", tp)
	}
	id, ok := feature.FrozenIdentity(dstFrozen, feature.PlaneTerminalDecisionProvider)
	if !ok || id != "term-provider-1" {
		t.Fatalf("replayed FrozenIdentity = (%q, %v), want (term-provider-1, true)", id, ok)
	}
	so := feature.Get(dstFrozen, feature.PlaneSessionOpeners)
	if so == nil {
		t.Fatal("replayed SessionOpeners was nil, expected non-nil empty slice")
	}
	if len(so) != 0 {
		t.Fatalf("replayed SessionOpeners len = %d, want 0", len(so))
	}

	// 3. Atomic failure: destination already has a conflicting exclusive provider
	conflictDst := feature.NewContributionSet()
	var existingProv terminaldecision.Provider = terminalDecisionProvider{id: "existing-provider"}
	if err := feature.Contribute(conflictDst, feature.PlaneTerminalDecisionProvider, "existing-provider", existingProv); err != nil {
		t.Fatalf("Contribute existing provider: %v", err)
	}
	// Also add a submit hook to conflictDst
	if err := feature.Contribute(conflictDst, feature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{stubSubmit{id: "init-hook", ord: 0}}); err != nil {
		t.Fatalf("Contribute init hook: %v", err)
	}

	err := frozen.ReplayTo(conflictDst, "replay-plugin")
	if err == nil {
		t.Fatal("expected exclusive conflict error on ReplayTo")
	}

	// Verify conflictDst was NOT modified (atomic failure)
	cDstFrozen := conflictDst.Freeze()
	cSH := feature.Get(cDstFrozen, feature.PlaneSubmitHooks)
	if len(cSH) != 1 || cSH[0].ID() != "init-hook" {
		t.Fatalf("conflictDst SubmitHooks mutated on failed replay: %v", cSH)
	}
	cTP := feature.Get(cDstFrozen, feature.PlaneTerminalDecisionProvider)
	if cTP == nil || cTP.ID() != "existing-provider" {
		t.Fatalf("conflictDst TerminalDecisionProvider mutated on failed replay: %v", cTP)
	}
	cTR := feature.Get(cDstFrozen, feature.PlaneToolReactors)
	if len(cTR) != 0 {
		t.Fatalf("conflictDst ToolReactors mutated on failed replay: %v", cTR)
	}
}

type callCountingAttemptTransform struct {
	id    string
	calls *int
}

func (c callCountingAttemptTransform) ID() string {
	if c.calls != nil {
		*c.calls++
	}
	return c.id
}
func (c callCountingAttemptTransform) Order() int                        { return 0 }
func (c callCountingAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (c callCountingAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type callCountingStreamObserverFactory struct {
	id    string
	calls *int
}

func (c callCountingStreamObserverFactory) ID() string {
	if c.calls != nil {
		*c.calls++
	}
	return c.id
}
func (c callCountingStreamObserverFactory) Order() int { return 0 }
func (c callCountingStreamObserverFactory) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailOpen
}
func (c callCountingStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return stubStreamObserver{}, nil
}

type callCountingCompactionPreserver struct {
	stubPreserver
	calls *int
}

func (c callCountingCompactionPreserver) ID() string {
	if c.calls != nil {
		*c.calls++
	}
	return c.stubPreserver.id
}

func TestPlaneSet_ValidateIdentity_ThreePlanes_AllTenCases(t *testing.T) {
	t.Parallel()

	longID := strings.Repeat("x", 300)
	whitespaceID := "   \t\n  "

	t.Run("PlaneAttemptTransforms", func(t *testing.T) {
		t.Parallel()

		// 1. valid contribution freezes ID
		csValid := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csValid, feature.PlaneAttemptTransforms, "p-valid", []request.AttemptTransform{stubAttemptTransform{id: "at-valid-1"}}))
		fValid := csValid.Freeze()
		idV, okV := feature.FrozenIdentity(fValid, feature.PlaneAttemptTransforms)
		assert.True(t, okV)
		assert.Equal(t, "at-valid-1", idV)

		// 2. FrozenIdentity returns exact ID without extra live ID call
		calls := 0
		csCount := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csCount, feature.PlaneAttemptTransforms, "p-count", []request.AttemptTransform{callCountingAttemptTransform{id: "at-count-1", calls: &calls}}))
		fCount := csCount.Freeze()
		callsAfterFreeze := calls
		idCount, okCount := feature.FrozenIdentity(fCount, feature.PlaneAttemptTransforms)
		assert.True(t, okCount)
		assert.Equal(t, "at-count-1", idCount)
		assert.Equal(t, callsAfterFreeze, calls, "FrozenIdentity must not call live ID")

		// 3. bundle Validate & frozen Validate call cached ValidateIdentity and no live ID
		bCount := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fCount}
		require.NoError(t, bCount.Validate())
		require.NoError(t, fCount.Validate())
		assert.Equal(t, callsAfterFreeze, calls, "Validate must not call live ID")

		// 4. >256 and whitespace IDs succeed
		csLong := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csLong, feature.PlaneAttemptTransforms, "p-long", []request.AttemptTransform{stubAttemptTransform{id: longID}}))
		fLong := csLong.Freeze()
		require.NoError(t, fLong.Validate())
		bLong := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fLong}
		require.NoError(t, bLong.Validate())
		idL, okL := feature.FrozenIdentity(fLong, feature.PlaneAttemptTransforms)
		assert.True(t, okL)
		assert.Equal(t, longID, idL)

		csWS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csWS, feature.PlaneAttemptTransforms, "p-ws", []request.AttemptTransform{stubAttemptTransform{id: whitespaceID}}))
		fWS := csWS.Freeze()
		require.NoError(t, fWS.Validate())
		bWS := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fWS}
		require.NoError(t, bWS.Validate())
		idW, okW := feature.FrozenIdentity(fWS, feature.PlaneAttemptTransforms)
		assert.True(t, okW)
		assert.Equal(t, whitespaceID, idW)

		// 5. empty cached identity with nonempty value rejects
		fEmpty1 := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest([]request.AttemptTransform{stubAttemptTransform{id: "at-1"}}, "", false)
		bEmpty1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty1}
		errE1 := bEmpty1.Validate()
		require.Error(t, errE1)
		assert.Contains(t, errE1.Error(), "missing cached identity")
		assert.Contains(t, fEmpty1.Validate().Error(), "missing cached identity")

		fEmpty2 := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest([]request.AttemptTransform{stubAttemptTransform{id: "at-1"}}, "", true)
		bEmpty2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty2}
		errE2 := bEmpty2.Validate()
		require.Error(t, errE2)
		assert.Contains(t, errE2.Error(), "missing cached identity")
		assert.Contains(t, fEmpty2.Validate().Error(), "missing cached identity")

		// 6. invalid metadata ID nonempty/HasID false rejects
		fInvMeta := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest([]request.AttemptTransform{stubAttemptTransform{id: "at-1"}}, "at-1", false)
		bInvMeta := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fInvMeta}
		errInv := bInvMeta.Validate()
		require.Error(t, errInv)
		assert.Contains(t, errInv.Error(), "missing cached identity")
		assert.Contains(t, fInvMeta.Validate().Error(), "missing cached identity")

		// 7. metadata without value rejects
		fNoVal1 := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest(nil, "at-1", true)
		bNoVal1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal1}
		errNV1 := bNoVal1.Validate()
		require.Error(t, errNV1)
		assert.Contains(t, errNV1.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal1.Validate().Error(), "malformed metadata without value")

		fNoVal2 := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest(nil, "", true)
		bNoVal2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal2}
		errNV2 := bNoVal2.Validate()
		require.Error(t, errNV2)
		assert.Contains(t, errNV2.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal2.Validate().Error(), "malformed metadata without value")

		fNoVal3 := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest(nil, "at-1", false)
		bNoVal3 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal3}
		errNV3 := bNoVal3.Validate()
		require.Error(t, errNV3)
		assert.Contains(t, errNV3.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal3.Validate().Error(), "malformed metadata without value")

		// 8. cached ID mutation to another nonempty ID: validator accepts cached valid ID, no live ID call
		fMut := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest([]request.AttemptTransform{stubAttemptTransform{id: "orig-id"}}, "mutated-id", true)
		bMut := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fMut}
		require.NoError(t, bMut.Validate())
		require.NoError(t, fMut.Validate())
		idMut, okMut := feature.FrozenIdentity(fMut, feature.PlaneAttemptTransforms)
		assert.True(t, okMut)
		assert.Equal(t, "mutated-id", idMut)

		// 9. clone/freeze/toContributions/request freeze retain exact ID
		fCloned := fValid.Clone()
		idC, okC := feature.FrozenIdentity(fCloned, feature.PlaneAttemptTransforms)
		assert.True(t, okC)
		assert.Equal(t, "at-valid-1", idC)

		fReq := feature.FreezeRequestPlanes(fValid)
		idR, okR := feature.FrozenIdentity(fReq, feature.PlaneAttemptTransforms)
		assert.True(t, okR)
		assert.Equal(t, "at-valid-1", idR)

		cset := fValid.ToContributions()
		fRecon := cset.Freeze()
		idRec, okRec := feature.FrozenIdentity(fRecon, feature.PlaneAttemptTransforms)
		assert.True(t, okRec)
		assert.Equal(t, "at-valid-1", idRec)

		// 10. replay into destination retains correct metadata and atomic failures
		dst := feature.NewContributionSet()
		require.NoError(t, fValid.ReplayTo(dst, "replay-p"))
		fDst := dst.Freeze()
		idDst, okDst := feature.FrozenIdentity(fDst, feature.PlaneAttemptTransforms)
		assert.True(t, okDst)
		assert.Equal(t, "at-valid-1", idDst)

		dstBefore := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dstBefore, feature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{stubSubmit{id: "init-sub", ord: 0}}))
		snapBefore := dstBefore.Freeze()

		errRep := fNoVal1.ReplayTo(dstBefore, "bad-plugin")
		require.Error(t, errRep)
		snapAfter := dstBefore.Freeze()
		assert.Equal(t, feature.Get(snapBefore, feature.PlaneSubmitHooks), feature.Get(snapAfter, feature.PlaneSubmitHooks))
	})

	t.Run("PlaneStreamObserverFactories", func(t *testing.T) {
		t.Parallel()

		// 1. valid contribution freezes ID
		csValid := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csValid, feature.PlaneStreamObserverFactories, "p-valid", []response.StreamObserverFactory{stubStreamObserverFactory{id: "sof-valid-1"}}))
		fValid := csValid.Freeze()
		idV, okV := feature.FrozenIdentity(fValid, feature.PlaneStreamObserverFactories)
		assert.True(t, okV)
		assert.Equal(t, "sof-valid-1", idV)

		// 2. FrozenIdentity returns exact ID without extra live ID call
		calls := 0
		csCount := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csCount, feature.PlaneStreamObserverFactories, "p-count", []response.StreamObserverFactory{callCountingStreamObserverFactory{id: "sof-count-1", calls: &calls}}))
		fCount := csCount.Freeze()
		callsAfterFreeze := calls
		idCount, okCount := feature.FrozenIdentity(fCount, feature.PlaneStreamObserverFactories)
		assert.True(t, okCount)
		assert.Equal(t, "sof-count-1", idCount)
		assert.Equal(t, callsAfterFreeze, calls, "FrozenIdentity must not call live ID")

		// 3. bundle Validate & frozen Validate call cached ValidateIdentity and no live ID
		bCount := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fCount}
		require.NoError(t, bCount.Validate())
		require.NoError(t, fCount.Validate())
		assert.Equal(t, callsAfterFreeze, calls, "Validate must not call live ID")

		// 4. >256 and whitespace IDs succeed
		csLong := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csLong, feature.PlaneStreamObserverFactories, "p-long", []response.StreamObserverFactory{stubStreamObserverFactory{id: longID}}))
		fLong := csLong.Freeze()
		require.NoError(t, fLong.Validate())
		bLong := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fLong}
		require.NoError(t, bLong.Validate())
		idL, okL := feature.FrozenIdentity(fLong, feature.PlaneStreamObserverFactories)
		assert.True(t, okL)
		assert.Equal(t, longID, idL)

		csWS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csWS, feature.PlaneStreamObserverFactories, "p-ws", []response.StreamObserverFactory{stubStreamObserverFactory{id: whitespaceID}}))
		fWS := csWS.Freeze()
		require.NoError(t, fWS.Validate())
		bWS := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fWS}
		require.NoError(t, bWS.Validate())
		idW, okW := feature.FrozenIdentity(fWS, feature.PlaneStreamObserverFactories)
		assert.True(t, okW)
		assert.Equal(t, whitespaceID, idW)

		// 5. empty cached identity with nonempty value rejects
		fEmpty1 := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest([]response.StreamObserverFactory{stubStreamObserverFactory{id: "sof-1"}}, "", false)
		bEmpty1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty1}
		errE1 := bEmpty1.Validate()
		require.Error(t, errE1)
		assert.Contains(t, errE1.Error(), "missing cached identity")
		assert.Contains(t, fEmpty1.Validate().Error(), "missing cached identity")

		fEmpty2 := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest([]response.StreamObserverFactory{stubStreamObserverFactory{id: "sof-1"}}, "", true)
		bEmpty2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty2}
		errE2 := bEmpty2.Validate()
		require.Error(t, errE2)
		assert.Contains(t, errE2.Error(), "missing cached identity")
		assert.Contains(t, fEmpty2.Validate().Error(), "missing cached identity")

		// 6. invalid metadata ID nonempty/HasID false rejects
		fInvMeta := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest([]response.StreamObserverFactory{stubStreamObserverFactory{id: "sof-1"}}, "sof-1", false)
		bInvMeta := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fInvMeta}
		errInv := bInvMeta.Validate()
		require.Error(t, errInv)
		assert.Contains(t, errInv.Error(), "missing cached identity")
		assert.Contains(t, fInvMeta.Validate().Error(), "missing cached identity")

		// 7. metadata without value rejects
		fNoVal1 := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(nil, "sof-1", true)
		bNoVal1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal1}
		errNV1 := bNoVal1.Validate()
		require.Error(t, errNV1)
		assert.Contains(t, errNV1.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal1.Validate().Error(), "malformed metadata without value")

		fNoVal2 := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(nil, "", true)
		bNoVal2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal2}
		errNV2 := bNoVal2.Validate()
		require.Error(t, errNV2)
		assert.Contains(t, errNV2.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal2.Validate().Error(), "malformed metadata without value")

		fNoVal3 := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(nil, "sof-1", false)
		bNoVal3 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal3}
		errNV3 := bNoVal3.Validate()
		require.Error(t, errNV3)
		assert.Contains(t, errNV3.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal3.Validate().Error(), "malformed metadata without value")

		// 8. cached ID mutation to another nonempty ID: validator accepts cached valid ID, no live ID call
		fMut := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest([]response.StreamObserverFactory{stubStreamObserverFactory{id: "orig-sof"}}, "mutated-sof", true)
		bMut := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fMut}
		require.NoError(t, bMut.Validate())
		require.NoError(t, fMut.Validate())
		idMut, okMut := feature.FrozenIdentity(fMut, feature.PlaneStreamObserverFactories)
		assert.True(t, okMut)
		assert.Equal(t, "mutated-sof", idMut)

		// 9. clone/freeze/toContributions/request freeze retain exact ID
		fCloned := fValid.Clone()
		idC, okC := feature.FrozenIdentity(fCloned, feature.PlaneStreamObserverFactories)
		assert.True(t, okC)
		assert.Equal(t, "sof-valid-1", idC)

		fReq := feature.FreezeRequestPlanes(fValid)
		idR, okR := feature.FrozenIdentity(fReq, feature.PlaneStreamObserverFactories)
		assert.True(t, okR)
		assert.Equal(t, "sof-valid-1", idR)

		cset := fValid.ToContributions()
		fRecon := cset.Freeze()
		idRec, okRec := feature.FrozenIdentity(fRecon, feature.PlaneStreamObserverFactories)
		assert.True(t, okRec)
		assert.Equal(t, "sof-valid-1", idRec)

		// 10. replay into destination retains correct metadata and atomic failures
		dst := feature.NewContributionSet()
		require.NoError(t, fValid.ReplayTo(dst, "replay-sof"))
		fDst := dst.Freeze()
		idDst, okDst := feature.FrozenIdentity(fDst, feature.PlaneStreamObserverFactories)
		assert.True(t, okDst)
		assert.Equal(t, "sof-valid-1", idDst)

		dstBefore := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dstBefore, feature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{stubSubmit{id: "init-sub", ord: 0}}))
		snapBefore := dstBefore.Freeze()

		errRep := fNoVal1.ReplayTo(dstBefore, "bad-plugin")
		require.Error(t, errRep)
		snapAfter := dstBefore.Freeze()
		assert.Equal(t, feature.Get(snapBefore, feature.PlaneSubmitHooks), feature.Get(snapAfter, feature.PlaneSubmitHooks))
	})

	t.Run("PlaneCompactionPreservers", func(t *testing.T) {
		t.Parallel()

		// 1. valid contribution freezes ID
		csValid := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csValid, feature.PlaneCompactionPreservers, "p-valid", []compaction.Preserver{stubPreserver{id: "cp-valid-1"}}))
		fValid := csValid.Freeze()
		idV, okV := feature.FrozenIdentity(fValid, feature.PlaneCompactionPreservers)
		assert.True(t, okV)
		assert.Equal(t, "cp-valid-1", idV)

		// 2. FrozenIdentity returns exact ID without extra live ID call
		calls := 0
		csCount := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csCount, feature.PlaneCompactionPreservers, "p-count", []compaction.Preserver{callCountingCompactionPreserver{stubPreserver: stubPreserver{id: "cp-count-1"}, calls: &calls}}))
		fCount := csCount.Freeze()
		callsAfterFreeze := calls
		idCount, okCount := feature.FrozenIdentity(fCount, feature.PlaneCompactionPreservers)
		assert.True(t, okCount)
		assert.Equal(t, "cp-count-1", idCount)
		assert.Equal(t, callsAfterFreeze, calls, "FrozenIdentity must not call live ID")

		// 3. bundle Validate & frozen Validate call cached ValidateIdentity and no live ID
		bCount := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fCount}
		require.NoError(t, bCount.Validate())
		require.NoError(t, fCount.Validate())
		assert.Equal(t, callsAfterFreeze, calls, "Validate must not call live ID")

		// 4. >256 and whitespace IDs succeed
		csLong := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csLong, feature.PlaneCompactionPreservers, "p-long", []compaction.Preserver{stubPreserver{id: longID}}))
		fLong := csLong.Freeze()
		require.NoError(t, fLong.Validate())
		bLong := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fLong}
		require.NoError(t, bLong.Validate())
		idL, okL := feature.FrozenIdentity(fLong, feature.PlaneCompactionPreservers)
		assert.True(t, okL)
		assert.Equal(t, longID, idL)

		csWS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csWS, feature.PlaneCompactionPreservers, "p-ws", []compaction.Preserver{stubPreserver{id: whitespaceID}}))
		fWS := csWS.Freeze()
		require.NoError(t, fWS.Validate())
		bWS := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fWS}
		require.NoError(t, bWS.Validate())
		idW, okW := feature.FrozenIdentity(fWS, feature.PlaneCompactionPreservers)
		assert.True(t, okW)
		assert.Equal(t, whitespaceID, idW)

		// 5. empty cached identity with nonempty value rejects
		fEmpty1 := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest([]compaction.Preserver{stubPreserver{id: "cp-1"}}, "", false)
		bEmpty1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty1}
		errE1 := bEmpty1.Validate()
		require.Error(t, errE1)
		assert.Contains(t, errE1.Error(), "missing cached identity")
		assert.Contains(t, fEmpty1.Validate().Error(), "missing cached identity")

		fEmpty2 := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest([]compaction.Preserver{stubPreserver{id: "cp-1"}}, "", true)
		bEmpty2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty2}
		errE2 := bEmpty2.Validate()
		require.Error(t, errE2)
		assert.Contains(t, errE2.Error(), "missing cached identity")
		assert.Contains(t, fEmpty2.Validate().Error(), "missing cached identity")

		// 6. invalid metadata ID nonempty/HasID false rejects
		fInvMeta := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest([]compaction.Preserver{stubPreserver{id: "cp-1"}}, "cp-1", false)
		bInvMeta := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fInvMeta}
		errInv := bInvMeta.Validate()
		require.Error(t, errInv)
		assert.Contains(t, errInv.Error(), "missing cached identity")
		assert.Contains(t, fInvMeta.Validate().Error(), "missing cached identity")

		// 7. metadata without value rejects
		fNoVal1 := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest(nil, "cp-1", true)
		bNoVal1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal1}
		errNV1 := bNoVal1.Validate()
		require.Error(t, errNV1)
		assert.Contains(t, errNV1.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal1.Validate().Error(), "malformed metadata without value")

		fNoVal2 := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest(nil, "", true)
		bNoVal2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal2}
		errNV2 := bNoVal2.Validate()
		require.Error(t, errNV2)
		assert.Contains(t, errNV2.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal2.Validate().Error(), "malformed metadata without value")

		fNoVal3 := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest(nil, "cp-1", false)
		bNoVal3 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal3}
		errNV3 := bNoVal3.Validate()
		require.Error(t, errNV3)
		assert.Contains(t, errNV3.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal3.Validate().Error(), "malformed metadata without value")

		// 8. cached ID mutation to another nonempty ID: validator accepts cached valid ID, no live ID call
		fMut := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest([]compaction.Preserver{stubPreserver{id: "orig-cp"}}, "mutated-cp", true)
		bMut := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fMut}
		require.NoError(t, bMut.Validate())
		require.NoError(t, fMut.Validate())
		idMut, okMut := feature.FrozenIdentity(fMut, feature.PlaneCompactionPreservers)
		assert.True(t, okMut)
		assert.Equal(t, "mutated-cp", idMut)

		// 9. clone/freeze/toContributions/request freeze retain exact ID
		fCloned := fValid.Clone()
		idC, okC := feature.FrozenIdentity(fCloned, feature.PlaneCompactionPreservers)
		assert.True(t, okC)
		assert.Equal(t, "cp-valid-1", idC)

		fReq := feature.FreezeRequestPlanes(fValid)
		idR, okR := feature.FrozenIdentity(fReq, feature.PlaneCompactionPreservers)
		assert.True(t, okR)
		assert.Equal(t, "cp-valid-1", idR)

		cset := fValid.ToContributions()
		fRecon := cset.Freeze()
		idRec, okRec := feature.FrozenIdentity(fRecon, feature.PlaneCompactionPreservers)
		assert.True(t, okRec)
		assert.Equal(t, "cp-valid-1", idRec)

		// 10. replay into destination retains correct metadata and atomic failures
		dst := feature.NewContributionSet()
		require.NoError(t, fValid.ReplayTo(dst, "replay-cp"))
		fDst := dst.Freeze()
		idDst, okDst := feature.FrozenIdentity(fDst, feature.PlaneCompactionPreservers)
		assert.True(t, okDst)
		assert.Equal(t, "cp-valid-1", idDst)

		dstBefore := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dstBefore, feature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{stubSubmit{id: "init-sub", ord: 0}}))
		snapBefore := dstBefore.Freeze()

		errRep := fNoVal1.ReplayTo(dstBefore, "bad-plugin")
		require.Error(t, errRep)
		snapAfter := dstBefore.Freeze()
		assert.Equal(t, feature.Get(snapBefore, feature.PlaneSubmitHooks), feature.Get(snapAfter, feature.PlaneSubmitHooks))
	})

	t.Run("PlaneTerminalDecisionProvider keeps ValidateProviderID", func(t *testing.T) {
		t.Parallel()

		if err := feature.PlaneTerminalDecisionProvider.ValidateIdentity(""); err == nil {
			t.Fatal("expected empty provider ID to reject")
		}
		if err := feature.PlaneTerminalDecisionProvider.ValidateIdentity("   "); err == nil {
			t.Fatal("expected whitespace provider ID to reject for terminaldecision")
		}
		if err := feature.PlaneTerminalDecisionProvider.ValidateIdentity(strings.Repeat("x", 200)); err == nil {
			t.Fatal("expected >128 byte provider ID to reject for terminaldecision")
		}
		if err := feature.PlaneTerminalDecisionProvider.ValidateIdentity("term.valid.id"); err != nil {
			t.Fatalf("expected valid provider ID to succeed: %v", err)
		}
	})
}

func TestFrozenPlaneSet_MapBacked_IdentityPlanes(t *testing.T) {
	t.Parallel()

	t.Run("PlaneAttemptTransforms", func(t *testing.T) {
		t.Parallel()
		tr := []request.AttemptTransform{stubAttemptTransform{id: "at-map-1"}}
		mapFrozen := feature.NewFrozenPlaneSetFromMapForTest(
			map[string]any{feature.PlaneAttemptTransforms.ID: tr},
			map[string]string{feature.PlaneAttemptTransforms.ID: "at-map-1"},
		)
		id, ok := feature.FrozenIdentity(mapFrozen, feature.PlaneAttemptTransforms)
		assert.True(t, ok)
		assert.Equal(t, "at-map-1", id)
		got := feature.Get(mapFrozen, feature.PlaneAttemptTransforms)
		assert.Len(t, got, 1)
		assert.Equal(t, "at-map-1", got[0].ID())

		dst := feature.NewContributionSet()
		require.NoError(t, mapFrozen.ReplayTo(dst, "map-plugin"))
		dstFrozen := dst.Freeze()
		idDst, okDst := feature.FrozenIdentity(dstFrozen, feature.PlaneAttemptTransforms)
		assert.True(t, okDst)
		assert.Equal(t, "at-map-1", idDst)
	})

	t.Run("PlaneStreamObserverFactories", func(t *testing.T) {
		t.Parallel()
		sof := []response.StreamObserverFactory{stubStreamObserverFactory{id: "sof-map-1"}}
		mapFrozen := feature.NewFrozenPlaneSetFromMapForTest(
			map[string]any{feature.PlaneStreamObserverFactories.ID: sof},
			map[string]string{feature.PlaneStreamObserverFactories.ID: "sof-map-1"},
		)
		id, ok := feature.FrozenIdentity(mapFrozen, feature.PlaneStreamObserverFactories)
		assert.True(t, ok)
		assert.Equal(t, "sof-map-1", id)
		got := feature.Get(mapFrozen, feature.PlaneStreamObserverFactories)
		assert.Len(t, got, 1)
		assert.Equal(t, "sof-map-1", got[0].ID())

		dst := feature.NewContributionSet()
		require.NoError(t, mapFrozen.ReplayTo(dst, "map-plugin"))
		dstFrozen := dst.Freeze()
		idDst, okDst := feature.FrozenIdentity(dstFrozen, feature.PlaneStreamObserverFactories)
		assert.True(t, okDst)
		assert.Equal(t, "sof-map-1", idDst)
	})

	t.Run("PlaneCompactionPreservers", func(t *testing.T) {
		t.Parallel()
		cp := []compaction.Preserver{stubPreserver{id: "cp-map-1"}}
		mapFrozen := feature.NewFrozenPlaneSetFromMapForTest(
			map[string]any{feature.PlaneCompactionPreservers.ID: cp},
			map[string]string{feature.PlaneCompactionPreservers.ID: "cp-map-1"},
		)
		id, ok := feature.FrozenIdentity(mapFrozen, feature.PlaneCompactionPreservers)
		assert.True(t, ok)
		assert.Equal(t, "cp-map-1", id)
		got := feature.Get(mapFrozen, feature.PlaneCompactionPreservers)
		assert.Len(t, got, 1)
		assert.Equal(t, "cp-map-1", got[0].ID())

		dst := feature.NewContributionSet()
		require.NoError(t, mapFrozen.ReplayTo(dst, "map-plugin"))
		dstFrozen := dst.Freeze()
		idDst, okDst := feature.FrozenIdentity(dstFrozen, feature.PlaneCompactionPreservers)
		assert.True(t, okDst)
		assert.Equal(t, "cp-map-1", idDst)
	})

	t.Run("PlaneTerminalDecisionProvider", func(t *testing.T) {
		t.Parallel()
		prov := terminalDecisionProvider{id: "term-map-1"}
		mapFrozen := feature.NewFrozenPlaneSetFromMapForTest(
			map[string]any{feature.PlaneTerminalDecisionProvider.ID: prov},
			map[string]string{feature.PlaneTerminalDecisionProvider.ID: "term-map-1"},
		)
		id, ok := feature.FrozenIdentity(mapFrozen, feature.PlaneTerminalDecisionProvider)
		assert.True(t, ok)
		assert.Equal(t, "term-map-1", id)
		got := feature.Get(mapFrozen, feature.PlaneTerminalDecisionProvider)
		assert.NotNil(t, got)
		assert.Equal(t, "term-map-1", got.ID())

		dst := feature.NewContributionSet()
		require.NoError(t, mapFrozen.ReplayTo(dst, "map-plugin"))
		dstFrozen := dst.Freeze()
		idDst, okDst := feature.FrozenIdentity(dstFrozen, feature.PlaneTerminalDecisionProvider)
		assert.True(t, okDst)
		assert.Equal(t, "term-map-1", idDst)
	})
}
