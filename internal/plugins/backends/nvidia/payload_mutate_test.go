package nvidia

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestRequestOptions_alwaysStripsStreamOptions(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{}
	opts := requestOptions(call)
	if len(opts) == 0 {
		t.Fatal("expected at least one option (stream_options strip)")
	}
}

func TestRequestOptions_addsMaxTokensWhenSet(t *testing.T) {
	t.Parallel()
	maxTokens := 1024
	call := lipapi.Call{
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxTokens},
	}
	opts := requestOptions(call)
	// stream_options del + max_tokens set + max_completion_tokens del = 3 opts
	if len(opts) < 3 {
		t.Fatalf("expected at least 3 options, got %d", len(opts))
	}
}

func TestRequestOptions_noMaxTokensWhenNil(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{}
	opts := requestOptions(call)
	// Only stream_options del
	if len(opts) != 1 {
		t.Fatalf("expected 1 option (stream_options strip only), got %d", len(opts))
	}
}

func TestRequestOptions_passesExtraBodyFields(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"nvidia.extra_body.chat_template_kwargs": json.RawMessage(`{"enable_thinking":true}`),
			"nvidia.extra_body.custom_field":         json.RawMessage(`"custom_value"`),
			"unrelated.key":                          json.RawMessage(`"ignored"`),
		},
	}
	opts := requestOptions(call)
	// stream_options del + 2 extra_body sets = 3
	if len(opts) != 3 {
		t.Fatalf("expected 3 options, got %d", len(opts))
	}
}

func TestRequestOptions_extraBodyExactPrefixNotInjected(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"nvidia.extra_body.": json.RawMessage(`"should-not-inject"`),
		},
	}
	opts := requestOptions(call)
	// Only stream_options del; "nvidia.extra_body." with empty field name is skipped
	if len(opts) != 1 {
		t.Fatalf("expected 1 option, got %d", len(opts))
	}
}

func TestRequestOptions_extraBodyUnsafeFieldNotInjected(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"nvidia.extra_body.safe_field":    json.RawMessage(`"ok"`),
			"nvidia.extra_body.unsafe.nested": json.RawMessage(`"ignored"`),
		},
	}
	opts := requestOptions(call)
	// stream_options del + safe_field set; dotted JSON-path-like field name is skipped.
	if len(opts) != 2 {
		t.Fatalf("expected 2 options, got %d", len(opts))
	}
}

func TestRequestOptions_combinedMaxTokensAndExtraBody(t *testing.T) {
	t.Parallel()
	maxTokens := 512
	call := lipapi.Call{
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxTokens},
		Extensions: map[string]json.RawMessage{
			"nvidia.extra_body.temperature_scale": json.RawMessage(`0.5`),
		},
	}
	opts := requestOptions(call)
	// stream_options del + max_tokens set + max_completion_tokens del + 1 extra_body = 4
	if len(opts) != 4 {
		t.Fatalf("expected 4 options, got %d", len(opts))
	}
}
