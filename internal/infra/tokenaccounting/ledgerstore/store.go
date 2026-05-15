package ledgerstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite" // register sqlite driver for durable ledger stores
)

// Store persists token-accounting ledger records using Bun-supported SQL dialects.
type Store struct {
	db *bun.DB
}

var _ ledger.Recorder = (*Store)(nil)

func NewContext(ctx context.Context, db *bun.DB) (*Store, error) {
	if ctx == nil {
		return nil, fmt.Errorf("tokenaccounting/ledgerstore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("tokenaccounting/ledgerstore: nil bun db")
	}
	if err := runSchemaMigrate(ctx, db); err != nil {
		return nil, fmt.Errorf("tokenaccounting/ledgerstore: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Record(ctx context.Context, record ledger.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ledger.ValidateRecord(record); err != nil {
		return err
	}
	metadata, err := json.Marshal(record.Metadata)
	if err != nil {
		return fmt.Errorf("tokenaccounting/ledgerstore: marshal metadata: %w", err)
	}
	_, err = s.db.NewRaw(`
INSERT INTO token_accounting_ledger_records(
	request_id, attempt_id, backend, model, plane,
	input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, total_tokens,
	metadata_json, created_at_unix, unavailable_reason, failure_reason
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`,
		record.RequestID, record.AttemptID, record.Backend, record.Model, string(record.Plane),
		record.InputTokens, record.OutputTokens, record.CacheReadTokens, record.CacheWriteTokens,
		record.ReasoningTokens, record.TotalTokens, string(metadata), record.CreatedAt.UnixNano(),
		record.UnavailableReason, record.FailureReason,
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("tokenaccounting/ledgerstore: insert record: %w", err)
	}
	return nil
}

func (s *Store) ListByRequest(ctx context.Context, requestID string) ([]ledger.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.list(ctx, `WHERE request_id = ? ORDER BY id ASC`, requestID)
}

func (s *Store) ListByAttempt(ctx context.Context, requestID, attemptID string) ([]ledger.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.list(ctx, `WHERE request_id = ? AND attempt_id = ? ORDER BY id ASC`, requestID, attemptID)
}

func (s *Store) list(ctx context.Context, where string, args ...any) (out []ledger.Record, err error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT request_id, attempt_id, backend, model, plane,
	input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, total_tokens,
	metadata_json, created_at_unix, unavailable_reason, failure_reason
FROM token_accounting_ledger_records `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("tokenaccounting/ledgerstore: query records: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			if err == nil {
				err = fmt.Errorf("tokenaccounting/ledgerstore: close rows: %w", cerr)
			} else {
				err = errors.Join(err, fmt.Errorf("tokenaccounting/ledgerstore: close rows: %w", cerr))
			}
		}
	}()
	out = []ledger.Record{}
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tokenaccounting/ledgerstore: iterate records: %w", err)
	}
	return out, nil
}

func scanRecord(rows *sql.Rows) (ledger.Record, error) {
	var record ledger.Record
	var plane string
	var metadataJSON string
	var createdAtUnix int64
	if err := rows.Scan(
		&record.RequestID, &record.AttemptID, &record.Backend, &record.Model, &plane,
		&record.InputTokens, &record.OutputTokens, &record.CacheReadTokens, &record.CacheWriteTokens,
		&record.ReasoningTokens, &record.TotalTokens, &metadataJSON, &createdAtUnix,
		&record.UnavailableReason, &record.FailureReason,
	); err != nil {
		return ledger.Record{}, fmt.Errorf("tokenaccounting/ledgerstore: scan record: %w", err)
	}
	var metadata lipapi.UsageAccountingMetadata
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return ledger.Record{}, fmt.Errorf("tokenaccounting/ledgerstore: unmarshal metadata: %w", err)
	}
	record.Plane = lipapi.UsagePlane(plane)
	record.Metadata = metadata
	record.CreatedAt = time.Unix(0, createdAtUnix).UTC()
	return record, nil
}
