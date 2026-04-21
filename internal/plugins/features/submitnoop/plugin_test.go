package submitnoop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func testCall() *lipapi.Call {
	return &lipapi.Call{
		ID: "trace-1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Route: lipapi.RouteIntent{Selector: "stub:model"},
	}
}

func TestNewSubmitHook_contract(t *testing.T) {
	t.Parallel()
	h := NewSubmitHook()
	if h.ID() != ID {
		t.Fatalf("ID: got %q want %q", h.ID(), ID)
	}
	if h.Order() != DefaultHookOrder {
		t.Fatalf("Order: got %d want %d", h.Order(), DefaultHookOrder)
	}
	if h.FailureMode() != sdk.FailOpen {
		t.Fatalf("FailureMode: got %v want FailOpen", h.FailureMode())
	}
}

func TestNewSubmitHook_noMutation(t *testing.T) {
	t.Parallel()
	h := NewSubmitHook()
	call := testCall()
	before, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	meta := &sdk.SubmitMeta{Annotations: map[string]string{"k": "v"}}
	dec, err := h.Handle(context.Background(), call, meta)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Reject || dec.Reason != "" {
		t.Fatalf("unexpected decision: %+v", dec)
	}
	after, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("call mutated:\nbefore %s\nafter %s", before, after)
	}
}
