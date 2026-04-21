package routing_test

import (
	"math/rand"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

func BenchmarkExpandFailover_simpleSelector(b *testing.B) {
	sel, err := routing.Parse("openai:gpt-4o-mini")
	if err != nil {
		b.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := routing.ExpandFailover(sel, routing.PlanOptions{Rand: rng})
		if err != nil {
			b.Fatal(err)
		}
	}
}
