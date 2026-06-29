package geminigenerate

import (
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestModelCapabilities(t *testing.T) {
	t.Parallel()
	// Construct expected capabilities for gemini-1.0-pro (no vision/documents)
	expected10ProCaps := lipapi.NewBackendCaps()
	for c := range defaultBackendCaps() {
		if c == lipapi.CapabilityVision || c == lipapi.CapabilityDocuments {
			continue
		}
		expected10ProCaps[c] = struct{}{}
	}

	tests := []struct {
		name     string
		model    string
		expected lipapi.BackendCaps
	}{
		{
			name:     "empty string",
			model:    "",
			expected: defaultBackendCaps(),
		},
		{
			name:     "whitespace only",
			model:    "   ",
			expected: defaultBackendCaps(),
		},
		{
			name:     "gemini-1.5-pro",
			model:    "gemini-1.5-pro",
			expected: defaultBackendCaps(),
		},
		{
			name:     "gemini-1.0-pro (no vision/documents)",
			model:    "gemini-1.0-pro",
			expected: expected10ProCaps,
		},
		{
			name:     "gemini-1.0-pro-vision",
			model:    "gemini-1.0-pro-vision",
			expected: defaultBackendCaps(),
		},
		{
			name:     "uppercase gemini-1.0-pro",
			model:    "GEMINI-1.0-PRO",
			expected: expected10ProCaps,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ModelCapabilities(tt.model)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("ModelCapabilities() = %v, want %v", got, tt.expected)
			}
		})
	}
}
