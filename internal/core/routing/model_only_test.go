package routing_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

func TestApplyModelOnlyBackends(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("gpt-4")
	if err != nil {
		t.Fatal(err)
	}
	routing.ApplyModelOnlyBackends(sel, "openai")
	if routing.SelectorHasEmptyBackend(sel) {
		t.Fatal("expected backend filled")
	}
	list, err := routing.ExpandFailover(sel, routing.PlanOptions{Rand: fixedRng{}})
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Primary.Backend != "openai" || list[0].Primary.Model != "gpt-4" {
		t.Fatalf("got %#v", list[0].Primary)
	}
}

func TestDefaultBackendFromRouteSelector(t *testing.T) {
	t.Parallel()
	b, err := routing.DefaultBackendFromRouteSelector("openai:gpt-4")
	if err != nil || b != "openai" {
		t.Fatalf("got %q %v", b, err)
	}
	if _, err := routing.DefaultBackendFromRouteSelector(""); err != nil {
		t.Fatal(err)
	}
}

type fixedRng struct{}

func (fixedRng) Intn(n int) int { return 0 }
