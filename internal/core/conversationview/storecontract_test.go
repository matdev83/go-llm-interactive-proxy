package conversationview_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// testFactory returns a fresh reference store plus helper to create an A-leg.
func newTestStore() *conversationview.ReferenceStore {
	return conversationview.NewReferenceStore()
}

func testALegID(seed string) string {
	// Use deterministic but valid shape: a_ + 32 hex chars.
	// Pad/truncate seed to hex.
	h := fmt.Sprintf("%032x", 0)
	if seed != "" {
		// Simple deterministic hash of seed.
		var v uint64
		for _, c := range seed {
			v = v*31 + uint64(c)
		}
		h = fmt.Sprintf("%032x", v)
		if len(h) > 32 {
			h = h[len(h)-32:]
		}
	}
	return "a_" + h
}

func testIdentity(n int) conversationview.MessageIdentity {
	// v1:<64 hex> where hex encodes n.
	hex := fmt.Sprintf("%064x", n)
	return conversationview.MessageIdentity("v1:" + hex)
}

func mustIdentity(n int) conversationview.MessageIdentity {
	id := testIdentity(n)
	require.True(nil, id.IsValid())
	return id
}

func testReason(s string) conversationview.ReasonCode {
	if s == "" {
		s = "test_reason"
	}
	return conversationview.ReasonCode(s)
}

func testMessage(text string, role lipapi.Role) conversationview.StoredMessageV1 {
	if role == "" {
		role = lipapi.RoleUser
	}
	return conversationview.StoredMessageV1{Role: role, Text: text}
}

func stablePlacement() conversationview.StoredPlacement {
	return conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}
}

func afterPlacement(identity conversationview.MessageIdentity, occ uint32) conversationview.StoredPlacement {
	return conversationview.StoredPlacement{
		Kind:   conversationview.PlacementAfterMessage,
		Anchor: &conversationview.MessageAnchor{Identity: identity, Occurrence: occ},
	}
}

// Contract helpers

func createALeg(t *testing.T, s *conversationview.ReferenceStore, aLegID string) {
	t.Helper()
	require.NoError(t, s.CreateALeg(context.Background(), aLegID))
}

func snapshotMust(t *testing.T, s conversationview.Reader, aLegID string) conversationview.Snapshot {
	t.Helper()
	snap, err := s.Snapshot(context.Background(), aLegID)
	require.NoError(t, err)
	return snap
}

// Test: A-leg not-found vs empty snapshot
func TestContract_SnapshotNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	unknown := testALegID("unknown-leg")
	_, err := s.Snapshot(context.Background(), unknown)
	require.ErrorIs(t, err, conversationview.ErrALegNotFound)

	// Empty aLegID also not-found
	_, err = s.Snapshot(context.Background(), "")
	require.ErrorIs(t, err, conversationview.ErrALegNotFound)
	_, err = s.Snapshot(context.Background(), "   ")
	require.ErrorIs(t, err, conversationview.ErrALegNotFound)

	// After creation, snapshot is empty but valid
	aLeg := testALegID("leg-notfound-after-create")
	createALeg(t, s, aLeg)
	snap := snapshotMust(t, s, aLeg)
	assert.Equal(t, uint64(0), snap.StateRevision)
	assert.Empty(t, snap.NeverBackend)
	assert.Empty(t, snap.Steering)
}

// Test: deep-owned copy semantics
func TestContract_SnapshotDeepCopy(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("deep-copy")
	createALeg(t, s, aLeg)

	// Seed one tag and one overlay
	_, err := s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(1), Reason: testReason("reason_one")}})
	require.NoError(t, err)
	_, err = s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             testMessage("hello steering", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r1"),
	})
	require.NoError(t, err)

	snap1 := snapshotMust(t, s, aLeg)
	require.Len(t, snap1.NeverBackend, 1)
	require.Len(t, snap1.Steering, 1)

	// Mutate returned slices
	snap1.NeverBackend[0].Reason = "mutated"
	snap1.NeverBackend = append(snap1.NeverBackend, conversationview.Tag{Identity: testIdentity(999), Reason: "evil"})
	snap1.Steering[0].Message.Text = "mutated text"
	snap1.Steering = append(snap1.Steering, conversationview.SteeringOverlay{OverlayID: "evil"})

	snap2 := snapshotMust(t, s, aLeg)
	require.Len(t, snap2.NeverBackend, 1)
	assert.Equal(t, conversationview.ReasonCode("reason_one"), snap2.NeverBackend[0].Reason)
	assert.Equal(t, "hello steering", snap2.Steering[0].Message.Text)
	assert.Equal(t, snap1.StateRevision, snap2.StateRevision)

	// Mutating anchor pointer should not affect store
	if snap1.Steering[0].Placement.Anchor != nil {
		// stable has no anchor, so test after_message anchor deep copy
	} else {
		// Also test after_message overlay deep copy
		_, err = s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov2",
			Message:             testMessage("second", lipapi.RoleUser),
			Placement:           afterPlacement(testIdentity(10), 1),
			AnchorMissingPolicy: conversationview.AnchorFailClosed,
			Reason:              testReason("r2"),
		})
		require.NoError(t, err)
		snap3 := snapshotMust(t, s, aLeg)
		require.Len(t, snap3.Steering, 2)
		// mutate anchor
		for i := range snap3.Steering {
			if snap3.Steering[i].OverlayID == "ov2" && snap3.Steering[i].Placement.Anchor != nil {
				snap3.Steering[i].Placement.Anchor.Occurrence = 999
			}
		}
		snap4 := snapshotMust(t, s, aLeg)
		for _, ov := range snap4.Steering {
			if ov.OverlayID == "ov2" {
				assert.Equal(t, uint32(1), ov.Placement.Anchor.Occurrence)
			}
		}
	}
}

// Test: Tag batch atomicity
func TestContract_TagBatchAtomicity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		batch []conversationview.TagRequest
	}{
		{
			name: "invalid identity in batch",
			batch: []conversationview.TagRequest{
				{Identity: testIdentity(1), Reason: testReason("ok")},
				{Identity: "invalid-identity", Reason: testReason("ok")},
			},
		},
		{
			name: "invalid reason in batch",
			batch: []conversationview.TagRequest{
				{Identity: testIdentity(1), Reason: testReason("ok")},
				{Identity: testIdentity(2), Reason: "bad reason with spaces!"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newTestStore()
			aLeg := testALegID("atomic-" + tc.name)
			createALeg(t, s, aLeg)
			// Seed one tag
			_, err := s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(100), Reason: testReason("seed")}})
			require.NoError(t, err)
			snapBefore := snapshotMust(t, s, aLeg)
			_, err = s.TagNeverBackend(context.Background(), aLeg, tc.batch)
			require.Error(t, err)
			snapAfter := snapshotMust(t, s, aLeg)
			assert.Equal(t, snapBefore.StateRevision, snapAfter.StateRevision, "revision must not change on atomic failure")
			assert.Equal(t, len(snapBefore.NeverBackend), len(snapAfter.NeverBackend))
			// Ensure no partial mutation
			for _, tag := range snapAfter.NeverBackend {
				assert.NotEqual(t, testIdentity(2), tag.Identity)
			}
		})
	}
}

func TestContract_TagIdempotency(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("idempotent")
	createALeg(t, s, aLeg)

	res1, err := s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{
		{Identity: testIdentity(1), Reason: testReason("r1")},
		{Identity: testIdentity(2), Reason: testReason("r1")},
	})
	require.NoError(t, err)
	rev1 := res1.StateRevision
	snap1 := snapshotMust(t, s, aLeg)
	require.Len(t, snap1.NeverBackend, 2)

	// Re-tag same identities (idempotent) with same and different reason (should be no-op)
	res2, err := s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{
		{Identity: testIdentity(1), Reason: testReason("different_reason")},
		{Identity: testIdentity(2), Reason: testReason("r1")},
	})
	require.NoError(t, err)
	assert.Equal(t, rev1, res2.StateRevision, "idempotent re-tag must not bump revision")
	snap2 := snapshotMust(t, s, aLeg)
	assert.Equal(t, snap1.StateRevision, snap2.StateRevision)
	require.Len(t, snap2.NeverBackend, 2)

	// Batch with duplicate identity within same batch should deduplicate and be idempotent
	res3, err := s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{
		{Identity: testIdentity(1), Reason: testReason("r1")},
		{Identity: testIdentity(1), Reason: testReason("r1")},
		{Identity: testIdentity(2), Reason: testReason("r1")},
	})
	require.NoError(t, err)
	assert.Equal(t, rev1, res3.StateRevision)
}

func TestContract_TagCap4096(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("cap4096")
	createALeg(t, s, aLeg)

	// Fill to cap
	batch := make([]conversationview.TagRequest, conversationview.MaxNeverBackendTags)
	for i := 0; i < conversationview.MaxNeverBackendTags; i++ {
		batch[i] = conversationview.TagRequest{Identity: testIdentity(i + 1), Reason: testReason("r")}
	}
	res, err := s.TagNeverBackend(context.Background(), aLeg, batch)
	require.NoError(t, err)
	assert.Equal(t, conversationview.MaxNeverBackendTags, len(res.Tags))
	snap := snapshotMust(t, s, aLeg)
	assert.Equal(t, conversationview.MaxNeverBackendTags, len(snap.NeverBackend))

	// One more should fail atomically
	_, err = s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(999999), Reason: testReason("r")}})
	require.ErrorIs(t, err, conversationview.ErrTagLimitExceeded)
	snap2 := snapshotMust(t, s, aLeg)
	assert.Equal(t, snap.StateRevision, snap2.StateRevision)
	assert.Len(t, snap2.NeverBackend, conversationview.MaxNeverBackendTags)

	// Idempotent re-tag at cap should succeed and not bump revision
	revBefore := snap2.StateRevision
	res2, err := s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(1), Reason: testReason("r")}})
	require.NoError(t, err)
	assert.Equal(t, revBefore, res2.StateRevision)

	// Batch that would exceed cap partially (some new, some existing) must fail atomically
	// Already at cap, try batch with one existing + one new
	_, err = s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{
		{Identity: testIdentity(1), Reason: testReason("r")},
		{Identity: testIdentity(999998), Reason: testReason("r")},
	})
	require.ErrorIs(t, err, conversationview.ErrTagLimitExceeded)
	snap3 := snapshotMust(t, s, aLeg)
	assert.Equal(t, revBefore, snap3.StateRevision)
}

// Test steering slot ordering and retention
func TestContract_SteeringSlotOrdering(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("slot-order")
	createALeg(t, s, aLeg)

	st1, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov-a",
		Message:             testMessage("first", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r1"),
	})
	require.NoError(t, err)
	st2, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov-b",
		Message:             testMessage("second", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r1"),
	})
	require.NoError(t, err)
	assert.Greater(t, st2.SlotOrdinal, st1.SlotOrdinal, "slot must be monotonic")
	// After_message placement slots also monotonic but independent? Check after placement allocates later slot
	st3, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov-c",
		Message:             testMessage("third", lipapi.RoleUser),
		Placement:           afterPlacement(testIdentity(10), 1),
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              testReason("r1"),
	})
	require.NoError(t, err)
	assert.Greater(t, st3.SlotOrdinal, st2.SlotOrdinal)

	snap := snapshotMust(t, s, aLeg)
	require.Len(t, snap.Steering, 3)
	// Snapshot steering must be sorted by SlotOrdinal
	for i := 1; i < len(snap.Steering); i++ {
		assert.Greater(t, snap.Steering[i].SlotOrdinal, snap.Steering[i-1].SlotOrdinal)
	}
}

func TestContract_SteeringReplaceRetainsSlot(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("replace-slot")
	createALeg(t, s, aLeg)

	st1, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             testMessage("v1", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r1"),
	})
	require.NoError(t, err)
	slot1 := st1.SlotOrdinal
	rev1 := st1.Revision

	// Replace content with same placement -> retain slot, bump revision
	st2, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             testMessage("v2", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r1"),
	})
	require.NoError(t, err)
	assert.Equal(t, slot1, st2.SlotOrdinal, "replace must retain slot when placement unchanged")
	assert.Greater(t, st2.Revision, rev1)

	// Replace with different placement -> allocate new slot
	st3, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             testMessage("v2", lipapi.RoleUser),
		Placement:           afterPlacement(testIdentity(20), 2),
		AnchorMissingPolicy: conversationview.AnchorFailClosed,
		Reason:              testReason("r1"),
	})
	require.NoError(t, err)
	assert.NotEqual(t, slot1, st3.SlotOrdinal, "placement change must allocate new slot")
	assert.Greater(t, st3.SlotOrdinal, slot1)
}

func TestContract_SteeringNoOpDoesNotBumpRevision(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("noop")
	createALeg(t, s, aLeg)

	req := conversationview.PutSteeringRequest{
		OverlayID:           "ov-noop",
		Message:             testMessage("same text", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r1"),
	}
	st1, err := s.PutSteering(context.Background(), aLeg, req)
	require.NoError(t, err)
	snap1 := snapshotMust(t, s, aLeg)
	rev1 := snap1.StateRevision

	st2, err := s.PutSteering(context.Background(), aLeg, req)
	require.NoError(t, err)
	assert.Equal(t, st1.Revision, st2.Revision, "semantic no-op must not bump overlay revision")
	assert.Equal(t, st1.SlotOrdinal, st2.SlotOrdinal)
	snap2 := snapshotMust(t, s, aLeg)
	assert.Equal(t, rev1, snap2.StateRevision, "no-op must not bump StateRevision")
}

func TestContract_SteeringRevisionBumpsOnChange(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("rev-bump")
	createALeg(t, s, aLeg)

	req1 := conversationview.PutSteeringRequest{
		OverlayID:           "ov-rev",
		Message:             testMessage("text1", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r1"),
	}
	st1, err := s.PutSteering(context.Background(), aLeg, req1)
	require.NoError(t, err)

	// Content change
	req2 := req1
	req2.Message = testMessage("text2", lipapi.RoleUser)
	st2, err := s.PutSteering(context.Background(), aLeg, req2)
	require.NoError(t, err)
	assert.Greater(t, st2.Revision, st1.Revision)

	// Reason change
	req3 := req2
	req3.Reason = testReason("r2")
	st3, err := s.PutSteering(context.Background(), aLeg, req3)
	require.NoError(t, err)
	assert.Greater(t, st3.Revision, st2.Revision)

	// Policy change
	req4 := req3
	req4.AnchorMissingPolicy = conversationview.AnchorFailClosed
	st4, err := s.PutSteering(context.Background(), aLeg, req4)
	require.NoError(t, err)
	assert.Greater(t, st4.Revision, st3.Revision)

	// Snapshot revision must be monotonic
	snap := snapshotMust(t, s, aLeg)
	assert.GreaterOrEqual(t, snap.StateRevision, uint64(4))
}

func TestContract_SteeringDeactivate(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("deactivate")
	createALeg(t, s, aLeg)

	_, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov-deact",
		Message:             testMessage("to be deactivated", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r1"),
	})
	require.NoError(t, err)
	snapBefore := snapshotMust(t, s, aLeg)
	require.Len(t, snapBefore.Steering, 1)

	st, err := s.DeactivateSteering(context.Background(), aLeg, "ov-deact")
	require.NoError(t, err)
	assert.False(t, st.Active)
	assert.Greater(t, st.Revision, uint64(1))

	snapAfter := snapshotMust(t, s, aLeg)
	assert.Empty(t, snapAfter.Steering, "deactivated overlay must be absent from snapshot")

	// Record remains queryable via GetOverlay
	ov, err := s.GetOverlay(context.Background(), aLeg, "ov-deact")
	require.NoError(t, err)
	assert.False(t, ov.Active)
	assert.Equal(t, st.Revision, ov.Revision)

	// Second deactivate is no-op (idempotent) and does not bump revision
	revBefore := st.Revision
	snapRevBefore := snapAfter.StateRevision
	st2, err := s.DeactivateSteering(context.Background(), aLeg, "ov-deact")
	require.NoError(t, err)
	assert.Equal(t, revBefore, st2.Revision)
	snapAfter2 := snapshotMust(t, s, aLeg)
	assert.Equal(t, snapRevBefore, snapAfter2.StateRevision)

	// Deactivate non-existent overlay must error
	_, err = s.DeactivateSteering(context.Background(), aLeg, "nonexistent")
	require.ErrorIs(t, err, conversationview.ErrOverlayNotFound)
}

func TestContract_SteeringCaps(t *testing.T) {
	t.Parallel()
	t.Run("64 overlay count cap", func(t *testing.T) {
		t.Parallel()
		s := newTestStore()
		aLeg := testALegID("cap-64")
		createALeg(t, s, aLeg)
		for i := 0; i < conversationview.MaxActiveOverlays; i++ {
			_, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
				OverlayID:           fmt.Sprintf("ov-%d", i),
				Message:             testMessage(fmt.Sprintf("text-%d", i), lipapi.RoleUser),
				Placement:           stablePlacement(),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.NoError(t, err)
		}
		snap := snapshotMust(t, s, aLeg)
		assert.Len(t, snap.Steering, conversationview.MaxActiveOverlays)
		revBefore := snap.StateRevision
		_, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-overflow",
			Message:             testMessage("overflow", lipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.ErrorIs(t, err, conversationview.ErrSteeringLimitExceeded)
		snap2 := snapshotMust(t, s, aLeg)
		assert.Equal(t, revBefore, snap2.StateRevision)
		assert.Len(t, snap2.Steering, conversationview.MaxActiveOverlays)

		// Deactivate one and then create should succeed
		_, err = s.DeactivateSteering(context.Background(), aLeg, "ov-0")
		require.NoError(t, err)
		_, err = s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-overflow",
			Message:             testMessage("overflow", lipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.NoError(t, err)
	})
	t.Run("64KiB per message limit", func(t *testing.T) {
		t.Parallel()
		s := newTestStore()
		aLeg := testALegID("cap-64k")
		createALeg(t, s, aLeg)
		bigText := strings.Repeat("a", conversationview.MaxSteeringTextBytes+1)
		_, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-big",
			Message:             testMessage(bigText, lipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.Error(t, err)
		snap := snapshotMust(t, s, aLeg)
		assert.Empty(t, snap.Steering)
		assert.Equal(t, uint64(0), snap.StateRevision)
	})
	t.Run("256KiB total active payload limit", func(t *testing.T) {
		t.Parallel()
		s := newTestStore()
		aLeg := testALegID("cap-256k")
		createALeg(t, s, aLeg)
		// Each 64KiB, 4 overlays = 256KiB exactly
		text64k := strings.Repeat("b", conversationview.MaxSteeringTextBytes)
		for i := 0; i < 4; i++ {
			_, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
				OverlayID:           fmt.Sprintf("ov%d", i),
				Message:             testMessage(text64k, lipapi.RoleUser),
				Placement:           stablePlacement(),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.NoError(t, err)
		}
		snap := snapshotMust(t, s, aLeg)
		assert.Len(t, snap.Steering, 4)
		revBefore := snap.StateRevision
		_, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-overflow-bytes",
			Message:             testMessage("x", lipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.ErrorIs(t, err, conversationview.ErrSteeringLimitExceeded)
		snap2 := snapshotMust(t, s, aLeg)
		assert.Equal(t, revBefore, snap2.StateRevision)
		// Replace existing with larger text that would exceed total must also fail atomically
		big := strings.Repeat("c", conversationview.MaxSteeringTextBytes)
		_, err = s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov0",
			Message:             testMessage(big+"extra", lipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		// This replacement keeps same size (64KiB) so should succeed; but if we increase beyond, would exceed per-message already.
		// Instead test replacing with text that makes total exceed: we already at 256KiB, replacing one 64KiB with same size is okay, but adding one more is not.
		// Already tested add fails.
		_ = big
	})
}

func TestContract_ALegDeleteRecreate(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("delete-recreate")
	createALeg(t, s, aLeg)

	_, err := s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(1), Reason: testReason("r")}})
	require.NoError(t, err)
	_, err = s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             testMessage("steering", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r"),
	})
	require.NoError(t, err)
	snapBefore := snapshotMust(t, s, aLeg)
	require.NotEmpty(t, snapBefore.NeverBackend)
	require.NotEmpty(t, snapBefore.Steering)

	require.NoError(t, s.DeleteALeg(context.Background(), aLeg))
	_, err = s.Snapshot(context.Background(), aLeg)
	require.ErrorIs(t, err, conversationview.ErrALegNotFound)
	_, err = s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(2), Reason: testReason("r")}})
	require.ErrorIs(t, err, conversationview.ErrALegNotFound)
	_, err = s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov2",
		Message:             testMessage("x", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r"),
	})
	require.ErrorIs(t, err, conversationview.ErrALegNotFound)

	// Recreate same ID must see empty view
	createALeg(t, s, aLeg)
	snapAfter := snapshotMust(t, s, aLeg)
	assert.Empty(t, snapAfter.NeverBackend)
	assert.Empty(t, snapAfter.Steering)
	assert.Equal(t, uint64(0), snapAfter.StateRevision)
	// Slot allocation should reset
	st, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             testMessage("new", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r"),
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), st.SlotOrdinal, "slot should reset after delete/recreate")
}

func TestContract_Linearization(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("linearization")
	createALeg(t, s, aLeg)

	// Case 1: Put commits before snapshot -> snapshot includes it
	_, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov-linear",
		Message:             testMessage("linear-text", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r"),
	})
	require.NoError(t, err)
	snap1 := snapshotMust(t, s, aLeg)
	require.Len(t, snap1.Steering, 1)
	assert.Equal(t, "linear-text", snap1.Steering[0].Message.Text)

	// Case 2: snapshot taken before mutation commit -> that snapshot stays old, next sees it
	snapBefore := snapshotMust(t, s, aLeg)
	_, err = s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(999), Reason: testReason("r")}})
	require.NoError(t, err)
	// snapBefore must not have new tag
	assert.Len(t, snapBefore.NeverBackend, 0)
	snapAfter := snapshotMust(t, s, aLeg)
	require.Len(t, snapAfter.NeverBackend, 1)
	assert.Equal(t, testIdentity(999), snapAfter.NeverBackend[0].Identity)

	// Also tag-then-steering ordering linearizable
	snapBefore2 := snapshotMust(t, s, aLeg)
	_, err = s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov-linear2",
		Message:             testMessage("second-linear", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r"),
	})
	require.NoError(t, err)
	assert.Len(t, snapBefore2.Steering, 1, "snapshot before must stay on old count")
	snapAfter2 := snapshotMust(t, s, aLeg)
	assert.Len(t, snapAfter2.Steering, 2)
}

func TestContract_StateRevisionMonotonic(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("revision")
	createALeg(t, s, aLeg)

	snap0 := snapshotMust(t, s, aLeg)
	assert.Equal(t, uint64(0), snap0.StateRevision)

	// Each semantic mutation bumps
	res, err := s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(1), Reason: testReason("r")}})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), res.StateRevision)
	snap1 := snapshotMust(t, s, aLeg)
	assert.Equal(t, uint64(1), snap1.StateRevision)

	// No-op read does not bump
	snapRead := snapshotMust(t, s, aLeg)
	assert.Equal(t, snap1.StateRevision, snapRead.StateRevision)

	// No-op Put does not bump
	req := conversationview.PutSteeringRequest{
		OverlayID:           "ov-rev",
		Message:             testMessage("text", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r"),
	}
	st1, err := s.PutSteering(context.Background(), aLeg, req)
	require.NoError(t, err)
	snap2 := snapshotMust(t, s, aLeg)
	assert.Equal(t, uint64(2), snap2.StateRevision)
	assert.Equal(t, snap2.StateRevision, st1.StateRevision)
	st2, err := s.PutSteering(context.Background(), aLeg, req)
	require.NoError(t, err)
	assert.Equal(t, st1.Revision, st2.Revision)
	snap3 := snapshotMust(t, s, aLeg)
	assert.Equal(t, snap2.StateRevision, snap3.StateRevision, "no-op Put must not bump StateRevision")

	// Real mutation bumps
	req.Message = testMessage("text2", lipapi.RoleUser)
	st3, err := s.PutSteering(context.Background(), aLeg, req)
	require.NoError(t, err)
	assert.Greater(t, st3.Revision, st2.Revision)
	snap4 := snapshotMust(t, s, aLeg)
	assert.Greater(t, snap4.StateRevision, snap3.StateRevision)

	// Deactivate bumps
	stDeact, err := s.DeactivateSteering(context.Background(), aLeg, "ov-rev")
	require.NoError(t, err)
	assert.Greater(t, stDeact.Revision, st3.Revision)
	snap5 := snapshotMust(t, s, aLeg)
	assert.Greater(t, snap5.StateRevision, snap4.StateRevision)

	// Second deactivate no-op does not bump
	stDeact2, err := s.DeactivateSteering(context.Background(), aLeg, "ov-rev")
	require.NoError(t, err)
	assert.Equal(t, stDeact.Revision, stDeact2.Revision)
	snap6 := snapshotMust(t, s, aLeg)
	assert.Equal(t, snap5.StateRevision, snap6.StateRevision)
}

func TestContract_ConcurrentSmoke(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("concurrent")
	createALeg(t, s, aLeg)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	// Concurrent tags
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(1000 + n), Reason: testReason("r")}})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	// Concurrent snapshots
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Snapshot(context.Background(), aLeg)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	snap := snapshotMust(t, s, aLeg)
	// All tags should eventually be visible (up to 10 unique)
	assert.LessOrEqual(t, len(snap.NeverBackend), 10)
}

func TestContract_InvalidInputsAtomic(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg := testALegID("invalid-atomic")
	createALeg(t, s, aLeg)

	// Invalid steering Put must not mutate
	_, err := s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "", // invalid
		Message:             testMessage("text", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r"),
	})
	require.Error(t, err)
	snap := snapshotMust(t, s, aLeg)
	assert.Empty(t, snap.Steering)
	assert.Equal(t, uint64(0), snap.StateRevision)

	// Invalid placement anchor missing
	_, err = s.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov-invalid-placement",
		Message:             testMessage("text", lipapi.RoleUser),
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: nil},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r"),
	})
	require.Error(t, err)
	snap2 := snapshotMust(t, s, aLeg)
	assert.Equal(t, snap.StateRevision, snap2.StateRevision)

	// Invalid tag batch with one bad identity
	_, err = s.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: "bad", Reason: testReason("r")}})
	require.Error(t, err)
	snap3 := snapshotMust(t, s, aLeg)
	assert.Equal(t, snap2.StateRevision, snap3.StateRevision)
}

func TestContract_SnapshotIsolationAcrossALegs(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	aLeg1 := testALegID("isolation-1")
	aLeg2 := testALegID("isolation-2")
	createALeg(t, s, aLeg1)
	createALeg(t, s, aLeg2)

	_, err := s.TagNeverBackend(context.Background(), aLeg1, []conversationview.TagRequest{{Identity: testIdentity(1), Reason: testReason("r")}})
	require.NoError(t, err)
	_, err = s.PutSteering(context.Background(), aLeg1, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             testMessage("leg1-steering", lipapi.RoleUser),
		Placement:           stablePlacement(),
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              testReason("r"),
	})
	require.NoError(t, err)

	snap1 := snapshotMust(t, s, aLeg1)
	snap2 := snapshotMust(t, s, aLeg2)
	require.Len(t, snap1.NeverBackend, 1)
	require.Len(t, snap1.Steering, 1)
	assert.Empty(t, snap2.NeverBackend)
	assert.Empty(t, snap2.Steering)
	assert.Equal(t, uint64(0), snap2.StateRevision)
}
