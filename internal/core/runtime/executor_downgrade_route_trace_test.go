package runtime_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// downgradeRouteTraceCatalogResolver returns distinct catalog match metadata depending on whether
// reasoning was already stripped from the call (post-downgrade) so route trace can detect stale facts.
type downgradeRouteTraceCatalogResolver struct{}

func (downgradeRouteTraceCatalogResolver) Resolve(
	ctx context.Context,
	cand routing.AttemptCandidate,
	call lipapi.Call,
	backend lipapi.BackendCaps,
) modelcatalog.EffectiveFacts {
	_ = ctx
	input := strings.TrimSpace(cand.Primary.Model)
	be := maps.Clone(backend)
	postDowngrade := strings.TrimSpace(call.Options.ReasoningEffort) == ""
	if !postDowngrade {
		return modelcatalog.EffectiveFacts{
			Facts: modelcatalog.ModelFacts{
				Source:    modelcatalog.FactSourceCatalog,
				MatchKind: modelcatalog.MatchExact,
			},
			BackendCaps:   backend,
			EffectiveCaps: be,
			Matched:       true,
			Match: modelcatalog.MatchResult{
				Kind: modelcatalog.MatchExact, InputModel: input, MatchedID: input,
			},
		}
	}
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchAmbiguous,
		},
		BackendCaps:   backend,
		EffectiveCaps: be,
		Matched:       true,
		Match: modelcatalog.MatchResult{
			Kind: modelcatalog.MatchAmbiguous, InputModel: input, MatchedID: "",
			Candidates: []string{"post-a", "post-b"},
		},
	}
}

func TestExecutor_downgrade_noEligibility_routeTraceUsesPostDowngradeFacts(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trace := diag.NewRouteTraceBuffer(16)
	ex := &runtime.Executor{
		Store:           st,
		Bus:             hooks.New(hooks.Config{}),
		Rand:            routing.NewSeededRng(11),
		RouteTrace:      trace,
		CatalogResolver: downgradeRouteTraceCatalogResolver{},
		// EligibilityResolver intentionally nil: regression covers facts refresh on this path.
		Backends: map[string]execbackend.Backend{
			"rb": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "rb:gpt-4o"},
		Options: lipapi.GenerationOptions{
			ReasoningEffort: "low",
		},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()

	var last *diag.RouteTraceCatalog
	for _, e := range trace.Snapshot() {
		if e.Decision == "plan_candidate" && e.Catalog != nil {
			last = e.Catalog
		}
	}
	if last == nil {
		t.Fatal("expected at least one plan_candidate route trace with catalog metadata")
	}
	if last.Negotiation != string(lipapi.NegotiationDowngrade) {
		t.Fatalf("negotiation in trace: %q", last.Negotiation)
	}
	if got, want := last.MatchKind, modelcatalog.MatchAmbiguous.String(); got != want {
		t.Fatalf("route trace catalog match_kind: got %q want %q (pre-downgrade would be exact)", got, want)
	}
}
