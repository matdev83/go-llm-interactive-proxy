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
