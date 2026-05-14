package runtime_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

func TestExecutor_OpenContext_carriesTransportPrincipalInViews(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var openCtx context.Context
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					openCtx = ctx
					_ = call
					_ = cand
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseFinished},
					}), nil
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
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "transport-user"})
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), stream)

	v, ok := execctx.FromContext(openCtx)
	if !ok {
		t.Fatal("expected execctx views on backend open context")
	}
	if v.Principal.ID != "transport-user" {
		t.Fatalf("principal id: want transport-user got %q", v.Principal.ID)
	}
}
