package b2bua_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview/storecontract"
)

func TestMemoryStoreImplementsConversationViewStore(t *testing.T) {
	t.Parallel()
	mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Via typed accessor.
	cap := mem.ConversationViewStore()
	if _, ok := conversationview.AsStore(cap); !ok {
		t.Fatal("ConversationViewStore does not satisfy conversationview.Store")
	}
	if _, ok := conversationview.AsReader(cap); !ok {
		t.Fatal("does not satisfy Reader")
	}
	if _, ok := conversationview.AsTagger(cap); !ok {
		t.Fatal("does not satisfy Tagger")
	}
	if _, ok := conversationview.AsSteeringStore(cap); !ok {
		t.Fatal("does not satisfy SteeringStore")
	}
	// AsConversationViewStore helper must also resolve via MemoryStore direct.
	if _, ok := b2bua.AsConversationViewStore(mem); !ok {
		t.Fatal("AsConversationViewStore helper failed for MemoryStore")
	}
	if _, ok := conversationview.AsStore(mem); !ok {
		t.Fatal("conversationview.AsStore must resolve via ConversationViewStore provider")
	}
	// Ensure base Store interface was not widened.
	var _ b2bua.Store = mem
}

func TestMemoryStoreConversationViewContract(t *testing.T) {
	t.Parallel()
	storecontract.Run(t, storecontract.Env{
		New: func(t *testing.T) storecontract.Deps {
			t.Helper()
			mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			cap := mem.ConversationViewStore()
			// Expose GetOverlay seam for deactivation persistence check.
			getOverlay := func(ctx context.Context, aLegID, overlayID string) (conversationview.SteeringOverlay, error) {
				if g, ok := cap.(interface {
					GetOverlay(ctx context.Context, aLegID string, overlayID string) (conversationview.SteeringOverlay, error)
				}); ok {
					return g.GetOverlay(ctx, aLegID, overlayID)
				}
				return conversationview.SteeringOverlay{}, conversationview.ErrOverlayNotFound
			}
			return storecontract.Deps{
				Store: cap,
				CreateALeg: func(ctx context.Context, aLegID string) error {
					// Deterministic leg creation for contract determinism.
					mem.CreateLegForConversationViewTest(aLegID)
					return nil
				},
				DeleteALeg: func(ctx context.Context, aLegID string) error {
					mem.DeleteLegForConversationViewTest(aLegID)
					return nil
				},
				GetOverlay: getOverlay,
			}
		},
		Spawn: func(fn func()) { go fn() },
	})
}

func TestMemoryStoreConversationView_OldLegEmptySnapshot(t *testing.T) {
	t.Parallel()
	mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	ctx := context.Background()
	// Create via standard b2bua path, which does not eagerly init conversationView.
	rec, err := mem.CreateALeg(ctx, "old-leg-continuity")
	require.NoError(t, err)
	cap := mem.ConversationViewStore()
	snap, err := cap.Snapshot(ctx, rec.ALegID)
	require.NoError(t, err)
	require.Equal(t, uint64(0), snap.StateRevision)
	require.Empty(t, snap.NeverBackend)
	require.Empty(t, snap.Steering)
}

func TestMemoryStoreConversationView_LifecycleEviction(t *testing.T) {
	t.Parallel()
	mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{MaxLegs: 2})
	require.NoError(t, err)
	ctx := context.Background()
	cap := mem.ConversationViewStore()

	// Seed three legs deterministically.
	mem.CreateLegForConversationViewTest("a_00000000000000000000000000000001")
	mem.CreateLegForConversationViewTest("a_00000000000000000000000000000002")
	_, err = cap.TagNeverBackend(ctx, "a_00000000000000000000000000000001", []conversationview.TagRequest{{
		Identity: conversationview.MessageIdentity("v1:0000000000000000000000000000000000000000000000000000000000000001"),
		Reason:   "r",
	}})
	require.NoError(t, err)
	_, err = cap.PutSteering(ctx, "a_00000000000000000000000000000001", conversationview.PutSteeringRequest{
		OverlayID:           "ov-ev",
		Message:             conversationview.StoredMessageV1{Role: "user", Text: "x"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	})
	require.NoError(t, err)

	// Third leg triggers maxLegs eviction of oldest (000...01).
	mem.CreateLegForConversationViewTest("a_00000000000000000000000000000003")
	// The evicted leg should be gone for conversation view.
	_, err = cap.Snapshot(ctx, "a_00000000000000000000000000000001")
	require.ErrorIs(t, err, conversationview.ErrALegNotFound)
	// Survivor still readable as empty or with its own state.
	snap, err := cap.Snapshot(ctx, "a_00000000000000000000000000000002")
	require.NoError(t, err)
	require.Equal(t, uint64(0), snap.StateRevision)
}

func TestMemoryStoreConversationView_NoStateRecreateFresh(t *testing.T) {
	t.Parallel()
	mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	ctx := context.Background()
	cap := mem.ConversationViewStore()
	aLeg := "a_000000000000000000000000000000aa"
	mem.CreateLegForConversationViewTest(aLeg)
	_, err = cap.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: conversationview.MessageIdentity("v1:1111111111111111111111111111111111111111111111111111111111111111"), Reason: "r"}})
	require.NoError(t, err)
	mem.DeleteLegForConversationViewTest(aLeg)
	_, err = cap.Snapshot(ctx, aLeg)
	require.ErrorIs(t, err, conversationview.ErrALegNotFound)
	mem.CreateLegForConversationViewTest(aLeg)
	snap, err := cap.Snapshot(ctx, aLeg)
	require.NoError(t, err)
	require.Empty(t, snap.NeverBackend)
	require.Equal(t, uint64(0), snap.StateRevision)
	// Slot allocation should reset after delete/recreate (covered by contract, but sanity here).
	st, err := cap.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             conversationview.StoredMessageV1{Role: "user", Text: "hello"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), st.SlotOrdinal)
}
