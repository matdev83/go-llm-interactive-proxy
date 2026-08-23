//go:build integration

package bunstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview/storecontract"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestConversationView_PostgresContract(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	migrateDB, err := db.OpenPostgresBun(ctx, adminDSN, pool)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := NewWithContext(ctx, migrateDB)
	if err != nil {
		_ = migrateDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })

	newDeps := func(t *testing.T) storecontract.Deps {
		t.Helper()
		openCtx, openCancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer openCancel()
		bunDB, err := db.OpenPostgresBun(openCtx, runtimeDSN, pool)
		if err != nil {
			t.Fatal(err)
		}
		s := &Store{db: bunDB}
		t.Cleanup(func() { _ = s.Close() })
		// Ensure schema is applied for this connection (idempotent).
		if err := runContinuitySchemaMigrate(openCtx, bunDB); err != nil {
			t.Fatal(err)
		}
		cv := s.ConversationViewStore()
		return storecontract.Deps{
			Store: cv,
			CreateALeg: func(ctx context.Context, aLegID string) error {
				_, err := s.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
				if err != nil {
					var count int
					if err2 := s.db.NewRaw(`SELECT count(*) FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(ctx, &count); err2 == nil && count == 1 {
						return nil
					}
				}
				return err
			},
			DeleteALeg: func(ctx context.Context, aLegID string) error {
				_, err := s.db.NewRaw(`DELETE FROM a_legs WHERE a_leg_id = ?`, aLegID).Exec(ctx)
				return err
			},
			GetOverlay: func(ctx context.Context, aLegID, overlayID string) (conversationview.SteeringOverlay, error) {
				return cv.(*conversationViewStore).GetOverlay(ctx, aLegID, overlayID)
			},
		}
	}

	storecontract.Run(t, storecontract.Env{
		New:   newDeps,
		Spawn: func(fn func()) { go fn() },
	})
}

func TestConversationView_PostgresSecondStoreSeesCommittedRevision(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	migrateDB, err := db.OpenPostgresBun(ctx, adminDSN, pool)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := NewWithContext(ctx, migrateDB)
	if err != nil {
		_ = migrateDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })

	openStore := func(t *testing.T) *Store {
		t.Helper()
		openCtx, openCancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer openCancel()
		bunDB, err := db.OpenPostgresBun(openCtx, runtimeDSN, pool)
		if err != nil {
			t.Fatal(err)
		}
		s := &Store{db: bunDB}
		if err := runContinuitySchemaMigrate(openCtx, bunDB); err != nil {
			_ = s.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	s1 := openStore(t)
	s2 := openStore(t)
	aLegID := fmt.Sprintf("a_pg_cv_second_%d", time.Now().UnixNano())
	_, err = s1.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cv1 := s1.ConversationViewStore()
	cv2 := s2.ConversationViewStore()
	id := conversationview.MessageIdentity("v1:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, err = cv1.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	snapFromS2, err := cv2.Snapshot(ctx, aLegID)
	if err != nil {
		t.Fatalf("second store Snapshot: %v", err)
	}
	if len(snapFromS2.NeverBackend) != 1 || snapFromS2.NeverBackend[0].Identity != id {
		t.Fatalf("second view stale/missing: %+v", snapFromS2)
	}
	_, err = cv2.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov_pg_second",
		Message:             conversationview.StoredMessageV1{Role: "user", Text: "from s2 pg"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapFromS1, err := cv1.Snapshot(ctx, aLegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapFromS1.Steering) != 1 || snapFromS1.Steering[0].Message.Text != "from s2 pg" {
		t.Fatalf("first view must observe second store commit: %+v", snapFromS1)
	}
}
