package gemini

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

func FuzzMessageToContentToolResultJSON(f *testing.F) {
	f.Add([]byte(`{"ok":true}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, lipapi.MaxPartJSONBytes)
		m := lipapi.Message{
			Role: lipapi.RoleTool,
			Parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: "id1",
				ToolName:   "n",
				Content:    raw,
			}},
		}
		_, _ = messageToContent(m)
	})
}
