//go:build integration

package journalstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPostgresStore_AppendIdempotent(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	store := newPostgresJournal(t, dsn)
	ctx := context.Background()
	f := validFact("pg-fact-1", "pg-stream", 1)
	f.Money = &metering.MoneyObservation{NanoUnits: 9, Currency: "USD", Present: true, Source: metering.SourceProviderReported}
	if err := store.Append(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, f); err != nil {
		t.Fatal(err)
	}
	collide := f
	collide.Sequence = 2
	if err := store.Append(ctx, collide); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("got %v", err)
	}
	page, err := store.List(ctx, metering.Query{StreamID: "pg-stream"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 || page.Facts[0].Money == nil || page.Facts[0].Money.NanoUnits != 9 {
		t.Fatalf("page=%+v", page)
	}
}

func TestPostgresStore_AppendRejectsSameIdentityDifferentContent(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	store := newPostgresJournal(t, dsn)
	ctx := context.Background()
	f := validFact("pg-fact-content", "pg-stream-content", 1)
	if err := store.Append(ctx, f); err != nil {
		t.Fatal(err)
	}

	diffKind := f
	diffKind.Kind = metering.FactKindDelta
	if err := store.Append(ctx, diffKind); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("different Kind collision got %v", err)
	}

	diffPayload := f
	diffPayload.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     99,
		Present:   true,
	}}
	if err := store.Append(ctx, diffPayload); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("different Quantities collision got %v", err)
	}

	if err := store.Append(ctx, f); err != nil {
		t.Fatalf("identical content must stay idempotent: %v", err)
	}
}

var (
	pgBuildMu   sync.Mutex
	pgSchemaSeq uint64
)

func newPostgresJournal(t *testing.T, dsn string) *journalstore.DurableStore {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	pgBuildMu.Lock()
	seq := atomic.AddUint64(&pgSchemaSeq, 1)
	schema := fmt.Sprintf("metering_test_%d_%d", time.Now().UnixNano(), seq)
	bootstrap, err := db.OpenPostgresBun(ctx, dsn, pool)
	if err != nil {
		pgBuildMu.Unlock()
		t.Fatal(err)
	}
	if _, err := bootstrap.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = bootstrap.Close()
		pgBuildMu.Unlock()
		t.Fatal(err)
	}
	_ = bootstrap.Close()
	schemaDSN := dsn
	if !strings.Contains(dsn, "search_path=") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		schemaDSN = dsn + sep + "search_path=" + schema
	}
	bunDB, err := db.OpenPostgresBun(ctx, schemaDSN, pool)
	pgBuildMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "pg-test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
