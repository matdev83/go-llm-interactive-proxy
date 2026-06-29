package anthropicmessages

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func BenchmarkUserPartsToBlocks(b *testing.B) {
	parts := make([]lipapi.Part, 100)
	for i := range 100 {
		parts[i] = lipapi.Part{
			Kind: lipapi.PartText,
			Text: "Hello, world!",
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = userPartsToBlocks(parts)
	}
}
