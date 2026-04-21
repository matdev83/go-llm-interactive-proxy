package toolreactornoop

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func TestNewToolReactor_contract(t *testing.T) {
	t.Parallel()
	r := NewToolReactor()
	if r.ID() != ID {
		t.Fatalf("ID: got %q want %q", r.ID(), ID)
	}
	if r.Order() != hookOrder {
		t.Fatalf("Order: got %d want %d", r.Order(), hookOrder)
	}
}

func TestNewToolReactor_passThrough(t *testing.T) {
	t.Parallel()
	r := NewToolReactor()
	in := lipapi.ToolEvent{
		Kind:       lipapi.ToolEventStarted,
		ToolCallID: "call-1",
		ToolName:   "fn",
	}
	meta := sdk.ToolMeta{TraceID: "t", ALegID: "a", BLegID: "b", AttemptSeq: 2}
	dec, out, err := r.HandleToolEvent(context.Background(), in, meta)
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolPass {
		t.Fatalf("decision: got %v want ToolPass", dec)
	}
	if out != in {
		t.Fatalf("event replaced: got %+v want %+v", out, in)
	}
}
