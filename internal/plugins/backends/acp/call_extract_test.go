package acp

import (
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestCallRouteModel_selectorWithColon(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "cursorcliacp:claude-3.5-sonnet"}}
	if got := CallRouteModel(call, "acp.model"); got != "claude-3.5-sonnet" {
		t.Fatalf("got %q, want claude-3.5-sonnet", got)
	}
}

func TestCallRouteModel_selectorWithoutColon(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{Route: lipapi.RouteIntent{Selector: "claude-3.5-sonnet"}}
	if got := CallRouteModel(call, "acp.model"); got != "claude-3.5-sonnet" {
		t.Fatalf("got %q, want claude-3.5-sonnet", got)
	}
}

func TestCallRouteModel_extensionFallback(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"codex.model": json.RawMessage(`"gpt-5.4"`),
		},
	}
	if got := CallRouteModel(call, "codex.model"); got != "gpt-5.4" {
		t.Fatalf("got %q, want gpt-5.4", got)
	}
}

func TestCallRouteModel_extensionKeyIsolation(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"acp.model": json.RawMessage(`"acp-model"`),
		},
	}
	// Asking for the codex key must not pick up the acp key.
	if got := CallRouteModel(call, "codex.model"); got != "" {
		t.Fatalf("got %q, want empty (key isolation)", got)
	}
}

func TestCallRouteModel_selectorBeatsExtension(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "backend:sel-model"},
		Extensions: map[string]json.RawMessage{
			"acp.model": json.RawMessage(`"ext-model"`),
		},
	}
	if got := CallRouteModel(call, "acp.model"); got != "sel-model" {
		t.Fatalf("selector precedence: got %q, want sel-model", got)
	}
}

func TestCallRouteModel_nilCall(t *testing.T) {
	t.Parallel()
	if got := CallRouteModel(nil, "acp.model"); got != "" {
		t.Fatalf("nil call: got %q, want empty", got)
	}
}

func TestCallRouteModel_emptySelectorFallsToExtension(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "   "},
		Extensions: map[string]json.RawMessage{"acp.model": json.RawMessage(`"m"`)},
	}
	if got := CallRouteModel(call, "acp.model"); got != "m" {
		t.Fatalf("whitespace selector should fall to extension: got %q", got)
	}
}

func TestCallClientSession_canonicalField(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{Session: lipapi.SessionRef{ClientSessionID: "sess-1"}}
	if got := CallClientSession(call); got != "sess-1" {
		t.Fatalf("got %q, want sess-1", got)
	}
}

func TestCallClientSession_extensionFallback(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"session.id": json.RawMessage(`"ext-sess"`),
		},
	}
	if got := CallClientSession(call); got != "ext-sess" {
		t.Fatalf("got %q, want ext-sess", got)
	}
}

func TestCallClientSession_canonicalBeatsExtension(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Session:    lipapi.SessionRef{ClientSessionID: "canonical"},
		Extensions: map[string]json.RawMessage{"session.id": json.RawMessage(`"ext"`)},
	}
	if got := CallClientSession(call); got != "canonical" {
		t.Fatalf("canonical precedence: got %q, want canonical", got)
	}
}

func TestCallClientSession_default(t *testing.T) {
	t.Parallel()
	if got := CallClientSession(&lipapi.Call{}); got != "default" {
		t.Fatalf("got %q, want default", got)
	}
	if got := CallClientSession(nil); got != "default" {
		t.Fatalf("nil call: got %q, want default", got)
	}
}

func TestCallClientSession_emptyExtensionFallsToDefault(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Extensions: map[string]json.RawMessage{"session.id": json.RawMessage(`"   "`)},
	}
	if got := CallClientSession(call); got != "default" {
		t.Fatalf("whitespace session.id should fall to default: got %q", got)
	}
}
