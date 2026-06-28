package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type preflightCountFunc func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error)

func (f preflightCountFunc) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return f(ctx, in)
}

type tokenAccountingCatalogResolver struct{}

func (tokenAccountingCatalogResolver) Resolve(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call, backend lipapi.BackendCaps) modelcatalog.EffectiveFacts {
	_ = ctx
	_ = call
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:       modelcatalog.FactSourceCatalog,
			MatchKind:    modelcatalog.MatchExact,
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 40},
		},
		BackendCaps:   backend,
		EffectiveCaps: backend,
		Matched:       true,
		Match: modelcatalog.MatchResult{
			Kind:       modelcatalog.MatchExact,
			InputModel: cand.Primary.Model,
			MatchedID:  cand.Primary.Model,
		},
	}
}

func TestExecutorRequiredPreflightRejectsBeforeBackendOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Preflight: accountingpreflight.NewChecker(preflightCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
			return accountingapp.CountResult{}, errors.New("counter unavailable")
		}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeStrict}),
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&opens, 1)
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	_, err = ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4o-mini"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("Backend.Open called %d times, want 0", opens)
	}
}

func TestExecutorAdvisoryPreflightUnavailableProceeds(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Preflight: accountingpreflight.NewChecker(preflightCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
			return accountingapp.CountResult{}, errors.New("counter unavailable")
		}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory}),
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&opens, 1)
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4o-mini"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("Backend.Open called %d times, want 1", opens)
	}
}

func TestExecutorPreflightOutputClampAppliesBeforeBackendOpen(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var openedMaxOutput int
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Preflight: accountingpreflight.NewChecker(preflightCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
			return accountingapp.CountResult{InputTokens: 3, TotalTokens: 3}, nil
		}), accountingpreflight.Config{
			Enabled:              true,
			Mode:                 accountingpreflight.ModeStrict,
			MaxOutputTokens:      5,
			ClampMaxOutputTokens: true,
		}),
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					if call.Options.MaxOutputTokens == nil {
						t.Fatal("Backend.Open received nil MaxOutputTokens")
					}
					openedMaxOutput = *call.Options.MaxOutputTokens
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	requestedMaxOutput := 20
	stream, err := ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4o-mini"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		Options:  lipapi.GenerationOptions{MaxOutputTokens: &requestedMaxOutput},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if openedMaxOutput != 5 {
		t.Fatalf("backend MaxOutputTokens = %d, want clamped 5", openedMaxOutput)
	}
	if requestedMaxOutput != 20 {
		t.Fatalf("original request MaxOutputTokens mutated to %d, want 20", requestedMaxOutput)
	}
}

func TestExecutorRequestSizeRoutingUsesTokenAccountingPreflightWhenEnabled(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	ex := &runtime.Executor{
		Store:           st,
		Bus:             hooks.New(hooks.Config{}),
		Rand:            routing.NewSeededRng(1),
		CatalogResolver: tokenAccountingCatalogResolver{},
		Preflight: accountingpreflight.NewChecker(preflightCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
			return accountingapp.CountResult{InputTokens: 35, TotalTokens: 35}, nil
		}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory}),
		RequestTokenEstimator: fixedRequestTokenEstimator{available: true, tokens: 5},
		Backends: map[string]execbackend.Backend{
			"small": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opened = "small"
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
				},
			},
			"large": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opened = "large"
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "[max_context=10]small:gpt-4o-mini|[max_context=100]large:gpt-4o-mini"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(strings.Repeat("x", 100))}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if opened != "large" {
		t.Fatalf("opened backend = %q, want large", opened)
	}
}

func TestExecutorRequestSizePreflightUsesHybridParallelLeaf(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var counted []string
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Preflight: accountingpreflight.NewChecker(preflightCountFunc(func(_ context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
			counted = append(counted, in.Backend+":"+in.Model)
			return accountingapp.CountResult{InputTokens: 3, TotalTokens: 3}, nil
		}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory}),
		Backends: map[string]execbackend.Backend{
			"exec-a": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
				},
			},
			"exec-b": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
				},
			},
			"thinker": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "[max_context=100]exec-a:small![max_context=100]exec-b:large^[thinker]thinker:plan"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if len(counted) == 0 || counted[0] != "exec-a:small" {
		t.Fatalf("first preflight counted %v, want first exec-a:small", counted)
	}
}
