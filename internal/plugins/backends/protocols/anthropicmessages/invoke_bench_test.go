package anthropicmessages

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func BenchmarkBuildSystemBlocks(b *testing.B) {
	call := &lipapi.Call{
		Instructions: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Instruction 1"}}},
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Instruction 2"}}},
		},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Sys 1"}, {Kind: lipapi.PartText, Text: "Sys 2"}}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "User 1"}}},
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Sys 3"}, {Kind: lipapi.PartText, Text: "Sys 4"}, {Kind: lipapi.PartText, Text: "Sys 5"}}},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildSystemBlocks(call)
	}
}

func BenchmarkBuildAnthropicMessages(b *testing.B) {
	call := &lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Sys 1"}}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "User 1"}}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Assistant 1"}}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "User 2"}}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "Assistant 2"}}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "User 3"}}},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = buildAnthropicMessages(call)
	}
}

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
