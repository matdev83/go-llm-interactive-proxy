package bunstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview/storecontract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newConversationViewDeps(t *testing.T) storecontract.Deps {
	t.Helper()
	st, cleanup := newTestStore(t)
	t.Cleanup(cleanup)
	cv := st.ConversationViewStore()
	return storecontract.Deps{
		Store: cv,
		CreateALeg: func(ctx context.Context, aLegID string) error {
			_, err := st.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
			if err != nil {
				// If already exists, treat as ok (idempotent for contract's reuse).
				var count int
				if err2 := st.db.NewRaw(`SELECT count(*) FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(ctx, &count); err2 == nil && count == 1 {
					return nil
				}
			}
			return err
		},
		DeleteALeg: func(ctx context.Context, aLegID string) error {
			_, err := st.db.NewRaw(`DELETE FROM a_legs WHERE a_leg_id = ?`, aLegID).Exec(ctx)
			return err
		},
		GetOverlay: func(ctx context.Context, aLegID, overlayID string) (conversationview.SteeringOverlay, error) {
			cvStore, ok := cv.(*conversationViewStore)
			if !ok {
				return conversationview.SteeringOverlay{}, fmt.Errorf("unexpected cv store type %T", cv)
			}
			return cvStore.GetOverlay(ctx, aLegID, overlayID)
		},
	}
}

func TestConversationView_BunContract_SQLite(t *testing.T) {
	t.Parallel()
	storecontract.Run(t, storecontract.Env{
		New: func(t *testing.T) storecontract.Deps {
			t.Helper()
			return newConversationViewDeps(t)
		},
		Spawn: func(fn func()) { go fn() },
	})
}

func TestConversationView_Schema_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	for _, tbl := range []string{"a_leg_conversation_view_state", "a_leg_never_backend_messages", "a_leg_steering_overlays"} {
		var n int
		err := st.db.NewRaw(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(ctx, &n)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "table %s missing", tbl)
	}
}

func TestConversationView_RestartSurvival_SQLite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cv.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	s1, err := New(bunDB)
	require.NoError(t, err)
	ctx := context.Background()
	aLegID := "a_restart_cv_12345678901234567890123456789012"
	_, err = s1.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	require.NoError(t, err)
	cv1 := s1.ConversationViewStore()
	id := conversationview.MessageIdentity("v1:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	_, err = cv1.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "test_reason"}})
	require.NoError(t, err)
	_, err = cv1.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov_restart",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleAssistant, Text: "steer text"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	})
	require.NoError(t, err)
	snapBefore, err := cv1.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snapBefore.NeverBackend, 1)
	require.Len(t, snapBefore.Steering, 1)
	require.NoError(t, s1.Close())

	sqlDB2, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB2.Close() })
	sqlDB2.SetMaxOpenConns(1)
	bunDB2, err := db.NewBunDB(sqlDB2, db.DialectSQLite)
	require.NoError(t, err)
	s2, err := New(bunDB2)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	cv2 := s2.ConversationViewStore()
	snapAfter, err := cv2.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	assert.Equal(t, snapBefore.StateRevision, snapAfter.StateRevision)
	require.Len(t, snapAfter.NeverBackend, 1)
	assert.Equal(t, id, snapAfter.NeverBackend[0].Identity)
	require.Len(t, snapAfter.Steering, 1)
	assert.Equal(t, "steer text", snapAfter.Steering[0].Message.Text)
	assert.Equal(t, snapBefore.Steering[0].SlotOrdinal, snapAfter.Steering[0].SlotOrdinal)
}

func TestConversationView_ALegDeleteCascades_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	aLegID := "a_cascade_cv_12345678901234567890123456789012"
	_, err := st.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "cascade-key", int64(0), int64(0)).Exec(ctx)
	require.NoError(t, err)
	cv := st.ConversationViewStore()
	id := conversationview.MessageIdentity("v1:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, err = cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "r"}})
	require.NoError(t, err)
	_, err = cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov_cascade",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "hello"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	})
	require.NoError(t, err)
	// Replace A-leg via same continuity key (existing CreateALeg semantics): delete old.
	_, err = st.CreateALeg(ctx, "cascade-key")
	require.NoError(t, err)
	var n int
	err = st.db.NewRaw(`SELECT count(*) FROM a_leg_never_backend_messages WHERE a_leg_id = ?`, aLegID).Scan(ctx, &n)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "never_backend row survived A-leg delete")
	err = st.db.NewRaw(`SELECT count(*) FROM a_leg_steering_overlays WHERE a_leg_id = ?`, aLegID).Scan(ctx, &n)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "steering row survived A-leg delete")
	err = st.db.NewRaw(`SELECT count(*) FROM a_leg_conversation_view_state WHERE a_leg_id = ?`, aLegID).Scan(ctx, &n)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "state row survived A-leg delete")
	_, err = cv.Snapshot(ctx, aLegID)
	require.ErrorIs(t, err, conversationview.ErrALegNotFound)
}

func TestConversationView_DeleteRecreateDoesNotInherit_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	aLegID := "a_recreate_cv_12345678901234567890123456789012"
	_, err := st.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	require.NoError(t, err)
	cv := st.ConversationViewStore()
	id := conversationview.MessageIdentity("v1:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	_, err = cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "r"}})
	require.NoError(t, err)
	_, err = cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "first"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	})
	require.NoError(t, err)
	// Delete and recreate same ID.
	_, err = st.db.NewRaw(`DELETE FROM a_legs WHERE a_leg_id = ?`, aLegID).Exec(ctx)
	require.NoError(t, err)
	_, err = st.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	require.NoError(t, err)
	snap, err := cv.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	assert.Empty(t, snap.NeverBackend)
	assert.Empty(t, snap.Steering)
	assert.Equal(t, uint64(0), snap.StateRevision)
	st2, err := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "new"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), st2.SlotOrdinal, "slot should reset after delete/recreate")
}

func TestConversationView_NoOpDoesNotBumpRevision_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	aLegID := "a_noop_cv_12345678901234567890123456789012"
	_, err := st.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	require.NoError(t, err)
	cv := st.ConversationViewStore()
	id := conversationview.MessageIdentity("v1:" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	res1, err := cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "r"}})
	require.NoError(t, err)
	rev1 := res1.StateRevision
	res2, err := cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "r"}})
	require.NoError(t, err)
	assert.Equal(t, rev1, res2.StateRevision, "idempotent tag must not bump revision")

	req := conversationview.PutSteeringRequest{
		OverlayID:           "ov_noop",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "same"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	}
	st1, err := cv.PutSteering(ctx, aLegID, req)
	require.NoError(t, err)
	snap1, err := cv.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	st2, err := cv.PutSteering(ctx, aLegID, req)
	require.NoError(t, err)
	assert.Equal(t, st1.Revision, st2.Revision)
	snap2, err := cv.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	assert.Equal(t, snap1.StateRevision, snap2.StateRevision, "no-op Put must not bump StateRevision")

	// Deactivate twice: second is no-op.
	_, err = cv.DeactivateSteering(ctx, aLegID, "ov_noop")
	require.NoError(t, err)
	snap3, err := cv.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	stDeact2, err := cv.DeactivateSteering(ctx, aLegID, "ov_noop")
	require.NoError(t, err)
	snap4, err := cv.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	assert.Equal(t, snap3.StateRevision, snap4.StateRevision)
	assert.Equal(t, stDeact2.Revision, stDeact2.Revision)
}

func TestConversationView_SnapshotOrdering_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	aLegID := "a_order_cv_12345678901234567890123456789012"
	_, err := st.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	require.NoError(t, err)
	cv := st.ConversationViewStore()
	// Insert tags out of order and verify snapshot sorts by digest.
	ids := []conversationview.MessageIdentity{
		conversationview.MessageIdentity("v1:" + "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
		conversationview.MessageIdentity("v1:" + "0000000000000000000000000000000000000000000000000000000000000000"),
		conversationview.MessageIdentity("v1:" + "7777777777777777777777777777777777777777777777777777777777777777"),
	}
	for _, id := range ids {
		_, err := cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "r"}})
		require.NoError(t, err)
	}
	snap, err := cv.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snap.NeverBackend, 3)
	for i := 1; i < len(snap.NeverBackend); i++ {
		assert.Less(t, string(snap.NeverBackend[i-1].Identity), string(snap.NeverBackend[i].Identity))
	}
	// Steering ordered by slot.
	for i := range 3 {
		_, err := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
			OverlayID:           "ov-" + string(rune('a'+i)),
			Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "text"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              "r",
		})
		require.NoError(t, err)
	}
	snap2, err := cv.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snap2.Steering, 3)
	for i := 1; i < len(snap2.Steering); i++ {
		assert.Less(t, snap2.Steering[i-1].SlotOrdinal, snap2.Steering[i].SlotOrdinal)
	}
}

func TestConversationView_SharedDBSeesCommittedMutations_SQLite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cv-shared.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	sqlDB1, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB1.SetMaxOpenConns(1)
	bunDB1, err := db.NewBunDB(sqlDB1, db.DialectSQLite)
	require.NoError(t, err)
	s1, err := New(bunDB1)
	require.NoError(t, err)
	defer func() { _ = s1.Close() }()

	sqlDB2, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB2.Close() })
	sqlDB2.SetMaxOpenConns(1)
	bunDB2, err := db.NewBunDB(sqlDB2, db.DialectSQLite)
	require.NoError(t, err)
	s2, err := New(bunDB2)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })

	ctx := context.Background()
	aLegID := "a_shared_cv_12345678901234567890123456789012"
	// Create A-leg via s1's handle (insert via s1's DB handle).
	_, err = s1.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	require.NoError(t, err)
	cv1 := s1.ConversationViewStore()
	cv2 := s2.ConversationViewStore()

	id := conversationview.MessageIdentity("v1:" + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	_, err = cv1.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "r"}})
	require.NoError(t, err)
	snapFromS2, err := cv2.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snapFromS2.NeverBackend, 1)
	assert.Equal(t, id, snapFromS2.NeverBackend[0].Identity)

	_, err = cv2.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov_shared",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: "from s2"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	})
	require.NoError(t, err)
	snapFromS1, err := cv1.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snapFromS1.Steering, 1)
	assert.Equal(t, "from s2", snapFromS1.Steering[0].Message.Text)
}

func TestConversationView_ConcurrentTagSnapshot_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	aLegID := "a_concurrent_cv_12345678901234567890123456789012"
	_, err := st.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	require.NoError(t, err)
	cv := st.ConversationViewStore()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for n := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			hex := fmt.Sprintf("%064x", 1000+n)
			id := conversationview.MessageIdentity("v1:" + hex)
			_, err := cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "r"}})
			if err != nil {
				errs <- err
			}
		}(n)
	}
	for range 10 {
		wg.Go(func() {
			_, err := cv.Snapshot(ctx, aLegID)
			if err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
