package runtime_test

import (
	"context"
	"errors"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"io"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)

// scopeCaptureToolPolicy records the authoritative Scope it receives on the
// stream path and allows the tool call so the stream continues.
type scopeCaptureToolPolicy struct {
	mu    sync.Mutex
	scope scope.PrincipalScopeView
	got   bool
}

func (s *scopeCaptureToolPolicy) ID() string                        { return "scope-capture-tool" }
func (s *scopeCaptureToolPolicy) Order() int                        { return 0 }
func (s *scopeCaptureToolPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s *scopeCaptureToolPolicy) Handle(_ context.Context, _ lipapi.ToolEvent, meta toolpolicy.Meta, _ toolpolicy.Services) (toolpolicy.Decision, error) {
	s.mu.Lock()
	s.scope = meta.Scope
	s.got = true
	s.mu.Unlock()
	return toolpolicy.DecisionAllow, nil
}

func (s *scopeCaptureToolPolicy) snapshot() (scope.PrincipalScopeView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scope, s.got
}

// scopeCaptureGate records the authoritative Scope/Session/Workspace it receives
// at completion time and passes the buffer through so the stream finishes.
type scopeCaptureGate struct {
	mu       sync.Mutex
	scope    scope.PrincipalScopeView
	session  string
	gotScope bool
}

func (s *scopeCaptureGate) ID() string                        { return "scope-capture-gate" }
func (s *scopeCaptureGate) Order() int                        { return 0 }
func (s *scopeCaptureGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s *scopeCaptureGate) Handle(_ context.Context, meta completion.Meta, _ completion.Buffered, _ completion.Services) (completion.Outcome, error) {
	s.mu.Lock()
	s.scope = meta.Scope
	s.session = meta.Session.AuthoritativeSessionID
	s.gotScope = true
	s.mu.Unlock()
	return completion.PassOriginalOutcome(), nil
}

func (s *scopeCaptureGate) snapshot() (scope.PrincipalScopeView, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scope, s.session, s.gotScope
}

// TestStreamToolPolicyMetaCarriesAuthoritativeScope asserts the runtime
// populates toolpolicy.Meta.Scope from the request-scoped execctx views on the
// stream path so tool policies see proxy-validated attribution. This fails if
// applyToolPolicies does not copy Scope from execctx views.
func TestStreamToolPolicyMetaCarriesAuthoritativeScope(t *testing.T) {
	t.Parallel()
	backendStream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "x"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "search"},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "search"},
		{Kind: lipapi.EventResponseFinished},
	})
	backends := map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}
	pol := &scopeCaptureToolPolicy{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeBundle(lipfeature.FeatureBundle{
			SchemaVersion:    lipfeature.SchemaVersionV1,
			ToolCallPolicies: []toolpolicy.Policy{pol},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	call.Tools = []lipapi.ToolDef{{Name: "search", Parameters: []byte(`{}`)}}
	call.ToolChoice = lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}
	stream, err := ex.Execute(principalCtx("user-scope-stream"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for {
		_, rerr := stream.Recv(context.Background())
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			t.Fatalf("recv: %v", rerr)
		}
	}
	_ = stream.Close()
	gotScope, got := pol.snapshot()
	if !got {
		t.Fatal("tool policy was not invoked on the stream path")
	}
	if gotScope.PrincipalID.String() != "user-scope-stream" {
		t.Fatalf("toolpolicy.Meta.Scope.PrincipalID: got %q want user-scope-stream", gotScope.PrincipalID.String())
	}
}

// TestStreamCompletionMetaCarriesAuthoritativeScope asserts the runtime
// populates completion.Meta.Scope (and Session) from the request-scoped execctx
// views at completion time. This fails if completion_recv does not copy Scope
// from execctx views.
func TestStreamCompletionMetaCarriesAuthoritativeScope(t *testing.T) {
	t.Parallel()
	backendStream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "x"},
		{Kind: lipapi.EventResponseFinished},
	})
	backends := map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}
	gate := &scopeCaptureGate{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeBundle(lipfeature.FeatureBundle{
			SchemaVersion:   lipfeature.SchemaVersionV1,
			CompletionGates: []completion.Gate{gate},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-scope-completion"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	gotScope, gotSession, got := gate.snapshot()
	if !got {
		t.Fatal("completion gate was not invoked at finish")
	}
	if gotScope.PrincipalID.String() != "user-scope-completion" {
		t.Fatalf("completion.Meta.Scope.PrincipalID: got %q want user-scope-completion", gotScope.PrincipalID.String())
	}
	if gotSession == "" {
		t.Fatalf("completion.Meta.Session.AuthoritativeSessionID must be populated from execctx views, got empty")
	}
}
