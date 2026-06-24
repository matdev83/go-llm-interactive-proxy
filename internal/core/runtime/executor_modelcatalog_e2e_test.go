package runtime_test

import (
	"context"
	"maps"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// noMatchCatalogResolver returns an explicit no-match to verify backend-only limiting (no catalog-driven rejection).
type noMatchCatalogResolver struct{}

func (noMatchCatalogResolver) Resolve(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call, backend lipapi.BackendCaps) modelcatalog.EffectiveFacts {
	_ = ctx
	_ = call
	input := strings.TrimSpace(cand.Primary.Model)
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

func TestExecutor_catalogNoMatch_opensWithBackendCapabilities(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened bool
	ex := &runtime.Executor{
		Store:               st,
		Bus:                 hooks.New(hooks.Config{}),
		Rand:                routing.NewSeededRng(1),
		EligibilityResolver: modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{}),
		CatalogResolver:     noMatchCatalogResolver{},
		Backends: map[string]execbackend.Backend{
			"be": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opened = true
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "be:some-model"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("expected backend open for no-match catalog")
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()
}

// snapshotGenResolver changes reported snapshot generation between Resolve calls to simulate
// a catalog refresh between requests (later requests see a new generation).
type snapshotGenResolver struct {
	n atomic.Int64
}

func (s *snapshotGenResolver) Resolve(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call, backend lipapi.BackendCaps) modelcatalog.EffectiveFacts {
	_ = ctx
	_ = call
	input := strings.TrimSpace(cand.Primary.Model)
	be := maps.Clone(backend)
	g := s.n.Add(1)
	return modelcatalog.EffectiveFacts{
		Facts:         modelcatalog.ModelFacts{Source: modelcatalog.FactSourceCatalog, MatchKind: modelcatalog.MatchExact},
		BackendCaps:   backend,
		EffectiveCaps: be,
		Matched:       true,
		Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchExact, InputModel: input, MatchedID: input},
		Snapshot:      modelcatalog.SnapshotRef{Generation: "gen" + strconv.FormatInt(g, 10)},
	}
}

func TestExecutor_sequentialExecutes_seeNewCatalogGenerations(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sgr := &snapshotGenResolver{}
	ex := &runtime.Executor{
		Store:               st,
		Bus:                 hooks.New(hooks.Config{}),
		Rand:                routing.NewSeededRng(1),
		EligibilityResolver: modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{}),
		CatalogResolver:     sgr,
		Backends: map[string]execbackend.Backend{
			"be": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "be:m1"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("a")},
		}},
	}
	s1, err := ex.Execute(context.Background(), call)
	_, _ = lipapi.Collect(context.Background(), mustStream(t, s1, err))
	gAfterFirst := sgr.n.Load()
	s2, err2 := ex.Execute(context.Background(), call)
	_, _ = lipapi.Collect(context.Background(), mustStream(t, s2, err2))
	gAfterSecond := sgr.n.Load()
	if gAfterSecond <= gAfterFirst {
		t.Fatalf("expected more resolve calls on second execute, first=%d second=%d", gAfterFirst, gAfterSecond)
	}
}

func mustStream(t *testing.T, s lipapi.EventStream, err error) lipapi.EventStream {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("nil stream")
	}
	return s
}
