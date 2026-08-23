package backend_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	legacy "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	anthropic "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/anthropicmessages"
	gemini "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/geminigenerate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func visibilityProjectedCall(t *testing.T, fixedRole lipapi.Role, fixedText string) (lipapi.Call, string) {
	t.Helper()
	ctx := context.Background()
	store := conversationview.NewReferenceStore()
	aLeg := "vis-" + string(fixedRole) + "-" + fixedText
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	_, err := store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "steer-stable",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "STEER_STABLE"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.NoError(t, err)
	sys := lipapi.Message{Role: lipapi.RoleSystem, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "SYS_BASE"}}}
	u1 := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "U1"}}}
	anchorCall := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}
	snap0, err := store.Snapshot(ctx, aLeg)
	require.NoError(t, err)
	anchor, err := conversationview.ResolveAfterIngressTailAnchor(anchorCall, snap0)
	require.NoError(t, err)
	_, err = store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "steer-fixed",
		Message:             conversationview.StoredMessageV1{Role: fixedRole, Text: fixedText},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.NoError(t, err)
	snap, err := store.Snapshot(ctx, aLeg)
	require.NoError(t, err)
	a1 := lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "A1"}}}
	u2 := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "U2"}}}
	call := lipapi.Call{
		Instructions:   []lipapi.Message{sys},
		Messages:       []lipapi.Message{u1, a1, u2},
		PromptCacheKey: "cache-key-123",
	}
	proj, _, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.NoError(t, proj.Validate())
	assert.Equal(t, "cache-key-123", proj.PromptCacheKey)
	return proj, "cache-key-123"
}

func TestVisibilitySentinel_OpenAI_SystemMidPreserves(t *testing.T) {
	t.Parallel()
	proj, _ := visibilityProjectedCall(t, lipapi.RoleSystem, "STEER_FIXED_SYSTEM")
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai", Model: "gpt-4o-mini"}}
	params, err := legacy.ParamsForCall(&proj, cand)
	require.NoError(t, err)
	require.Len(t, params.Messages, 5)
	sysJSON, _ := params.Messages[0].MarshalJSON()
	assert.Contains(t, string(sysJSON), "SYS_BASE")
	assert.Contains(t, string(sysJSON), "STEER_STABLE")
	assert.True(t, strings.Index(string(sysJSON), "SYS_BASE") < strings.Index(string(sysJSON), "STEER_STABLE"))
	for i, exp := range []struct{ role, text string }{
		{"user", "U1"},
		{"system", "STEER_FIXED_SYSTEM"},
		{"assistant", "A1"},
		{"user", "U2"},
	} {
		b, _ := params.Messages[i+1].MarshalJSON()
		s := string(b)
		assert.Contains(t, s, exp.text)
		assert.Contains(t, s, exp.role)
	}
	b2, _ := params.Messages[2].MarshalJSON()
	assert.Contains(t, string(b2), "STEER_FIXED_SYSTEM")
	assert.Equal(t, "cache-key-123", proj.PromptCacheKey)
}

func TestVisibilitySentinel_Anthropic_Gemini_SystemMidRejects(t *testing.T) {
	t.Parallel()
	proj, _ := visibilityProjectedCall(t, lipapi.RoleSystem, "STEER_FIXED_SYSTEM")
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "any", Model: "sentinel-model"}}
	_, err := anthropic.ParamsForCall(&proj, cand)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "unsupported")
	assert.NotContains(t, err.Error(), "STEER_FIXED_SYSTEM")

	_, err = gemini.StreamParamsForCall(&proj, cand)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "unsupported")
	assert.NotContains(t, err.Error(), "STEER_FIXED_SYSTEM")

	found := false
	for _, m := range proj.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == "STEER_FIXED_SYSTEM" {
			found = true
			assert.Equal(t, lipapi.RoleSystem, m.Role)
		}
	}
	assert.True(t, found)
	// Stable prefix still maps via Instructions, not Messages mid lift
	sysCall := lipapi.Call{
		Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("SYS_BASE")}}, {Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("STEER_STABLE")}}},
		Messages:     []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("U1")}}},
	}
	cand2 := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic", Model: "claude-3-5-sonnet"}}
	p, err := anthropic.ParamsForCall(&sysCall, cand2)
	require.NoError(t, err)
	require.Len(t, p.System, 1)
	assert.Contains(t, p.System[0].Text, "SYS_BASE")
	assert.Contains(t, p.System[0].Text, "STEER_STABLE")
	sp, err := gemini.StreamParamsForCall(&sysCall, cand2)
	require.NoError(t, err)
	require.NotNil(t, sp.Config.SystemInstruction)
	assert.Contains(t, sp.Config.SystemInstruction.Parts[0].Text, "STEER_STABLE")
}

func TestVisibilitySentinel_UserFixedPreservesAllFamilies(t *testing.T) {
	t.Parallel()
	proj, _ := visibilityProjectedCall(t, lipapi.RoleUser, "STEER_FIXED_USER")
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "any", Model: "sentinel-model"}}
	// OpenAI
	pO, err := legacy.ParamsForCall(&proj, cand)
	require.NoError(t, err)
	require.Len(t, pO.Messages, 5)
	// Anthropic - user fixed merges with preceding U1 but preserves order
	pA, err := anthropic.ParamsForCall(&proj, cand)
	require.NoError(t, err)
	require.Len(t, pA.System, 1)
	assert.NotContains(t, pA.System[0].Text, "STEER_FIXED_USER")
	var flat []string
	for _, m := range pA.Messages {
		for _, blk := range m.Content {
			b, _ := blk.MarshalJSON()
			s := string(b)
			if strings.Contains(s, "U1") {
				flat = append(flat, "U1")
			}
			if strings.Contains(s, "STEER_FIXED_USER") {
				flat = append(flat, "STEER_FIXED_USER")
			}
			if strings.Contains(s, "A1") {
				flat = append(flat, "A1")
			}
			if strings.Contains(s, "U2") {
				flat = append(flat, "U2")
			}
		}
	}
	assert.Equal(t, []string{"U1", "STEER_FIXED_USER", "A1", "U2"}, flat)
	// Gemini
	pG, err := gemini.StreamParamsForCall(&proj, cand)
	require.NoError(t, err)
	require.Len(t, pG.Contents, 4)
	assert.Equal(t, "U1", pG.Contents[0].Parts[0].Text)
	assert.Equal(t, "STEER_FIXED_USER", pG.Contents[1].Parts[0].Text)
	assert.Equal(t, "A1", pG.Contents[2].Parts[0].Text)
	assert.Equal(t, "U2", pG.Contents[3].Parts[0].Text)
	assert.Equal(t, "cache-key-123", proj.PromptCacheKey)
}

func TestVisibilitySentinel_PromptCacheAndCacheControlsUnchanged(t *testing.T) {
	t.Parallel()
	proj, origKey := visibilityProjectedCall(t, lipapi.RoleUser, "STEER_FIXED_USER")
	assert.Equal(t, "cache-key-123", origKey)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "any", Model: "m"}}
	_, err := legacy.ParamsForCall(&proj, cand)
	require.NoError(t, err)
	assert.Equal(t, origKey, proj.PromptCacheKey)
	_, err = anthropic.ParamsForCall(&proj, cand)
	require.NoError(t, err)
	assert.Equal(t, origKey, proj.PromptCacheKey)
	_, err = gemini.StreamParamsForCall(&proj, cand)
	require.NoError(t, err)
	assert.Equal(t, origKey, proj.PromptCacheKey)
	pA, _ := anthropic.ParamsForCall(&proj, cand)
	for _, blk := range pA.System {
		assert.NotContains(t, strings.ToLower(blk.Text), "cache_control")
	}
	pG, _ := gemini.StreamParamsForCall(&proj, cand)
	if pG.Config.SystemInstruction != nil {
		assert.NotContains(t, strings.ToLower(pG.Config.SystemInstruction.Parts[0].Text), "cache")
	}
	// No frontend×backend matrix: sentinel uses canonical call directly, no frontend decode
}
