package journalstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	_ "modernc.org/sqlite" // register sqlite driver for durable metering journals
)

// DurableConfig configures a Bun-backed metering journal.
type DurableConfig struct {
	StoreID         string
	DefaultPageSize int
	MaxPageSize     int
	Now             func() time.Time
}

// DurableStore persists metering facts via Bun (SQLite or Postgres).
type DurableStore struct {
	cfg             DurableConfig
	db              *bun.DB
	defaultPageSize int
	maxPageSize     int
	now             func() time.Time
	nonOwning       bool
}

// Migrate applies the metering-journal schema on an admin connection.
func Migrate(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("metering/journalstore: nil context")
	}
	if db == nil {
		return fmt.Errorf("metering/journalstore: nil bun db")
	}
	return runSchemaMigrate(ctx, db)
}

// VerifySchema checks required runtime relations without applying migrations.
func VerifySchema(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("metering/journalstore: nil context")
	}
	if db == nil {
		return fmt.Errorf("metering/journalstore: nil bun db")
	}
	if db.Dialect().Name() != dialect.PG {
		for _, probe := range []string{
			`SELECT 1 FROM metering_facts WHERE 1 = 0`,
			`SELECT 1 FROM metering_fact_filters WHERE 1 = 0`,
		} {
			if _, err := db.ExecContext(ctx, probe); err != nil {
				return fmt.Errorf("metering/journalstore: schema verification failed: %w", err)
			}
		}
		return nil
	}
	for _, probe := range []string{
		`SELECT * FROM metering_facts WHERE 1 = 0`,
		`SELECT * FROM metering_fact_filters WHERE 1 = 0`,
	} {
		if _, err := db.ExecContext(ctx, probe); err != nil {
			return fmt.Errorf("metering/journalstore: schema verification failed: %w", err)
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
			query:       `SELECT name FROM bun_metering_journal_migrations WHERE name = ? LIMIT 1`,
			args:        []any{BaselineMigrationName},
			fragments:   []string{BaselineMigrationName},
		},
		{
			description: "metering_facts store-scoped source_event_key unique constraint",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'metering_facts'
  AND c.contype = 'u'
  AND c.conname = 'metering_facts_store_source_event_key_key'
LIMIT 1`,
			fragments: []string{"unique (store_id, source_event_key)"},
		},
		{
			description: "idx_metering_facts_stream_seq",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = 'idx_metering_facts_stream_seq'
LIMIT 1`,
			fragments: []string{"(stream_id, sequence)"},
		},
		{
			description: "idx_metering_facts_request",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = 'idx_metering_facts_request'
LIMIT 1`,
			fragments: []string{"(request_id)", "request_id <> ''"},
		},
		{
			description: "idx_metering_fact_filters_field",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_fact_filters'
  AND indexname = 'idx_metering_fact_filters_field'
LIMIT 1`,
			fragments: []string{"(store_id, field_name, field_value, stream_id)"},
		},
		{
			description: "metering_fact_filters.store_id",
			query: `SELECT lower(column_name) FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'metering_fact_filters'
  AND column_name = 'store_id'
LIMIT 1`,
			fragments: []string{"store_id"},
		},
	}
	for _, check := range checks {
		if err := dbinfra.VerifyPostgresQueryRowContains(ctx, db, check.description, check.query, check.args, check.fragments...); err != nil {
			return fmt.Errorf("metering/journalstore: schema verification failed: %w", err)
		}
	}
	return nil
}

// NewDurableStore migrates schema and returns a durable journal. Caller owns closing db
// via DurableStore.Close (closes the bun handle).
func NewDurableStore(ctx context.Context, db *bun.DB, cfg DurableConfig) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("metering/journalstore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("metering/journalstore: nil bun db")
	}
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("metering/journalstore: durable store id is required")
	}
	if err := runSchemaMigrate(ctx, db); err != nil {
		return nil, fmt.Errorf("metering/journalstore: migrate: %w", err)
	}
	return openStore(ctx, db, cfg, false)
}

// OpenStore opens a journal without migrations and without taking ownership
// of db. The composition root owns the shared runtime pool.
func OpenStore(ctx context.Context, db *bun.DB, cfg DurableConfig) (*DurableStore, error) {
	return openStore(ctx, db, cfg, true)
}

func openStore(ctx context.Context, db *bun.DB, cfg DurableConfig, nonOwning bool) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("metering/journalstore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("metering/journalstore: nil bun db")
	}
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("metering/journalstore: durable store id is required")
	}
	def := cfg.DefaultPageSize
	if def <= 0 {
		def = 100
	}
	max := cfg.MaxPageSize
	if max <= 0 {
		max = 500
	}
	if max < def {
		return nil, fmt.Errorf("metering/journalstore: max page size %d < default %d", max, def)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &DurableStore{
		cfg:             cfg,
		db:              db,
		defaultPageSize: def,
		maxPageSize:     max,
		now:             now,
		nonOwning:       nonOwning,
	}, nil
}

// Close closes the underlying bun DB handle.
func (s *DurableStore) Close() error {
	if s == nil || s.db == nil || s.nonOwning {
		return nil
	}
	return s.db.Close()
}

// CheckReadiness pings the database.
func (s *DurableStore) CheckReadiness(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("metering/journalstore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.PingContext(ctx)
}

// Append inserts one fact or applies idempotent/collision rules on UNIQUE source_event_key.
// SameFactReplay → no-op; same source key otherwise → ErrIdentityCollision.
func (s *DurableStore) Append(ctx context.Context, fact metering.Fact) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("metering/journalstore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := fact.Validate(); err != nil {
		return fmt.Errorf("metering/journalstore: %w", err)
	}
	cloned, err := cloneFact(fact)
	if err != nil {
		return err
	}
	if cloned.RecordedAt.IsZero() {
		cloned.RecordedAt = s.now().UTC()
	}
	key := cloned.SourceEventKey()
	lookupKeys := cloned.SourceEventLookupKeys()
	payload, err := json.Marshal(cloned)
	if err != nil {
		return fmt.Errorf("metering/journalstore: marshal payload: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("metering/journalstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.validateDurableSupersession(ctx, tx, cloned); err != nil {
		return err
	}

	existingPayload, found, lerr := lookupDurableSourcePayload(ctx, tx, s.cfg.StoreID, lookupKeys)
	if lerr != nil {
		return lerr
	}
	if found {
		var existing metering.Fact
		if uerr := json.Unmarshal([]byte(existingPayload), &existing); uerr != nil {
			return fmt.Errorf("metering/journalstore: decode existing: %w", uerr)
		}
		if metering.SameFactReplay(existing, cloned) {
			return nil
		}
		return fmt.Errorf("%w: stream_id=%q fact_id=%q stored_seq=%d new_seq=%d",
			ErrIdentityCollision, cloned.StreamID, cloned.FactID, existing.Sequence, cloned.Sequence)
	}

	_, err = tx.NewRaw(`
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope,
	request_id, a_leg_id, b_leg_id, attempt_id,
	frontend_id, backend_id, model, presence, source, authority,
	recorded_at_unix, payload_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`,
		s.cfg.StoreID,
		cloned.FactID,
		cloned.StreamID,
		cloned.Sequence,
		key,
		string(cloned.Kind),
		string(cloned.Perspective),
		string(cloned.Boundary),
		string(cloned.Lifecycle),
		cloned.Correlation.RequestID,
		cloned.Correlation.ALegID,
		cloned.Correlation.BLegID,
		cloned.Correlation.AttemptID,
		cloned.FrontendID,
		cloned.BackendID,
		cloned.Model,
		string(cloned.Presence),
		string(cloned.Source),
		string(cloned.Authority),
		cloned.RecordedAt.UnixNano(),
		string(payload),
	).Exec(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			// Postgres aborts the transaction on unique violation; release the
			// connection before resolving on a fresh read (also safe under MaxOpenConns=1).
			_ = tx.Rollback()
			return s.resolveAppendConflict(ctx, cloned)
		}
		return fmt.Errorf("metering/journalstore: insert fact: %w", err)
	}
	for _, p := range filterPairs(cloned) {
		if _, ferr := tx.NewRaw(`
INSERT INTO metering_fact_filters(store_id, fact_id, stream_id, field_name, field_value)
VALUES (?,?,?,?,?)
`, s.cfg.StoreID, cloned.FactID, cloned.StreamID, p[0], p[1]).Exec(ctx); ferr != nil {
			return fmt.Errorf("metering/journalstore: insert filter: %w", ferr)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("metering/journalstore: commit: %w", err)
	}
	return nil
}

// resolveAppendConflict reads the winning row after a unique-constraint race.
// It must not reuse the aborted insert transaction (Postgres 25P02).
func (s *DurableStore) resolveAppendConflict(ctx context.Context, cloned metering.Fact) error {
	key := cloned.SourceEventKey()
	existingPayload, found, err := lookupDurableSourcePayload(ctx, s.db, s.cfg.StoreID, cloned.SourceEventLookupKeys())
	if err != nil {
		return fmt.Errorf("metering/journalstore: insert fact unique race lookup: %w", err)
	}
	if !found {
		return fmt.Errorf("%w: source_event_key=%q (retry append)", ErrUniqueRaceMissingRow, key)
	}
	var existing metering.Fact
	if uerr := json.Unmarshal([]byte(existingPayload), &existing); uerr != nil {
		return fmt.Errorf("metering/journalstore: decode existing after unique race: %w", uerr)
	}
	if metering.SameFactReplay(existing, cloned) {
		return nil
	}
	return fmt.Errorf("%w: stream_id=%q fact_id=%q stored_seq=%d new_seq=%d",
		ErrIdentityCollision, cloned.StreamID, cloned.FactID, existing.Sequence, cloned.Sequence)
}

// lookupDurableSourcePayload finds a row via SourceEventLookupKeys order:
// canonical, phase-3.1 NUL (literal version), V0/V1 NUL aliases when effective
// V1, then IdempotencyKey.
func lookupDurableSourcePayload(ctx context.Context, q bun.IDB, storeID string, keys []string) (string, bool, error) {
	for i, key := range keys {
		if key == "" {
			continue
		}
		var payload string
		err := q.NewRaw(
			`SELECT payload_json FROM metering_facts WHERE store_id = ? AND source_event_key = ?`,
			storeID, key,
		).Scan(ctx, &payload)
		if err == nil {
			return payload, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			if i == 0 {
				return "", false, fmt.Errorf("metering/journalstore: lookup: %w", err)
			}
			return "", false, fmt.Errorf("metering/journalstore: legacy lookup: %w", err)
		}
	}
	return "", false, nil
}

func (s *DurableStore) validateDurableSupersession(ctx context.Context, tx bun.Tx, fact metering.Fact) error {
	if !fact.Kind.RequiresSupersedes() {
		return nil
	}
	type row struct {
		FactID  string `bun:"fact_id"`
		Payload string `bun:"payload_json"`
	}
	var rows []row
	err := tx.NewRaw(
		`SELECT fact_id, payload_json FROM metering_facts WHERE store_id = ?`,
		s.cfg.StoreID,
	).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("metering/journalstore: supersession scan: %w", err)
	}
	facts := make([]metering.Fact, 0, len(rows))
	byID := make(map[string]metering.Fact, len(rows))
	for _, r := range rows {
		var existing metering.Fact
		if uerr := json.Unmarshal([]byte(r.Payload), &existing); uerr != nil {
			return fmt.Errorf("metering/journalstore: decode supersession row: %w", uerr)
		}
		facts = append(facts, existing)
		byID[strings.TrimSpace(existing.FactID)] = existing
	}
	lookup := func(factID string) (metering.Fact, bool) {
		f, ok := byID[factID]
		return f, ok
	}
	return validateSupersessionGraph(fact, lookup, supersessionEdgesFromFacts(facts))
}

// List returns a bounded page filtered by indexed selective bounds.
func (s *DurableStore) List(ctx context.Context, q metering.Query) (metering.Page, error) {
	if s == nil || s.db == nil {
		return metering.Page{}, fmt.Errorf("metering/journalstore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return metering.Page{}, err
	}
	unsupported := metering.QueryUnsupported(q)
	if err := metering.ValidateQuery(q); err != nil {
		return metering.Page{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = s.defaultPageSize
	}
	if limit > s.maxPageSize {
		limit = s.maxPageSize
	}
	offset := 0
	if cur := strings.TrimSpace(q.Cursor); cur != "" {
		n, err := strconv.Atoi(cur)
		if err != nil || n < 0 {
			return metering.Page{}, fmt.Errorf("metering/journalstore: invalid cursor")
		}
		offset = n
	}

	query, args := buildDurableListQuery(s.cfg.StoreID, q, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return metering.Page{}, fmt.Errorf("metering/journalstore: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	facts := make([]metering.Fact, 0, limit)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return metering.Page{}, fmt.Errorf("metering/journalstore: scan: %w", err)
		}
		var f metering.Fact
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			return metering.Page{}, fmt.Errorf("metering/journalstore: decode: %w", err)
		}
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return metering.Page{}, err
	}
	page := metering.Page{Unsupported: append([]metering.UnsupportedFilter(nil), unsupported...)}
	if len(facts) > limit {
		page.NextCursor = strconv.Itoa(offset + limit)
		facts = facts[:limit]
	}
	page.Facts = facts
	return page, nil
}

var (
	_ metering.Recorder = (*DurableStore)(nil)
	_ metering.Querier  = (*DurableStore)(nil)
)
