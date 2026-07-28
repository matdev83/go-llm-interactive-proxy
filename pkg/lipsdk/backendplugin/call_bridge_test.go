package backendplugin_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func invocationWithParts(parts ...backendplugin.Part) backendplugin.Invocation {
	return backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: "m",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: parts,
		}},
	}
}

// A plugin-side part kind with no canonical mapping must fail explicitly
// instead of being silently dropped from the canonical call.
func TestCallFromInvocation_UnsupportedPartKindFailsClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		kind backendplugin.PartKind
	}{
		{name: "tool_call has no canonical mapping", kind: backendplugin.PartKindToolCall},
		{name: "unknown kind", kind: backendplugin.PartKind("bogus")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := backendplugin.CallFromInvocation(invocationWithParts(backendplugin.Part{Kind: tc.kind}))
			if err == nil {
				t.Fatalf("kind %q: expected explicit error, got nil (part silently dropped)", tc.kind)
			}
			if !errors.Is(err, backendplugin.ErrUnsupportedPartKind) {
				t.Fatalf("kind %q: errors.Is(err, ErrUnsupportedPartKind)=false: %v", tc.kind, err)
			}
			if !strings.Contains(err.Error(), string(tc.kind)) {
				t.Fatalf("kind %q: error must name the offending kind, got %q", tc.kind, err.Error())
			}
		})
	}
}

// A plugin-side json part must map to a canonical json part with payload intact.
func TestCallFromInvocation_JSONPartMapped(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"key":"value"}`)
	inv := invocationWithParts(backendplugin.Part{
		Kind:         backendplugin.PartKindJSON,
		ToolArgsJSON: backendplugin.RawJSONFromBytes(payload),
	})
	call, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatal(err)
	}
	parts := call.Messages[0].Parts
	if len(parts) != 1 || parts[0].Kind != lipapi.PartJSON {
		t.Fatalf("parts=%+v, want one json part", parts)
	}
	if string(parts[0].Content) != string(payload) {
		t.Fatalf("content=%q, want %q", parts[0].Content, payload)
	}
}

// A json part without payload must fail closed instead of producing an
// invalid canonical part.
func TestCallFromInvocation_JSONPartRequiresContent(t *testing.T) {
	t.Parallel()
	_, err := backendplugin.CallFromInvocation(invocationWithParts(backendplugin.Part{Kind: backendplugin.PartKindJSON}))
	if err == nil {
		t.Fatal("expected explicit error for json part without content, got nil")
	}
	if errors.Is(err, backendplugin.ErrUnsupportedPartKind) {
		t.Fatalf("json kind is supported; want invalid-invocation error, got %v", err)
	}
}
