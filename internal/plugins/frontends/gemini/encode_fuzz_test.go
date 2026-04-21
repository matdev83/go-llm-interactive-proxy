package gemini

import (
	"strings"
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
		b := new(strings.Builder)
		_, _ = b.Write(raw)
		col := lipapi.Collected{
			ToolArgs:      map[string]*strings.Builder{"1": b},
			ToolNames:     map[string]string{"1": "fn"},
			ToolCallOrder: []string{"1"},
		}
		_, _ = buildGenerateContentWire(col)
	})
}
