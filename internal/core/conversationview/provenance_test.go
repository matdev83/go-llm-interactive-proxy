package conversationview_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestProvenance_DeterministicAndNonIntrusive(t *testing.T) {
	t.Parallel()
	sys := textMessage(lipapi.RoleSystem, "sys")
	u1 := textMessage(lipapi.RoleUser, "hello user")
	anchorID, err := conversationview.MessageIdentityOf(u1)
	require.NoError(t, err)
	anchor := conversationview.MessageAnchor{Identity: anchorID, Occurrence: 1}
	stable := conversationview.SteeringOverlay{
		OverlayID:           "ov-stable",
		Revision:            3,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "steering stable"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	}
	fixed := conversationview.SteeringOverlay{
		OverlayID:           "ov-fixed",
		Revision:            5,
		SlotOrdinal:         2,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "steering fixed"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              "r",
	}
	snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{stable, fixed})
	call := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, textMessage(lipapi.RoleAssistant, "reply")}}

	proj1, ev1, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.NotNil(t, ev1)
	proj2, ev2, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.NotNil(t, ev2)

	// Provenance must be present and deterministic across identical invocations.
	require.NotNil(t, ev1.Provenance, "provenance must be populated")
	require.NotNil(t, ev2.Provenance, "provenance must be populated")
	assert.Equal(t, ev1.Provenance, ev2.Provenance, "provenance must be deterministic")
	assert.Len(t, ev1.Provenance, 2)

	// Provenance entries contain required fields.
	for _, p := range ev1.Provenance {
		assert.NotEmpty(t, p.OverlayID)
		assert.NotZero(t, p.Revision)
		assert.NotZero(t, p.SlotOrdinal)
		assert.NotEmpty(t, p.ResolvedKind)
		assert.NotEmpty(t, p.InjectedIdentity)
		assert.True(t, p.InjectedIdentity.IsValid())
	}
	// Check specific entries by OverlayID
	var stableProv, fixedProv *conversationview.OverlayProvenance
	for i := range ev1.Provenance {
		switch ev1.Provenance[i].OverlayID {
		case "ov-stable":
			stableProv = &ev1.Provenance[i]
		case "ov-fixed":
			fixedProv = &ev1.Provenance[i]
		}
	}
	require.NotNil(t, stableProv)
	require.NotNil(t, fixedProv)
	assert.Equal(t, uint64(3), stableProv.Revision)
	assert.Equal(t, uint64(1), stableProv.SlotOrdinal)
	assert.Equal(t, conversationview.PlacementStablePrefix, stableProv.ResolvedKind)
	assert.Nil(t, stableProv.ResolvedAnchor)
	assert.Equal(t, uint64(5), fixedProv.Revision)
	assert.Equal(t, conversationview.PlacementAfterMessage, fixedProv.ResolvedKind)
	require.NotNil(t, fixedProv.ResolvedAnchor)
	assert.Equal(t, anchor, *fixedProv.ResolvedAnchor)

	// Provenance must NOT alter identities or model-visible content.
	// Identities of projected messages with provenance must equal identities without provenance conceptually.
	// Verify injected identities match actual projected message identities.
	foundStable := false
	foundFixed := false
	for _, m := range proj1.Instructions {
		id, _ := conversationview.MessageIdentityOf(m)
		if id == stableProv.InjectedIdentity {
			foundStable = true
		}
	}
	for _, m := range proj1.Messages {
		id, _ := conversationview.MessageIdentityOf(m)
		if id == fixedProv.InjectedIdentity {
			foundFixed = true
		}
	}
	// For legacy, stable is in Instructions, fixed in Messages
	assert.True(t, foundStable, "stable provenance identity must correspond to actual projected instruction")
	assert.True(t, foundFixed, "fixed provenance identity must correspond to actual projected message")

	// Also verify second projection identical trajectory
	assert.Equal(t, normalizedTrajectory(proj1), normalizedTrajectory(proj2))

	// Helper recognition must work by semantic identity
	require.NotNil(t, proj1)
}

func TestProvenance_DoesNotAlterIdentities(t *testing.T) {
	t.Parallel()
	sys := textMessage(lipapi.RoleSystem, "sys")
	user := textMessage(lipapi.RoleUser, "hello")
	overlay := conversationview.SteeringOverlay{
		OverlayID:           "ov1",
		Revision:            1,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "steering"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	}
	snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay})
	call := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{user}}
	proj, ev, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.NotNil(t, ev)
	require.Len(t, ev.Provenance, 1)
	prov := ev.Provenance[0]
	// Compute identity of actual injected message in projected call
	var injected lipapi.Message
	for _, m := range proj.Instructions {
		if m.Parts[0].Text == "steering" {
			injected = m
			break
		}
	}
	idInjected, err := conversationview.MessageIdentityOf(injected)
	require.NoError(t, err)
	assert.Equal(t, prov.InjectedIdentity, idInjected, "provenance identity must equal message identity in projected call")
	// Provenance must not affect other messages' identities
	idSysBefore, _ := conversationview.MessageIdentityOf(sys)
	idSysAfter, _ := conversationview.MessageIdentityOf(proj.Instructions[0])
	assert.Equal(t, idSysBefore, idSysAfter)
}

func TestProvenance_RecognitionHelper(t *testing.T) {
	t.Parallel()
	sys := textMessage(lipapi.RoleSystem, "sys")
	u1 := textMessage(lipapi.RoleUser, "user1")
	overlay := conversationview.SteeringOverlay{
		OverlayID:           "ov-recog",
		Revision:            7,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "recognize me"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	}
	snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay})
	call := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}
	proj, ev, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.Len(t, ev.Provenance, 1)
	prov := ev.Provenance[0]

	// Recognition via helper should match the injected steering message, not normal messages
	// Use MatchesMessage helper
	assert.True(t, prov.MatchesMessage(proj.Instructions[1]), "provenance should match its injected message")
	assert.False(t, prov.MatchesMessage(sys), "provenance must not match unrelated message")
	assert.False(t, prov.MatchesMessage(u1), "provenance must not match unrelated user message")

	// Item authority recognition
	itemSys := textItem(lipapi.RoleSystem, "s1", "sys")
	itemUser := textItem(lipapi.RoleUser, "u1", "user1")
	itemCall := lipapi.Call{Items: []lipapi.Item{itemSys, itemUser}}
	projItem, evItem, err := conversationview.Project(itemCall, snap)
	require.NoError(t, err)
	require.Len(t, evItem.Provenance, 1)
	provItem := evItem.Provenance[0]
	// Find injected item
	var injectedItem lipapi.Item
	for _, it := range projItem.Items {
		if it.Kind == lipapi.ItemKindMessage && len(it.Content) > 0 && it.Content[0].Text == "recognize me" {
			injectedItem = it
			break
		}
	}
	require.NotEmpty(t, injectedItem.Content)
	assert.True(t, provItem.MatchesItem(injectedItem))
	assert.False(t, provItem.MatchesItem(itemSys))
}

func TestProvenance_FallbackResolvedKind(t *testing.T) {
	t.Parallel()
	sys := textMessage(lipapi.RoleSystem, "sys")
	user := textMessage(lipapi.RoleUser, "hello")
	fakeID := conversationview.MessageIdentity("v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	anchor := conversationview.MessageAnchor{Identity: fakeID, Occurrence: 1}
	overlay := conversationview.SteeringOverlay{
		OverlayID:           "ov-fallback",
		Revision:            2,
		SlotOrdinal:         5,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "fallback steering"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	}
	snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay})
	call := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{user}}
	proj, ev, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.Len(t, ev.Provenance, 1)
	require.Len(t, ev.Fallbacks, 1)
	p := ev.Provenance[0]
	assert.Equal(t, conversationview.PlacementStablePrefix, p.ResolvedKind, "fallback should resolve to stable_prefix")
	assert.Nil(t, p.ResolvedAnchor, "fallback provenance anchor should be nil when resolved to stable_prefix")
	// Injected identity must still correspond to projected instruction
	found := false
	for _, m := range proj.Instructions {
		id, _ := conversationview.MessageIdentityOf(m)
		if id == p.InjectedIdentity {
			found = true
		}
	}
	assert.True(t, found)
}
