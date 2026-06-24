package routeselect

import (
	"testing"
)

func TestInlineOrDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		model        string
		defaultRoute string
		want         string
	}{
		{
			name:         "known prefix with model",
			model:        "anthropic:claude-3-5-sonnet",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "anthropic:claude-3-5-sonnet",
		},
		{
			name:         "ollama cloud prefix with canonical model",
			model:        "ollama-cloud:google/gemma3:4b",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "ollama-cloud:google/gemma3:4b",
		},
		{
			name:         "llamacpp prefix with local model",
			model:        "llamacpp:local-model",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "llamacpp:local-model",
		},
		{
			name:         "unknown prefix",
			model:        "xyz:claude-3-5-sonnet",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "openai-legacy:gpt-4o",
		},
		{
			name:         "test stub prefix",
			model:        "stub:claude-3-5-sonnet",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "stub:claude-3-5-sonnet",
		},
		{
			name:         "no prefix",
			model:        "claude-3-5-sonnet",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "openai-legacy:gpt-4o",
		},
		{
			name:         "empty model",
			model:        "",
			defaultRoute: "openai-legacy:gpt-4o",
			want:         "openai-legacy:gpt-4o",
		},
		{
			name:         "whitespace trimmed",
			model:        "  anthropic:claude-3-5-sonnet  ",
			defaultRoute: "  openai-legacy:gpt-4o  ",
			want:         "anthropic:claude-3-5-sonnet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := InlineOrDefault(tt.model, tt.defaultRoute)
			if got != tt.want {
				t.Errorf("InlineOrDefault(%q, %q) = %q, want %q", tt.model, tt.defaultRoute, got, tt.want)
			}
		})
	}
}

func TestFromModelOrDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		defaultRoute string
		want         string
	}{
		{
			name:         "valid json with known inline model",
			body:         `{"model": "anthropic:claude-3-5"}`,
			defaultRoute: "openai-responses:gpt-4o",
			want:         "anthropic:claude-3-5",
		},
		{
			name:         "valid json with unknown inline model",
			body:         `{"model": "foo:bar"}`,
			defaultRoute: "openai-responses:gpt-4o",
			want:         "openai-responses:gpt-4o",
		},
		{
			name:         "valid json with no model",
			body:         `{"messages": []}`,
			defaultRoute: "openai-responses:gpt-4o",
			want:         "openai-responses:gpt-4o",
		},
		{
			name:         "invalid json",
			body:         `{invalid`,
			defaultRoute: "openai-responses:gpt-4o",
			want:         "openai-responses:gpt-4o",
		},
		{
			name:         "empty body",
			body:         "",
			defaultRoute: "openai-responses:gpt-4o",
			want:         "openai-responses:gpt-4o",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := FromModelOrDefault([]byte(tt.body), tt.defaultRoute)
			if got != tt.want {
				t.Errorf("FromModelOrDefault(%q, %q) = %q, want %q", tt.body, tt.defaultRoute, got, tt.want)
			}
		})
	}
}
