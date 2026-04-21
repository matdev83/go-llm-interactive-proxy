package gemini

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// FuzzBuildGenerateContentResponse_toolJSON exercises json.Unmarshal on tool arguments in encode.
func FuzzBuildGenerateContentResponse_toolJSON(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"x":[1,2,3]}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 32<<10)
		_, _ = buildGenerateContentWire("", []lipapi.ToolCallSummary{{
			ID:        "1",
			Name:      "fn",
			Arguments: string(raw),
		}})
	})
}
