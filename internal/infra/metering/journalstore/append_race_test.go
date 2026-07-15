package journalstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// TestSQLiteStore_ConcurrentSameKeyAppendIsIdempotent races identical Appends.
// The UNIQUE(source_event_key) loser must resolve via replay, not a raw insert error.
func TestSQLiteStore_ConcurrentSameKeyAppendIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newSQLiteJournalMultiConn(t)
	ctx := context.Background()

	const workers = 8
	const rounds = 20
	for round := 0; round < rounds; round++ {
		fact := validFact("fact-race-"+strconv.Itoa(round), "stream-race-shared", int64(round+1))

		var wg sync.WaitGroup
		errs := make([]error, workers)
		start := make(chan struct{})
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = retrySQLiteBusy(func() error {
					return store.Append(ctx, fact)
				})
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d worker %d: %v", round, i, err)
			}
		}
	}

	page, err := store.List(ctx, metering.Query{StreamID: "stream-race-shared", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != rounds {
		t.Fatalf("facts=%d want %d", len(page.Facts), rounds)
	}
}

func newSQLiteJournalMultiConn(t *testing.T) *journalstore.DurableStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metering-race.db")
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.ExecContext(context.Background(), `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: "sqlite-race"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func retrySQLiteBusy(fn func() error) error {
	var err error
	for attempt := 0; attempt < 16; attempt++ {
		err = fn()
		if err == nil || !isSQLiteBusy(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return err
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_busy")
}
