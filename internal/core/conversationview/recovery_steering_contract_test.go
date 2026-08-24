package conversationview_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview/sdkadapter"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/steering"
)

// Point 7: Store contracts: where generic existing conversationview store contract can be reused,
// add feature integration test against Memory and SQLite (and PostgreSQL gated by DSN).
// Requirements: 6.15, 12.15.
func TestConversationView_RecoverySteeringLifecycle_MemoryStore(t *testing.T) {
	t.Parallel()

	memStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	cvStore := memStore.ConversationViewStore()

	ctx := context.Background()
	rec, err := memStore.CreateALeg(ctx, "recovery-contract-key-1")
	require.NoError(t, err)

	runRecoverySteeringStoreLifecycle(t, cvStore, rec.ALegID)
}

func TestConversationView_RecoverySteeringLifecycle_ReferenceStore(t *testing.T) {
	t.Parallel()

	refStore := conversationview.NewReferenceStore()
	aLegID := "a_recovery_ref_contract_test_leg_123"
	require.NoError(t, refStore.CreateALeg(context.Background(), aLegID))

	runRecoverySteeringStoreLifecycle(t, refStore, aLegID)
}

func runRecoverySteeringStoreLifecycle(t *testing.T, store conversationview.SteeringStore, aLegID string) {
	t.Helper()
	ctx := context.Background()

	reader, ok := conversationview.AsReader(store)
	require.True(t, ok, "store must implement conversationview.Reader")

	// Accepted user ingress call ending in RoleUser
	userMsg := lipapi.Message{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("user initial prompt")}}
	userCall := lipapi.Call{
		Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("sys")}}},
		Messages:     []lipapi.Message{userMsg},
	}

	resolver := func(ctx context.Context) (lipapi.Call, conversationview.Snapshot, error) {
		snap, err := reader.Snapshot(ctx, aLegID)
		if err != nil {
			return lipapi.Call{}, conversationview.Snapshot{}, err
		}
		return userCall, snap, nil
	}

	writer, err := sdkadapter.NewWriter(store, aLegID, resolver)
	require.NoError(t, err)

	// 1. First Put: registers "alg-rec" with AfterIngressTail and FailClosed
	st1, err := writer.Put(ctx, steering.PutRequest{
		OverlayID: steering.OverlayID("alg-rec"),
		Message: steering.Message{
			Role: lipapi.RoleDeveloper,
			Text: "<automated-recovery>Attempt 1/3: resume unfinished work</automated-recovery>",
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              steering.ReasonCode("loop_guard_recovery"),
	})
	require.NoError(t, err)
	assert.Equal(t, steering.OverlayID("alg-rec"), st1.OverlayID)
	assert.Equal(t, uint64(1), st1.Revision)
	assert.Equal(t, uint64(1), st1.SlotOrdinal)
	assert.True(t, st1.Active)

	// Verify in Snapshot
	snap1, err := reader.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snap1.Steering, 1)
	assert.Equal(t, "alg-rec", snap1.Steering[0].OverlayID)
	assert.Equal(t, lipapi.RoleDeveloper, snap1.Steering[0].Message.Role)
	assert.Contains(t, snap1.Steering[0].Message.Text, "Attempt 1/3")
	assert.Equal(t, conversationview.PlacementAfterMessage, snap1.Steering[0].Placement.Kind)
	require.NotNil(t, snap1.Steering[0].Placement.Anchor)

	// 2. Second Put with updated instruction (Attempt 2/3): reuses SAME slot ordinal, bumps revision
	st2, err := writer.Put(ctx, steering.PutRequest{
		OverlayID: steering.OverlayID("alg-rec"),
		Message: steering.Message{
			Role: lipapi.RoleDeveloper,
			Text: "<automated-recovery>Attempt 2/3: resume unfinished work</automated-recovery>",
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              steering.ReasonCode("loop_guard_recovery"),
	})
	require.NoError(t, err)
	assert.Equal(t, st1.SlotOrdinal, st2.SlotOrdinal, "slot ordinal must be retained on update")
	assert.Equal(t, uint64(2), st2.Revision, "revision must bump on text update")

	snap2, err := reader.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snap2.Steering, 1, "update must not create duplicate overlay")
	assert.Contains(t, snap2.Steering[0].Message.Text, "Attempt 2/3")

	// 3. Semantic no-op Put (identical content): revision and StateRevision must not bump
	st3, err := writer.Put(ctx, steering.PutRequest{
		OverlayID: steering.OverlayID("alg-rec"),
		Message: steering.Message{
			Role: lipapi.RoleDeveloper,
			Text: "<automated-recovery>Attempt 2/3: resume unfinished work</automated-recovery>",
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              steering.ReasonCode("loop_guard_recovery"),
	})
	require.NoError(t, err)
	assert.Equal(t, st2.Revision, st3.Revision, "no-op Put must not bump revision")

	// 4. Deactivate overlay: absent from snapshot
	stDeact, err := writer.Deactivate(ctx, steering.OverlayID("alg-rec"))
	require.NoError(t, err)
	assert.False(t, stDeact.Active)
	assert.Greater(t, stDeact.Revision, st2.Revision)

	snap3, err := reader.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	assert.Empty(t, snap3.Steering, "deactivated overlay must be absent from active snapshot")

	// 5. Second Deactivate: idempotent no-op
	stDeact2, err := writer.Deactivate(ctx, steering.OverlayID("alg-rec"))
	require.NoError(t, err)
	assert.Equal(t, stDeact.Revision, stDeact2.Revision, "second deactivate must be idempotent no-op")

	// 6. Deactivating unknown overlay: returns ErrOverlayNotFound
	_, err = writer.Deactivate(ctx, steering.OverlayID("non-existent-overlay"))
	require.ErrorIs(t, err, conversationview.ErrOverlayNotFound)
}
