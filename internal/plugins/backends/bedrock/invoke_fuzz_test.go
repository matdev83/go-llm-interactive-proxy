package bedrock

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func FuzzAssistantPartsToContentBlocksJSON(f *testing.F) {
	f.Add([]byte(`{"tool_use_id":"t","name":"fn","input":{}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, lipapi.MaxPartJSONBytes)
		_, _ = assistantPartsToContentBlocks([]lipapi.Part{{
			Kind:    lipapi.PartJSON,
			Content: raw,
		}})
	})
}
