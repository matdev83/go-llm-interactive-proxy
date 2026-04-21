package runtime_test

import (
	"context"
	"math/rand"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestExecutor_candidateHealthSkipsUnhealthyKey(t *testing.T) {
	t.Parallel()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	var opened string
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(2)),
		CandidateHealth: policy.StaticUnhealthy{
			"bad:m": {},
		},
		Backends: map[string]runtime.Backend{
			"bad": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					panic("unhealthy candidate must not open")
				},
			},
			"ok": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					opened = cand.Primary.Backend + ":" + cand.Primary.Model
					return lipapi.FixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: "health-skip"},
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
	if opened != "ok:m" {
		t.Fatalf("expected failover to healthy candidate, opened %q", opened)
	}
	_, _ = lipapi.Collect(context.Background(), s)
}
