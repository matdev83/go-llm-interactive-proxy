package geminigenerate

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func BenchmarkBuildContents(b *testing.B) {
	call := &lipapi.Call{
		Messages: make([]lipapi.Message, 1000),
	}
	for i := 0; i < 1000; i++ {
		call.Messages[i] = lipapi.Message{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("hello world"),
			},
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = buildContents(call)
	}
}

func BenchmarkUserPartsToGenaiParts(b *testing.B) {
	parts := make([]lipapi.Part, 1000)
	for i := 0; i < 1000; i++ {
		parts[i] = lipapi.TextPart("hello world")
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = userPartsToGenaiParts(parts)
	}
}

func BenchmarkAssistantPartsToGenaiParts(b *testing.B) {
	parts := make([]lipapi.Part, 1000)
	for i := 0; i < 1000; i++ {
		parts[i] = lipapi.TextPart("hello world")
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = assistantPartsToGenaiParts(parts)
	}
}

func BenchmarkBuildTools(b *testing.B) {
	tools := make([]lipapi.ToolDef, 1000)
	for i := 0; i < 1000; i++ {
		tools[i] = lipapi.ToolDef{
			Name:        "test",
			Description: "desc",
			Parameters:  json.RawMessage(`{"type": "object"}`),
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = buildTools(tools)
	}
}
