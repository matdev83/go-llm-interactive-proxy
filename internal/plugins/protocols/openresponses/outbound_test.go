package openresponses

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestEncodeOutboundRequest_IncludeExtensions_True(t *testing.T) {
	t.Parallel()

	// Valid extensions
	call := lipapi.Call{
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
		Extensions: map[string]json.RawMessage{
			"valid_ext": json.RawMessage(`{"a": 1}`),
		},
	}

	body, err := EncodeOutboundRequest(call, OutboundEncodeOptions{
		Model:             "gpt-4o",
		IncludeExtensions: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if _, ok := got["valid_ext"]; !ok {
		t.Errorf("expected valid_ext to be present when IncludeExtensions=true")
	}

	// Invalid extension should error
	badCall := call
	badCall.Extensions = map[string]json.RawMessage{
		"bad_ext": json.RawMessage(`{invalid_json}`),
	}
	_, err = EncodeOutboundRequest(badCall, OutboundEncodeOptions{
		Model:             "gpt-4o",
		IncludeExtensions: true,
	})
	if err == nil {
		t.Errorf("expected error for invalid extension when IncludeExtensions=true")
	}

	// Extension key collision on "model" should error
	colCall := call
	colCall.Extensions = map[string]json.RawMessage{
		"model": json.RawMessage(`"gpt-3.5-turbo"`),
	}
	_, err = EncodeOutboundRequest(colCall, OutboundEncodeOptions{
		Model:             "gpt-4o",
		IncludeExtensions: true,
	})
	if err == nil {
		t.Errorf("expected error for extension key 'model' collision when IncludeExtensions=true")
	}

	// Extension key collision on "stream" should error
	colCall2 := call
	colCall2.Extensions = map[string]json.RawMessage{
		"stream": json.RawMessage(`true`),
	}
	_, err = EncodeOutboundRequest(colCall2, OutboundEncodeOptions{
		Model:             "gpt-4o",
		IncludeExtensions: true,
	})
	if err == nil {
		t.Errorf("expected error for extension key 'stream' collision when IncludeExtensions=true")
	}
}

func TestEncodeOutboundRequest_IncludeExtensions_False(t *testing.T) {
	t.Parallel()

	// Invalid extension should be ignored (not validated) when IncludeExtensions=false
	call := lipapi.Call{
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
		Extensions: map[string]json.RawMessage{
			"bad_ext": json.RawMessage(`{invalid_json}`),
		},
	}

	body, err := EncodeOutboundRequest(call, OutboundEncodeOptions{
		Model:             "gpt-4o",
		IncludeExtensions: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if _, ok := got["bad_ext"]; ok {
		t.Errorf("expected bad_ext to be ignored when IncludeExtensions=false")
	}
}

func TestEncodeOutboundRequest_StreamTrue(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "hello"},
				},
			},
		},
	}
	body, err := EncodeOutboundRequest(call, OutboundEncodeOptions{
		Model:  "gpt-4o",
		Stream: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if stream, ok := got["stream"]; !ok || stream != true {
		t.Errorf("expected stream to be true, got %v", stream)
	}
}

func TestEncodeOutboundRequest_ReasoningEffort(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "hello"},
				},
			},
		},
		Options: lipapi.GenerationOptions{
			ReasoningEffort: "low",
		},
	}
	body, err := EncodeOutboundRequest(call, OutboundEncodeOptions{
		Model: "gpt-4o",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	reasoning, ok := got["reasoning"]
	if !ok {
		t.Fatalf("expected reasoning to be present")
	}
	reasoningMap, ok := reasoning.(map[string]any)
	if !ok {
		t.Fatalf("expected reasoning to be a map, got %T", reasoning)
	}
	if effort, ok := reasoningMap["effort"]; !ok || effort != "low" {
		t.Errorf("expected effort to be 'low', got %v", effort)
	}
}

func TestEncodeOutboundRequest_VerbosityRejection(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "hello"},
				},
			},
		},
		Options: lipapi.GenerationOptions{
			Verbosity: "verbose",
		},
	}
	_, err := EncodeOutboundRequest(call, OutboundEncodeOptions{
		Model: "gpt-4o",
	})
	if err == nil {
		t.Fatal("expected error when verbosity is set, got nil")
	}
}

func TestEncodeOutboundRequest_InvalidMIME(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "hello"},
				},
			},
		},
		Options: lipapi.GenerationOptions{
			ResponseMIMEType: "application/xml",
		},
	}
	_, err := EncodeOutboundRequest(call, OutboundEncodeOptions{
		Model: "gpt-4o",
	})
	if err == nil {
		t.Fatal("expected error when ResponseMIMEType is application/xml, got nil")
	}
}

func TestEncodeOutboundRequest_PromptCacheKeyConflict(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "hello"},
				},
			},
		},
		PromptCacheKey: "legacy-key",
		SemanticExtensions: []lipapi.SemanticExtension{
			{
				Namespace:   "lip",
				Type:        "prompt_cache_key",
				Implementor: "proxy",
				Direction:   "request",
				Presence:    lipapi.SemanticExtensionValue,
				Data:        json.RawMessage(`"semantic-key"`),
			},
		},
	}
	_, err := EncodeOutboundRequest(call, OutboundEncodeOptions{
		Model: "gpt-4o",
	})
	if err == nil {
		t.Fatal("expected error due to prompt cache key conflict, got nil")
	}
}

func TestEncodeOutboundRequest_PromptCacheKeyValid(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind: lipapi.ItemKindMessage,
				Role: lipapi.RoleUser,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "hello"},
				},
			},
		},
		PromptCacheKey: "valid-key",
	}
	body, err := EncodeOutboundRequest(call, OutboundEncodeOptions{
		Model: "gpt-4o",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if key, ok := got["prompt_cache_key"]; !ok || key != "valid-key" {
		t.Errorf("expected prompt_cache_key to be 'valid-key', got %v", key)
	}
}
