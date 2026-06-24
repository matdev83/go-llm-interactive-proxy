package runtime_test

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type contextLimitCatalogResolver struct{}

type fixedRequestTokenEstimator struct {
	available bool
	tokens    int64
}

type errorEventStream struct {
	err error
}

func (e *errorEventStream) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, e.err
}

func (e *errorEventStream) Close() error { return nil }

func (e *errorEventStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (f fixedRequestTokenEstimator) EstimateRequestTokens(context.Context, lipapi.Call) modelcatalog.SizeEstimate {
	return modelcatalog.SizeEstimate{Available: f.available, Units: "tokens", Input: f.tokens, Basis: "test"}
}

func (contextLimitCatalogResolver) Resolve(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call, backend lipapi.BackendCaps) modelcatalog.EffectiveFacts {
	_ = ctx
	_ = call
	input := strings.TrimSpace(cand.Primary.Model)
	be := maps.Clone(backend)
	switch cand.Primary.Backend {
	case "smallctx":
		return modelcatalog.EffectiveFacts{
			Facts: modelcatalog.ModelFacts{
				Source:       modelcatalog.FactSourceCatalog,
				MatchKind:    modelcatalog.MatchExact,
				ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 20},
			},
			BackendCaps:   backend,
			EffectiveCaps: be,
			Matched:       true,
			Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchExact, InputModel: input, MatchedID: input},
		}
	case "bigctx":
		return modelcatalog.EffectiveFacts{
			Facts: modelcatalog.ModelFacts{
				Source:       modelcatalog.FactSourceCatalog,
				MatchKind:    modelcatalog.MatchExact,
				ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 1_000_000},
			},
			BackendCaps:   backend,
			EffectiveCaps: be,
			Matched:       true,
			Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchExact, InputModel: input, MatchedID: input},
		}
	default:
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
}

func longUserMessage(n int) []lipapi.Message {
	text := strings.Repeat("x", n)
	return []lipapi.Message{{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{lipapi.TextPart(text)},
	}}
}

func TestExecutor_contextLimit_failoverToLargerLimitBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	se := modelcatalog.DefaultSizeEstimator{}
	el := modelcatalog.NewEligibilityResolver(se)
	var opened string
	ex := &runtime.Executor{
		Store:               st,
		Bus:                 hooks.New(hooks.Config{}),
		Rand:                routing.NewSeededRng(2),
		CatalogResolver:     contextLimitCatalogResolver{},
		EligibilityResolver: el,
		Backends: map[string]execbackend.Backend{
			"smallctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					t.Fatal("smallctx must be skipped by context limit")
					return nil, nil
				},
			},
			"bigctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opened = "bigctx"
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "smallctx:m|bigctx:m"},
		Messages: longUserMessage(100),
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "bigctx" {
		t.Fatalf("opened: %q", opened)
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()
}

func TestExecutor_requestSizeConstraints_failoverToEligibleBackend(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	ex := &runtime.Executor{
		Store:                 st,
		Bus:                   hooks.New(hooks.Config{}),
		Rand:                  routing.NewSeededRng(2),
		RequestTokenEstimator: fixedRequestTokenEstimator{available: true, tokens: 11},
		Backends: map[string]execbackend.Backend{
			"smallctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					t.Fatal("smallctx must be skipped by max_context")
					return nil, nil
				},
			},
			"bigctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opened = cand.Key
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "[max_context=10]smallctx:m|[min_context=10]bigctx:m"},
		Messages: longUserMessage(5),
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "bigctx:m" {
		t.Fatalf("opened: %q", opened)
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()
}

func TestExecutor_requestSizeConstraints_supportsSuffixes(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	ex := &runtime.Executor{
		Store:                 st,
		Bus:                   hooks.New(hooks.Config{}),
		Rand:                  routing.NewSeededRng(2),
		RequestTokenEstimator: fixedRequestTokenEstimator{available: true, tokens: 250001},
		Backends: map[string]execbackend.Backend{
			"smallctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					t.Fatal("smallctx must be skipped by max_context suffix")
					return nil, nil
				},
			},
			"bigctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opened = cand.Key
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "[max_context=250K]smallctx:m|bigctx:m"},
		Messages: longUserMessage(5),
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "bigctx:m" {
		t.Fatalf("opened: %q", opened)
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()
}

func TestExecutor_requestSizeConstraints_failOpenWhenEstimateUnavailable(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	ex := &runtime.Executor{
		Store:                 st,
		Bus:                   hooks.New(hooks.Config{}),
		Rand:                  routing.NewSeededRng(2),
		RequestTokenEstimator: fixedRequestTokenEstimator{available: false, tokens: 11},
		Backends: map[string]execbackend.Backend{
			"smallctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opened = cand.Key
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
			"bigctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					t.Fatal("fail-open should keep first constrained branch eligible")
					return nil, nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "[max_context=10]smallctx:m|bigctx:m"},
		Messages: longUserMessage(5),
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "smallctx:m" {
		t.Fatalf("opened: %q", opened)
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()
}

func TestExecutor_requestSizeConstraints_preservedDuringRecvReplacement(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opened := []string{}
	ex := &runtime.Executor{
		Store:                 st,
		Bus:                   hooks.New(hooks.Config{}),
		Rand:                  routing.NewSeededRng(2),
		RequestTokenEstimator: fixedRequestTokenEstimator{available: true, tokens: 11},
		Backends: map[string]execbackend.Backend{
			"smallctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					t.Fatal("smallctx must remain skipped during recv replacement")
					return nil, nil
				},
			},
			"bigctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opened = append(opened, cand.Key)
					if len(opened) == 1 {
						return &errorEventStream{err: &lipapi.UpstreamFailure{
							Phase:        lipapi.PhasePreOutput,
							Recoverable:  true,
							Reason:       "recv replacement trigger",
							CandidateKey: cand.Key,
						}}, nil
					}
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "[max_context=10]smallctx:m|bigctx:m"},
		Messages: longUserMessage(5),
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lipapi.Collect(context.Background(), s)
	_ = s.Close()
	if err == nil {
		t.Fatal("expected replacement exhaustion after size-ineligible fallback")
	}
	if len(opened) != 1 || opened[0] != "bigctx:m" {
		t.Fatalf("opened sequence: %#v", opened)
	}
}

func TestExecutor_contextLimit_allCandidatesExcluded_returnsSentinel(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	se := modelcatalog.DefaultSizeEstimator{}
	el := modelcatalog.NewEligibilityResolver(se)
	ex := &runtime.Executor{
		Store:               st,
		Bus:                 hooks.New(hooks.Config{}),
		Rand:                routing.NewSeededRng(3),
		CatalogResolver:     contextLimitCatalogResolver{},
		EligibilityResolver: el,
		Backends: map[string]execbackend.Backend{
			"smallctx": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					t.Fatal("must not open")
					return nil, nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "smallctx:a|smallctx:b"},
		Messages: longUserMessage(200),
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !lipapi.IsAllCandidatesContextLimitExceeded(err) && !errors.Is(err, lipapi.ErrAllCandidatesContextLimitExceeded) {
		t.Fatalf("want context limit exhaustion, got %T %v", err, err)
	}
}

// twoPhaseLimitAfterDowngradeResolver returns a strict context limit while the call still
// requests reasoning (pre-negotiation), and a permissive limit after ApplyNegotiatedDowngrades clears it.
// Eligibility must use the post-downgrade re-resolve; otherwise the first-phase limit would exclude the candidate.
type twoPhaseLimitAfterDowngradeResolver struct{}

func (twoPhaseLimitAfterDowngradeResolver) Resolve(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call, backend lipapi.BackendCaps) modelcatalog.EffectiveFacts {
	_ = ctx
	input := strings.TrimSpace(cand.Primary.Model)
	be := maps.Clone(backend)
	if strings.TrimSpace(call.Options.ReasoningEffort) != "" {
		return modelcatalog.EffectiveFacts{
			Facts: modelcatalog.ModelFacts{
				Source:       modelcatalog.FactSourceCatalog,
				MatchKind:    modelcatalog.MatchExact,
				ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 10},
			},
			BackendCaps:   be,
			EffectiveCaps: be,
			Matched:       true,
			Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchExact, InputModel: input, MatchedID: input},
		}
	}
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:       modelcatalog.FactSourceCatalog,
			MatchKind:    modelcatalog.MatchExact,
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 1_000_000},
		},
		BackendCaps:   be,
		EffectiveCaps: be,
		Matched:       true,
		Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchExact, InputModel: input, MatchedID: input},
	}
}

func TestExecutor_downgrade_reResolvesCatalogFactsBeforeEligibility(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	se := modelcatalog.DefaultSizeEstimator{}
	el := modelcatalog.NewEligibilityResolver(se)
	var opened string
	ex := &runtime.Executor{
		Store:               st,
		Bus:                 hooks.New(hooks.Config{}),
		Rand:                routing.NewSeededRng(7),
		CatalogResolver:     twoPhaseLimitAfterDowngradeResolver{},
		EligibilityResolver: el,
		Backends: map[string]execbackend.Backend{
			"only": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opened = "only"
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "only:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(strings.Repeat("x", 50))},
		}},
		Options: lipapi.GenerationOptions{ReasoningEffort: "high"},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "only" {
		t.Fatalf("opened backend %q (eligibility must use post-downgrade catalog facts)", opened)
	}
	_, _ = lipapi.Collect(context.Background(), s)
	_ = s.Close()
}
