package authoritystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	_ "modernc.org/sqlite" // register sqlite driver for durable authority stores
)

func (s *DurableStore) lockStateTx(ctx context.Context, tx bun.Tx, core *storeCore) error {
	query := `SELECT readiness_json, next_decision_seq FROM usage_authority_state WHERE store_id = ?`
	if s.db.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	var readinessJSON string
	if err := tx.QueryRowContext(ctx, query, s.c.storeID).Scan(&readinessJSON, &core.nextDecision); err != nil {
		return storeUnavailableError("load state", err)
	}
	if err := json.Unmarshal([]byte(readinessJSON), &core.state); err != nil {
		return fmt.Errorf("authoritystore load readiness: %w", err)
	}
	if core.nextDecision == 0 {
		core.nextDecision = 1
	}
	return nil
}

func (s *DurableStore) lockReservationTx(ctx context.Context, tx bun.Tx, core *storeCore, reservationKey, sourceKey string) (map[string]string, error) {
	query := `SELECT reservation_key, source_key, record_json FROM usage_authority_reservations WHERE store_id = ? AND (reservation_key = ? OR source_key = ?)`
	if s.db.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	rows, err := tx.QueryContext(ctx, query, s.c.storeID, reservationKey, sourceKey)
	if err != nil {
		return nil, storeUnavailableError("load reservation", err)
	}
	defer func() { _ = rows.Close() }()
	preImage := make(map[string]string)
	for rows.Next() {
		var key, source, raw string
		if err := rows.Scan(&key, &source, &raw); err != nil {
			return nil, fmt.Errorf("authoritystore load reservation scan: %w", err)
		}
		var rec reservationRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil, fmt.Errorf("authoritystore load reservation decode: %w", err)
		}
		preImage[key] = raw
		cp := rec
		core.reservations[key] = &cp
		if source != "" {
			core.resBySource[source] = key
		}
		for _, settlementSource := range rec.SettlementSources {
			core.settleBySrc[settlementSource] = key
		}
		for _, releaseSource := range rec.ReleaseSources {
			core.releaseBySrc[releaseSource] = key
		}
	}
	return preImage, rows.Err()
}

func mergePreImages(dst, src map[string]string) {
	maps.Copy(dst, src)
}

func (s *DurableStore) lockUsageFactTx(ctx context.Context, tx bun.Tx, core *storeCore, factKey string) (string, bool, error) {
	if factKey == "" {
		return "", false, nil
	}
	delete(core.unreservedUsageFacts, factKey)
	query := `SELECT record_json FROM usage_authority_unreserved_usage_facts WHERE store_id = ? AND fact_key = ?`
	if s.db.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	var raw string
	err := tx.QueryRowContext(ctx, query, s.c.storeID, factKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, storeUnavailableError("load usage fact", err)
	}
	var fact unreservedUsageFact
	if err := json.Unmarshal([]byte(raw), &fact); err != nil {
		return "", false, fmt.Errorf("authoritystore load usage fact decode: %w", err)
	}
	core.unreservedUsageFacts[factKey] = fact
	if fact.SourceKey != "" {
		core.applyUsageBySrc[fact.SourceKey] = struct{}{}
	}
	return raw, true, nil
}

func (s *DurableStore) lockLimitRowsByKeyTx(ctx context.Context, tx bun.Tx, core *storeCore, keys map[string]struct{}) (map[string]string, error) {
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	preImage := make(map[string]string, len(ordered))
	for _, key := range ordered {
		query := `SELECT row_json FROM usage_authority_limit_rows WHERE store_id = ? AND row_key = ?`
		if s.db.Dialect().Name() == dialect.PG {
			query += ` FOR UPDATE`
		}
		var raw string
		err := tx.QueryRowContext(ctx, query, s.c.storeID, key).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, storeUnavailableError("load limit row", err)
		}
		var row controlplane.AccountingLimitStatusRow
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, fmt.Errorf("authoritystore load limit row decode: %w", err)
		}
		cp := row
		core.limits[key] = &cp
		preImage[key] = raw
	}
	return preImage, nil
}

// decisionBySourceTx reads the existing logical decision after the
// transaction has acquired the store coordination row. This is the durable
// replay fence for deterministic outcomes whose reservation row was never
// created, most notably strict-cap denials.
func (s *DurableStore) decisionBySourceTx(ctx context.Context, tx bun.Tx, sourceKey string) (decisionRecord, bool, error) {
	if sourceKey == "" {
		return decisionRecord{}, false, nil
	}
	query := `SELECT decision_seq, row_json FROM usage_authority_decisions WHERE store_id = ? AND source_key = ?`
	if s.db.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	var rec decisionRecord
	var raw string
	if err := tx.QueryRowContext(ctx, query, s.c.storeID, sourceKey).Scan(&rec.Seq, &raw); errors.Is(err, sql.ErrNoRows) {
		return decisionRecord{}, false, nil
	} else if err != nil {
		return decisionRecord{}, false, storeUnavailableError("load decision replay", err)
	}
	rec.SourceKey = sourceKey
	if err := json.Unmarshal([]byte(raw), &rec.Row); err != nil {
		return decisionRecord{}, false, fmt.Errorf("authoritystore load decision replay decode: %w", err)
	}
	return rec, true, nil
}

func reservationCapacityReplay(descriptor app.ReservationDescriptor, decision decisionRecord) error {
	amount := descriptor.Amount
	if amount.Unit == "" {
		amount.Unit = domain.AmountUnit(decision.Row.Unit)
	}
	if amount.Currency == "" {
		amount.Currency = decision.Row.Currency
	}
	remaining := domain.Amount{Unit: amount.Unit, Value: max(0, decision.Row.Remaining), Currency: amount.Currency}
	return &app.RuleReservationError{
		RuleID: decision.Row.RuleID,
		Err:    &app.ReservationCapacityError{Requested: amount, Remaining: remaining},
	}
}

// runReserveTx runs a reservation inside a locking transaction. It returns the
// storeCore result and a plain error for business-logic outcomes, or an error
