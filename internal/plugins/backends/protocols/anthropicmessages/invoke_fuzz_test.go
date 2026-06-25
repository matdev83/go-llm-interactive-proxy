package anthropicmessages

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func FuzzToolInputSchemaParametersJSON(f *testing.F) {
	f.Add([]byte(`{"type":"object","properties":{}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, lipapi.MaxToolParametersBytes)
		_, _ = toolInputSchema(lipapi.ToolDef{Name: "fn", Parameters: raw})
	})
}
