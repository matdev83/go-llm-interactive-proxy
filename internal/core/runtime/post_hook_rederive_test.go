package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type postHookMutator struct {
	mode string
}

func (h postHookMutator) ID() string                      { return "post-hook-" + h.mode }
func (postHookMutator) Order() int                        { return 0 }
func (postHookMutator) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h postHookMutator) HandleRequestParts(_ context.Context, call *lipapi.Call, _ sdkhooks.PartMeta) error {
	if call == nil {
		return nil
	}
	switch h.mode {
	case "invalid":
		call.Messages = nil
	case "tools":
		call.Tools = append(call.Tools, lipapi.ToolDef{Name: "need-tools", Description: "x"})
	case "transport":
		call.Invocation.DeliveryMode = lipapi.DeliveryModeStreaming
	case "context":
		call.Messages = append(call.Messages, lipapi.Message{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(strings.Repeat("z", 200))},
		})
	case "preflight":
		call.Messages = append(call.Messages, lipapi.Message{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(strings.Repeat("p", 64))},
		})
	case "route":
		call.Route.Selector = "hijacked:evil-model"
	}
	return nil
}

func postHookBaseCall(sel string) *lipapi.Call {
	return &lipapi.Call{
		ID:    "post-hook-base",
		Route: lipapi.RouteIntent{Selector: sel},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeNonStreaming,
		},
	}
}

func TestPostHookRederive_dimensionsTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		mode       string
		selector   string
		setup      func(*runtime.Executor, *atomic.Int64, *atomic.Int64)
		wantOpenA  int64
		wantOpenB  int64
		wantErrSub string
	}{
		{
			name:     "invalid_call",
			mode:     "invalid",
			selector: "a:m|b:m",
			setup: func(ex *runtime.Executor, a, b *atomic.Int64) {
				ex.Backends = map[string]execbackend.Backend{
					"a": streamingOpenCounter(a),
					"b": streamingOpenCounter(b),
				}
			},
			wantErrSub: "validation",
		},
		{
			name:     "tools_capability",
			mode:     "tools",
			selector: "a:m|b:m",
			setup: func(ex *runtime.Executor, a, b *atomic.Int64) {
				ex.Backends = map[string]execbackend.Backend{
					"a": {
						Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
						TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
							Operation: lipapi.OperationOpenAIChatCompletions,
							Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							a.Add(1)
							return nil, errors.New("a must not open")
						},
					},
					"b": {
						Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
						TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
							Operation: lipapi.OperationOpenAIChatCompletions,
							Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							b.Add(1)
							return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
						},
					},
				}
			},
			wantOpenB: 1,
		},
		{
			name:     "unsupported_transport",
			mode:     "transport",
			selector: "a:m|b:m",
			setup: func(ex *runtime.Executor, a, b *atomic.Int64) {
				ex.Backends = map[string]execbackend.Backend{
					"a": {
						Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
						TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
							Operation: lipapi.OperationOpenAIChatCompletions,
							Modes:     []lipapi.TransportMode{lipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							a.Add(1)
							return nil, errors.New("a must not open")
						},
					},
					"b": {
						Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
						TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
							Operation: lipapi.OperationOpenAIChatCompletions,
							Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							b.Add(1)
							return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
						},
					},
				}
			},
			wantOpenB: 1,
		},
		{
			name:     "context_size",
			mode:     "context",
			selector: "smallctx:m|bigctx:m",
			setup: func(ex *runtime.Executor, a, b *atomic.Int64) {
				ex.CatalogResolver = contextLimitCatalogResolver{}
				ex.EligibilityResolver = modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
				ex.Backends = map[string]execbackend.Backend{
					"smallctx": {
						Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
						TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
							Operation: lipapi.OperationOpenAIChatCompletions,
							Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							a.Add(1)
							return nil, errors.New("smallctx must not open")
						},
					},
					"bigctx": {
						Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
						TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
							Operation: lipapi.OperationOpenAIChatCompletions,
							Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							b.Add(1)
							return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
						},
					},
				}
			},
			wantOpenB: 1,
		},
		{
			name:     "preflight_accounting",
			mode:     "preflight",
			selector: "a:m",
			setup: func(ex *runtime.Executor, a, b *atomic.Int64) {
				ex.Preflight = accountingpreflight.NewChecker(
					accountingAdmissionCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
						return accountingapp.CountResult{InputTokens: 100, OutputTokens: 1, TotalTokens: 101}, nil
					}),
					accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeStrict, MaxInputTokens: 8},
				)
				ex.Backends = map[string]execbackend.Backend{
					"a": {
						Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
						TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
							Operation: lipapi.OperationOpenAIChatCompletions,
							Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
						}),
						Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
							a.Add(1)
							return nil, errors.New("must not open")
						},
					},
				}
			},
			wantErrSub: "preflight",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var openedA, openedB atomic.Int64
			ex := runtime.TestExecutor()
			ex.Store = st
			ex.MaxAttempts = 4
			ex.Rand = routing.NewSeededRng(3)
			ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{postHookMutator{mode: tc.mode}}})
			tc.setup(ex, &openedA, &openedB)
			stream, execErr := ex.Execute(t.Context(), postHookBaseCall(tc.selector))
			if tc.wantErrSub != "" {
				if execErr == nil || !strings.Contains(strings.ToLower(execErr.Error()), strings.ToLower(tc.wantErrSub)) {
					t.Fatalf("want err containing %q, got %v", tc.wantErrSub, execErr)
				}
			} else if execErr != nil {
				t.Fatalf("execute: %v", execErr)
			}
			if stream != nil {
				_, _ = lipapi.Collect(t.Context(), stream)
			}
			if openedA.Load() != tc.wantOpenA || openedB.Load() != tc.wantOpenB {
				t.Fatalf("opens a=%d b=%d want a=%d b=%d", openedA.Load(), openedB.Load(), tc.wantOpenA, tc.wantOpenB)
			}
		})
	}
}

type accountingAdmissionCountFunc func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error)

func (f accountingAdmissionCountFunc) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return f(ctx, in)
}

func streamingOpenCounter(n *atomic.Int64) execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		}),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			n.Add(1)
			return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
		},
	}
}

func TestPostHookRederive_pinsRouteIdentity(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotSel string
	var gotModel string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{postHookMutator{mode: "route"}}})
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				gotSel = call.Route.Selector
				gotModel = cand.Primary.Model
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, execErr := ex.Execute(t.Context(), postHookBaseCall("a:good-model"))
	if execErr != nil {
		t.Fatal(execErr)
	}
	_, _ = lipapi.Collect(t.Context(), stream)
	if gotSel != "a:good-model" {
		t.Fatalf("route selector hijacked: %q", gotSel)
	}
	if gotModel != "good-model" {
		t.Fatalf("candidate model=%q", gotModel)
	}
}

type fixedWorkspaceResolver struct {
	view lipworkspace.WorkspaceView
}

func (f fixedWorkspaceResolver) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	out := f.view
	out.Markers = append([]string(nil), f.view.Markers...)
	return out, nil
}

func TestAttemptMeta_completenessAndDefensiveCopies(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	xform := &metaCaptureTransform{}
	bundle := contributeAttemptTransformBundle(t, xform)
	merged := featurebundle.MergeBundles(bundle)
	bus := hooks.New(hooks.Config{})
	wsView := lipworkspace.WorkspaceView{ID: "ws-1", ProjectRoot: "/proj", Markers: []string{"m1"}}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		AttemptTransforms: merged.AttemptTransforms,
		Workspace:         fixedWorkspaceResolver{view: wsView},
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	prefixes := []string{"family-a", "family-b"}
	dialects := []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: prefixes,
			ReplaySupport:   lipapi.ReasoningReplaySupport{Dialects: dialects},
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	stream, execErr := ex.Execute(t.Context(), postHookBaseCall("a:m"))
	if execErr != nil {
		t.Fatal(execErr)
	}
	_, _ = lipapi.Collect(t.Context(), stream)
	if xform.meta.BackendID != "a" || xform.meta.CandidateKey == "" || xform.meta.Model != "m" {
		t.Fatalf("identity incomplete: %+v", xform.meta)
	}
	if xform.meta.TraceID == "" || xform.meta.ALegID == "" {
		t.Fatalf("trace/aleg empty: %+v", xform.meta)
	}
	if len(xform.meta.BackendPrefixes) != 2 || xform.meta.BackendPrefixes[0] != "family-a" {
		t.Fatalf("prefixes=%v", xform.meta.BackendPrefixes)
	}
	if len(xform.meta.ReplaySupport.Dialects) != 1 {
		t.Fatalf("replay=%v", xform.meta.ReplaySupport)
	}
	if xform.meta.Workspace.ID != "ws-1" || xform.meta.Workspace.ProjectRoot != "/proj" {
		t.Fatalf("workspace=%+v", xform.meta.Workspace)
	}
	if xform.meta.Scope.PrincipalID.String() == "" || xform.meta.Session.AuthoritativeSessionID == "" {
		t.Fatalf("scope/session incomplete: scope=%+v session=%+v", xform.meta.Scope, xform.meta.Session)
	}
	xform.meta.BackendPrefixes[0] = "mutated"
	xform.meta.ReplaySupport.Dialects[0] = "mutated"
	xform.meta.Workspace.Markers[0] = "mutated"
	be := ex.Backends["a"]
	if be.BackendPrefixes[0] != "family-a" {
		t.Fatal("backend prefixes mutated via AttemptMeta")
	}
	if be.ReplaySupport.Dialects[0] != lipapi.ReasoningDialectOpenAIChatTextV1 {
		t.Fatal("backend replay dialects mutated via AttemptMeta")
	}
}

type metaCaptureTransform struct {
	meta request.AttemptMeta
}

func (m *metaCaptureTransform) ID() string                      { return "meta-capture" }
func (*metaCaptureTransform) Order() int                        { return 0 }
func (*metaCaptureTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (m *metaCaptureTransform) HandleAttempt(_ context.Context, _ *lipapi.Call, meta request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	m.meta = meta
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}
