package reftoolpolicy_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftoolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
)

func TestToolCatalogFilter_dropsBlocked(t *testing.T) {
	t.Parallel()
	f := reftoolpolicy.NewToolCatalogFilter(reftoolpolicy.Config{
		BlockNames:    []string{"gone"},
		BlockPrefixes: nil,
	})
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "x"}},
		}},
		Tools: []lipapi.ToolDef{
			{Name: "ok"},
			{Name: "gone"},
			{Name: "ok2"},
		},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto},
	}
	if err := extensions.RunToolCatalogFilterStage(context.Background(), nil, nil, []toolcatalog.Filter{f}, &call, toolcatalog.CatalogMeta{}, toolcatalog.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if len(call.Tools) != 2 {
		t.Fatalf("tools %#v", call.Tools)
	}
	if call.Tools[0].Name != "ok" || call.Tools[1].Name != "ok2" {
		t.Fatalf("tools %#v", call.Tools)
	}
}

func TestToolReactor_swallowsBlockedName(t *testing.T) {
	t.Parallel()
	r := reftoolpolicy.NewToolReactor(reftoolpolicy.Config{
		BlockNames:    []string{"blockme"},
		BlockPrefixes: nil,
	})
	if r.ID() != reftoolpolicy.ID+"-reactor" {
		t.Fatalf("id %q", r.ID())
	}
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolName: "blockme", ToolCallID: "c1", ArgsDelta: "{}"}
	dec, out, err := r.HandleToolEvent(context.Background(), te, sdk.ToolMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolSwallow || (out != lipapi.ToolEvent{}) {
		t.Fatalf("dec %v out %#v", dec, out)
	}
}

func TestToolReactor_passesUnblocked(t *testing.T) {
	t.Parallel()
	r := reftoolpolicy.NewToolReactor(reftoolpolicy.Config{BlockNames: []string{"blockme"}})
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolName: "ok", ToolCallID: "c1"}
	dec, out, err := r.HandleToolEvent(context.Background(), te, sdk.ToolMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolPass || (out != lipapi.ToolEvent{}) {
		t.Fatalf("dec %v", dec)
	}
}

func TestToolReactor_blockByPrefix(t *testing.T) {
	t.Parallel()
	r := reftoolpolicy.NewToolReactor(reftoolpolicy.Config{BlockPrefixes: []string{"ref_blocked_"}})
	te := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolName: "ref_blocked_x", ToolCallID: "1"}
	dec, out, err := r.HandleToolEvent(context.Background(), te, sdk.ToolMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != sdk.ToolSwallow || (out != lipapi.ToolEvent{}) {
		t.Fatalf("dec %v", dec)
	}
}
