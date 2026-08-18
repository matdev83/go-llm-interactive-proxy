package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// ErrUsageAppendDrainBlocked means historical central transport work could not
// be proven delivered. Callers must reconcile it before any destructive schema
// migration; rows are intentionally preserved.
var ErrUsageAppendDrainBlocked = errors.New("billingstore: usage append outbox drain blocked")

// DrainUsageAppendOutbox performs the preserve-or-block part of Phase 2
// cutover. It validates and replays every pending row into the current
// central call/leg records, treats an identical central replay as success, and
// refuses to claim completion for malformed, conflicting, or unresolved rows.
// Schema deletion is deliberately separate and must run its own dialect-locked
// migration critical section after this proof.
func (s *DurableStore) DrainUsageAppendOutbox(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("%w: nil store", ErrUsageAppendDrainBlocked)
	}
	for {
		work, err := s.listAllPendingUsageAppendWork(ctx, 256)
		if err != nil {
			return fmt.Errorf("%w: read pending rows: %v", ErrUsageAppendDrainBlocked, err)
		}
		if len(work) == 0 {
			break
		}
		for _, item := range work {
			var replayErr error
			switch item.Kind {
			case billing.UsageAppendCall:
				if item.Call == nil {
					replayErr = errors.New("nil call payload")
				} else {
					replayErr = s.AppendCall(ctx, *item.Call)
				}
			case billing.UsageAppendLeg:
				if item.Leg == nil {
					replayErr = errors.New("nil leg payload")
				} else {
					replayErr = s.AppendLeg(ctx, *item.Leg)
				}
			default:
				replayErr = fmt.Errorf("unsupported kind %q", item.Kind)
			}
			if replayErr != nil {
				return fmt.Errorf("%w: key %s: %v", ErrUsageAppendDrainBlocked, item.Key, replayErr)
			}
			if err := s.MarkUsageAppendProcessed(ctx, item.Key); err != nil {
				return fmt.Errorf("%w: mark %s processed: %v", ErrUsageAppendDrainBlocked, item.Key, err)
			}
		}
	}
	if err := s.reconcileProcessedUsageAppend(ctx); err != nil {
		return fmt.Errorf("%w: reconcile processed rows: %v", ErrUsageAppendDrainBlocked, err)
	}
	var unresolved int
	if err := s.db.NewRaw(`SELECT COUNT(1) FROM usage_append_outbox WHERE status NOT IN ('processed')`).Scan(ctx, &unresolved); err != nil {
		return fmt.Errorf("%w: verify unresolved rows: %v", ErrUsageAppendDrainBlocked, err)
	}
	if unresolved != 0 {
		return fmt.Errorf("%w: %d unresolved rows remain", ErrUsageAppendDrainBlocked, unresolved)
	}
	return nil
}

func (s *DurableStore) reconcileProcessedUsageAppend(ctx context.Context) error {
	type row struct {
		Key     string `bun:"append_key"`
		Kind    string `bun:"kind"`
		Payload string `bun:"payload_json"`
	}
	var rows []row
	if err := s.db.NewRaw(`SELECT append_key, kind, payload_json FROM usage_append_outbox WHERE status = 'processed' ORDER BY append_key`).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, row := range rows {
		switch billing.UsageAppendKind(row.Kind) {
		case billing.UsageAppendCall:
			var record billing.CallUsageRecord
			if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
				return fmt.Errorf("processed call %s: %w", row.Key, err)
			}
			sealed, err := record.Seal()
			if err != nil {
				return fmt.Errorf("processed call %s: %w", row.Key, err)
			}
			if sealed.Key != row.Key {
				return fmt.Errorf("processed call identity mismatch: %s", row.Key)
			}
			if err := s.AppendCall(ctx, sealed); err != nil {
				return fmt.Errorf("processed call %s replay: %w", row.Key, err)
			}
		case billing.UsageAppendLeg:
			var record billing.CallLegUsageRecord
			if err := json.Unmarshal([]byte(row.Payload), &record); err != nil {
				return fmt.Errorf("processed leg %s: %w", row.Key, err)
			}
			sealed, err := record.Seal()
			if err != nil {
				return fmt.Errorf("processed leg %s: %w", row.Key, err)
			}
			if sealed.Key != row.Key {
				return fmt.Errorf("processed leg identity mismatch: %s", row.Key)
			}
			if err := s.AppendLeg(ctx, sealed); err != nil {
				return fmt.Errorf("processed leg %s replay: %w", row.Key, err)
			}
		default:
			return fmt.Errorf("processed row %s has unsupported kind %q", row.Key, row.Kind)
		}
	}
	return nil
}

// UsageAppendOutboxUnresolved reports the destructive-migration proof input.
// It is intentionally a read-only query and does not silently classify failed
// or malformed work as obsolete.
func (s *DurableStore) UsageAppendOutboxUnresolved(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("billingstore: nil store")
	}
	var count int
	if err := s.db.NewRaw(`SELECT COUNT(1) FROM usage_append_outbox WHERE status NOT IN ('processed')`).Scan(ctx, &count); err != nil {
		// After an explicit successful cutover the source schema is gone; its
		// unresolved proof is therefore the empty set.
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "no such table") || strings.Contains(lower, "does not exist") {
			return 0, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	return count, nil
}
