package controltool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type testProvider struct{ spec Spec }
func (p testProvider) ID() string { return "test-control" }
func (p testProvider) Spec() Spec { return p.spec }
func (p testProvider) Handle(context.Context, CompletedCall, Meta) (Outcome, error) {
	return Outcome{Kind: OutcomeComplete, ResultText: "done", ReasonCode: "ok"}, nil
}

func testSpec() Spec {
	return Spec{
		Tool: lipapi.ToolDef{Name:"proxy_control", Description:"control", Parameters:json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		Instruction: Instruction{Role:lipapi.RoleSystem, Text:"control instruction"},
		MaxArgsBytes: DefaultMaxArgsBytes,
	}
}

func TestProjectEligibilityAndReassert(t *testing.T) {
	base := lipapi.Call{
		Messages: []lipapi.Message{{Role:lipapi.RoleUser, Parts:[]lipapi.Part{{Kind:lipapi.PartText, Text:"hi"}}}},
		ToolChoice: lipapi.ToolChoice{Mode:lipapi.ToolChoiceAuto},
	}
	p := testProvider{spec:testSpec()}
	got, projection, err := Project(base, lipapi.NewBackendCaps(lipapi.CapabilityTools), p)
	if err != nil { t.Fatal(err) }
	if !projection.Active || len(got.Tools)!=1 || len(got.Instructions)!=1 { t.Fatalf("projection=%+v call=%+v", projection, got) }
	if len(base.Tools)!=0 || len(base.Instructions)!=0 { t.Fatal("baseline mutated") }
	reasserted, err := Reassert(got, projection)
	if err != nil { t.Fatal(err) }
	if len(reasserted.Tools)!=1 || len(reasserted.Instructions)!=1 { t.Fatal("reassert duplicated projection") }

	for _, tc := range []struct{name string; call lipapi.Call; caps lipapi.BackendCaps; reason string}{
		{"no tools", base, lipapi.NewBackendCaps(), ReasonBackendToolsUnsupported},
		{"none", func() lipapi.Call { c:=base; c.ToolChoice.Mode=lipapi.ToolChoiceNone; return c }(), lipapi.NewBackendCaps(lipapi.CapabilityTools), ReasonToolChoiceNone},
		{"any", func() lipapi.Call { c:=base; c.ToolChoice.Mode=lipapi.ToolChoiceAny; return c }(), lipapi.NewBackendCaps(lipapi.CapabilityTools), ReasonToolChoiceConstrained},
		{"required", func() lipapi.Call { c:=base; c.ToolChoice=lipapi.ToolChoice{Mode:lipapi.ToolChoiceRequired,Name:"client"}; c.Tools=[]lipapi.ToolDef{{Name:"client",Parameters:json.RawMessage(`{"type":"object"}`)}}; return c }(), lipapi.NewBackendCaps(lipapi.CapabilityTools), ReasonToolChoiceRequired},
		{"allowed", func() lipapi.Call { c:=base; c.Tools=[]lipapi.ToolDef{{Name:"client",Parameters:json.RawMessage(`{"type":"object"}`)}}; c.ToolChoice.AllowedTools=[]string{"client"}; return c }(), lipapi.NewBackendCaps(lipapi.CapabilityTools), ReasonAllowedToolsConstrained},
		{"collision", func() lipapi.Call { c:=base; c.Tools=[]lipapi.ToolDef{testSpec().Tool}; return c }(), lipapi.NewBackendCaps(lipapi.CapabilityTools), ReasonToolNameCollision},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, a, err := Project(tc.call, tc.caps, p)
			if err != nil { t.Fatal(err) }
			if a.Active || a.ReasonCode != tc.reason { t.Fatalf("active=%v reason=%q want=%q", a.Active, a.ReasonCode, tc.reason) }
		})
	}
}

func TestProjectItemAuthority(t *testing.T) {
	base := lipapi.Call{Items:[]lipapi.Item{{Kind:lipapi.ItemKindMessage,Status:lipapi.ItemStatusCompleted,Role:lipapi.RoleUser,Content:[]lipapi.ContentPart{{Kind:lipapi.ContentPartText,Text:"hi"}}}}}
	got, a, err := Project(base, lipapi.NewBackendCaps(lipapi.CapabilityTools), testProvider{spec:testSpec()})
	if err != nil { t.Fatal(err) }
	if !a.Active || len(got.Items)!=2 || got.Items[0].Role!=lipapi.RoleSystem || got.Items[0].Content[0].Text!="control instruction" { t.Fatalf("got=%+v", got.Items) }
}
