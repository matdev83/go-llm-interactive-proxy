package featurehost_test

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationprojection"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

type bunDBProvider interface {
	DB() *bun.DB
}

func TestConversationStore_PersistenceSelection_SQLiteYieldsBunStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persist_selection.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	defer sqlDB.Close()
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	defer bunDB.Close()

	ctx := context.Background()
	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger: slog.Default(),
		BunDB:  bunDB,
	})
	require.NoError(t, err)
	require.NotNil(t, rt)

	store := rt.ConversationStore()
	require.NotNil(t, store)

	// Assert the store is backed by Bun (borrowed BunDB handle)
	provider, ok := store.(bunDBProvider)
	require.True(t, ok, "store must implement DB() when Bun/SQLite/Postgres is configured")
	require.Equal(t, bunDB, provider.DB())

	// Write steering overlay to verify data survives across store rebuild over same DB
	aLegID := "aleg-sqlite-persist-1"
	_, err = store.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-1",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "sqlite-round-trip-text"},
		Placement:           conversationview.StoredPlacement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.NoError(t, err)

	require.NoError(t, rt.Close())

	// Rebuild a second process over the same BunDB/SQLite file
	rt2, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger: slog.Default(),
		BunDB:  bunDB,
	})
	require.NoError(t, err)
	require.NotNil(t, rt2)
	defer rt2.Close()

	store2 := rt2.ConversationStore()
	require.NotNil(t, store2)
	p2, ok := store2.(bunDBProvider)
	require.True(t, ok)
	require.Equal(t, bunDB, p2.DB())

	snap, err := store2.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snap.Steering, 1)
	require.Equal(t, "sqlite-round-trip-text", snap.Steering[0].Message.Text)
}

func TestConversationStore_PersistenceSelection_InMemoryYieldsReferenceStore(t *testing.T) {
	ctx := context.Background()
	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger: slog.Default(),
	})
	require.NoError(t, err)
	require.NotNil(t, rt)
	defer rt.Close()

	store := rt.ConversationStore()
	require.NotNil(t, store)

	provider, ok := store.(bunDBProvider)
	require.True(t, ok, "store must implement bunDBProvider")
	require.Nil(t, provider.DB(), "in-memory configuration must yield ReferenceStore with nil DB()")
}

func TestConversationStore_ALegLifecycle_BoundedEvictionDeletesConversationState(t *testing.T) {
	ctx := context.Background()
	curTime := time.Unix(1_700_000_000, 0)
	b2bStore, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{
		MaxLegs: 2,
		Now: func() time.Time {
			curTime = curTime.Add(time.Second)
			return curTime
		},
	})
	require.NoError(t, err)

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:          slog.Default(),
		ContinuityStore: b2bStore,
	})
	require.NoError(t, err)
	require.NotNil(t, rt)
	defer rt.Close()

	convStore := rt.ConversationStore()
	require.NotNil(t, convStore)

	// Create 2 A-legs in b2bStore
	leg1, err := b2bStore.CreateALeg(ctx, "key-1")
	require.NoError(t, err)
	leg2, err := b2bStore.CreateALeg(ctx, "key-2")
	require.NoError(t, err)

	// Add steering to leg1 and leg2 in convStore
	_, err = convStore.PutSteering(ctx, leg1.ALegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-leg1",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "steer-leg1"},
		Placement:           conversationview.StoredPlacement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.NoError(t, err)

	_, err = convStore.PutSteering(ctx, leg2.ALegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-leg2",
		Message:             conversationview.StoredMessageV1{Role: lipapi.RoleSystem, Text: "steer-leg2"},
		Placement:           conversationview.StoredPlacement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	require.NoError(t, err)

	// Verify both legs have steering initially
	snap1, err := convStore.Snapshot(ctx, leg1.ALegID)
	require.NoError(t, err)
	require.Len(t, snap1.Steering, 1)

	snap2, err := convStore.Snapshot(ctx, leg2.ALegID)
	require.NoError(t, err)
	require.Len(t, snap2.Steering, 1)

	// Create 3rd leg in b2bStore, which exceeds MaxLegs: 2.
	// This evicts the oldest leg (leg1) and fires retirement observers.
	_, err = b2bStore.CreateALeg(ctx, "key-3")
	require.NoError(t, err)

	// Verify leg1's conversation state was deleted via the retirement observer.
	snap1Evicted, err := convStore.Snapshot(ctx, leg1.ALegID)
	require.NoError(t, err)
	require.Empty(t, snap1Evicted.Steering, "evicted A-leg must have its conversation state deleted")

	// Verify leg2 still has steering
	snap2Remaining, err := convStore.Snapshot(ctx, leg2.ALegID)
	require.NoError(t, err)
	require.Len(t, snap2Remaining.Steering, 1, "un-evicted A-leg must retain its conversation state")
}
