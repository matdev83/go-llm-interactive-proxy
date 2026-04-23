package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func cbTestNow() time.Time {
	return time.Unix(1715620001, 0).UTC()
}

func TestExecutor_circuitBreakerSkipsAfterFailures(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 1,
		OpenDuration:     time.Hour,
		Now:              cbTestNow,
	})
	var badOpens int
	ex := &runtime.Executor{
		Store:           st,
		Bus:             hooks.New(hooks.Config{}),
		Rand:            routing.NewSeededRng(3),
		Now:             cbTestNow,
		CandidateHealth: cb,
		Backends: map[string]execbackend.Backend{
			"bad": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					badOpens++
					return nil, lipapi.RecoverablePreOutputError(errors.New("boom"))
				},
			},
			"ok": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "cb-skip"},
		Route:   lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if badOpens != 1 {
		t.Fatalf("want one bad open on first request, got %d", badOpens)
	}
	_, _ = lipapi.Collect(context.Background(), s)

	call2 := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "cb-skip-2"},
		Route:   lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	s2, err := ex.Execute(context.Background(), call2)
	if err != nil {
		t.Fatal(err)
	}
	if badOpens != 1 {
		t.Fatalf("expected bad skipped by circuit on second request, badOpens=%d", badOpens)
	}
	_, _ = lipapi.Collect(context.Background(), s2)
}
