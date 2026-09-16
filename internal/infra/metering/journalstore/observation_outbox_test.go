package journalstore_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestObservationOutboxIsAtomicWithObservationAndIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	sink := journalstore.NewObservationSinkWithOutbox(store)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	require.True(t, ok)
	first := phase4Observation("sqlite-test", "outbox-first", 1)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{first}))
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{first}))
	replay := first.Clone()
	replay.ReceivedAt = replay.ReceivedAt.Add(time.Minute)
	require.NoError(t, atomicSink.AppendObservations(ctx, []metering.Observation{replay}), "transport receipt replay must not collide")

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
	require.Error(t, atomicSink.AppendObservations(ctx, []metering.Observation{second}))
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
