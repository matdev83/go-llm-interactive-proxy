package openresponsescompat

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestBackendToolChoice_OmittedWhenToolsEmpty(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Tools: nil,
		ToolChoice: lipapi.ToolChoice{
			Mode: lipapi.ToolChoiceAuto,
		},
		Items: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindMessage,
				Role:   lipapi.RoleUser,
				Status: lipapi.ItemStatusCompleted,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "hello"},
				},
			},
		},
	}
	body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatalf("buildCreateRequest failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if _, ok := got["tool_choice"]; ok {
		t.Errorf("expected tool_choice to be omitted when call.Tools is empty")
	}
}

func TestBackendToolChoice_EncodingCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tc   lipapi.ToolChoice
		want map[string]any
	}{
		{
			name: "auto subset",
			tc:   lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto, AllowedTools: []string{"fn1", "fn2"}},
			want: map[string]any{
				"type": "allowed_tools",
				"mode": "auto",
				"tools": []any{
					map[string]any{"type": "function", "name": "fn1"},
					map[string]any{"type": "function", "name": "fn2"},
				},
			},
		},
		{
			name: "required subset maps to mode required",
			tc:   lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny, AllowedTools: []string{"fn1"}},
			want: map[string]any{
				"type":  "allowed_tools",
				"mode":  "required",
				"tools": []any{map[string]any{"type": "function", "name": "fn1"}},
			},
		},
		{
			name: "none subset",
			tc:   lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone, AllowedTools: []string{"fn1"}},
			want: map[string]any{
				"type":  "allowed_tools",
				"mode":  "none",
				"tools": []any{map[string]any{"type": "function", "name": "fn1"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := lipapi.Call{
				Tools: []lipapi.ToolDef{
					{Name: "fn1", Description: "desc"},
					{Name: "fn2", Description: "desc"},
				},
				ToolChoice: tc.tc,
				Items: []lipapi.Item{
					{
						Kind:    lipapi.ItemKindMessage,
						Role:    lipapi.RoleUser,
						Status:  lipapi.ItemStatusCompleted,
						Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hello"}},
					},
				},
			}
			body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err != nil {
				t.Fatalf("buildCreateRequest failed: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if !reflect.DeepEqual(got["tool_choice"], tc.want) {
				t.Errorf("tool_choice mismatch\ngot:  %+v\nwant: %+v", got["tool_choice"], tc.want)
			}
		})
	}
}

func TestBackendToolChoice_PreservesOrder(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Tools: []lipapi.ToolDef{
			{Name: "z", Description: "desc"},
			{Name: "a", Description: "desc"},
			{Name: "m", Description: "desc"},
		},
		ToolChoice: lipapi.ToolChoice{
			Mode:         lipapi.ToolChoiceAuto,
			AllowedTools: []string{"z", "a", "m"},
		},
		Items: []lipapi.Item{
			{
				Kind:    lipapi.ItemKindMessage,
				Role:    lipapi.RoleUser,
				Status:  lipapi.ItemStatusCompleted,
				Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "hello"}},
			},
		},
	}
	body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatalf("buildCreateRequest failed: %v", err)
	}
	var got struct {
		ToolChoice struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	tools := got.ToolChoice.Tools
	if len(tools) != 3 || tools[0].Name != "z" || tools[1].Name != "a" || tools[2].Name != "m" {
		t.Fatalf("subset order lost: %+v", tools)
	}
}
