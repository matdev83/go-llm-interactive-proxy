package adapter_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// A canonical json part routed to a connector must reach the plugin
// invocation with its payload intact, never be silently dropped.
func TestInvocationFromCall_JSONPartMapped(t *testing.T) {
	t.Parallel()
	call := testCall()
	call.Messages[0].Parts = append(call.Messages[0].Parts, lipapi.Part{
		Kind:    lipapi.PartJSON,
		Content: json.RawMessage(`{"key":"value"}`),
	})
	inv, err := adapter.InvocationFromCall(call, testCand())
	if err != nil {
		t.Fatal(err)
	}
	parts := inv.Messages[0].Parts
	if len(parts) != 2 {
		t.Fatalf("json part silently dropped: mapped parts=%d, want 2", len(parts))
	}
	if parts[1].Kind != backendplugin.PartKindJSON {
		t.Fatalf("kind=%q, want %q", parts[1].Kind, backendplugin.PartKindJSON)
	}
	if got := string(parts[1].ToolArgsJSON.Bytes()); got != `{"key":"value"}` {
		t.Fatalf("json payload=%q, want %q", got, `{"key":"value"}`)
	}
}

// Golden round trip: canonical -> ABI DTO -> proto wire -> ABI DTO -> canonical
// must preserve the json part and its payload exactly.
func TestInvocationFromCall_JSONPartGoldenRoundTrip(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{"schema":{"type":"object","properties":{"a":{"type":"string"}}},"nested":[1,2,3]}`)
	call := testCall()
	call.Messages[0].Parts = append(call.Messages[0].Parts, lipapi.Part{
		Kind:    lipapi.PartJSON,
		Content: payload,
	})
	inv, err := adapter.InvocationFromCall(call, testCand())
	if err != nil {
		t.Fatalf("canonical->ABI: %v", err)
	}
	wire, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatalf("ABI->proto: %v", err)
	}
	back, err := backendplugin.InvocationFromProto(wire)
	if err != nil {
		t.Fatalf("proto->ABI: %v", err)
	}
	out, err := backendplugin.CallFromInvocation(back)
	if err != nil {
		t.Fatalf("ABI->canonical: %v", err)
	}
	parts := out.Messages[0].Parts
	if len(parts) != 2 {
		t.Fatalf("round-trip parts=%d, want 2", len(parts))
	}
	if parts[1].Kind != lipapi.PartJSON {
		t.Fatalf("round-trip kind=%q, want %q", parts[1].Kind, lipapi.PartJSON)
	}
	if string(parts[1].Content) != string(payload) {
		t.Fatalf("round-trip payload=%q, want %q", parts[1].Content, payload)
	}
}

// Empty canonical json parts must fail host-side mapping, matching reverse bridge rules.
func TestInvocationFromCall_JSONPartEmptyContentFails(t *testing.T) {
	t.Parallel()
	call := testCall()
	call.Messages[0].Parts = append(call.Messages[0].Parts, lipapi.Part{
		Kind: lipapi.PartJSON,
	})
	_, err := adapter.InvocationFromCall(call, testCand())
	if err == nil {
		t.Fatal("expected error for empty json part content")
	}
	if !errors.Is(err, backendplugin.ErrInvalidInvocation) {
		t.Fatalf("errors.Is(err, ErrInvalidInvocation)=false: %v", err)
	}
	if !strings.Contains(err.Error(), "json part requires content") {
		t.Fatalf("error=%q", err.Error())
	}
}

// A canonical part kind with no ABI mapping must fail explicitly
// instead of being silently dropped from the plugin invocation.
func TestInvocationFromCall_UnsupportedPartKindFailsClosed(t *testing.T) {
	t.Parallel()
	call := testCall()
	call.Messages[0].Parts = append(call.Messages[0].Parts, lipapi.Part{Kind: lipapi.PartKind("bogus")})
	_, err := adapter.InvocationFromCall(call, testCand())
	if err == nil {
		t.Fatal("expected explicit error for unmappable part kind, got nil (part silently dropped)")
	}
	if !errors.Is(err, backendplugin.ErrUnsupportedPartKind) {
		t.Fatalf("errors.Is(err, ErrUnsupportedPartKind)=false: %v", err)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error must name the offending kind, got %q", err.Error())
	}
}
