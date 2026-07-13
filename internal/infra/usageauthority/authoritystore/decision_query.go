package authoritystore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/uptrace/bun"
)

const (
	decisionFilterMarker = "_decision"
	decisionBackfillSize = 500
)

type decisionFilter struct {
	name  string
	value string
}

type decisionCursor struct {
	Version int    `json:"v"`
	After   int64  `json:"after"`
	Digest  string `json:"digest"`
}

func decisionFiltersForRow(row controlplane.AccountingDecisionRow) []decisionFilter {
	filters := []decisionFilter{{decisionFilterMarker, "1"}}
	add := func(name, value string) {
		if value != "" {
			filters = append(filters, decisionFilter{name, value})
		}
	}
	add("rule_id", row.RuleID)
	add("unit", row.Unit)
	add("currency", strings.ToLower(row.Currency))
	add("authority", string(row.Authority))
	add("settlement_state", string(row.SettlementState))
	add("evidence_state", string(row.EvidenceState))
	add("redaction_state", string(row.RedactionState))
	add("outcome", string(row.Outcome))
	add("reason_code", row.ReasonCode)
	add("backend_id", row.Correlation.BackendID)
	add("model", row.Correlation.Model)
	add("frontend_id", row.Correlation.FrontendID)
	add("trace_id", row.Correlation.TraceID)
	add("session_id", row.Correlation.SessionID)
	add("a_leg_id", row.Correlation.ALegID)
	add("b_leg_id", row.Correlation.BLegID)
	filters = append(filters,
		decisionFilter{"principal_id", encodeScopeValue(row.Scope.PrincipalID)},
		decisionFilter{"credential_id", encodeScopeValue(row.Scope.CredentialID)},
		decisionFilter{"tenant_id", encodeScopeValue(row.Scope.TenantID)},
		decisionFilter{"organization_id", encodeScopeValue(row.Scope.OrganizationID)},
		decisionFilter{"workspace_id", encodeScopeValue(row.Scope.WorkspaceID)},
		decisionFilter{"project_id", encodeScopeValue(row.Scope.ProjectID)},
		decisionFilter{"department_id", encodeScopeValue(row.Scope.DepartmentID)},
		decisionFilter{"cost_center_id", encodeScopeValue(row.Scope.CostCenterID)},
	)
	return filters
}

func decisionFiltersForQuery(q controlplane.AccountingDecisionQuery) []decisionFilter {
	filters := make([]decisionFilter, 0, 24)
	add := func(name, value string) {
		if value != "" {
			filters = append(filters, decisionFilter{name, value})
		}
	}
	add("rule_id", q.RuleID)
	add("unit", q.Unit)
	add("currency", strings.ToLower(q.Currency))
	add("authority", string(q.Authority))
	add("settlement_state", string(q.SettlementState))
	add("evidence_state", string(q.EvidenceState))
	add("redaction_state", string(q.RedactionState))
	add("outcome", q.Common.Outcome)
	add("reason_code", q.Common.ReasonCode)
	add("backend_id", q.Common.BackendID)
	add("model", q.Common.Model)
	add("frontend_id", q.Common.FrontendID)
	add("trace_id", q.Common.TraceID)
	add("session_id", q.Common.SessionID)
	add("a_leg_id", q.Common.ALegID)
	add("b_leg_id", q.Common.BLegID)
	addScope := func(name string, value scope.Value) {
		if value.IsKnown() {
			filters = append(filters, decisionFilter{name, encodeScopeValue(value)})
		}
	}
	addScope("principal_id", q.Common.Scope.PrincipalID)
	addScope("credential_id", q.Common.Scope.CredentialID)
	addScope("tenant_id", q.Common.Scope.TenantID)
	addScope("organization_id", q.Common.Scope.OrganizationID)
	addScope("workspace_id", q.Common.Scope.WorkspaceID)
	addScope("project_id", q.Common.Scope.ProjectID)
	addScope("department_id", q.Common.Scope.DepartmentID)
	addScope("cost_center_id", q.Common.Scope.CostCenterID)
	return filters
}

func encodeScopeValue(value scope.Value) string {
	if !value.IsKnown() {
		return "u:"
	}
	return "k:" + value.String()
}

func decisionQueryDigest(q controlplane.AccountingDecisionQuery) string {
	q.Cursor = controlplane.Cursor{}
	raw, _ := json.Marshal(q)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func decodeDecisionCursor(q controlplane.AccountingDecisionQuery) (int64, error) {
	if q.Cursor.Token == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.Cursor.Token)
	if err != nil {
		return 0, fmt.Errorf("%w: malformed decision cursor", app.ErrInvalidQuery)
	}
	var cursor decisionCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Version != 1 || cursor.After < 0 || cursor.Digest != decisionQueryDigest(q) {
		return 0, fmt.Errorf("%w: invalid decision cursor", app.ErrInvalidQuery)
	}
	return cursor.After, nil
}

func encodeDecisionCursor(q controlplane.AccountingDecisionQuery, after int64) controlplane.Cursor {
	raw, _ := json.Marshal(decisionCursor{Version: 1, After: after, Digest: decisionQueryDigest(q)})
	return controlplane.Cursor{Token: base64.RawURLEncoding.EncodeToString(raw)}
}

func (s *DurableStore) replaceDecisionFiltersTx(ctx context.Context, tx bun.Tx, storeID string, rec decisionRecord) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_authority_decision_filters WHERE store_id = ? AND decision_seq = ?`, storeID, rec.Seq); err != nil {
		return storeUnavailableError("replace decision filters", err)
	}
	filters := decisionFiltersForRow(rec.Row)
	var query strings.Builder
	query.WriteString(`INSERT INTO usage_authority_decision_filters(store_id, decision_seq, field_name, field_value) VALUES `)
	args := make([]any, 0, len(filters)*4)
	for i, filter := range filters {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteString(`(?,?,?,?)`)
		args = append(args, storeID, rec.Seq, filter.name, filter.value)
	}
	query.WriteString(` ON CONFLICT DO NOTHING`)
	if _, err := tx.ExecContext(ctx, query.String(), args...); err != nil {
		return storeUnavailableError("insert decision filters", err)
	}
	return nil
}

func (s *DurableStore) backfillDecisionFilters(ctx context.Context) error {
	var after int64
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT d.decision_seq, d.source_key, d.row_json FROM usage_authority_decisions d WHERE d.store_id = ? AND d.decision_seq > ? AND NOT EXISTS (SELECT 1 FROM usage_authority_decision_filters f WHERE f.store_id = d.store_id AND f.decision_seq = d.decision_seq AND f.field_name = ?) ORDER BY d.decision_seq ASC LIMIT ?`, s.c.storeID, after, decisionFilterMarker, decisionBackfillSize)
		if err != nil {
			return storeUnavailableError("backfill decision filters", err)
		}
		batch := make([]decisionRecord, 0, decisionBackfillSize)
		for rows.Next() {
			var rec decisionRecord
			var raw string
			if err := rows.Scan(&rec.Seq, &rec.SourceKey, &raw); err != nil {
				_ = rows.Close()
				return storeUnavailableError("backfill decision filters scan", err)
			}
			if err := json.Unmarshal([]byte(raw), &rec.Row); err != nil {
				_ = rows.Close()
				return fmt.Errorf("authoritystore backfill decision decode: %w", err)
			}
			batch = append(batch, rec)
			after = rec.Seq
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return storeUnavailableError("backfill decision filters iter", err)
		}
		if err := rows.Close(); err != nil {
			return storeUnavailableError("backfill decision filters close", err)
		}
		if len(batch) == 0 {
			return nil
		}
		if err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			for _, rec := range batch {
				if err := s.replaceDecisionFiltersTx(ctx, tx, s.c.storeID, rec); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
}

func (s *DurableStore) queryDecisions(ctx context.Context, q controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	after, err := decodeDecisionCursor(q)
	if err != nil {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, err
	}
	filters := decisionFiltersForQuery(q)
	args := make([]any, 0, 3+len(filters)*2)
	query := `SELECT d.decision_seq, d.row_json FROM usage_authority_decisions d`
	if len(filters) > 0 {
		query += ` JOIN usage_authority_decision_filters f0 ON f0.store_id = d.store_id AND f0.decision_seq = d.decision_seq AND f0.field_name = ? AND f0.field_value = ?`
		args = append(args, filters[0].name, filters[0].value)
	}
	query += ` WHERE d.store_id = ? AND d.decision_seq > ?`
	args = append(args, s.c.storeID, after)
	if len(filters) > 1 {
		var extra strings.Builder
		for _, filter := range filters[1:] {
			extra.WriteString(` AND EXISTS (SELECT 1 FROM usage_authority_decision_filters fx WHERE fx.store_id = d.store_id AND fx.decision_seq = d.decision_seq AND fx.field_name = ? AND fx.field_value = ?)`)
			args = append(args, filter.name, filter.value)
		}
		query += extra.String()
	}
	query += ` ORDER BY d.decision_seq ASC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, storeUnavailableError("query decisions", err)
	}
	defer func() { _ = rows.Close() }()
	type item struct {
		seq int64
		row controlplane.AccountingDecisionRow
	}
	items := make([]item, 0, limit+1)
	for rows.Next() {
		var current item
		var raw string
		if err := rows.Scan(&current.seq, &raw); err != nil {
			return controlplane.Page[controlplane.AccountingDecisionRow]{}, storeUnavailableError("query decisions scan", err)
		}
		if err := json.Unmarshal([]byte(raw), &current.row); err != nil {
			return controlplane.Page[controlplane.AccountingDecisionRow]{}, fmt.Errorf("authoritystore query decision decode: %w", err)
		}
		items = append(items, current)
	}
	if err := rows.Err(); err != nil {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, storeUnavailableError("query decisions iter", err)
	}
	page := controlplane.Page[controlplane.AccountingDecisionRow]{Visibility: q.Visibility}
	if !q.Common.TimeRange.From.IsZero() || !q.Common.TimeRange.To.IsZero() {
		page.Unsupported = []controlplane.UnsupportedFilter{{Field: "time_range", Reason: "decision rows do not record time ranges"}}
	}
	if len(items) > limit {
		items = items[:limit]
		page.Next = encodeDecisionCursor(q, items[len(items)-1].seq)
	}
	page.Items = make([]controlplane.AccountingDecisionRow, len(items))
	for i := range items {
		page.Items[i] = items[i].row
	}
	return page, nil
}

func pageDecisionRecords(records []decisionRecord, q controlplane.AccountingDecisionQuery, unsupported []controlplane.UnsupportedFilter) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	after, err := decodeDecisionCursor(q)
	if err != nil {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, err
	}
	page := controlplane.Page[controlplane.AccountingDecisionRow]{Visibility: q.Visibility, Unsupported: append([]controlplane.UnsupportedFilter(nil), unsupported...)}
	var last int64
	more := false
	for _, rec := range records {
		if rec.Seq <= after || !decisionRowMatchesQuery(rec.Row, q) {
			continue
		}
		if len(page.Items) == limit {
			more = true
			break
		}
		page.Items = append(page.Items, rec.Row)
		last = rec.Seq
	}
	if more {
		page.Next = encodeDecisionCursor(q, last)
	}
	return page, nil
}
