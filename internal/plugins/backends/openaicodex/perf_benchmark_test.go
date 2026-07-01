package openaicodex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func BenchmarkBuildToolsStrictSchema(b *testing.B) {
	params := json.RawMessage(`{
		"type":"object",
		"properties":{
			"command":{"type":"string"},
			"workdir":{"type":"string"},
			"timeout":{"type":"integer"},
			"env":{"type":"object","properties":{"PATH":{"type":"string"},"HOME":{"type":"string"}},"required":["PATH","HOME"]},
			"args":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"value":{"type":"string"}},"required":["name","value"]}}
		},
		"required":["command","workdir","timeout","env","args"]
	}`)
	tools := []lipapi.ToolDef{{Name: "bash", Description: "run command", Parameters: params}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := buildTools(tools, false); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkToolResultStringLargeOutput(b *testing.B) {
	raw, err := json.Marshal(map[string]any{
		"output":    strings.Repeat("line output\n", 4096),
		"exit_code": 0,
		"workdir":   `C:\Users\Mateusz\source\repos\go-llm-interactive-proxy`,
		"metadata": map[string]any{
			"ignored": strings.Repeat("nested data", 256),
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	part := lipapi.Part{Kind: lipapi.PartToolResult, ToolCallID: "call_1", Content: raw}
	b.ReportAllocs()
	for b.Loop() {
		if got := toolResultString(part); got == "" {
			b.Fatal("empty result")
		}
	}
}

func BenchmarkCodexEventMapperResponseCompleted(b *testing.B) {
	data := `{"type":"response.completed","response":{"id":"resp_1","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]},{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}}`
	b.ReportAllocs()
	for b.Loop() {
		mapper := newCodexEventMapper(0)
		if err := mapper.handleData(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWSContinuationPrepareRecord(b *testing.B) {
	cfg := &Config{AccountID: "acct-1"}
	call := lipapi.Call{
		Session:    lipapi.SessionRef{ClientSessionID: "session-1"},
		Extensions: map[string]json.RawMessage{"agent": json.RawMessage(`"opencode"`)},
	}
	base := benchmarkContinuationPayload(50)
	next := base
	next.Input = append(append([]inputItem(nil), base.Input...), textMessageItem{Type: "message", Role: "user", Content: "continue"})
	baseFP := fingerprintInputItems(base.Input)
	nextFP := fingerprintInputItems(next.Input)
	b.ReportAllocs()
	for b.Loop() {
		store := newWSContinuationStore(time.Minute, 8)
		store.recordWithFingerprints(cfg, call, base, baseFP, "resp_1")
		candidate := next
		if !store.prepareWithFingerprints(context.Background(), cfg, call, &candidate, nextFP) {
			b.Fatal("expected continuation")
		}
	}
}

func benchmarkContinuationPayload(items int) Payload {
	input := make([]inputItem, 0, items)
	for i := range items {
		input = append(input, textMessageItem{Type: "message", Role: "user", Content: strings.Repeat("context ", 16) + string(rune('a'+i%26))})
	}
	return Payload{
		Model:          "gpt-5.4-mini",
		Instructions:   "instructions",
		PromptCacheKey: "session-1",
		Input:          input,
		Tools:          []toolPayload{{Type: "function", Name: "bash"}},
	}
}
