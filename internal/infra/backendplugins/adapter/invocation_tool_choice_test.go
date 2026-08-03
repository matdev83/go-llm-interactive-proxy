package adapter_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/adapter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

func TestInvocationFromCall_mapsToolChoiceBothDirections(t *testing.T) {
	t.Parallel()

	call := lipapi.Call{
		ID:      "req-tc",
		Session: lipapi.SessionRef{ALegID: "aleg"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools:      []lipapi.ToolDef{{Name: "fn", Parameters: []byte(`{"type":"object"}`)}},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "fn"},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "m"},
		Key:     "fake:m",
	}
	neg := backendplugin.Negotiation{Compatible: true, NegotiatedMinor: 0}

	inv, err := adapter.InvocationFromCall(call, cand, neg)
	if err != nil {
		t.Fatal(err)
	}
	if inv.ToolChoice == nil || *inv.ToolChoice != "required:fn" {
		t.Fatalf("inv tool_choice=%v", inv.ToolChoice)
	}

	back, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatal(err)
	}
	if back.ToolChoice.Mode != lipapi.ToolChoiceRequired || back.ToolChoice.Name != "fn" {
		t.Fatalf("back tool_choice=%+v", back.ToolChoice)
	}
}

func TestInvocationFromCall_rejectsNamedNonRequiredToolChoiceBeforeExecute(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		tc    lipapi.ToolChoice
		tools []lipapi.ToolDef
	}{
		{name: "auto named", tc: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto, Name: "fn"}, tools: []lipapi.ToolDef{{Name: "fn", Parameters: []byte(`{"type":"object"}`)}}},
		{name: "any named", tc: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny, Name: "fn"}, tools: []lipapi.ToolDef{{Name: "fn", Parameters: []byte(`{"type":"object"}`)}}},
		{name: "none named", tc: lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone, Name: "fn"}, tools: nil},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "m"},
		Key:     "fake:m",
	}
	neg := backendplugin.Negotiation{Compatible: true, NegotiatedMinor: 0}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := lipapi.Call{
				ID:      "req-tc-bad",
				Session: lipapi.SessionRef{ALegID: "aleg"},
				Messages: []lipapi.Message{{
					Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
				}},
				Tools:      tc.tools,
				ToolChoice: tc.tc,
			}
			_, err := adapter.InvocationFromCall(call, cand, neg)
			if err == nil {
				t.Fatal("expected InvocationFromCall rejection before plugin Execute")
			}
		})
	}
}
