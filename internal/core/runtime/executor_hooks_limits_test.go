package runtime_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// submitRouteRewrite implements sdkhooks.SubmitHook and replaces Route.Selector for routing tests.
type submitRouteRewrite struct {
	to string
}

func (submitRouteRewrite) ID() string                        { return "test-route-rewrite" }
func (submitRouteRewrite) Order() int                        { return 0 }
func (submitRouteRewrite) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (s submitRouteRewrite) Handle(ctx context.Context, call *lipapi.Call, meta *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	_ = ctx
	_ = meta
	call.Route.Selector = s.to
	return sdkhooks.SubmitDecision{}, nil
}

// submitInflateUserText grows the first text part beyond canonical limits (DoS probe).
type submitInflateUserText struct{}

func (submitInflateUserText) ID() string                        { return "test-inflate" }
func (submitInflateUserText) Order() int                        { return 0 }
func (submitInflateUserText) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (submitInflateUserText) Handle(ctx context.Context, call *lipapi.Call, meta *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	_ = ctx
	_ = meta
	if len(call.Messages) > 0 && len(call.Messages[0].Parts) > 0 {
		call.Messages[0].Parts[0].Text = strings.Repeat("z", lipapi.MaxPartTextBytes+1)
	}
	return sdkhooks.SubmitDecision{}, nil
}

// executorSubmitHooksBus returns a Bus with only submit hooks (no request-part hooks).
func executorSubmitHooksBus(submit ...sdkhooks.SubmitHook) *hooks.Bus {
	return hooks.New(hooks.Config{SubmitHooks: submit})
}

func TestExecutor_submitHook_routeSelector_usedForPlanning(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var backendOpened atomic.Value
	ex := &runtime.Executor{
		Store: st,
		Bus: executorSubmitHooksBus(submitRouteRewrite{
			to: "backendB:model-x",
		}),
		Backends: map[string]execbackend.Backend{
			"backendA": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					backendOpened.Store("A")
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
			"backendB": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					backendOpened.Store("B")
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "backendA:old"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	v := backendOpened.Load()
	if v != "B" {
		t.Fatalf("routing must use post-submit Route.Selector (want backend B opened), got %v", v)
	}
}

func TestExecutor_submitHook_oversizedCall_rejectedBeforeBackendOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := &runtime.Executor{
		Store: st,
		Bus:   executorSubmitHooksBus(submitInflateUserText{}),
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					atomic.AddInt32(&opens, 1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected validation error after submit hook inflated message text")
	}
	if opens != 0 {
		t.Fatalf("backend must not open (opens=%d)", opens)
	}
}
