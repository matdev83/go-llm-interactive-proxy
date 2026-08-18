// Package billingspool owns the process-local durable terminal handoff.
package billingspool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const (
	defaultMaxPendingRecords  = 100000
	defaultMaxPayloadBytes    = 512 << 20
	defaultMaxDatabaseBytes   = 1 << 30
	defaultMinFreeDiskBytes   = 256 << 20
	defaultProcessedRetention = 24 * time.Hour
	defaultClaimTimeout       = 2 * time.Minute
	defaultBackoffBase        = time.Second
	defaultBackoffMax         = time.Hour
	defaultDrainBatchSize     = 256
)

var (
	ErrPendingCapacity      = errors.New("billingspool: pending record capacity exhausted")
	ErrPayloadCapacity      = errors.New("billingspool: pending payload capacity exhausted")
	ErrDatabaseCapacity     = errors.New("billingspool: database capacity exhausted")
	ErrFreeDiskCapacity     = errors.New("billingspool: free disk capacity exhausted")
	ErrFingerprintConflict  = errors.New("billingspool: stable key fingerprint conflict")
	ErrSpoolClosed          = errors.New("billingspool: spool is closed")
	ErrWorkerAlreadyRunning = errors.New("billingspool: flusher already running")
)

type HealthState string

const (
	HealthReady    HealthState = "ready"
	HealthDegraded HealthState = "degraded"
	HealthFull     HealthState = "full"
)

type Health struct {
	PendingRecords         int
	PendingPayloadBytes    int64
	DatabaseBytes          int64
	LiveDatabaseBytes      int64
	FreeDiskBytes          int64
	OldestPendingAge       time.Duration
	ErrorRows              int
	AppendCapacityFailures uint64
	LastDeliveryError      string
	ProbeError             string
	State                  HealthState
}

type Config struct {
	Path                   string
	DB                     *bun.DB
	MaxPendingRecords      int
	MaxPendingPayloadBytes int64
	MaxDatabaseBytes       int64
	MinFreeDiskBytes       int64
	ProcessedRetention     time.Duration
	ClaimTimeout           time.Duration
	BackoffBase            time.Duration
	BackoffMax             time.Duration
	Now                    func() time.Time
	// CommitAcknowledgedHook is test-only fault injection. It runs after the
	// SQLite commit and before Append returns, making the acknowledgement edge
	// observable without weakening the commit-before-success contract.
	CommitAcknowledgedHook func() error
	// FreeDiskBytes is an injectable filesystem watermark probe. Production
	// callers may provide the platform-specific durable-state probe; nil means
	// the database-size and pending-payload bounds remain authoritative.
	FreeDiskBytes func() int64
}

var _ billing.TerminalUsageSink = (*Spool)(nil)

type Spool struct {
	db                     *bun.DB
	ownsDB                 bool
	path                   string
	sink                   billing.TerminalUsageSink
	cfg                    Config
	stateMu                sync.Mutex
	databaseMu             sync.RWMutex
	deliveryMu             sync.Mutex
	cancel                 context.CancelFunc
	done                   chan struct{}
	wake                   chan struct{}
	closed                 bool
	closing                bool
	appendCapacityFailures atomic.Uint64
	lastDelivery           atomic.Value // string
}

type spoolRow struct {
	SpoolKey           string     `bun:"spool_key"`
	Kind               string     `bun:"kind"`
	RecordKey          string     `bun:"record_key"`
	FingerprintVersion int        `bun:"fingerprint_version"`
	Fingerprint        string     `bun:"fingerprint"`
	PayloadJSON        string     `bun:"payload_json"`
	PayloadBytes       int64      `bun:"payload_bytes"`
	Status             string     `bun:"status"`
	AttemptCount       int        `bun:"attempt_count"`
	NextAttemptAt      time.Time  `bun:"next_attempt_at"`
	ClaimedAt          *time.Time `bun:"claimed_at"`
	LastError          string     `bun:"last_error"`
	EnqueuedAt         time.Time  `bun:"enqueued_at"`
	UpdatedAt          time.Time  `bun:"updated_at"`
}

const (
	kindCall         = "call"
	kindLeg          = "leg"
	statusPending    = "pending"
	statusDelivering = "delivering"
	statusProcessed  = "processed"
	statusError      = "error"
)

func Open(ctx context.Context, cfg Config, sink billing.TerminalUsageSink) (*Spool, error) {
	if ctx == nil {
		return nil, errors.New("billingspool: nil context")
	}
	if sink == nil {
		return nil, errors.New("billingspool: central sink is required")
	}
	cfg = normalizeConfig(cfg)
	sp := &Spool{sink: sink, cfg: cfg, path: strings.TrimSpace(cfg.Path), wake: make(chan struct{}, 1)}
	if cfg.DB != nil {
		sp.db = cfg.DB
	} else {
		if sp.path == "" {
			return nil, errors.New("billingspool: stable path is required")
		}
		dir := filepath.Dir(sp.path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("billingspool: create state directory: %w", err)
		}
		_ = os.Chmod(dir, 0o700)
		db, err := dbinfra.OpenSQLiteBun(ctx, sp.path)
		if err != nil {
			return nil, fmt.Errorf("billingspool: open sqlite: %w", err)
		}
		sp.db, sp.ownsDB = db, true
		_ = os.Chmod(sp.path, 0o600)
	}
	if sp.ownsDB {
		// SQLite PRAGMAs such as synchronous, busy_timeout and foreign_keys are
		// connection-local. The production file store therefore has one stable
		// pooled connection; every production connection receives configure's
		// durability settings rather than only whichever connection first opens.
		sp.db.DB.SetMaxOpenConns(1)
	}
	if err := sp.configure(ctx); err != nil {
		_ = sp.closeDB()
		return nil, err
	}
	if err := sp.reclaim(ctx); err != nil {
		_ = sp.closeDB()
		return nil, err
	}
	return sp, nil
}

func New(ctx context.Context, cfg Config, sink billing.TerminalUsageSink) (*Spool, error) {
	return Open(ctx, cfg, sink)
}

// ValidateStablePath rejects volatile OS temp directories for durable spool state.
func ValidateStablePath(path string) error {
	clean := filepath.Clean(path)
	for _, candidate := range []string{os.TempDir(), "/tmp", "/var/tmp", "/dev/shm"} {
		if candidate == "" {
			continue
		}
		cand := filepath.Clean(candidate)
		if clean == cand || strings.HasPrefix(clean, cand+string(filepath.Separator)) {
			return fmt.Errorf("billingspool: path %q is inside volatile temp directory %q", path, candidate)
		}
	}
	return nil
}

func normalizeConfig(c Config) Config {
	if c.MaxPendingRecords <= 0 {
		c.MaxPendingRecords = defaultMaxPendingRecords
	}
	if c.MaxPendingPayloadBytes <= 0 {
		c.MaxPendingPayloadBytes = defaultMaxPayloadBytes
	}
	if c.MaxDatabaseBytes <= 0 {
		c.MaxDatabaseBytes = defaultMaxDatabaseBytes
	}
	if c.MinFreeDiskBytes <= 0 {
		c.MinFreeDiskBytes = defaultMinFreeDiskBytes
	}
	if c.ProcessedRetention <= 0 {
		c.ProcessedRetention = defaultProcessedRetention
	}
	if c.ClaimTimeout <= 0 {
		c.ClaimTimeout = defaultClaimTimeout
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = defaultBackoffBase
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = defaultBackoffMax
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

func (s *Spool) configure(ctx context.Context) error {
	if s.db == nil {
		return errors.New("billingspool: nil database")
	}
	if s.db.Dialect().Name() != dialect.SQLite {
		return errors.New("billingspool: spool requires SQLite")
	}
	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `PRAGMA busy_timeout=5000`, `PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS terminal_usage_spool (spool_key TEXT PRIMARY KEY, kind TEXT NOT NULL, record_key TEXT NOT NULL, fingerprint_version INTEGER NOT NULL, fingerprint TEXT NOT NULL, payload_json TEXT NOT NULL, payload_bytes INTEGER NOT NULL, status TEXT NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0, next_attempt_at TIMESTAMP NOT NULL, claimed_at TIMESTAMP NULL, last_error TEXT NOT NULL DEFAULT '', enqueued_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL, UNIQUE(kind, record_key))`,
		`CREATE INDEX IF NOT EXISTS idx_terminal_usage_spool_pending ON terminal_usage_spool(status, next_attempt_at, enqueued_at, spool_key)`,
	} {
		if _, err := s.db.NewRaw(stmt).Exec(ctx); err != nil {
			return fmt.Errorf("billingspool: configure sqlite: %w", err)
		}
	}
	return nil
}

func (s *Spool) reclaim(ctx context.Context) error {
	_, err := s.db.NewRaw(`UPDATE terminal_usage_spool SET status = ?, claimed_at = NULL, updated_at = ? WHERE status = ?`, statusPending, s.now(), statusDelivering).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingspool: reclaim delivering rows: %w", err)
	}
	return nil
}

func (s *Spool) now() time.Time { return s.cfg.Now().UTC() }
func (s *Spool) AppendCall(ctx context.Context, r billing.CallUsageRecord) error {
	return s.append(ctx, kindCall, func() (string, string, string, error) {
		sealed, err := r.Seal()
		if err != nil {
			return "", "", "", err
		}
		b, err := json.Marshal(sealed)
		return sealed.Key, sealed.Fingerprint, string(b), err
	})
}
func (s *Spool) AppendLeg(ctx context.Context, r billing.CallLegUsageRecord) error {
	return s.append(ctx, kindLeg, func() (string, string, string, error) {
		sealed, err := r.Seal()
		if err != nil {
			return "", "", "", err
		}
		b, err := json.Marshal(sealed)
		return sealed.Key, sealed.Fingerprint, string(b), err
	})
}

func (s *Spool) append(ctx context.Context, kind string, payload func() (string, string, string, error)) error {
	if s == nil || s.db == nil {
		return ErrSpoolClosed
	}
	if ctx == nil {
		return errors.New("billingspool: nil context")
	}
	recKey, fp, jsonPayload, err := payload()
	if err != nil {
		return err
	}
	if recKey == "" || fp == "" {
		return billing.ErrInvalidRecord
	}
	bytes := int64(len([]byte(jsonPayload)))
	s.databaseMu.RLock()
	defer s.databaseMu.RUnlock()
	if s.isClosedOrClosing() {
		return ErrSpoolClosed
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("billingspool: begin append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var existing spoolRow
	err = tx.NewRaw(`SELECT spool_key, kind, record_key, fingerprint_version, fingerprint, payload_json, payload_bytes, status, attempt_count, next_attempt_at, claimed_at, last_error, enqueued_at, updated_at FROM terminal_usage_spool WHERE kind = ? AND record_key = ?`, kind, recKey).Scan(ctx, &existing)
	if err == nil {
		if existing.FingerprintVersion != 1 || existing.Fingerprint != fp {
			return ErrFingerprintConflict
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("billingspool: commit replay: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("billingspool: lookup append: %w", err)
	}
	var capacity struct {
		Count        int   `bun:"count"`
		PayloadTotal int64 `bun:"payload_total"`
	}
	if err := tx.NewRaw(`SELECT COUNT(1) AS count, COALESCE(SUM(payload_bytes),0) AS payload_total FROM terminal_usage_spool WHERE status IN (?,?)`, statusPending, statusDelivering).Scan(ctx, &capacity); err != nil {
		return err
	}
	count, payloadTotal := capacity.Count, capacity.PayloadTotal
	if count >= s.cfg.MaxPendingRecords {
		s.appendCapacityFailures.Add(1)
		return ErrPendingCapacity
	}
	if payloadTotal+bytes > s.cfg.MaxPendingPayloadBytes {
		s.appendCapacityFailures.Add(1)
		return ErrPayloadCapacity
	}
	if err := s.checkDiskCapacity(ctx, tx); err != nil {
		s.appendCapacityFailures.Add(1)
		return err
	}
	key := kind + ":" + recKey
	_, err = tx.NewRaw(`INSERT INTO terminal_usage_spool(spool_key,kind,record_key,fingerprint_version,fingerprint,payload_json,payload_bytes,status,attempt_count,next_attempt_at,claimed_at,last_error,enqueued_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, key, kind, recKey, 1, fp, jsonPayload, bytes, statusPending, 0, now, nil, "", now, now).Exec(ctx)
	if err != nil {
		// A concurrent identical append can win the race between our SELECT
		// and INSERT. Replay is only safe when the stored row carries the same
		// fingerprint; surface the typed conflict so the flusher can quarantine
		// it instead of treating a duplicate as a transient delivery failure.
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			var stored spoolRow
			if scanErr := tx.NewRaw(`SELECT fingerprint_version, fingerprint FROM terminal_usage_spool WHERE kind = ? AND record_key = ?`, kind, recKey).Scan(ctx, &stored); scanErr == nil && stored.FingerprintVersion == 1 && stored.Fingerprint == fp {
				if commitErr := tx.Commit(); commitErr != nil {
					return fmt.Errorf("billingspool: commit replay: %w", commitErr)
				}
				return nil
			}
			return fmt.Errorf("%w: %s", ErrFingerprintConflict, recKey)
		}
		return fmt.Errorf("billingspool: insert append: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("billingspool: commit append: %w", err)
	}
	s.signalWake()
	if s.cfg.CommitAcknowledgedHook != nil {
		if err := s.cfg.CommitAcknowledgedHook(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) checkDiskCapacity(ctx context.Context, q bun.IDB) error {
	if liveBytes, err := liveDatabaseBytes(ctx, q); err != nil {
		return fmt.Errorf("billingspool: database capacity probe: %w", err)
	} else if liveBytes >= s.cfg.MaxDatabaseBytes {
		return ErrDatabaseCapacity
	}
	if s.path == "" && s.cfg.FreeDiskBytes == nil {
		return nil
	}
	free, err := s.freeDiskBytes()
	if err != nil {
		return fmt.Errorf("billingspool: free disk probe: %w", err)
	}
	if free < s.cfg.MinFreeDiskBytes {
		return ErrFreeDiskCapacity
	}
	return nil
}

func (s *Spool) ProcessOnce(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrSpoolClosed
	}
	if ctx == nil {
		return errors.New("billingspool: nil context")
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if s.isClosedOrClosing() {
		return ErrSpoolClosed
	}
	_, err := s.processOnce(ctx)
	return err
}

func (s *Spool) processOnce(ctx context.Context) (bool, error) {
	if err := s.prune(ctx); err != nil {
		return false, fmt.Errorf("billingspool: prune: %w", err)
	}
	row, ok, err := s.claim(ctx)
	if err != nil || !ok {
		if err != nil {
			return false, fmt.Errorf("billingspool: claim: %w", err)
		}
		return false, nil
	}
	var deliveryErr error
	switch row.Kind {
	case kindCall:
		var r billing.CallUsageRecord
		if err := json.Unmarshal([]byte(row.PayloadJSON), &r); err != nil {
			deliveryErr = fmt.Errorf("billingspool: decode call payload: %w", err)
		} else if sealed, err := r.Seal(); err != nil {
			deliveryErr = err
		} else if err := billing.CheckCallUsageReplay(sealed, r); err != nil {
			deliveryErr = err
		} else {
			deliveryErr = sinkCall(s.sink, ctx, r)
		}
	case kindLeg:
		var r billing.CallLegUsageRecord
		if err := json.Unmarshal([]byte(row.PayloadJSON), &r); err != nil {
			deliveryErr = fmt.Errorf("billingspool: decode leg payload: %w", err)
		} else if sealed, err := r.Seal(); err != nil {
			deliveryErr = err
		} else if err := billing.CheckCallLegUsageReplay(sealed, r); err != nil {
			deliveryErr = err
		} else {
			deliveryErr = sinkLeg(s.sink, ctx, r)
		}
	default:
		deliveryErr = fmt.Errorf("billingspool: unknown row kind %q", row.Kind)
	}
	if deliveryErr == nil {
		return true, s.markProcessed(ctx, row.SpoolKey)
	}
	s.lastDelivery.Store(deliveryErr.Error())
	if errors.Is(deliveryErr, billing.ErrReplayConflict) || errors.Is(deliveryErr, ErrFingerprintConflict) {
		return true, s.markError(ctx, row, deliveryErr)
	}
	if err := s.deferRow(ctx, row, deliveryErr); err != nil {
		return true, errors.Join(deliveryErr, err)
	}
	return true, deliveryErr
}
func sinkCall(s billing.TerminalUsageSink, ctx context.Context, r billing.CallUsageRecord) error {
	return s.AppendCall(ctx, r)
}
func sinkLeg(s billing.TerminalUsageSink, ctx context.Context, r billing.CallLegUsageRecord) error {
	return s.AppendLeg(ctx, r)
}

func (s *Spool) claim(ctx context.Context) (spoolRow, bool, error) {
	s.databaseMu.RLock()
	defer s.databaseMu.RUnlock()
	if s.isClosedOrClosing() {
		return spoolRow{}, false, ErrSpoolClosed
	}
	now := s.now()
	stale := now.Add(-s.cfg.ClaimTimeout)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return spoolRow{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var row spoolRow
	err = tx.NewRaw(`SELECT spool_key,kind,record_key,fingerprint_version,fingerprint,payload_json,payload_bytes,status,attempt_count,next_attempt_at,claimed_at,last_error,enqueued_at,updated_at FROM terminal_usage_spool WHERE (status = ? AND next_attempt_at <= ?) OR (status = ? AND claimed_at <= ?) ORDER BY enqueued_at,spool_key LIMIT 1`, statusPending, now, statusDelivering, stale).Scan(ctx, &row)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return row, false, fmt.Errorf("billingspool: claim row: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return spoolRow{}, false, nil
	}
	if err != nil {
		return row, false, err
	}
	_, err = tx.NewRaw(`UPDATE terminal_usage_spool SET status=?,claimed_at=?,updated_at=? WHERE spool_key=?`, statusDelivering, now, now, row.SpoolKey).Exec(ctx)
	if err != nil {
		return row, false, err
	}
	if err := tx.Commit(); err != nil {
		return row, false, err
	}
	return row, true, nil
}
func (s *Spool) markProcessed(ctx context.Context, key string) error {
	s.databaseMu.RLock()
	defer s.databaseMu.RUnlock()
	if s.isClosedOrClosing() {
		return ErrSpoolClosed
	}
	_, err := s.db.NewRaw(`UPDATE terminal_usage_spool SET status=?,claimed_at=NULL,last_error='',updated_at=? WHERE spool_key=?`, statusProcessed, s.now(), key).Exec(ctx)
	if err != nil {
		return fmt.Errorf("billingspool: mark processed: %w", err)
	}
	return nil
}
func (s *Spool) markError(ctx context.Context, row spoolRow, e error) error {
	s.databaseMu.RLock()
	defer s.databaseMu.RUnlock()
	if s.isClosedOrClosing() {
		return ErrSpoolClosed
	}
	_, err := s.db.NewRaw(`UPDATE terminal_usage_spool SET status=?,claimed_at=NULL,last_error=?,attempt_count=attempt_count+1,updated_at=? WHERE spool_key=?`, statusError, e.Error(), s.now(), row.SpoolKey).Exec(ctx)
	return errors.Join(e, err)
}
func (s *Spool) deferRow(ctx context.Context, row spoolRow, e error) error {
	s.databaseMu.RLock()
	defer s.databaseMu.RUnlock()
	if s.isClosedOrClosing() {
		return ErrSpoolClosed
	}
	attempt := row.AttemptCount + 1
	delay := s.cfg.BackoffBase
	for i := 1; i < attempt && delay < s.cfg.BackoffMax; i++ {
		delay *= 2
	}
	if delay > s.cfg.BackoffMax {
		delay = s.cfg.BackoffMax
	}
	_, err := s.db.NewRaw(`UPDATE terminal_usage_spool SET status=?,claimed_at=NULL,last_error=?,attempt_count=?,next_attempt_at=?,updated_at=? WHERE spool_key=?`, statusPending, e.Error(), attempt, s.now().Add(delay), s.now(), row.SpoolKey).Exec(ctx)
	return err
}
func (s *Spool) prune(ctx context.Context) error {
	s.databaseMu.RLock()
	defer s.databaseMu.RUnlock()
	if s.isClosedOrClosing() {
		return ErrSpoolClosed
	}
	cut := s.now().Add(-s.cfg.ProcessedRetention)
	_, err := s.db.NewRaw(`DELETE FROM terminal_usage_spool WHERE status=? AND updated_at < ?`, statusProcessed, cut).Exec(ctx)
	return err
}

func (s *Spool) PendingCount() int { h := s.Health(); return h.PendingRecords }
func (s *Spool) Health() Health {
	h := Health{State: HealthReady}
	if s == nil || s.db == nil {
		h.State = HealthDegraded
		h.ProbeError = "spool database is unavailable"
		return h
	}
	s.databaseMu.RLock()
	defer s.databaseMu.RUnlock()
	if s.isClosedOrClosing() {
		h.State = HealthDegraded
		h.ProbeError = "spool is closed"
		return h
	}
	var stats struct {
		Pending int       `bun:"pending"`
		Payload int64     `bun:"payload"`
		Errors  int       `bun:"errors"`
		Oldest  time.Time `bun:"oldest"`
	}
	if err := s.db.NewRaw(`SELECT COALESCE(SUM(CASE WHEN status IN (?,?) THEN 1 ELSE 0 END),0) AS pending,COALESCE(SUM(CASE WHEN status IN (?,?) THEN payload_bytes ELSE 0 END),0) AS payload,COALESCE(SUM(CASE WHEN status=? THEN 1 ELSE 0 END),0) AS errors,MIN(CASE WHEN status IN (?,?) THEN enqueued_at ELSE NULL END) AS oldest FROM terminal_usage_spool`, statusPending, statusDelivering, statusPending, statusDelivering, statusError, statusPending, statusDelivering).Scan(context.Background(), &stats); err != nil {
		h.State = HealthDegraded
		h.ProbeError = err.Error()
	}
	h.PendingRecords, h.PendingPayloadBytes, h.ErrorRows = stats.Pending, stats.Payload, stats.Errors
	if databaseBytes, err := s.databaseBytesWithError(); err != nil {
		h.State = HealthDegraded
		h.ProbeError = err.Error()
	} else {
		h.DatabaseBytes = databaseBytes
	}
	if liveBytes, err := liveDatabaseBytes(context.Background(), s.db); err != nil {
		h.State = HealthDegraded
		h.ProbeError = err.Error()
	} else {
		h.LiveDatabaseBytes = liveBytes
	}
	if s.path != "" {
		if free, err := s.freeDiskBytes(); err != nil {
			h.State = HealthDegraded
			h.ProbeError = err.Error()
		} else {
			h.FreeDiskBytes = free
			if free < s.cfg.MinFreeDiskBytes {
				h.State = HealthFull
			}
		}
	}
	if !stats.Oldest.IsZero() {
		h.OldestPendingAge = s.now().Sub(stats.Oldest)
		if h.OldestPendingAge < 0 {
			h.OldestPendingAge = 0
		}
	}
	if v := s.lastDelivery.Load(); v != nil {
		h.LastDeliveryError = v.(string)
	}
	h.AppendCapacityFailures = s.appendCapacityFailures.Load()
	if h.ErrorRows > 0 || h.LastDeliveryError != "" || h.ProbeError != "" {
		h.State = HealthDegraded
	}
	if h.PendingRecords >= s.cfg.MaxPendingRecords || h.PendingPayloadBytes >= s.cfg.MaxPendingPayloadBytes {
		h.State = HealthFull
	}
	if h.LiveDatabaseBytes >= s.cfg.MaxDatabaseBytes {
		h.State = HealthFull
	}
	return h
}

func liveDatabaseBytes(ctx context.Context, q bun.IDB) (int64, error) {
	var pageSize, pageCount, freelistCount int64
	if err := q.NewRaw(`PRAGMA page_size`).Scan(ctx, &pageSize); err != nil {
		return 0, err
	}
	if err := q.NewRaw(`PRAGMA page_count`).Scan(ctx, &pageCount); err != nil {
		return 0, err
	}
	if err := q.NewRaw(`PRAGMA freelist_count`).Scan(ctx, &freelistCount); err != nil {
		return 0, err
	}
	if pageSize < 0 || pageCount < 0 || freelistCount < 0 || freelistCount > pageCount {
		return 0, fmt.Errorf("invalid sqlite page accounting: page_size=%d page_count=%d freelist_count=%d", pageSize, pageCount, freelistCount)
	}
	return (pageCount - freelistCount) * pageSize, nil
}

func (s *Spool) databaseBytes() int64 {
	total, _ := s.databaseBytesWithError()
	return total
}

func (s *Spool) databaseBytesWithError() (int64, error) {
	if s.path == "" {
		return 0, nil
	}
	var total int64
	for _, path := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		st, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		total += st.Size()
	}
	return total, nil
}

func (s *Spool) freeDiskBytes() (int64, error) {
	if s.cfg.FreeDiskBytes != nil {
		return s.cfg.FreeDiskBytes(), nil
	}
	if s.path == "" {
		return 0, nil
	}
	return filesystemFreeBytes(filepath.Dir(s.path))
}

func (s *Spool) Start(ctx context.Context) error {
	if s == nil {
		return ErrSpoolClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.stateMu.Lock()
	if s.closed || s.closing {
		s.stateMu.Unlock()
		return ErrSpoolClosed
	}
	if s.done != nil {
		s.stateMu.Unlock()
		return nil
	}
	wctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	done := make(chan struct{})
	s.done = done
	s.stateMu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			s.drain(wctx)
			select {
			case <-wctx.Done():
				return
			case <-s.wake:
			case <-ticker.C:
			}
		}
	}()
	s.signalWake()
	return nil
}

func (s *Spool) drain(ctx context.Context) {
	for i := 0; i < defaultDrainBatchSize; i++ {
		worked, err := s.processOnceLocked(ctx)
		if !worked {
			return
		}
		if err != nil && ctx.Err() != nil {
			return
		}
	}
	s.signalWake()
}

func (s *Spool) processOnceLocked(ctx context.Context) (bool, error) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if s.isClosedOrClosing() {
		return false, ErrSpoolClosed
	}
	return s.processOnce(ctx)
}

func (s *Spool) signalWake() {
	if s == nil || s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Spool) Stop(ctx context.Context) error {
	s.stateMu.Lock()
	cancel, done := s.cancel, s.done
	s.stateMu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		s.stateMu.Lock()
		if s.done == done {
			s.cancel = nil
			s.done = nil
		}
		s.stateMu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Spool) Close() error {
	if s == nil {
		return nil
	}
	_ = s.Stop(context.Background())
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return nil
	}
	s.closing = true
	s.stateMu.Unlock()
	s.databaseMu.Lock()
	defer s.databaseMu.Unlock()
	s.stateMu.Lock()
	s.closed = true
	s.stateMu.Unlock()
	return s.closeDB()
}

func (s *Spool) isClosedOrClosing() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.closed || s.closing
}
func (s *Spool) closeDB() error {
	if s.ownsDB && s.db != nil {
		return s.db.Close()
	}
	return nil
}
