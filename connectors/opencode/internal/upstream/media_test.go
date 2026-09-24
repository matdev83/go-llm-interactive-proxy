package upstream

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestTextOnlyRequestBuildersRejectMedia(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		call lipapi.Call
	}{
		{
			name: "image reference",
			call: lipapi.Call{Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{{Kind: lipapi.PartImageRef, ImageRef: "https://example.invalid/image.png"}},
			}}},
		},
		{
			name: "file reference",
			call: lipapi.Call{Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{{Kind: lipapi.PartFileRef, FileRef: "https://example.invalid/document.pdf"}},
			}}},
		},
		{
			name: "video item",
			call: lipapi.Call{Items: []lipapi.Item{{
				Kind:    lipapi.ItemKindMessage,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartVideoRef, VideoRef: "https://example.invalid/video.mp4"}},
			}}},
		},
		{
			name: "tool result image part",
			call: lipapi.Call{Items: []lipapi.Item{{
				Kind: lipapi.ItemKindToolResult,
				ToolResult: &lipapi.ToolResultItem{Parts: []lipapi.ContentPart{{
					Kind: lipapi.ContentPartImageRef, ImageRef: "https://example.invalid/tool-image.png",
				}}},
			}}},
		},
		{
			name: "tool result file part",
			call: lipapi.Call{Items: []lipapi.Item{{
				Kind: lipapi.ItemKindToolResult,
				ToolResult: &lipapi.ToolResultItem{Parts: []lipapi.ContentPart{{
					Kind: lipapi.ContentPartFileRef, FileRef: "https://example.invalid/tool-file.pdf",
				}}},
			}}},
		},
		{
			name: "tool result video part",
			call: lipapi.Call{Items: []lipapi.Item{{
				Kind: lipapi.ItemKindToolResult,
				ToolResult: &lipapi.ToolResultItem{Parts: []lipapi.ContentPart{{
					Kind: lipapi.ContentPartVideoRef, VideoRef: "https://example.invalid/tool-video.mp4",
				}}},
			}}},
		},
	} {
		for _, builder := range []struct {
			name  string
			build func(lipapi.Call) ([]byte, error)
		}{
			{name: "anthropic", build: func(c lipapi.Call) ([]byte, error) { return anthropicRequestBody(c, "claude") }},
			{name: "gemini", build: geminiRequestBody},
		} {
			t.Run(tc.name+"/"+builder.name, func(t *testing.T) {
				t.Parallel()
				_, err := builder.build(tc.call)
				if err == nil || !strings.Contains(err.Error(), "unsupported canonical media") {
					t.Fatalf("error=%v, want explicit unsupported canonical media error", err)
				}
			})
		}
	}
}

func TestTextOnlyRequestBuildersAcceptText(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}}}
	if _, err := anthropicRequestBody(call, "claude"); err != nil {
		t.Fatalf("anthropic text request: %v", err)
	}
	if _, err := geminiRequestBody(call); err != nil {
		t.Fatalf("gemini text request: %v", err)
	}
}
