package authoritystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	_ "modernc.org/sqlite" // register sqlite driver for durable authority stores
)

// DurableStore persists the same store core to a relational database using
// row-targeted writes recorded per call. No shadow state; each Reserve,
// Settle, Release, and ApplyUsage persists only the rows it actually mutated,
// and idempotent re-runs (no mutation) skip the SQL transaction entirely.
//
// Persistence goes through Bun so the same code path serves SQLite and
// PostgreSQL: Bun translates the "?" placeholders per dialect (SQLite keeps
// "?", PostgreSQL rewrites them to "$N"), which the raw database/sql driver
// could not do.
//
// Concurrency model (requirements 10.6, 10.9, 11.1, 16.1, 16.2):
//   - Process-wide locking is limited to Close and readiness lifecycle state.
//     Mutations and queries do not hold an in-process mutex across database I/O.
//   - Each mutating operation opens a Bun transaction and locks the affected
//     live limit rows BEFORE the in-memory capacity check runs, so two proxy
//     instances cannot reserve from stale copies. On PostgreSQL the lock is
//     "SELECT ... FOR UPDATE" on the store's limit rows; on SQLite the write
//     transaction is opened "BEGIN IMMEDIATE" via the _txlock=immediate DSN
//     parameter (the modernc.org/sqlite driver maps _txlock to the BEGIN mode
//     and ignores sql.TxOptions.Isolation, so the DSN is the only knob).
//   - Each call builds an operation-local storeCore projection from only the
//     relevant reservation, usage fact, state, and limit rows. The capacity
//     check therefore sees committed counters without scanning durable history.
//   - The flush writes limit rows with a conditional UPDATE keyed on the
//     pre-image row_json. If zero rows match, a concurrent writer changed the
//     row between the lock and the write (a lost update); the transaction
//     rolls back and the call returns app.ErrReservationConflict. New rows
//     produced by window rollover use INSERT ... ON CONFLICT DO NOTHING and
//     treat zero rows as the same conflict.
//   - On any flush failure (lost-update detection or commit failure), the
//     operation-local projection is discarded. Business-logic outcomes from storeCore
//     (e.g. a strict-cap deny) are NOT flush failures: they return without
//     writing, matching the prior behavior where the deny decision stays in
//     the in-memory ledger.
type DurableStore struct {
	lifecycleMu sync.Mutex // Close + readiness state only (16.1/16.2)
	closed      atomic.Bool
	db          *bun.DB
	c           *storeCore
	// beginTxHook is an optional test seam invoked immediately before BeginTx
	// on mutation paths. Production code leaves it nil.
	beginTxHook func()
	nonOwning   bool
}

// errDurableFlushFailed marks errors where the transactional flush did not
// commit (lost-update conflict or commit failure).
var errDurableFlushFailed = errors.New("authoritystore: durable flush failed")

var (
	errConcurrentReservationCreate = errors.New("authoritystore: concurrent reservation create")
	errConcurrentUsageFactMutation = errors.New("authoritystore: concurrent usage fact mutation")
	errConcurrentLimitRowCreate    = errors.New("authoritystore: concurrent limit row create")
)

// NewDurable returns a durable store backed by a Bun DB. The DB must already
// be opened with the correct dialect; NewDurable runs migrations and hydrates
// the in-memory projection from existing rows, seeding when the DB is empty.
func NewDurable(ctx context.Context, db *bun.DB, cfg Config) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("authoritystore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("authoritystore: nil db")
	}
	if err := Migrate(ctx, db); err != nil {
		return nil, err
	}
	return openStore(ctx, db, cfg, false)
}

// OpenStore opens an authority store without running migrations and without
// taking ownership of db. The composition root owns the shared runtime pool.
func OpenStore(ctx context.Context, db *bun.DB, cfg Config) (*DurableStore, error) {
	return openStore(ctx, db, cfg, true)
}

func openStore(ctx context.Context, db *bun.DB, cfg Config, nonOwning bool) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("authoritystore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("authoritystore: nil db")
	}
	st := &DurableStore{db: db, c: newStoreCore(cfg), nonOwning: nonOwning}
	if err := st.backfillDecisionFilters(ctx); err != nil {
		return nil, err
	}
	if err := st.backfillLimitFilters(ctx); err != nil {
		return nil, err
	}
	loaded, err := st.load(ctx)
	if err != nil {
		return nil, err
	}
	if !loaded {
		if err := st.seedAndFlush(ctx); err != nil {
			return nil, err
		}
		if _, err := st.load(ctx); err != nil {
			return nil, err
		}
	}
	if err := st.reconcileAndFlush(ctx); err != nil {
		return nil, err
	}
	return st, nil
}

// Close closes the underlying Bun database handle (and its *sql.DB).
func (s *DurableStore) Close() error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	db := s.db
	s.closed.Store(true)
	s.lifecycleMu.Unlock()
	if db == nil || s.nonOwning {
		return nil
	}
	return db.Close()
}

// Migrate applies the durable authority-store schema via bun/migrate.
func Migrate(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("authoritystore: nil context")
	}
	if db == nil {
		return fmt.Errorf("authoritystore: nil db")
	}
	if err := runSchemaMigrate(ctx, db); err != nil {
		return storeUnavailableError("migrate", err)
	}
	return nil
}

// VerifySchema checks the required runtime relations without mutating schema.
func VerifySchema(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("authoritystore: nil context")
	}
	if db == nil {
		return fmt.Errorf("authoritystore: nil db")
	}
	if db.Dialect().Name() != dialect.PG {
		for _, probe := range []string{
			`SELECT 1 FROM usage_authority_state WHERE 1 = 0`,
			`SELECT 1 FROM usage_authority_limit_rows WHERE 1 = 0`,
			`SELECT 1 FROM usage_authority_decisions WHERE 1 = 0`,
			`SELECT 1 FROM usage_authority_decision_filters WHERE 1 = 0`,
			`SELECT 1 FROM usage_authority_limit_filters WHERE 1 = 0`,
			`SELECT 1 FROM usage_authority_reservations WHERE 1 = 0`,
			`SELECT 1 FROM usage_authority_unreserved_usage_facts WHERE 1 = 0`,
		} {
			if _, err := db.ExecContext(ctx, probe); err != nil {
				return fmt.Errorf("authoritystore: schema verification failed: %w", err)
			}
		}
		return nil
	}
	for _, probe := range []string{
		`SELECT * FROM usage_authority_state WHERE 1 = 0`,
		`SELECT * FROM usage_authority_limit_rows WHERE 1 = 0`,
		`SELECT * FROM usage_authority_decisions WHERE 1 = 0`,
		`SELECT * FROM usage_authority_decision_filters WHERE 1 = 0`,
		`SELECT * FROM usage_authority_limit_filters WHERE 1 = 0`,
		`SELECT * FROM usage_authority_reservations WHERE 1 = 0`,
		`SELECT * FROM usage_authority_unreserved_usage_facts WHERE 1 = 0`,
	} {
		if _, err := db.ExecContext(ctx, probe); err != nil {
			return fmt.Errorf("authoritystore: schema verification failed: %w", err)
		}
	}
	checks := []struct {
		description string
		query       string
		args        []any
		fragments   []string
	}{
		{
			description: "migration history",
			query:       `SELECT name FROM bun_usage_authority_migrations WHERE name = ? LIMIT 1`,
			args:        []any{BaselineMigrationName},
			fragments:   []string{BaselineMigrationName},
		},
		{
			description: "usage_authority_state primary key",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'usage_authority_state'
  AND c.contype = 'p'
  AND lower(pg_get_constraintdef(c.oid)) = 'primary key (store_id)'
LIMIT 1`,
			fragments: []string{"primary key (store_id)"},
		},
		{
			description: "usage_authority_limit_rows primary key",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'usage_authority_limit_rows'
  AND c.contype = 'p'
  AND lower(pg_get_constraintdef(c.oid)) = 'primary key (store_id, row_key)'
LIMIT 1`,
			fragments: []string{"primary key (store_id, row_key)"},
		},
		{
			description: "usage_authority_decisions primary key",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'usage_authority_decisions'
  AND c.contype = 'p'
  AND lower(pg_get_constraintdef(c.oid)) = 'primary key (store_id, decision_seq)'
LIMIT 1`,
			fragments: []string{"primary key (store_id, decision_seq)"},
		},
		{
			description: "usage_authority_decisions source_key unique constraint",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'usage_authority_decisions'
  AND c.contype = 'u'
  AND lower(pg_get_constraintdef(c.oid)) = 'unique (store_id, source_key)'
LIMIT 1`,
			fragments: []string{"unique (store_id, source_key)"},
		},
		{
			description: "usage_authority_decision_filters primary key",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'usage_authority_decision_filters'
  AND c.contype = 'p'
  AND lower(pg_get_constraintdef(c.oid)) = 'primary key (store_id, decision_seq, field_name)'
LIMIT 1`,
			fragments: []string{"primary key (store_id, decision_seq, field_name)"},
		},
		{
			description: "usage_authority_decision_filters_lookup",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'usage_authority_decision_filters'
  AND indexname = 'usage_authority_decision_filters_lookup'
LIMIT 1`,
			fragments: []string{"(store_id, field_name, field_value, decision_seq)"},
		},
		{
			description: "usage_authority_limit_filters primary key",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'usage_authority_limit_filters'
  AND c.contype = 'p'
  AND lower(pg_get_constraintdef(c.oid)) = 'primary key (store_id, row_key, field_name)'
LIMIT 1`,
			fragments: []string{"primary key (store_id, row_key, field_name)"},
		},
		{
			description: "usage_authority_limit_filters_lookup",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'usage_authority_limit_filters'
  AND indexname = 'usage_authority_limit_filters_lookup'
LIMIT 1`,
			fragments: []string{"(store_id, field_name, field_value, row_key)"},
		},
		{
			description: "usage_authority_reservations primary key",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'usage_authority_reservations'
  AND c.contype = 'p'
  AND lower(pg_get_constraintdef(c.oid)) = 'primary key (store_id, reservation_key)'
LIMIT 1`,
			fragments: []string{"primary key (store_id, reservation_key)"},
		},
		{
			description: "usage_authority_reservations source_key unique constraint",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'usage_authority_reservations'
  AND c.contype = 'u'
  AND lower(pg_get_constraintdef(c.oid)) = 'unique (store_id, source_key)'
LIMIT 1`,
			fragments: []string{"unique (store_id, source_key)"},
		},
		{
			description: "usage_authority_unreserved_usage_facts primary key",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'usage_authority_unreserved_usage_facts'
  AND c.contype = 'p'
  AND lower(pg_get_constraintdef(c.oid)) = 'primary key (store_id, fact_key)'
LIMIT 1`,
			fragments: []string{"primary key (store_id, fact_key)"},
		},
	}
	for _, check := range checks {
		if err := dbinfra.VerifyPostgresQueryRowContains(ctx, db, check.description, check.query, check.args, check.fragments...); err != nil {
			return fmt.Errorf("authoritystore: schema verification failed: %w", err)
		}
	}
	return nil
}

// CheckReadiness returns the current posture and surfaces backing failures without leaking driver details.
func (s *DurableStore) CheckReadiness(ctx context.Context) (domain.AuthorityStatus, error) {
	if s == nil || s.db == nil {
		return domain.StatusFromBacking(domain.BackingCapabilityUnavailable), fmt.Errorf("authoritystore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return domain.AuthorityStatus{}, err
	}
	if s.closed.Load() {
		status := domain.StatusFromBacking(domain.BackingCapabilityUnavailable)
		s.lifecycleMu.Lock()
		s.c.state = status
		s.lifecycleMu.Unlock()
		return status, fmt.Errorf("authoritystore readiness: %w", app.ErrUnavailable)
	}
	if err := s.db.PingContext(ctx); err != nil {
		status := domain.StatusFromBacking(domain.BackingCapabilityUnavailable)
		s.lifecycleMu.Lock()
		s.c.state = status
		s.lifecycleMu.Unlock()
		return status, fmt.Errorf("authoritystore readiness: %w", app.ErrUnavailable)
	}
	// A successful ping restores the configured readiness posture so a prior
	// transient failure does not permanently disable admissions until restart.
	// Restore cfg.Readiness (not StatusFromBacking(Backing)): production may
	// deliberately configure Readiness=advisory_only with Backing=atomic for
	// startup_posture fail_open, and a successful probe must not promote that
	// to ready.
	s.lifecycleMu.Lock()
	s.c.state = s.c.cfg.Readiness
	status := s.c.readiness()
	s.lifecycleMu.Unlock()
	return status, nil
}

// Reserve atomically records a reservation and persists only the rows this call mutated.
func (s *DurableStore) Reserve(ctx context.Context, cmd app.ReserveCommand) (app.ReserveResult, error) {
	if s == nil || s.db == nil {
		return app.ReserveResult{}, fmt.Errorf("authoritystore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return app.ReserveResult{}, err
	}
	if s.closed.Load() {
		return app.ReserveResult{}, unavailableError("reserve")
	}
	return s.runReserveTx(ctx, cmd)
}

// Settle reconciles usage and persists only the rows this call mutated.
func (s *DurableStore) Settle(ctx context.Context, cmd app.SettleCommand) (app.SettleResult, error) {
	if s == nil || s.db == nil {
		return app.SettleResult{}, fmt.Errorf("authoritystore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return app.SettleResult{}, err
	}
	if s.closed.Load() {
		return app.SettleResult{}, unavailableError("settle")
	}
	return s.runSettleTx(ctx, cmd)
}

// Release releases reservation capacity and persists only the rows this call mutated.
func (s *DurableStore) Release(ctx context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	if s == nil || s.db == nil {
		return app.ReleaseResult{}, fmt.Errorf("authoritystore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return app.ReleaseResult{}, err
	}
	if s.closed.Load() {
		return app.ReleaseResult{}, unavailableError("release")
	}
	return s.runReleaseTx(ctx, cmd)
}

// ApplyUsage applies usage/cost corrections to matched advisory windows without a
// reservation and persists the captured limit updates, decisions, and latest
// unreserved facts (requirement 7.7). Advisory usage and authority upgrades
// survive restarts. Each transaction hydrates the exact source-and-rule fact,
// allowing same-authority amount corrections while exact replays remain no-ops.
func (s *DurableStore) ApplyUsage(ctx context.Context, cmd app.ApplyUsageCommand) (app.ApplyUsageResult, error) {
	if s == nil || s.db == nil {
		return app.ApplyUsageResult{}, fmt.Errorf("authoritystore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return app.ApplyUsageResult{}, err
	}
	if s.closed.Load() {
		return app.ApplyUsageResult{}, unavailableError("apply_usage")
	}
	return s.runApplyUsageTx(ctx, cmd)
}

// ActiveLimit resolves the configured current row key and reads at most that
// one durable row. An unpersisted current window has zero counters.
func (s *DurableStore) ActiveLimit(ctx context.Context, q app.ActiveLimitQuery) (controlplane.AccountingLimitStatusRow, bool, error) {
	if s == nil || s.db == nil {
		return controlplane.AccountingLimitStatusRow{}, false, fmt.Errorf("authoritystore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return controlplane.AccountingLimitStatusRow{}, false, err
	}
	if s.closed.Load() {
		return controlplane.AccountingLimitStatusRow{}, false, unavailableError("active_limit")
	}
	candidate, key, ok := s.c.configuredLimitRow(q.RuleID, q.Dimensions, q.At)
	if !ok {
		return controlplane.AccountingLimitStatusRow{}, false, nil
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT row_json FROM usage_authority_limit_rows WHERE store_id = ? AND row_key = ?`, s.c.storeID, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return candidate, true, nil
	}
	if err != nil {
		return controlplane.AccountingLimitStatusRow{}, false, storeUnavailableError("active limit", err)
	}
	var row controlplane.AccountingLimitStatusRow
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return controlplane.AccountingLimitStatusRow{}, false, fmt.Errorf("authoritystore active limit decode: %w", err)
	}
	return row, true, nil
}

// LimitStatus returns bounded live limit rows from committed durable state.
func (s *DurableStore) LimitStatus(ctx context.Context, q controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	if s == nil || s.db == nil {
		return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, fmt.Errorf("authoritystore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, err
	}
	if s.closed.Load() {
		return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, unavailableError("limit_status")
	}
	return s.queryLimits(ctx, q)
}

// DecisionHistory returns bounded decision rows from committed durable state.
func (s *DurableStore) DecisionHistory(ctx context.Context, q controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	if s == nil || s.db == nil {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, fmt.Errorf("authoritystore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, err
	}
	if s.closed.Load() {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, unavailableError("decision_history")
	}
	return s.queryDecisions(ctx, q)
}

func (s *DurableStore) newMutationCore() *storeCore {
	core := newStoreCore(s.c.cfg)
	core.limitTemplates = cloneLimitTemplates(s.c.limitTemplates)
	core.ruleWindows = cloneRuleWindows(s.c.ruleWindows)
	core.decisions = nil
	core.reservations = make(map[string]*reservationRecord)
	core.resBySource = make(map[string]string)
	core.settleBySrc = make(map[string]string)
	core.releaseBySrc = make(map[string]string)
	core.applyUsageBySrc = make(map[string]struct{})
	core.unreservedUsageFacts = make(map[string]unreservedUsageFact)
	return core
}

func (s *DurableStore) String() string {
	if s == nil || s.c == nil {
		return defaultStoreID
	}
	return strings.TrimSpace(s.c.storeID)
}

func unavailableError(op string) error {
	return fmt.Errorf("authoritystore %s: %w", op, app.ErrUnavailable)
}

func storeUnavailableError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("authoritystore %s: %w: %w", op, app.ErrUnavailable, err)
}
