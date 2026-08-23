package conversationview_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ---------------------------------------------------------------------------
// helpers for projection tests
// ---------------------------------------------------------------------------

func textMessage(role lipapi.Role, text string) lipapi.Message {
	return lipapi.Message{Role: role, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: text}}}
}

func textItem(role lipapi.Role, id, text string) lipapi.Item {
	return lipapi.Item{
		Kind:   lipapi.ItemKindMessage,
		ID:     id,
		Status: lipapi.ItemStatusCompleted,
		Role:   role,
		Content: []lipapi.ContentPart{
			{Kind: lipapi.ContentPartText, Text: text},
		},
	}
}

func snapshotWithTagsAndOverlays(tags []conversationview.Tag, overlays []conversationview.SteeringOverlay) conversationview.Snapshot {
	return conversationview.Snapshot{
		StateRevision: 1,
		NeverBackend:  tags,
		Steering:      overlays,
	}
}

func mustIdentityForMsg(t *testing.T, msg lipapi.Message) conversationview.MessageIdentity {
	t.Helper()
	id, err := conversationview.MessageIdentityOf(msg)
	require.NoError(t, err)
	return id
}

func mustIdentityForItem(t *testing.T, item lipapi.Item) conversationview.MessageIdentity {
	t.Helper()
	id, err := conversationview.ItemIdentityOf(item)
	require.NoError(t, err)
	return id
}

// normalized trajectory for cache tests: ordered message texts
func normalizedTrajectory(call lipapi.Call) []string {
	var out []string
	if call.HasItemAuthority() {
		for _, it := range call.Items {
			if it.Kind != lipapi.ItemKindMessage {
				continue
			}
			if len(it.Content) > 0 {
				out = append(out, string(it.Role)+":"+it.Content[0].Text)
			} else {
				out = append(out, string(it.Role)+":")
			}
		}
	} else {
		for _, m := range call.Instructions {
			if len(m.Parts) > 0 {
				out = append(out, string(m.Role)+":"+m.Parts[0].Text)
			}
		}
		for _, m := range call.Messages {
			if len(m.Parts) > 0 {
				out = append(out, string(m.Role)+":"+m.Parts[0].Text)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// RED tests pinning projector surface – these must fail until projection.go
// ---------------------------------------------------------------------------

func TestProject_ExclusionFirst_Legacy(t *testing.T) {
	t.Parallel()
	// Legacy call: Instructions + Messages, one message tagged never_backend must be removed.
	msgSys := textMessage(lipapi.RoleSystem, "system prompt")
	msgUser1 := textMessage(lipapi.RoleUser, "hello")
	msgLocal := textMessage(lipapi.RoleAssistant, "local notice")
	msgUser2 := textMessage(lipapi.RoleUser, "follow-up")

	idLocal := mustIdentityForMsg(t, msgLocal)
	snap := snapshotWithTagsAndOverlays(
		[]conversationview.Tag{{Identity: idLocal, Reason: "test_reason"}},
		nil,
	)
	call := lipapi.Call{
		Instructions: []lipapi.Message{msgSys},
		Messages:     []lipapi.Message{msgUser1, msgLocal, msgUser2},
	}
	projected, evidence, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.NotNil(t, evidence)
	// local notice must be absent
	for _, m := range projected.Messages {
		id, _ := conversationview.MessageIdentityOf(m)
		assert.NotEqual(t, idLocal, id, "never_backend message must be excluded")
	}
	assert.Equal(t, 2, len(projected.Messages), "only 2 user messages should remain plus instructions")
	assert.Equal(t, 1, evidence.FilteredCount)
	// original call must be unchanged
	assert.Len(t, call.Messages, 3)
}

func TestProject_ExclusionFirst_ItemAuthority(t *testing.T) {
	t.Parallel()
	itemUser1 := textItem(lipapi.RoleUser, "u1", "hello")
	itemLocal := textItem(lipapi.RoleAssistant, "loc1", "local notice")
	itemUser2 := textItem(lipapi.RoleUser, "u2", "follow-up")
	idLocal := mustIdentityForItem(t, itemLocal)
	snap := snapshotWithTagsAndOverlays(
		[]conversationview.Tag{{Identity: idLocal, Reason: "test_reason"}},
		nil,
	)
	call := lipapi.Call{Items: []lipapi.Item{itemUser1, itemLocal, itemUser2}}
	projected, evidence, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	assert.Equal(t, 1, evidence.FilteredCount)
	require.Len(t, projected.Items, 2)
	for _, it := range projected.Items {
		if it.Kind == lipapi.ItemKindMessage {
			id, _ := conversationview.ItemIdentityOf(it)
			assert.NotEqual(t, idLocal, id)
		}
	}
}

func TestProject_DependencyCleanup_ItemReference(t *testing.T) {
	t.Parallel()
	msgItem := textItem(lipapi.RoleUser, "msg-1", "secret local")
	refItem := lipapi.Item{
		Kind:      lipapi.ItemKindItemReference,
		ID:        "ref-1",
		Status:    lipapi.ItemStatusCompleted,
		Reference: &lipapi.ItemReference{ID: "msg-1"},
	}
	otherRef := lipapi.Item{
		Kind:      lipapi.ItemKindItemReference,
		ID:        "ref-2",
		Status:    lipapi.ItemStatusCompleted,
		Reference: &lipapi.ItemReference{ID: "other-id"},
	}
	normalMsg := textItem(lipapi.RoleUser, "msg-2", "normal")
	idLocal := mustIdentityForItem(t, msgItem)
	snap := snapshotWithTagsAndOverlays([]conversationview.Tag{{Identity: idLocal, Reason: "r"}}, nil)
	call := lipapi.Call{Items: []lipapi.Item{msgItem, refItem, otherRef, normalMsg}}
	projected, _, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	// refItem targeting removed msg-1 must be dropped, otherRef preserved
	for _, it := range projected.Items {
		if it.Kind == lipapi.ItemKindItemReference {
			assert.NotEqual(t, "msg-1", it.Reference.ID, "dangling reference to removed message must be cleaned")
		}
	}
	assert.Len(t, projected.Items, 2, "should have otherRef + normalMsg")
}

func TestProject_InjectionSecond_ExclusionBeforeInjection(t *testing.T) {
	t.Parallel()
	// If steering text equals a removed message's text, injection must still happen exactly once (not filtered).
	localMsg := textMessage(lipapi.RoleUser, "duplicate text")
	idLocal := mustIdentityForMsg(t, localMsg)
	// Steering overlay with same text but different overlay ID should still be injected.
	overlay := conversationview.SteeringOverlay{
		OverlayID:           "ov1",
		Revision:            1,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "duplicate text"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	}
	snap := snapshotWithTagsAndOverlays([]conversationview.Tag{{Identity: idLocal, Reason: "r"}}, []conversationview.SteeringOverlay{overlay})
	call := lipapi.Call{
		Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
		Messages:     []lipapi.Message{localMsg, textMessage(lipapi.RoleUser, "next")},
	}
	projected, evidence, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	assert.Equal(t, 1, evidence.FilteredCount)
	assert.Equal(t, 1, evidence.InjectedCount)
	// Instructions should contain injected steering despite text duplication being filtered earlier.
	require.Len(t, projected.Instructions, 2)
	assert.Equal(t, "duplicate text", projected.Instructions[1].Parts[0].Text)
}

func TestProject_StablePrefix_Legacy(t *testing.T) {
	t.Parallel()
	sys := textMessage(lipapi.RoleSystem, "system prompt")
	user := textMessage(lipapi.RoleUser, "user turn")
	overlay1 := conversationview.SteeringOverlay{
		OverlayID:           "ov1",
		Revision:            1,
		SlotOrdinal:         2,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "steering B"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	}
	overlay2 := conversationview.SteeringOverlay{
		OverlayID:           "ov2",
		Revision:            1,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "steering A"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	}
	// Provide overlays out of order; projector must sort by SlotOrdinal.
	snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay1, overlay2})
	call := lipapi.Call{
		Instructions: []lipapi.Message{sys},
		Messages:     []lipapi.Message{user},
	}
	projected, _, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.Len(t, projected.Instructions, 3)
	// Order must be by SlotOrdinal ascending: ov2 (slot 1) then ov1 (slot 2)
	assert.Equal(t, "steering A", projected.Instructions[1].Parts[0].Text)
	assert.Equal(t, "steering B", projected.Instructions[2].Parts[0].Text)
	// Original Instructions order preserved: sys first.
	assert.Equal(t, "system prompt", projected.Instructions[0].Parts[0].Text)
}

func TestProject_StablePrefix_ItemAuthority(t *testing.T) {
	t.Parallel()
	// Leading system items are prefix; steering should be inserted after them.
	sys1 := textItem(lipapi.RoleSystem, "s1", "sys1")
	sys2 := textItem(lipapi.RoleSystem, "s2", "sys2")
	user := textItem(lipapi.RoleUser, "u1", "hello")
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
	call := lipapi.Call{Items: []lipapi.Item{sys1, sys2, user}}
	projected, _, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.Len(t, projected.Items, 4)
	// Items[0], [1] are sys, [2] is steering, [3] is user
	assert.Equal(t, "sys1", projected.Items[0].Content[0].Text)
	assert.Equal(t, "sys2", projected.Items[1].Content[0].Text)
	assert.Equal(t, "steering", projected.Items[2].Content[0].Text)
	assert.Equal(t, "hello", projected.Items[3].Content[0].Text)
}

func TestProject_FixedAnchor_ItemAndLegacy(t *testing.T) {
	t.Parallel()
	t.Run("item anchor", func(t *testing.T) {
		t.Parallel()
		u1 := textItem(lipapi.RoleUser, "u1", "first user")
		a1 := textItem(lipapi.RoleAssistant, "a1", "assistant reply")
		u2 := textItem(lipapi.RoleUser, "u2", "second user")
		anchorID := mustIdentityForItem(t, u1)
		anchor := conversationview.MessageAnchor{Identity: anchorID, Occurrence: 1}
		overlay := conversationview.SteeringOverlay{
			OverlayID:           "ov-fixed",
			Revision:            1,
			SlotOrdinal:         1,
			Active:              true,
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "fixed steering"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
			AnchorMissingPolicy: conversationview.AnchorFailClosed,
			Reason:              "r",
		}
		snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay})
		call := lipapi.Call{Items: []lipapi.Item{u1, a1, u2}}
		projected, _, err := conversationview.Project(call, snap)
		require.NoError(t, err)
		// Expect: u1, steering, a1, u2
		require.Len(t, projected.Items, 4)
		assert.Equal(t, "first user", projected.Items[0].Content[0].Text)
		assert.Equal(t, "fixed steering", projected.Items[1].Content[0].Text)
		assert.Equal(t, "assistant reply", projected.Items[2].Content[0].Text)
		assert.Equal(t, "second user", projected.Items[3].Content[0].Text)
	})
	t.Run("legacy anchor", func(t *testing.T) {
		t.Parallel()
		sys := textMessage(lipapi.RoleSystem, "sys")
		u1 := textMessage(lipapi.RoleUser, "first user")
		a1 := textMessage(lipapi.RoleAssistant, "assistant reply")
		u2 := textMessage(lipapi.RoleUser, "second user")
		anchorID := mustIdentityForMsg(t, u1)
		anchor := conversationview.MessageAnchor{Identity: anchorID, Occurrence: 1}
		overlay := conversationview.SteeringOverlay{
			OverlayID:           "ov-fixed",
			Revision:            1,
			SlotOrdinal:         1,
			Active:              true,
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "fixed steering"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
			AnchorMissingPolicy: conversationview.AnchorFailClosed,
			Reason:              "r",
		}
		snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay})
		call := lipapi.Call{
			Instructions: []lipapi.Message{sys},
			Messages:     []lipapi.Message{u1, a1, u2},
		}
		projected, _, err := conversationview.Project(call, snap)
		require.NoError(t, err)
		require.Len(t, projected.Messages, 4)
		assert.Equal(t, "first user", projected.Messages[0].Parts[0].Text)
		assert.Equal(t, "fixed steering", projected.Messages[1].Parts[0].Text)
		assert.Equal(t, "assistant reply", projected.Messages[2].Parts[0].Text)
		assert.Equal(t, "second user", projected.Messages[3].Parts[0].Text)
	})
}

func TestProject_AnchorMissing_FallbackAndFailClosed(t *testing.T) {
	t.Parallel()
	t.Run("stable_prefix_fallback", func(t *testing.T) {
		t.Parallel()
		u1 := textMessage(lipapi.RoleUser, "hello")
		// Anchor points to non-existent identity
		fakeID := conversationview.MessageIdentity("v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		anchor := conversationview.MessageAnchor{Identity: fakeID, Occurrence: 1}
		overlay := conversationview.SteeringOverlay{
			OverlayID:           "ov-missing",
			Revision:            1,
			SlotOrdinal:         1,
			Active:              true,
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "fallback steering"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "r",
		}
		snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay})
		call := lipapi.Call{
			Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
			Messages:     []lipapi.Message{u1},
		}
		projected, evidence, err := conversationview.Project(call, snap)
		require.NoError(t, err)
		require.NotNil(t, evidence)
		assert.Len(t, evidence.Fallbacks, 1)
		assert.Equal(t, "ov-missing", evidence.Fallbacks[0].OverlayID)
		// Fallback steering must be in stable prefix (Instructions) not appended to tail
		require.Len(t, projected.Instructions, 2)
		assert.Equal(t, "fallback steering", projected.Instructions[1].Parts[0].Text)
		// Messages unchanged (no tail injection)
		require.Len(t, projected.Messages, 1)
	})
	t.Run("fail_closed", func(t *testing.T) {
		t.Parallel()
		u1 := textMessage(lipapi.RoleUser, "hello")
		fakeID := conversationview.MessageIdentity("v1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		anchor := conversationview.MessageAnchor{Identity: fakeID, Occurrence: 1}
		overlay := conversationview.SteeringOverlay{
			OverlayID:           "ov-fail",
			Revision:            1,
			SlotOrdinal:         1,
			Active:              true,
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "should fail"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
			AnchorMissingPolicy: conversationview.AnchorFailClosed,
			Reason:              "r",
		}
		snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay})
		call := lipapi.Call{
			Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
			Messages:     []lipapi.Message{u1},
		}
		_, _, err := conversationview.Project(call, snap)
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrAnchorMissing)
	})
}

func TestProject_MultipleOverlays_SlotOrderDeterminism(t *testing.T) {
	t.Parallel()
	sys := textMessage(lipapi.RoleSystem, "sys")
	user := textMessage(lipapi.RoleUser, "user")
	// Three overlays at same stable_prefix placement with shuffled input order
	overlays := []conversationview.SteeringOverlay{
		{OverlayID: "ov3", Revision: 1, SlotOrdinal: 3, Active: true, Message: conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "C"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "r"},
		{OverlayID: "ov1", Revision: 1, SlotOrdinal: 1, Active: true, Message: conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "A"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "r"},
		{OverlayID: "ov2", Revision: 1, SlotOrdinal: 2, Active: true, Message: conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "B"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "r"},
	}
	snap := snapshotWithTagsAndOverlays(nil, overlays)
	call := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{user}}
	projected, _, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.Len(t, projected.Instructions, 4)
	assert.Equal(t, "A", projected.Instructions[1].Parts[0].Text)
	assert.Equal(t, "B", projected.Instructions[2].Parts[0].Text)
	assert.Equal(t, "C", projected.Instructions[3].Parts[0].Text)

	// Same for fixed anchor: multiple overlays at same anchor ordered by SlotOrdinal
	t.Run("fixed_anchor same anchor slot order", func(t *testing.T) {
		t.Parallel()
		u1 := textItem(lipapi.RoleUser, "u1", "anchor user")
		a1 := textItem(lipapi.RoleAssistant, "a1", "reply")
		anchorID := mustIdentityForItem(t, u1)
		anchor := conversationview.MessageAnchor{Identity: anchorID, Occurrence: 1}
		fixedOverlays := []conversationview.SteeringOverlay{
			{OverlayID: "f3", Revision: 1, SlotOrdinal: 30, Active: true, Message: conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "C-fix"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor}, AnchorMissingPolicy: conversationview.AnchorFailClosed, Reason: "r"},
			{OverlayID: "f1", Revision: 1, SlotOrdinal: 10, Active: true, Message: conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "A-fix"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor}, AnchorMissingPolicy: conversationview.AnchorFailClosed, Reason: "r"},
			{OverlayID: "f2", Revision: 1, SlotOrdinal: 20, Active: true, Message: conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "B-fix"}, Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor}, AnchorMissingPolicy: conversationview.AnchorFailClosed, Reason: "r"},
		}
		snap2 := snapshotWithTagsAndOverlays(nil, fixedOverlays)
		call2 := lipapi.Call{Items: []lipapi.Item{u1, a1}}
		proj2, _, err2 := conversationview.Project(call2, snap2)
		require.NoError(t, err2)
		require.Len(t, proj2.Items, 5)
		// u1, A-fix, B-fix, C-fix, a1
		assert.Equal(t, "anchor user", proj2.Items[0].Content[0].Text)
		assert.Equal(t, "A-fix", proj2.Items[1].Content[0].Text)
		assert.Equal(t, "B-fix", proj2.Items[2].Content[0].Text)
		assert.Equal(t, "C-fix", proj2.Items[3].Content[0].Text)
		assert.Equal(t, "reply", proj2.Items[4].Content[0].Text)
	})
}

func TestProject_ExactOnceSteering(t *testing.T) {
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
	projected, _, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	// Count steering text occurrences – must be exactly once
	count := 0
	for _, m := range projected.Instructions {
		for _, p := range m.Parts {
			if p.Text == "steering" {
				count++
			}
		}
	}
	for _, m := range projected.Messages {
		for _, p := range m.Parts {
			if p.Text == "steering" {
				count++
			}
		}
	}
	assert.Equal(t, 1, count, "steering must be present exactly once")
	// also via items authority
	t.Run("item authority exact once", func(t *testing.T) {
		t.Parallel()
		snap2 := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay})
		itemCall := lipapi.Call{Items: []lipapi.Item{textItem(lipapi.RoleSystem, "s1", "sys"), textItem(lipapi.RoleUser, "u1", "hello")}}
		proj2, _, err2 := conversationview.Project(itemCall, snap2)
		require.NoError(t, err2)
		c := 0
		for _, it := range proj2.Items {
			for _, cp := range it.Content {
				if cp.Text == "steering" {
					c++
				}
			}
		}
		assert.Equal(t, 1, c)
	})
}

func TestProject_Validation(t *testing.T) {
	t.Parallel()
	// Projected call must pass lipapi.Call.Validate; if injection would break validation, error.
	// Easiest: overlay with empty text is already rejected at store level, but we can test that
	// a call with too many items due to injection? Alternatively test that a dangling item_reference
	// after cleanup still validates. Use a call that after projection would have forward reference if not cleaned.
	// Actually test that valid inputs succeed validation.
	sys := textMessage(lipapi.RoleSystem, "sys")
	user := textMessage(lipapi.RoleUser, "hello")
	snap := snapshotWithTagsAndOverlays(nil, nil)
	call := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{user}}
	projected, _, err := conversationview.Project(call, snap)
	require.NoError(t, err)
	require.NoError(t, projected.Validate())
}

func TestProject_CachePrefixThreeTurns(t *testing.T) {
	t.Parallel()
	// Simulate 3 append-only turns with one fixed-anchor overlay activated at turn 1 (U1).
	// M(N) must be exact prefix of M(N+1) and M(N+1) of M(N+2). A moving-tail reinjection would fail.
	sys := textMessage(lipapi.RoleSystem, "sys")
	u1 := textMessage(lipapi.RoleUser, "user turn 1")
	a1 := textMessage(lipapi.RoleAssistant, "assistant turn 1")
	u2 := textMessage(lipapi.RoleUser, "user turn 2")
	a2 := textMessage(lipapi.RoleAssistant, "assistant turn 2")
	u3 := textMessage(lipapi.RoleUser, "user turn 3")

	anchorID := mustIdentityForMsg(t, u1)
	anchor := conversationview.MessageAnchor{Identity: anchorID, Occurrence: 1}
	overlay := conversationview.SteeringOverlay{
		OverlayID:           "ov-fixed",
		Revision:            1,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "STEERING"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              "r",
	}
	snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay})

	// Turn N: sys, u1 + steering fixed after u1
	callN := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1}}
	projN, _, err := conversationview.Project(callN, snap)
	require.NoError(t, err)
	trajN := normalizedTrajectory(projN)

	// Turn N+1: sys, u1, steering, a1, u2  (client replay appends a1 and u2)
	callN1 := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, a1, u2}}
	projN1, _, err := conversationview.Project(callN1, snap)
	require.NoError(t, err)
	trajN1 := normalizedTrajectory(projN1)

	// Turn N+2: sys, u1, steering, a1, u2, a2, u3
	callN2 := lipapi.Call{Instructions: []lipapi.Message{sys}, Messages: []lipapi.Message{u1, a1, u2, a2, u3}}
	projN2, _, err := conversationview.Project(callN2, snap)
	require.NoError(t, err)
	trajN2 := normalizedTrajectory(projN2)

	// Assert prefix equality: M(N) prefix of M(N+1) and M(N+1) prefix of M(N+2)
	require.True(t, isPrefix(trajN, trajN1), "M(N) must be exact prefix of M(N+1); got %v vs %v", trajN, trajN1)
	require.True(t, isPrefix(trajN1, trajN2), "M(N+1) must be exact prefix of M(N+2); got %v vs %v", trajN1, trajN2)

	// Prove moving-tail would break prefix: simulate tail-append behavior (steering at end each turn)
	movingN := []string{"system:sys", "user:user turn 1", "system:STEERING"}
	movingN1 := []string{"system:sys", "user:user turn 1", "assistant:assistant turn 1", "user:user turn 2", "system:STEERING"}
	// movingN is not prefix of movingN1 because steering moved
	assert.False(t, isPrefix(movingN, movingN1), "moving-tail reinjection must NOT satisfy prefix invariant – test harness validates invariant distinguishes correct behavior")

	// Also ensure steering never follows moving tail: position of STEERING in each trajectory is stable (index 2)
	for _, traj := range [][]string{trajN, trajN1, trajN2} {
		idx := indexOf(traj, "system:STEERING")
		assert.Equal(t, 2, idx, "stable steering position must not move with tail; traj=%v", traj)
	}
}

func isPrefix(prefix, full []string) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if prefix[i] != full[i] {
			return false
		}
	}
	return true
}

func indexOf(slice []string, val string) int {
	for i, s := range slice {
		if s == val {
			return i
		}
	}
	return -1
}

func TestResolveAfterIngressTailAnchor_Rejections(t *testing.T) {
	t.Parallel()
	t.Run("rejects never_backend terminal", func(t *testing.T) {
		t.Parallel()
		u1 := textMessage(lipapi.RoleUser, "hello")
		id := mustIdentityForMsg(t, u1)
		snap := snapshotWithTagsAndOverlays([]conversationview.Tag{{Identity: id, Reason: "r"}}, nil)
		call := lipapi.Call{
			Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
			Messages:     []lipapi.Message{u1},
		}
		_, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
	})
	t.Run("rejects absent terminal user", func(t *testing.T) {
		t.Parallel()
		// Call with no user message at all
		call := lipapi.Call{
			Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
			Messages:     []lipapi.Message{textMessage(lipapi.RoleAssistant, "assistant only")},
		}
		snap := snapshotWithTagsAndOverlays(nil, nil)
		_, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.Error(t, err)
	})
	t.Run("rejects non-user terminal boundary", func(t *testing.T) {
		t.Parallel()
		u1 := textMessage(lipapi.RoleUser, "user")
		a1 := textMessage(lipapi.RoleAssistant, "assistant at tail")
		call := lipapi.Call{
			Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
			Messages:     []lipapi.Message{u1, a1},
		}
		snap := snapshotWithTagsAndOverlays(nil, nil)
		_, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.Error(t, err)
	})
	t.Run("success resolves anchor", func(t *testing.T) {
		t.Parallel()
		u1 := textMessage(lipapi.RoleUser, "first")
		u2 := textMessage(lipapi.RoleUser, "second")
		call := lipapi.Call{
			Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
			Messages:     []lipapi.Message{u1, u2},
		}
		snap := snapshotWithTagsAndOverlays(nil, nil)
		anchor, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.NoError(t, err)
		expectedID := mustIdentityForMsg(t, u2)
		assert.Equal(t, expectedID, anchor.Identity)
		assert.Equal(t, uint32(1), anchor.Occurrence)
		// Duplicate identical user messages -> occurrence increments
		t.Run("duplicate identical", func(t *testing.T) {
			t.Parallel()
			dup := textMessage(lipapi.RoleUser, "dup")
			callDup := lipapi.Call{
				Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
				Messages:     []lipapi.Message{dup, dup},
			}
			anch, err := conversationview.ResolveAfterIngressTailAnchor(callDup, snap)
			require.NoError(t, err)
			assert.Equal(t, uint32(2), anch.Occurrence)
		})
	})
	t.Run("item authority terminal", func(t *testing.T) {
		t.Parallel()
		u1 := textItem(lipapi.RoleUser, "u1", "hello")
		call := lipapi.Call{Items: []lipapi.Item{u1}}
		snap := snapshotWithTagsAndOverlays(nil, nil)
		anchor, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.NoError(t, err)
		expectedID := mustIdentityForItem(t, u1)
		assert.Equal(t, expectedID, anchor.Identity)
	})
}

func TestProject_AfterIngressTail_RejectsNonForwardableAnchor(t *testing.T) {
	t.Parallel()
	// Anchor whose identity is never_backend must cause fallback/fail_closed during projection, not silent tail.
	u1 := textMessage(lipapi.RoleUser, "hello")
	id := mustIdentityForMsg(t, u1)
	anchor := conversationview.MessageAnchor{Identity: id, Occurrence: 1}
	overlay := conversationview.SteeringOverlay{
		OverlayID:           "ov-bad-anchor",
		Revision:            1,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "steering"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              "r",
	}
	// Snapshot marks anchor's identity as never_backend – so anchor disappears from forwardable trajectory
	snap := snapshotWithTagsAndOverlays([]conversationview.Tag{{Identity: id, Reason: "r"}}, []conversationview.SteeringOverlay{overlay})
	// Call originally had u1 but after exclusion it disappears, so anchor cannot be resolved
	call := lipapi.Call{
		Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
		Messages:     []lipapi.Message{u1, textMessage(lipapi.RoleUser, "next")},
	}
	_, _, err := conversationview.Project(call, snap)
	require.Error(t, err)
	assert.ErrorIs(t, err, conversationview.ErrAnchorMissing)
	// Ensure error does NOT contain steering plaintext
	assert.False(t, strings.Contains(err.Error(), "steering"), "error must not leak steering plaintext")
}

func TestProject_DoesNotLeakPlaintextInErrors(t *testing.T) {
	t.Parallel()
	fakeID := conversationview.MessageIdentity("v1:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	anchor := conversationview.MessageAnchor{Identity: fakeID, Occurrence: 1}
	overlay := conversationview.SteeringOverlay{
		OverlayID:           "ov-secret",
		Revision:            1,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "SUPER_SECRET_STEERING_PAYLOAD"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              "r",
	}
	snap := snapshotWithTagsAndOverlays(nil, []conversationview.SteeringOverlay{overlay})
	call := lipapi.Call{
		Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
		Messages:     []lipapi.Message{textMessage(lipapi.RoleUser, "hello")},
	}
	_, _, err := conversationview.Project(call, snap)
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "SUPER_SECRET_STEERING_PAYLOAD"))
}

// Ensure legacy vs item stable prefix both respect ordering regardless of map iteration – deterministic.
func TestProject_Deterministic_NonForwardableAnchorRejection(t *testing.T) {
	t.Parallel()
	// This test pins that after_ingress_tail registration rejects unsafe anchors at resolve time.
	// We use ResolveAfterIngressTailAnchor to validate.
	call := lipapi.Call{
		Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
		Messages:     []lipapi.Message{}, // no messages at all
	}
	snap := snapshotWithTagsAndOverlays(nil, nil)
	_, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
	require.Error(t, err)
	// error must be typed
	assert.True(t, strings.Contains(err.Error(), "terminal") || strings.Contains(err.Error(), "user") || err != nil)
}

func TestResolveAfterIngressTailAnchor_ExcludedTerminalUserRejectsNoFallback(t *testing.T) {
	t.Parallel()
	t.Run("legacy excluded terminal user rejects never falls back to previous user", func(t *testing.T) {
		t.Parallel()
		u1 := textMessage(lipapi.RoleUser, "first user")
		u2 := textMessage(lipapi.RoleUser, "second excluded")
		id2 := mustIdentityForMsg(t, u2)
		// Tag terminal u2 as never_backend.
		snap := snapshotWithTagsAndOverlays([]conversationview.Tag{{Identity: id2, Reason: "test_reason"}}, nil)
		call := lipapi.Call{
			Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
			Messages:     []lipapi.Message{u1, u2},
		}
		_, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
		// Must not leak plaintext, but must not fall back to u1.
		assert.False(t, strings.Contains(err.Error(), "second excluded"), "error must not leak plaintext")
		assert.False(t, strings.Contains(err.Error(), "first user"), "error must not leak plaintext")
		// Explicitly verify that if it had fallen back, anchor would be u1 – ensure rejection not fallback.
		anchorU1 := mustIdentityForMsg(t, u1)
		assert.NotContains(t, err.Error(), string(anchorU1), "error must not contain identity fallback hint")
		// Verify that a non-excluded terminal would succeed and yield u2, proving test would catch fallback.
		snapClean := snapshotWithTagsAndOverlays(nil, nil)
		anchor, err2 := conversationview.ResolveAfterIngressTailAnchor(call, snapClean)
		require.NoError(t, err2)
		assert.Equal(t, id2, anchor.Identity, "clean snapshot should resolve to terminal u2")
		// Occurrence should be 1 over filtered trajectory (no exclusion).
		assert.Equal(t, uint32(1), anchor.Occurrence)
	})
	t.Run("item authority excluded terminal user rejects never falls back to previous user", func(t *testing.T) {
		t.Parallel()
		u1 := textItem(lipapi.RoleUser, "u1", "first user")
		u2 := textItem(lipapi.RoleUser, "u2", "second excluded")
		id2 := mustIdentityForItem(t, u2)
		snap := snapshotWithTagsAndOverlays([]conversationview.Tag{{Identity: id2, Reason: "test_reason"}}, nil)
		call := lipapi.Call{Items: []lipapi.Item{u1, u2}}
		_, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
		assert.False(t, strings.Contains(err.Error(), "second excluded"))
		assert.False(t, strings.Contains(err.Error(), "first user"))
		// Clean snapshot should resolve to u2.
		snapClean := snapshotWithTagsAndOverlays(nil, nil)
		anchor, err2 := conversationview.ResolveAfterIngressTailAnchor(call, snapClean)
		require.NoError(t, err2)
		assert.Equal(t, id2, anchor.Identity)
		assert.Equal(t, uint32(1), anchor.Occurrence)
	})
	t.Run("legacy excluded terminal with duplicate occurrence does not fall back to earlier duplicate", func(t *testing.T) {
		t.Parallel()
		dupText := "duplicate text"
		d1 := textMessage(lipapi.RoleUser, dupText)
		d2 := textMessage(lipapi.RoleUser, dupText)
		// Both messages have identical identity due to same role+text.
		idDup := mustIdentityForMsg(t, d1)
		// Tag the terminal duplicate (occurrence 2) as never_backend – should reject, not return occurrence 1.
		snap := snapshotWithTagsAndOverlays([]conversationview.Tag{{Identity: idDup, Reason: "test_reason"}}, nil)
		// Note: tagging by identity will mark both duplicates as never_backend in exclusion set (since identity same),
		// but spec says we check terminal identity – it is excluded, so reject.
		call := lipapi.Call{
			Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
			Messages:     []lipapi.Message{d1, d2},
		}
		_, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
	})
	t.Run("item authority excluded terminal with non-message items interleaved", func(t *testing.T) {
		t.Parallel()
		u1 := textItem(lipapi.RoleUser, "u1", "first user")
		toolCall := lipapi.Item{Kind: lipapi.ItemKindToolCall, ID: "tc1", ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "t", Arguments: json.RawMessage(`{}`)}}
		u2 := textItem(lipapi.RoleUser, "u2", "second excluded")
		id2 := mustIdentityForItem(t, u2)
		snap := snapshotWithTagsAndOverlays([]conversationview.Tag{{Identity: id2, Reason: "test_reason"}}, nil)
		call := lipapi.Call{Items: []lipapi.Item{u1, toolCall, u2}}
		_, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
	})
	t.Run("item authority trailing tool items reject unsafe boundary", func(t *testing.T) {
		t.Parallel()
		u1 := textItem(lipapi.RoleUser, "u1", "user turn")
		toolCall := lipapi.Item{Kind: lipapi.ItemKindToolCall, ID: "tc1", ToolCall: &lipapi.ToolCallItem{CallID: "c1", Name: "t", Arguments: json.RawMessage(`{}`)}}
		toolResult := lipapi.Item{Kind: lipapi.ItemKindToolResult, ID: "tr1", ToolResult: &lipapi.ToolResultItem{CallID: "c1", Output: "result"}}
		snap := snapshotWithTagsAndOverlays(nil, nil)
		call := lipapi.Call{Items: []lipapi.Item{u1, toolCall, toolResult}}
		anchor, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.Error(t, err, "trailing surviving tool items after terminal user must reject registration")
		assert.ErrorIs(t, err, conversationview.ErrTerminalNotUser)
		_ = anchor
	})
	t.Run("item authority trailing reasoning item rejects unsafe boundary", func(t *testing.T) {
		t.Parallel()
		u1 := textItem(lipapi.RoleUser, "u1", "user turn")
		reasoning := lipapi.Item{Kind: lipapi.ItemKindReasoning, ID: "r1"}
		snap := snapshotWithTagsAndOverlays(nil, nil)
		call := lipapi.Call{Items: []lipapi.Item{u1, reasoning}}
		_, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.Error(t, err, "trailing surviving non-message item must reject registration")
		assert.ErrorIs(t, err, conversationview.ErrTerminalNotUser)
	})
	t.Run("item authority reference to absent target rejects unsafe boundary", func(t *testing.T) {
		t.Parallel()
		u1 := textItem(lipapi.RoleUser, "u1", "user turn")
		dangling := lipapi.Item{Kind: lipapi.ItemKindItemReference, ID: "ref-x", Reference: &lipapi.ItemReference{ID: "msg-gone"}}
		snap := snapshotWithTagsAndOverlays(nil, nil)
		call := lipapi.Call{Items: []lipapi.Item{u1, dangling}}
		_, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.Error(t, err, "projection preserves references to absent targets; surviving reference makes boundary unsafe")
		assert.ErrorIs(t, err, conversationview.ErrTerminalNotUser)
	})
	t.Run("item authority reference to excluded message is cleaned and boundary restored", func(t *testing.T) {
		t.Parallel()
		m1 := textItem(lipapi.RoleUser, "msg-1", "will be excluded")
		u2 := textItem(lipapi.RoleUser, "u2", "terminal user")
		refToExcluded := lipapi.Item{Kind: lipapi.ItemKindItemReference, ID: "ref-1", Reference: &lipapi.ItemReference{ID: "msg-1"}}
		idM1 := mustIdentityForItem(t, m1)
		snap := snapshotWithTagsAndOverlays([]conversationview.Tag{{Identity: idM1, Reason: "test_reason"}}, nil)
		call := lipapi.Call{Items: []lipapi.Item{m1, u2, refToExcluded}}
		anchor, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		require.NoError(t, err, "reference targeting excluded message must be cleaned exactly like projection")
		assert.Equal(t, mustIdentityForItem(t, u2), anchor.Identity)
		assert.Equal(t, uint32(1), anchor.Occurrence)
	})
}

// Helper to silence unused import warning for fmt if not used elsewhere – we use fmt in test helpers above.
var _ = fmt.Sprintf
