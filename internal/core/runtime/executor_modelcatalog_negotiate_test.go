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

// narrowVisionCatalogResolver removes vision from effective caps for backend "narrow" to force negotiation reject.
type narrowVisionCatalogResolver struct{}

func (narrowVisionCatalogResolver) Resolve(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call, backend lipapi.BackendCaps) modelcatalog.EffectiveFacts {
	_ = ctx
	_ = call
	input := strings.TrimSpace(cand.Primary.Model)
	if cand.Primary.Backend == "narrow" {
		out := maps.Clone(backend)
		delete(out, lipapi.CapabilityVision)
		return modelcatalog.EffectiveFacts{
			Facts: modelcatalog.ModelFacts{
				Source:            modelcatalog.FactSourceCatalog,
				MatchKind:         modelcatalog.MatchExact,
				ContextLimit:      modelcatalog.LimitFact{State: modelcatalog.LimitUnknown},
				Tools:             modelcatalog.CapabilityUnknown,
				StructuredOutputs: modelcatalog.CapabilityUnknown,
				Reasoning:         modelcatalog.CapabilityUnknown,
				Vision:            modelcatalog.CapabilityUnknown,
				Documents:         modelcatalog.CapabilityUnknown,
			},
			BackendCaps:   backend,
			EffectiveCaps: out,
			Matched:       true,
			Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchExact, InputModel: input, MatchedID: input},
		}
	}
	be := maps.Clone(backend)
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:    modelcatalog.FactSourceBackendDeclaration,
			MatchKind: modelcatalog.MatchNoMatch,
		},
		BackendCaps:   backend,
		EffectiveCaps: be,
		Matched:       false,
		Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchNoMatch, InputModel: input},
	}
}

func TestExecutor_catalogNarrowsCaps_firstCandidateRejected_secondOpens(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	rt := diag.NewRouteTraceBuffer(16)
	ex := &runtime.Executor{
		Store:           st,
		Bus:             hooks.New(hooks.Config{}),
		Rand:            routing.NewSeededRng(1),
		RouteTrace:      rt,
		CatalogResolver: narrowVisionCatalogResolver{},
		Backends: map[string]execbackend.Backend{
			"narrow": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityVision),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					t.Fatal("narrow must not open after vision-removed negotiation reject")
					return nil, nil
				},
			},
			"wide": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityVision),
				Open: func(_ context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					opened = cand.Primary.Backend
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "narrow:gpt-4o|wide:gpt-4o"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:      lipapi.PartImageRef,
				ImageRef:  "https://example.com/x.png",
				ImageMIME: "image/png",
			}},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "wide" {
		t.Fatalf("opened backend: %q", opened)
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()

	snap := rt.Snapshot()
	var rejectTrace *diag.RouteTraceEntry
	for i := range snap {
		e := snap[i]
		if e.Decision != "plan_candidate" || e.Catalog == nil {
			continue
		}
		if e.Catalog.Negotiation == string(lipapi.NegotiationReject) && e.Catalog.FactSource == modelcatalog.FactSourceCatalog.String() {
			rejectTrace = &snap[i]
			break
		}
	}
	if rejectTrace == nil {
		t.Fatalf("expected route trace plan_candidate with negotiation reject and catalog source, got %#v", snap)
	}
	if rejectTrace.Catalog.MatchKind != modelcatalog.MatchExact.String() {
		t.Fatalf("catalog match_kind: %q", rejectTrace.Catalog.MatchKind)
	}
	if rejectTrace.Catalog.Eligibility != "skipped" {
		t.Fatalf("catalog eligibility: %q", rejectTrace.Catalog.Eligibility)
	}
}

func TestExecutor_catalogDisabled_noResolver_usesBackendCaps(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			"only": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityVision),
				Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.EventStream, error) {
					opened = "only"
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "only:gpt-4o"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{{
				Kind:      lipapi.PartImageRef,
				ImageRef:  "https://example.com/x.png",
				ImageMIME: "image/png",
			}},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "only" {
		t.Fatalf("opened: %q", opened)
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()
}
