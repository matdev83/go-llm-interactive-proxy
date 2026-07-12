package authoritystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	_ "modernc.org/sqlite"
)

func (s *DurableStore) load(ctx context.Context) (bool, error) {
	var readinessJSON string
	var nextSeq int64
	err := s.db.NewRaw(`SELECT readiness_json, next_decision_seq FROM usage_authority_state WHERE store_id = ?`, s.c.storeID).
		Scan(ctx, &readinessJSON, &nextSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storeUnavailableError("load state", err)
	}
	if err := json.Unmarshal([]byte(readinessJSON), &s.c.state); err != nil {
		return false, fmt.Errorf("authoritystore load readiness: %w", err)
	}
	s.c.nextDecision = nextSeq

	limitRows, err := s.loadLimitRows(ctx)
	if err != nil {
		return false, err
	}
	s.c.limits = s.c.reconcileLimitRows(limitRows)

	reservations, resBySource, settleBySrc, releaseBySrc, err := s.loadReservations(ctx)
	if err != nil {
		return false, err
	}
	s.c.reservations = reservations
	s.c.resBySource = resBySource
	s.c.settleBySrc = settleBySrc
	s.c.releaseBySrc = releaseBySrc

	decisions, err := s.loadDecisions(ctx)
	if err != nil {
		return false, err
	}
	facts, err := s.loadUnreservedUsageFacts(ctx)
	if err != nil {
		return false, err
	}
	s.c.decisions = decisions
	s.c.applyUsageBySrc = make(map[string]struct{})
	s.c.unreservedUsageFacts = facts
	// Hydrate the advisory-usage idempotency map from the decision ledger so
	// replayed applyUsage calls after a restart stay no-ops (requirement 7.8).
	// Advisory decisions store source keys as "<cmd.SourceKey>\x1f<ruleID>";
	// the prefix before the unit separator is the original apply source key.
	for _, rec := range decisions {
		if rec.Row.ReasonCode != "advisory_usage" {
			continue
		}
		if idx := strings.IndexByte(rec.SourceKey, '\x1f'); idx > 0 {
			s.c.applyUsageBySrc[rec.SourceKey[:idx]] = struct{}{}
		}
	}
	if s.c.nextDecision == 0 {
		s.c.nextDecision = 1
	}
	return true, nil
}

func (s *DurableStore) loadLimitRows(ctx context.Context) (map[string]*controlplane.AccountingLimitStatusRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT row_key, row_json FROM usage_authority_limit_rows WHERE store_id = ?`, s.c.storeID)
	if err != nil {
		return nil, storeUnavailableError("load limit rows", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]*controlplane.AccountingLimitStatusRow{}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, fmt.Errorf("authoritystore load limit rows scan: %w", err)
		}
		var row controlplane.AccountingLimitStatusRow
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, fmt.Errorf("authoritystore load limit rows decode: %w", err)
		}
		cp := row
		out[key] = &cp
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authoritystore load limit rows iter: %w", err)
	}
	return out, nil
}

func (s *DurableStore) loadReservations(ctx context.Context) (map[string]*reservationRecord, map[string]string, map[string]string, map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT reservation_key, source_key, record_json FROM usage_authority_reservations WHERE store_id = ?`, s.c.storeID)
	if err != nil {
		return nil, nil, nil, nil, storeUnavailableError("load reservations", err)
	}
	defer func() { _ = rows.Close() }()
	reservations := map[string]*reservationRecord{}
	resBySource := map[string]string{}
	settleBySrc := map[string]string{}
	releaseBySrc := map[string]string{}
	for rows.Next() {
		var key, source, raw string
		if err := rows.Scan(&key, &source, &raw); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("authoritystore load reservations scan: %w", err)
		}
		var rec reservationRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("authoritystore load reservations decode: %w", err)
		}
		cp := rec
		reservations[key] = &cp
		if source != "" {
			resBySource[source] = key
		}
		for _, settlementSource := range cp.SettlementSources {
			settleBySrc[settlementSource] = key
		}
		for _, releaseSource := range cp.ReleaseSources {
			releaseBySrc[releaseSource] = key
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("authoritystore load reservations iter: %w", err)
	}
	return reservations, resBySource, settleBySrc, releaseBySrc, nil
}

func (s *DurableStore) loadDecisions(ctx context.Context) ([]decisionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT decision_seq, source_key, row_json FROM usage_authority_decisions WHERE store_id = ? ORDER BY decision_seq ASC`, s.c.storeID)
	if err != nil {
		return nil, storeUnavailableError("load decisions", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]decisionRecord, 0, 8)
	for rows.Next() {
		var seq int64
		var source, raw string
		if err := rows.Scan(&seq, &source, &raw); err != nil {
			return nil, fmt.Errorf("authoritystore load decisions scan: %w", err)
		}
		var row controlplane.AccountingDecisionRow
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, fmt.Errorf("authoritystore load decisions decode: %w", err)
		}
		out = append(out, decisionRecord{Seq: seq, SourceKey: source, Row: row})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authoritystore load decisions iter: %w", err)
	}
	return out, nil
}

func (s *DurableStore) loadUnreservedUsageFacts(ctx context.Context) (map[string]unreservedUsageFact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT fact_key, record_json FROM usage_authority_unreserved_usage_facts WHERE store_id = ?`, s.c.storeID)
	if err != nil {
		return nil, storeUnavailableError("load unreserved usage facts", err)
	}
	defer func() { _ = rows.Close() }()
	facts := make(map[string]unreservedUsageFact)
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, fmt.Errorf("authoritystore load unreserved usage facts scan: %w", err)
		}
		var fact unreservedUsageFact
		if err := json.Unmarshal([]byte(raw), &fact); err != nil {
			return nil, fmt.Errorf("authoritystore load unreserved usage facts decode: %w", err)
		}
		facts[key] = fact
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("authoritystore load unreserved usage facts iter: %w", err)
	}
	return facts, nil
}
