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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
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

func TestToolCallPolicy_deniesBlockedEmittedTool(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockNames: []string{"blocked"},
	})
	if p.ID() != reftoolpolicy.ID+"-tool-policy" {
		t.Fatalf("id %q", p.ID())
	}
	ev := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolName: "blocked", ToolCallID: "tc1"}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Policies: toolpolicy.MaterializeSorted([]toolpolicy.Policy{p}),
		Event:    ev,
	})
	if err == nil {
		t.Fatal("want deny error")
	}
}

func TestToolCallPolicy_allowsUnblockedTool(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{BlockNames: []string{"blocked"}})
	ev := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolName: "ok", ToolCallID: "tc1"}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Policies: toolpolicy.MaterializeSorted([]toolpolicy.Policy{p}),
		Event:    ev,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestToolCallPolicy_emptyBlocksIsNoOp(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockNames:    []string{},
		BlockPrefixes: []string{},
	})
	ev := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolName: "anything", ToolCallID: "tc1"}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      context.Background(),
		Policies: toolpolicy.MaterializeSorted([]toolpolicy.Policy{p}),
		Event:    ev,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestToolCallPolicy_Handle_DenyBlocked(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockNames: []string{"blocked"},
	})
	ev := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolName: "blocked", ToolCallID: "tc1"}
	dec, err := p.Handle(context.Background(), ev, toolpolicy.Meta{}, toolpolicy.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != toolpolicy.DecisionDeny {
		t.Fatalf("want DecisionDeny, got %v", dec)
	}
}

func TestToolCallPolicy_Handle_AllowUnblocked(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockNames: []string{"blocked"},
	})
	ev := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolName: "ok", ToolCallID: "tc1"}
	dec, err := p.Handle(context.Background(), ev, toolpolicy.Meta{}, toolpolicy.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != toolpolicy.DecisionAllow {
		t.Fatalf("want DecisionAllow, got %v", dec)
	}
}

func TestToolCallPolicy_Handle_AllowEmptyToolName(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockNames: []string{"blocked"},
	})
	// ToolName is empty, which can happen for argument delta events.
	ev := lipapi.ToolEvent{Kind: lipapi.ToolEventArgsDelta, ToolName: "", ToolCallID: "tc1", ArgsDelta: "{}"}
	dec, err := p.Handle(context.Background(), ev, toolpolicy.Meta{}, toolpolicy.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != toolpolicy.DecisionAllow {
		t.Fatalf("want DecisionAllow, got %v", dec)
	}
}

func TestToolCallPolicy_Handle_DenyBlockedPrefix(t *testing.T) {
	t.Parallel()
	p := reftoolpolicy.NewToolCallPolicy(reftoolpolicy.Config{
		BlockPrefixes: []string{"bad_"},
	})
	ev := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolName: "bad_tool", ToolCallID: "tc1"}
	dec, err := p.Handle(context.Background(), ev, toolpolicy.Meta{}, toolpolicy.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if dec != toolpolicy.DecisionDeny {
		t.Fatalf("want DecisionDeny, got %v", dec)
	}
}
