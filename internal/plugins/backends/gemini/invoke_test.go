package gemini_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestStreamParamsForCall_textOnly(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "t1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "gemini", Model: "gemini-2.0-flash"},
	}
	sp, err := backend.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Model != "gemini-2.0-flash" {
		t.Fatalf("model: %s", sp.Model)
	}
	if len(sp.Contents) != 1 || sp.Contents[0].Parts[0].Text != "hello" {
		t.Fatalf("contents: %+v", sp.Contents)
	}
}

func TestStreamParamsForCall_modelFromExtensions(t *testing.T) {
	t.Parallel()
	rawModel, _ := json.Marshal("gemini-2.0-flash")
	call := lipapi.Call{
		ID: "t2",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
		Extensions: map[string]json.RawMessage{"gemini.model": rawModel},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "gemini"}}
	sp, err := backend.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Model != "gemini-2.0-flash" {
		t.Fatalf("model: %s", sp.Model)
	}
}

func TestStreamParamsForCall_systemInstruction(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "t3",
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("Be brief.")},
		}},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	sp, err := backend.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Config.SystemInstruction == nil || sp.Config.SystemInstruction.Parts[0].Text != "Be brief." {
		t.Fatalf("system: %+v", sp.Config.SystemInstruction)
	}
}

func TestStreamParamsForCall_maxOutputTokensInt32Bound(t *testing.T) {
	t.Parallel()
	maxTok := math.MaxInt32
	call := lipapi.Call{
		ID: "mt",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxTok},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	sp, err := backend.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Config.MaxOutputTokens != math.MaxInt32 {
		t.Fatalf("MaxOutputTokens: %d", sp.Config.MaxOutputTokens)
	}
}

func TestStreamParamsForCall_toolsAndChoice(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "t4",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "do_thing",
			Description: "d",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
		}},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "do_thing"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	sp, err := backend.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sp.Config)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "do_thing") {
		t.Fatalf("expected tool name in config: %s", s)
	}
	if sp.Config.ToolConfig == nil || sp.Config.ToolConfig.FunctionCallingConfig == nil {
		t.Fatal("missing toolConfig")
	}
	if len(sp.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) != 1 ||
		sp.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames[0] != "do_thing" {
		t.Fatalf("allowed names: %+v", sp.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames)
	}
}

func TestStreamParamsForCall_toolResultMessage(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "tool-res",
		Messages: []lipapi.Message{
			{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("call the tool")},
			},
			{
				Role: lipapi.RoleTool,
				Parts: []lipapi.Part{{
					Kind:       lipapi.PartToolResult,
					ToolCallID: "call_gem_1",
					ToolName:   "get_weather",
					Content:    json.RawMessage(`{"ok":true}`),
				}},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	sp, err := backend.StreamParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sp.Contents)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "functionResponse") || !strings.Contains(s, "get_weather") || !strings.Contains(s, "call_gem_1") {
		t.Fatalf("expected functionResponse tool result mapping, got: %s", s)
	}
}
