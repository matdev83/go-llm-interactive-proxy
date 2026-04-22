package testkit

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// MustLIPCall returns v as lipapi.Call or fails the test.
func MustLIPCall(t *testing.T, v any) lipapi.Call {
	t.Helper()
	c, ok := v.(lipapi.Call)
	if !ok {
		t.Fatalf("expected lipapi.Call, got %T", v)
	}
	return c
}

// MustMapStringAny returns v as map[string]any or fails the test.
func MustMapStringAny(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", v)
	}
	return m
}

// MustSliceAny returns v as []any or fails the test.
func MustSliceAny(t *testing.T, v any) []any {
	t.Helper()
	s, ok := v.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", v)
	}
	return s
}
