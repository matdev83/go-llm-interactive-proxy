package refparts

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func TestRequestHook_appendsSuffixToFirstUserText(t *testing.T) {
	t.Parallel()
	h := NewRequestHook(Config{Suffix: " [x]"})
	call := &lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}}}
	if err := h.HandleRequestParts(context.Background(), call, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
	if got := call.Messages[0].Parts[0].Text; got != "hi [x]" {
		t.Fatalf("text %q", got)
	}
}

func TestResponseHook_prefixesTextDelta(t *testing.T) {
	t.Parallel()
	h := NewResponseHook(Config{ResponsePrefix: "P:"})
	ev := &lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "out"}
	if err := h.HandleEvent(context.Background(), ev, sdk.PartMeta{}); err != nil {
		t.Fatal(err)
	}
	if ev.Delta != "P:out" {
		t.Fatalf("delta %q", ev.Delta)
	}
}
