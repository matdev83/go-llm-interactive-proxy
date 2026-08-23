package bunstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestIntegration_SQLiteCloseReopen_RetainsTagsAndSteering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cv_5_1.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	sqlDB1, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB1.SetMaxOpenConns(1)
	bunDB1, err := db.NewBunDB(sqlDB1, db.DialectSQLite)
	require.NoError(t, err)
	s1, err := New(bunDB1)
	require.NoError(t, err)
	ctx := context.Background()
	aLegID := "a_5_1_sqlite_close_reopen_12345678901234567890123456789012"
	_, err = s1.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	require.NoError(t, err)
	cv1 := s1.ConversationViewStore()
	tagID := conversationview.MessageIdentity("v1:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, err = cv1.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: tagID, Reason: "test"}})
	require.NoError(t, err)
	steeringText := "hidden-steering-sqlite-restart-5-1"
	_, err = cv1.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID: "ov-sqlite-5-1", Message: conversationview.StoredMessageV1{Role: lipapi.RoleUser, Text: steeringText},
		Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "test",
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
	assert.Equal(t, tagID, snapAfter.NeverBackend[0].Identity)
	require.Len(t, snapAfter.Steering, 1)
	assert.Equal(t, steeringText, snapAfter.Steering[0].Message.Text)
	assert.Equal(t, snapBefore.Steering[0].SlotOrdinal, snapAfter.Steering[0].SlotOrdinal)

	call := lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
	}
	out, ev, err := conversationview.Project(call, snapAfter)
	require.NoError(t, err)
	assert.Equal(t, 0, ev.FilteredCount)
	assert.Equal(t, 1, ev.InjectedCount)
	found := false
	for _, m := range out.Instructions {
		if len(m.Parts) > 0 && m.Parts[0].Text == steeringText {
			found = true
		}
	}
	assert.True(t, found, "reopened steering must be injected")

	sqlDB3, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB3.Close() })
	sqlDB3.SetMaxOpenConns(1)
	bunDB3, err := db.NewBunDB(sqlDB3, db.DialectSQLite)
	require.NoError(t, err)
	s3, err := New(bunDB3)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s3.Close() })
	cv3 := s3.ConversationViewStore()
	secondTag := conversationview.MessageIdentity("v1:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	_, err = cv3.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: secondTag, Reason: "test2"}})
	require.NoError(t, err)
	snapAfterSecond, err := cv2.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snapAfterSecond.NeverBackend, 2)
	foundSecond := false
	for _, tg := range snapAfterSecond.NeverBackend {
		if tg.Identity == secondTag {
			foundSecond = true
		}
	}
	assert.True(t, foundSecond, "second process write must be visible to first process reader on next snapshot")
}

func TestIntegration_Postgres_WriterLaterReader_NoStaleCache(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	ctx := context.Background()
	s1 := openPostgresStoreForIntegration(t, runtimeDSN, adminDSN)
	t.Cleanup(func() { _ = s1.Close() })
	s2 := openPostgresStoreForIntegration(t, runtimeDSN, adminDSN)
	t.Cleanup(func() { _ = s2.Close() })

	aLegID := "a_5_1_pg_writer_reader_12345678901234567890123456789012"
	_, err := s1.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	require.NoError(t, err)
	cvWriter := s1.ConversationViewStore()
	cvReader := s2.ConversationViewStore()
	tagID := conversationview.MessageIdentity("v1:" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	_, err = cvWriter.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: tagID, Reason: "test"}})
	require.NoError(t, err)
	snapReader, err := cvReader.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snapReader.NeverBackend, 1)
	assert.Equal(t, tagID, snapReader.NeverBackend[0].Identity)

	steeringText := "hidden-steering-pg-5-1"
	_, err = cvWriter.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID: "ov-pg-5-1", Message: conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: steeringText},
		Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "test",
	})
	require.NoError(t, err)
	snapReader2, err := cvReader.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snapReader2.Steering, 1)
	assert.Equal(t, steeringText, snapReader2.Steering[0].Message.Text)

	secondTag := conversationview.MessageIdentity("v1:" + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	_, err = cvWriter.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: secondTag, Reason: "test2"}})
	require.NoError(t, err)
	snapReader3, err := cvReader.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snapReader3.NeverBackend, 2)
}

func openPostgresStoreForIntegration(t *testing.T, runtimeDSN, adminDSN string) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	pool := db.PoolSettings{MaxOpenConns: 4, MaxIdleConns: 2}
	migrateDB, err := db.OpenPostgresBun(ctx, adminDSN, pool)
	require.NoError(t, err)
	migrated, err := NewWithContext(ctx, migrateDB)
	require.NoError(t, err)
	t.Cleanup(func() { _ = migrated.Close() })
	openCtx, openCancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer openCancel()
	bunDB, err := db.OpenPostgresBun(openCtx, runtimeDSN, pool)
	require.NoError(t, err)
	s := &Store{db: bunDB}
	require.NoError(t, runContinuitySchemaMigrate(openCtx, bunDB))
	return s
}
