package reftool

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

func TestReactor_prefixesArgsDelta(t *testing.T) {
	t.Parallel()
	r := NewToolReactor(Config{Prefix: ">>"})
	dec, out, err := r.HandleToolEvent(context.Background(), lipapi.ToolEvent{
		Kind:       lipapi.ToolEventArgsDelta,
		ToolCallID: "c1",
		ArgsDelta:  `{"a":1}`,
	}, sdk.ToolMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolRewrite {
		t.Fatalf("decision %v", dec)
	}
	if out.ArgsDelta != `>>{"a":1}` {
		t.Fatalf("args %q", out.ArgsDelta)
	}
}

func TestReactor_passThroughNonDeltaKinds(t *testing.T) {
	t.Parallel()
	r := NewToolReactor(Config{Prefix: ">>"})
	dec, out, err := r.HandleToolEvent(context.Background(), lipapi.ToolEvent{
		Kind: lipapi.ToolEventStarted,
	}, sdk.ToolMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolPass {
		t.Fatalf("decision %v", dec)
	}
	if out.ArgsDelta != "" {
		t.Fatalf("unexpected out %#v", out)
	}
}
