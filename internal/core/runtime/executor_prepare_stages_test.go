package runtime_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
)

// dropToolNamed removes tools matching name (catalog filter test).
type dropToolNamed struct{ name string }

func (d dropToolNamed) ID() string                        { return "drop-" + d.name }
func (d dropToolNamed) Order() int                        { return 0 }
func (d dropToolNamed) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (d dropToolNamed) Handle(_ context.Context, call *lipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	out := call.Tools[:0]
	for _, t := range call.Tools {
		if t.Name != d.name {
			out = append(out, t)
		}
	}
	call.Tools = out
	return nil
}

func TestExecutor_toolCatalogFilter_beforeBackendOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		ToolCatalogFilters: []toolcatalog.Filter{dropToolNamed{name: "b"}},
	})
	var toolsSeen int
	ex := &runtime.Executor{
		Store:           st,
		Bus:             bus,
		RuntimeSnapshot: snap,
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.EventStream, error) {
					toolsSeen = len(call.Tools)
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{
			{Name: "a", Parameters: []byte(`{}`)},
			{Name: "b", Parameters: []byte(`{}`)},
		},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), stream)
	if toolsSeen != 1 {
		t.Fatalf("backend saw %d tools want 1", toolsSeen)
	}
}
