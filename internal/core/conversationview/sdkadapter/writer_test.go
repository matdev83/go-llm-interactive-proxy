package sdkadapter_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview/sdkadapter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/steering"
)

func isTerminalError(err error) bool {
	return errors.Is(err, conversationview.ErrTerminalUserNotFound) || errors.Is(err, conversationview.ErrTerminalNotUser)
}

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

func TestWriter_Construction(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	_, err := sdkadapter.NewWriter(nil, "a_leg", nil)
	require.Error(t, err)
	_, err = sdkadapter.NewWriter(store, "   ", nil)
	require.Error(t, err)
	_, err = sdkadapter.NewWriter(store, strings.Repeat("a", 257), nil)
	require.Error(t, err)
	w, err := sdkadapter.NewWriter(store, "a_leg_001", nil)
	require.NoError(t, err)
	require.NotNil(t, w)
	var _ steering.Writer = w
}

func TestWriter_Put_Validation(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	require.NoError(t, store.CreateALeg(ctx, "a_writer_val"))
	w, err := sdkadapter.NewWriter(store, "a_writer_val", nil)
	require.NoError(t, err)

	tests := []struct {
		name    string
		mutate  func(steering.PutRequest) steering.PutRequest
		wantErr bool
	}{
		{
			name: "empty overlay id",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.OverlayID = ""
				return r
			},
			wantErr: true,
		},
		{
			name: "oversized overlay id",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.OverlayID = steering.OverlayID(strings.Repeat("a", 129))
				return r
			},
			wantErr: true,
		},
		{
			name: "empty text",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.Message.Text = ""
				return r
			},
			wantErr: true,
		},
		{
			name: "oversized text",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.Message.Text = strings.Repeat("a", 64*1024+1)
				return r
			},
			wantErr: true,
		},
		{
			name: "unknown placement",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.Placement = "unknown"
				return r
			},
			wantErr: true,
		},
		{
			name: "unknown policy",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.AnchorMissingPolicy = "unknown"
				return r
			},
			wantErr: true,
		},
		{
			name: "empty reason",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.Reason = ""
				return r
			},
			wantErr: true,
		},
	}
	valid := steering.PutRequest{
		OverlayID:           "ov1",
		Message:             steering.Message{Role: lipapi.RoleUser, Text: "hello"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "test_reason",
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := w.Put(ctx, tc.mutate(valid))
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestWriter_Put_StablePrefixPassthrough(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_stable_passthrough"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	w, err := sdkadapter.NewWriter(store, aLeg, nil)
	require.NoError(t, err)

	req := steering.PutRequest{
		OverlayID:           "ov-stable",
		Message:             steering.Message{Role: lipapi.RoleUser, Text: "stable steering"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r1",
	}
	st, err := w.Put(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, steering.OverlayID("ov-stable"), st.OverlayID)
	assert.Equal(t, uint64(1), st.Revision)
	assert.True(t, st.Active)
	assert.Greater(t, st.SlotOrdinal, uint64(0))

	// Verify stored placement is stable_prefix without anchor
	ov, err := store.GetOverlay(ctx, aLeg, "ov-stable")
	require.NoError(t, err)
	assert.Equal(t, conversationview.PlacementStablePrefix, ov.Placement.Kind)
	assert.Nil(t, ov.Placement.Anchor)
	assert.Equal(t, "stable steering", ov.Message.Text)
	assert.Equal(t, lipapi.RoleUser, ov.Message.Role)
}

func TestWriter_Put_AfterIngressTailSuccess(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_after_success"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	u1 := textMessage(lipapi.RoleUser, "hello tail")
	u2 := textMessage(lipapi.RoleUser, "second tail")
	call := lipapi.Call{
		Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")},
		Messages:     []lipapi.Message{u1, u2},
	}
	snap := conversationview.Snapshot{
		StateRevision: 0,
		NeverBackend:  nil,
		Steering:      nil,
	}
	resolver := func(context.Context) (lipapi.Call, conversationview.Snapshot, error) {
		return call, snap, nil
	}
	w, err := sdkadapter.NewWriter(store, aLeg, resolver)
	require.NoError(t, err)

	req := steering.PutRequest{
		OverlayID:           "ov-after",
		Message:             steering.Message{Role: lipapi.RoleSystem, Text: "after steering payload"},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              "r1",
	}
	st, err := w.Put(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), st.Revision)
	assert.True(t, st.Active)

	// Verify anchor is persisted correctly (terminal user u2)
	ov, err := store.GetOverlay(ctx, aLeg, "ov-after")
	require.NoError(t, err)
	require.NotNil(t, ov.Placement.Anchor)
	expectedID, _ := conversationview.MessageIdentityOf(u2)
	assert.Equal(t, expectedID, ov.Placement.Anchor.Identity)
	assert.Equal(t, uint32(1), ov.Placement.Anchor.Occurrence)
	assert.Equal(t, conversationview.PlacementAfterMessage, ov.Placement.Kind)
	assert.Equal(t, "after steering payload", ov.Message.Text)
	// Ensure verbatim persistence: stored text equals input
	assert.Equal(t, req.Message.Text, ov.Message.Text)
}

func TestWriter_Put_AfterIngressTailRejections(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegBase := "a_after_reject"
	require.NoError(t, store.CreateALeg(ctx, aLegBase+"_never"))
	require.NoError(t, store.CreateALeg(ctx, aLegBase+"_absent"))
	require.NoError(t, store.CreateALeg(ctx, aLegBase+"_unsafe"))
	require.NoError(t, store.CreateALeg(ctx, aLegBase+"_nil_resolver"))

	// 1) never_backend terminal: snapshot marks terminal as excluded -> ErrTerminalUserNotFound
	t.Run("never_backend terminal", func(t *testing.T) {
		t.Parallel()
		u1 := textMessage(lipapi.RoleUser, "hello never")
		id, _ := conversationview.MessageIdentityOf(u1)
		call := lipapi.Call{Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")}, Messages: []lipapi.Message{u1}}
		snap := conversationview.Snapshot{NeverBackend: []conversationview.Tag{{Identity: id, Reason: "r"}}}
		resolver := func(context.Context) (lipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
		w, _ := sdkadapter.NewWriter(store, aLegBase+"_never", resolver)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov1",
			Message:             steering.Message{Role: lipapi.RoleUser, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
		// No mutation on failure
		snapStore, _ := store.Snapshot(ctx, aLegBase+"_never")
		assert.Empty(t, snapStore.Steering)
	})

	// 2) absent terminal: no user message
	t.Run("absent terminal", func(t *testing.T) {
		t.Parallel()
		call := lipapi.Call{Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")}, Messages: []lipapi.Message{textMessage(lipapi.RoleAssistant, "assistant only")}}
		snap := conversationview.Snapshot{}
		resolver := func(context.Context) (lipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
		w, _ := sdkadapter.NewWriter(store, aLegBase+"_absent", resolver)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov1",
			Message:             steering.Message{Role: lipapi.RoleUser, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		// Expect terminal not found or not user
		assert.True(t, isTerminalError(err))
	})

	// 3) unsafe anchor: terminal not user (assistant at tail)
	t.Run("terminal not user", func(t *testing.T) {
		t.Parallel()
		u1 := textMessage(lipapi.RoleUser, "user")
		a1 := textMessage(lipapi.RoleAssistant, "assistant tail")
		call := lipapi.Call{Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")}, Messages: []lipapi.Message{u1, a1}}
		snap := conversationview.Snapshot{}
		resolver := func(context.Context) (lipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
		w, _ := sdkadapter.NewWriter(store, aLegBase+"_unsafe", resolver)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov1",
			Message:             steering.Message{Role: lipapi.RoleUser, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalNotUser)
	})

	// 4) nil resolver with after_ingress_tail
	t.Run("nil resolver", func(t *testing.T) {
		t.Parallel()
		w, _ := sdkadapter.NewWriter(store, aLegBase+"_nil_resolver", nil)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov1",
			Message:             steering.Message{Role: lipapi.RoleUser, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
	})

	// 5) resolver returns error
	t.Run("resolver error", func(t *testing.T) {
		t.Parallel()
		aLeg := aLegBase + "_resolver_err"
		require.NoError(t, store.CreateALeg(ctx, aLeg))
		resolver := func(context.Context) (lipapi.Call, conversationview.Snapshot, error) {
			return lipapi.Call{}, conversationview.Snapshot{}, assert.AnError
		}
		w, _ := sdkadapter.NewWriter(store, aLeg, resolver)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov1",
			Message:             steering.Message{Role: lipapi.RoleUser, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, assert.AnError)
	})
}

func TestWriter_Put_StateMappingAndErrorMapping(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_state_map"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	w, err := sdkadapter.NewWriter(store, aLeg, nil)
	require.NoError(t, err)

	// Create first overlay
	req := steering.PutRequest{
		OverlayID:           "ov-map",
		Message:             steering.Message{Role: lipapi.RoleUser, Text: "first"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r1",
	}
	st1, err := w.Put(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, steering.OverlayID("ov-map"), st1.OverlayID)
	assert.Equal(t, uint64(1), st1.Revision)
	assert.True(t, st1.Active)

	// Replace with different text - revision should bump, slot retained
	req.Message.Text = "second"
	st2, err := w.Put(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, st1.SlotOrdinal, st2.SlotOrdinal, "replace must retain slot")
	assert.Greater(t, st2.Revision, st1.Revision)

	// Caps: fill 64 overlays
	aLegCap := "a_cap_test"
	require.NoError(t, store.CreateALeg(ctx, aLegCap))
	wCap, _ := sdkadapter.NewWriter(store, aLegCap, nil)
	for i := 0; i < conversationview.MaxActiveOverlays; i++ {
		_, err := wCap.Put(ctx, steering.PutRequest{
			OverlayID:           steering.OverlayID(fmt.Sprintf("ov-cap-%03d", i)),
			Message:             steering.Message{Role: lipapi.RoleUser, Text: "text"},
			Placement:           steering.StablePrefix,
			AnchorMissingPolicy: steering.StablePrefixFallback,
			Reason:              "r",
		})
		require.NoError(t, err)
	}
	_, err = wCap.Put(ctx, steering.PutRequest{
		OverlayID:           "ov-overflow",
		Message:             steering.Message{Role: lipapi.RoleUser, Text: "overflow"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, conversationview.ErrSteeringLimitExceeded)
}

func TestWriter_Put_NoPlaintextInErrors(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_no_plaintext"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	// Create a writer and try to exceed cap with secret text; error must not contain secret
	w, _ := sdkadapter.NewWriter(store, aLeg, nil)
	secret := "SUPER_SECRET_STEERING_PAYLOAD_123"
	// Fill to trigger limit: we will use a failing after_ingress_tail with secret text but error should not leak
	u1 := textMessage(lipapi.RoleUser, "hello")
	call := lipapi.Call{Instructions: []lipapi.Message{textMessage(lipapi.RoleSystem, "sys")}, Messages: []lipapi.Message{u1}}
	// snapshot marks terminal as never_backend to cause rejection after already having secret in request
	id, _ := conversationview.MessageIdentityOf(u1)
	snap := conversationview.Snapshot{NeverBackend: []conversationview.Tag{{Identity: id, Reason: "r"}}}
	resolver := func(context.Context) (lipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
	w2, _ := sdkadapter.NewWriter(store, aLeg, resolver)
	_, err := w2.Put(ctx, steering.PutRequest{
		OverlayID:           "ov-secret",
		Message:             steering.Message{Role: lipapi.RoleUser, Text: secret},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              "r",
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret, "error must not leak steering plaintext")
	_ = w
	_ = secret
}

func TestWriter_Put_IdempotentNoOp(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_noop"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	w, _ := sdkadapter.NewWriter(store, aLeg, nil)
	req := steering.PutRequest{
		OverlayID:           "ov-noop",
		Message:             steering.Message{Role: lipapi.RoleUser, Text: "same text"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r1",
	}
	st1, err := w.Put(ctx, req)
	require.NoError(t, err)
	snap1, _ := store.Snapshot(ctx, aLeg)
	rev1 := snap1.StateRevision
	st2, err := w.Put(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, st1.Revision, st2.Revision, "semantic no-op must not bump revision")
	assert.Equal(t, st1.SlotOrdinal, st2.SlotOrdinal)
	snap2, _ := store.Snapshot(ctx, aLeg)
	assert.Equal(t, rev1, snap2.StateRevision, "no-op must not bump StateRevision")
}

func TestWriter_Deactivate(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_deact"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	w, _ := sdkadapter.NewWriter(store, aLeg, nil)
	// Put then deactivate
	req := steering.PutRequest{
		OverlayID:           "ov-deact",
		Message:             steering.Message{Role: lipapi.RoleUser, Text: "to be deactivated"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r1",
	}
	stPut, err := w.Put(ctx, req)
	require.NoError(t, err)
	require.True(t, stPut.Active)

	stDeact, err := w.Deactivate(ctx, "ov-deact")
	require.NoError(t, err)
	assert.False(t, stDeact.Active)
	assert.Greater(t, stDeact.Revision, stPut.Revision)
	assert.Equal(t, stPut.SlotOrdinal, stDeact.SlotOrdinal)

	// Idempotent second deactivate
	stDeact2, err := w.Deactivate(ctx, "ov-deact")
	require.NoError(t, err)
	assert.Equal(t, stDeact.Revision, stDeact2.Revision)
	assert.False(t, stDeact2.Active)

	// Invalid ID
	_, err = w.Deactivate(ctx, "")
	require.Error(t, err)

	// Not found
	_, err = w.Deactivate(ctx, "nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, conversationview.ErrOverlayNotFound)

	// Validation of overlay ID via steering
	_, err = w.Deactivate(ctx, steering.OverlayID("bad/id"))
	require.Error(t, err)
}

func TestWriter_ItemAuthorityTrajectory(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_item_traj"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	u1 := textItem(lipapi.RoleUser, "u1", "hello tail")
	call := lipapi.Call{Items: []lipapi.Item{u1}}
	snap := conversationview.Snapshot{}
	resolver := func(context.Context) (lipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
	w, _ := sdkadapter.NewWriter(store, aLeg, resolver)
	st, err := w.Put(ctx, steering.PutRequest{
		OverlayID:           "ov-item",
		Message:             steering.Message{Role: lipapi.RoleUser, Text: "steering"},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              "r",
	})
	require.NoError(t, err)
	assert.True(t, st.Active)
	ov, _ := store.GetOverlay(ctx, aLeg, "ov-item")
	require.NotNil(t, ov.Placement.Anchor)
}

func TestWriter_TrustedConstructionNoGlobal(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	_ = store.CreateALeg(context.Background(), "a_trusted_1")
	_ = store.CreateALeg(context.Background(), "a_trusted_2")
	w1, err := sdkadapter.NewWriter(store, "a_trusted_1", nil)
	require.NoError(t, err)
	w2, err := sdkadapter.NewWriter(store, "a_trusted_2", nil)
	require.NoError(t, err)
	assert.NotSame(t, w1, w2)
}
