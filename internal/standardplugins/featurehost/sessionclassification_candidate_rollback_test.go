package featurehost_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/keepwarm"
	featureclassification "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/sessionclassification"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

type failingCandidateLifecycle struct{ err error }

func (l failingCandidateLifecycle) Start(context.Context) error   { return l.err }
func (failingCandidateLifecycle) Stop(context.Context) error      { return nil }
func (failingCandidateLifecycle) SafeUnderCandidateOverlap() bool { return true }

var _ lipplugin.Lifecycle = failingCandidateLifecycle{}

func TestCandidateRollback_ClassificationLifecycleLeavesNoRowsAndKeepsBunBorrowed(t *testing.T) {
	t.Parallel()

	sqlDB, bunDB := openCandidateRollbackBunDB(t)
	ctx := context.Background()
	runtime, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger: slog.Default(),
		BunDB:  bunDB,
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	registrations := []lipsdk.Registration{
		featureRegistration(t, featureclassification.ID, true, ""),
		featureRegistration(t, keepwarm.ID, false, ""),
	}
	compiled, err := runtime.CompileGeneration(ctx, featurehost.GenerationInput{Registrations: registrations})
	if err != nil {
		_ = runtime.Close()
		t.Fatalf("CompileGeneration: %v", err)
	}
	if !compiled.Planes.IsZero() {
		_ = runtime.Close()
		t.Fatalf("task 5.1 unexpectedly published feature planes: %+v", compiled.Planes)
	}
	if len(compiled.Lifecycles) != 1 {
		_ = runtime.Close()
		t.Fatalf("compiled lifecycles = %d, want the classification lifecycle only", len(compiled.Lifecycles))
	}

	failure := errors.New("later candidate prepare failed")
	lifecycles := append(append([]lipplugin.Lifecycle(nil), compiled.Lifecycles...), failingCandidateLifecycle{err: failure})
	ledger := runtimebundle.NewResourceLedger()
	if err := runtimebundle.AdaptOverlapSafeLifecycles(ledger, lifecycles); err != nil {
		_ = runtime.Close()
		t.Fatalf("AdaptOverlapSafeLifecycles: %v", err)
	}
	if err := ledger.Prepare(ctx); !errors.Is(err, failure) {
		_ = runtime.Close()
		t.Fatalf("candidate Prepare = %v, want injected later lifecycle failure", err)
	}
	if err := ledger.Rollback(ctx); err != nil {
		_ = runtime.Close()
		t.Fatalf("candidate Rollback: %v", err)
	}
	if got := classificationCandidateRowCount(t, bunDB); got != 0 {
		_ = runtime.Close()
		t.Fatalf("classification rows after rejected candidate = %d, want 0", got)
	}
	retry, err := runtime.CompileGeneration(ctx, featurehost.GenerationInput{Registrations: registrations})
	if err != nil || len(retry.Lifecycles) != 1 {
		_ = runtime.Close()
		t.Fatalf("later generation compile = (%d lifecycles, %v), want one reusable classification lifecycle", len(retry.Lifecycles), err)
	}
	if err := retry.Lifecycles[0].Start(ctx); err != nil {
		_ = runtime.Close()
		t.Fatalf("later generation Start after rollback: %v", err)
	}
	if err := retry.Lifecycles[0].Stop(ctx); err != nil {
		_ = runtime.Close()
		t.Fatalf("later generation Stop after rollback: %v", err)
	}
	if got := classificationCandidateRowCount(t, bunDB); got != 0 {
		_ = runtime.Close()
		t.Fatalf("classification rows after later generation reuse = %d, want 0", got)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Runtime.Close: %v", err)
	}
	var one int
	if err := bunDB.NewRaw("SELECT 1").Scan(ctx, &one); err != nil || one != 1 {
		t.Fatalf("post-process-close query = (%d, %v), want (1, nil); Bun DB is borrowed", one, err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatalf("process close closed borrowed SQL DB: %v", err)
	}
}

func openCandidateRollbackBunDB(t *testing.T) (*sql.DB, *bun.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "classification-candidate.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })
	return sqlDB, bunDB
}

func classificationCandidateRowCount(t *testing.T, bunDB *bun.DB) int {
	t.Helper()
	var count int
	if err := bunDB.NewRaw("SELECT COUNT(*) FROM session_classification").Scan(context.Background(), &count); err != nil {
		t.Fatalf("count classification rows: %v", err)
	}
	return count
}
