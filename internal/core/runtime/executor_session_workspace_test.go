package runtime_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	coreworkspace "github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type labelOpener struct{}

func (labelOpener) ID() string { return "label-opener" }
func (labelOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{SessionLabelUpserts: map[string]string{"boot": "1"}}, nil
}

type memWS struct{}

func (memWS) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{ID: "ws-1", ProjectRoot: "/proj"}, nil
}

func TestExecutor_backendOpenContext_hasSessionLabelsAndWorkspace(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		SessionOpeners: []session.Opener{labelOpener{}},
		Workspace:      coreworkspace.NewResolverChain([]lipworkspace.Resolver{memWS{}}),
	})
	var openCtx context.Context
	ex := &runtime.Executor{
		Store:           st,
		Bus:             bus,
		RuntimeSnapshot: snap,
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					openCtx = ctx
					_ = call
					_ = cand
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(3),
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), stream)
	v, ok := execctx.FromContext(openCtx)
	if !ok {
		t.Fatal("expected views")
	}
	if v.Session.Labels["boot"] != "1" {
		t.Fatalf("session labels %+v", v.Session.Labels)
	}
	if v.Workspace.ProjectRoot != "/proj" {
		t.Fatalf("workspace root %q", v.Workspace.ProjectRoot)
	}
	if v.Session.WorkspaceID != "ws-1" {
		t.Fatalf("session workspace id: got %q", v.Session.WorkspaceID)
	}
}
