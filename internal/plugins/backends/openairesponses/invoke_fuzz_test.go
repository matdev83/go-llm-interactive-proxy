package openairesponses

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func FuzzBuildToolsParametersJSON(f *testing.F) {
	f.Add([]byte(`{"type":"object"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, lipapi.MaxToolParametersBytes)
		_, _ = buildTools([]lipapi.ToolDef{{Name: "fn", Parameters: raw}})
	})
}
