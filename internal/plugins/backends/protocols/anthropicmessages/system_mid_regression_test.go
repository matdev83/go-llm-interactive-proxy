package anthropicmessages_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestSystemInMessages_ExplicitlyRejected(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("stable instruction")}}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("U1")}},
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("mid-system")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("A1")}},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic", Model: "claude-3-5-sonnet"}}
	_, err := anthropicmessages.ParamsForCall(&call, cand)
	if err == nil {
		t.Fatal("expected error for mid-conversation system, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unsupported") {
		t.Fatalf("expected unsupported error, got %v", err)
	}
	if strings.Contains(err.Error(), "mid-system") {
		t.Fatalf("error must not leak plaintext: %v", err)
	}
}

func TestSystemInInstructions_StillMapsToSystemBlocks(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Instructions: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("stable instruction")}},
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("steering stable")}},
		},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("U1")}}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic", Model: "claude-3-5-sonnet"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.System) != 1 {
		t.Fatalf("system blocks len %d", len(p.System))
	}
	if !strings.Contains(p.System[0].Text, "stable instruction") || !strings.Contains(p.System[0].Text, "steering stable") {
		t.Fatalf("system missing expected texts: %q", p.System[0].Text)
	}
	if len(p.Messages) != 1 {
		t.Fatalf("messages len %d", len(p.Messages))
	}
}
