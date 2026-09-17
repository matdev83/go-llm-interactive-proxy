package journalstore_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestObservationSinkEconomicCapabilityIsOutboxOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)

	plain := journalstore.NewObservationSink(store)
	if _, ok := plain.(coremetering.EconomicObservationSink); ok {
		t.Fatal("plain observation sink must not expose economic outbox capability")
	}
	economic := journalstore.NewObservationSinkWithOutbox(store)
	economicSink, ok := economic.(coremetering.EconomicObservationSink)
	if !ok {
		t.Fatalf("outbox observation sink = %T, want economic outbox capability", economic)
	}
	observation := phase4Observation("sqlite-test", "outbox-capability", 1)
	if err := economicSink.AppendEconomicObservationWithOutbox(ctx, observation); err != nil {
		t.Fatalf("economic observation append: %v", err)
	}
	if err := economicSink.AppendEconomicObservationWithOutbox(ctx, observation); err != nil {
		t.Fatalf("economic observation replay: %v", err)
	}
	conflict := observation.Clone()
	conflict.SourceEventKey = "outbox-capability-conflict"
	if err := economicSink.AppendEconomicObservationWithOutbox(ctx, conflict); err == nil {
		t.Fatal("same observation identity with changed payload must fail closed")
	}
	pending, err := store.ListPendingObservationOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("list economic outbox: %v", err)
	}
	if len(pending) != 1 || pending[0].Observation.ID != observation.ID {
		t.Fatalf("economic outbox rows = %+v, want one exact observation trigger", pending)
	}
}

func TestObservationOutboxIsAtomicWithObservationAndIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	sink := journalstore.NewObservationSinkWithOutbox(store)
	economicSink, ok := sink.(coremetering.EconomicObservationSink)
	require.True(t, ok)
	first := phase4Observation("sqlite-test", "outbox-first", 1)
	require.NoError(t, economicSink.AppendEconomicObservationWithOutbox(ctx, first))
	require.NoError(t, economicSink.AppendEconomicObservationWithOutbox(ctx, first))
	replay := first.Clone()
	replay.ReceivedAt = replay.ReceivedAt.Add(time.Minute)
	require.NoError(t, economicSink.AppendEconomicObservationWithOutbox(ctx, replay), "transport receipt replay must not collide")

	pending, err := store.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, first.ID, pending[0].Observation.ID)
	require.Equal(t, first.Revision, pending[0].Observation.Revision)

	second := phase4Observation("sqlite-test", "outbox-rollback", 2)
	store.SetObservationFaultHook(func(stage string) error {
		if stage == journalstore.ObservationFaultAfterOutbox {
			return errors.New("injected outbox transaction failure")
		}
		return nil
	})
	require.Error(t, economicSink.AppendEconomicObservationWithOutbox(ctx, second))
	store.SetObservationFaultHook(nil)
	_, err = store.GetObservation(ctx, second.ID, second.Revision)
	require.Error(t, err, "observation and outbox must roll back together")
	pending, err = store.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
}

func TestObservationOutboxBackpressureRollsObservationBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", memorySQLiteDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{
		StoreID: "sqlite-test", ObservationOutboxMaxPending: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	sink := journalstore.NewObservationSinkWithOutbox(store)
	atomicSink := sink.(metering.AtomicObservationSink)
	first := phase4Observation("sqlite-test", "outbox-capacity-first", 1)
	second := phase4Observation("sqlite-test", "outbox-capacity-second", 2)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{first}))
	require.ErrorIs(t, atomicSink.AppendObservations(ctx, []metering.Observation{second}), journalstore.ErrObservationOutboxBackpressure)
	_, err = store.GetObservation(ctx, second.ID, second.Revision)
	require.Error(t, err, "backpressure must not persist an observation without its trigger")
	pending, err := store.ListPendingObservationOutbox(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, first.ID, pending[0].Observation.ID)
}
