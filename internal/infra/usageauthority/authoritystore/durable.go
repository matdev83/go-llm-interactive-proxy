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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/uptrace/bun"
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
	st := &DurableStore{
		db: db,
		c:  newStoreCore(cfg),
	}
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
	if db == nil {
		return nil
	}
	return db.Close()
}

// Migrate creates the durable authority-store tables. DDL has no placeholders,
// so it is dialect-agnostic; it is executed through Bun for consistency with
// the rest of the adapter and to honor query hooks.
func Migrate(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("authoritystore: nil context")
	}
	if db == nil {
		return fmt.Errorf("authoritystore: nil db")
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS usage_authority_state (
			store_id TEXT NOT NULL PRIMARY KEY,
			readiness_json TEXT NOT NULL,
			next_decision_seq INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_limit_rows (
			store_id TEXT NOT NULL,
			row_key TEXT NOT NULL,
			row_json TEXT NOT NULL,
			PRIMARY KEY (store_id, row_key)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_decisions (
			store_id TEXT NOT NULL,
			decision_seq INTEGER NOT NULL,
			source_key TEXT NOT NULL,
			row_json TEXT NOT NULL,
			PRIMARY KEY (store_id, decision_seq),
			UNIQUE (store_id, source_key)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_decision_filters (
			store_id TEXT NOT NULL,
			decision_seq INTEGER NOT NULL,
			field_name TEXT NOT NULL,
			field_value TEXT NOT NULL,
			PRIMARY KEY (store_id, decision_seq, field_name)
		)`,
		`CREATE INDEX IF NOT EXISTS usage_authority_decision_filters_lookup
			ON usage_authority_decision_filters(store_id, field_name, field_value, decision_seq)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_limit_filters (
			store_id TEXT NOT NULL,
			row_key TEXT NOT NULL,
			field_name TEXT NOT NULL,
			field_value TEXT NOT NULL,
			PRIMARY KEY (store_id, row_key, field_name)
		)`,
		`CREATE INDEX IF NOT EXISTS usage_authority_limit_filters_lookup
			ON usage_authority_limit_filters(store_id, field_name, field_value, row_key)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_reservations (
			store_id TEXT NOT NULL,
			reservation_key TEXT NOT NULL,
			source_key TEXT NOT NULL,
			record_json TEXT NOT NULL,
			PRIMARY KEY (store_id, reservation_key),
			UNIQUE (store_id, source_key)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_authority_unreserved_usage_facts (
			store_id TEXT NOT NULL,
			fact_key TEXT NOT NULL,
			record_json TEXT NOT NULL,
			PRIMARY KEY (store_id, fact_key)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return storeUnavailableError("migrate", err)
		}
	}
	return nil
}

// CheckReadiness returns the current posture and surfaces backing failures without leaking driver details.
func (s *DurableStore) CheckReadiness(ctx context.Context) (domain.AuthorityStatus, error) {
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
