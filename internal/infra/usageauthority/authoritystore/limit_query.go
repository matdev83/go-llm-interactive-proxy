package authoritystore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/uptrace/bun"
)

const (
	limitFilterMarker = "_limit"
	limitSortKeyField = "_sort_key"
	limitWindowStart  = "window_start"
	limitWindowEnd    = "window_end"
	limitBackfillSize = 500
)

type limitFilter struct {
	name  string
	value string
}

type limitCursor struct {
	Version int    `json:"v"`
	After   string `json:"after"`
	Digest  string `json:"digest"`
}

func limitStatusUnsupported(q controlplane.AccountingLimitStatusQuery) []controlplane.UnsupportedFilter {
	unsupported := make([]controlplane.UnsupportedFilter, 0, 3)
	if q.SettlementState != "" {
		unsupported = append(unsupported, controlplane.UnsupportedFilter{Field: "settlement_state", Reason: "limit rows do not record settlement state"})
	}
	if q.Common.Outcome != "" {
		unsupported = append(unsupported, controlplane.UnsupportedFilter{Field: "outcome", Reason: "limit rows do not record outcome"})
	}
	if q.Common.ReasonCode != "" {
		unsupported = append(unsupported, controlplane.UnsupportedFilter{Field: "reason_code", Reason: "limit rows do not record reason codes"})
	}
	return unsupported
}

func encodeLimitTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func limitSortKey(rowKey string, row controlplane.AccountingLimitStatusRow) string {
	return strings.Join([]string{
		encodeLimitTime(row.WindowStart),
		row.RuleID,
		row.Correlation.RequestID,
		rowKey,
	}, "\x1f")
}

func limitFiltersForRow(rowKey string, row controlplane.AccountingLimitStatusRow) []limitFilter {
	filters := []limitFilter{
		{limitFilterMarker, "1"},
		{limitSortKeyField, limitSortKey(rowKey, row)},
		{limitWindowStart, encodeLimitTime(row.WindowStart)},
		{limitWindowEnd, encodeLimitTime(row.WindowEnd)},
	}
	add := func(name, value string) {
		if value != "" {
			filters = append(filters, limitFilter{name, value})
		}
	}
	add("rule_id", row.RuleID)
	add("unit", row.Unit)
	add("currency", strings.ToLower(row.Currency))
	add("authority", string(row.Authority))
	add("evidence_state", string(row.EvidenceState))
	add("redaction_state", string(row.RedactionState))
	add("perspective", strings.TrimSpace(row.Perspective))
	add("lifecycle_scope", strings.TrimSpace(row.LifecycleScope))
	add("basis", strings.TrimSpace(row.Basis))
	add("backend_id", row.Correlation.BackendID)
	add("model", row.Correlation.Model)
	add("frontend_id", row.Correlation.FrontendID)
	add("trace_id", row.Correlation.TraceID)
	add("session_id", row.Correlation.SessionID)
	add("a_leg_id", row.Correlation.ALegID)
	add("b_leg_id", row.Correlation.BLegID)
	filters = append(
		filters,
		limitFilter{"principal_id", encodeScopeValue(row.Scope.PrincipalID)},
		limitFilter{"credential_id", encodeScopeValue(row.Scope.CredentialID)},
		limitFilter{"tenant_id", encodeScopeValue(row.Scope.TenantID)},
		limitFilter{"organization_id", encodeScopeValue(row.Scope.OrganizationID)},
		limitFilter{"workspace_id", encodeScopeValue(row.Scope.WorkspaceID)},
		limitFilter{"project_id", encodeScopeValue(row.Scope.ProjectID)},
		limitFilter{"department_id", encodeScopeValue(row.Scope.DepartmentID)},
		limitFilter{"cost_center_id", encodeScopeValue(row.Scope.CostCenterID)},
	)
	return filters
}

func limitFiltersForQuery(q controlplane.AccountingLimitStatusQuery) []limitFilter {
	filters := make([]limitFilter, 0, 24)
	add := func(name, value string) {
		if value != "" {
			filters = append(filters, limitFilter{name, value})
		}
	}
	add("rule_id", q.RuleID)
	add("unit", q.Unit)
	add("currency", strings.ToLower(q.Currency))
	add("authority", string(q.Authority))
	add("evidence_state", string(q.EvidenceState))
	add("redaction_state", string(q.RedactionState))
	add("perspective", strings.TrimSpace(string(q.Perspective)))
	add("lifecycle_scope", strings.TrimSpace(string(q.LifecycleScope)))
	add("basis", strings.TrimSpace(q.Basis))
	add("backend_id", q.Common.BackendID)
	add("model", q.Common.Model)
	add("frontend_id", q.Common.FrontendID)
	add("trace_id", q.Common.TraceID)
	add("session_id", q.Common.SessionID)
	add("a_leg_id", q.Common.ALegID)
	add("b_leg_id", q.Common.BLegID)
	addScope := func(name string, value scope.Value) {
		if value.IsKnown() {
			filters = append(filters, limitFilter{name, encodeScopeValue(value)})
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

func limitQueryDigest(q controlplane.AccountingLimitStatusQuery) string {
	q.Cursor = controlplane.Cursor{}
	raw, _ := json.Marshal(q)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func decodeLimitCursor(q controlplane.AccountingLimitStatusQuery) (string, error) {
	if q.Cursor.Token == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.Cursor.Token)
	if err != nil {
		return "", fmt.Errorf("%w: malformed limit cursor", app.ErrInvalidQuery)
	}
	var cursor limitCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Version != 1 || cursor.Digest != limitQueryDigest(q) {
		return "", fmt.Errorf("%w: invalid limit cursor", app.ErrInvalidQuery)
	}
	return cursor.After, nil
}

func encodeLimitCursor(q controlplane.AccountingLimitStatusQuery, after string) controlplane.Cursor {
	raw, _ := json.Marshal(limitCursor{Version: 1, After: after, Digest: limitQueryDigest(q)})
	return controlplane.Cursor{Token: base64.RawURLEncoding.EncodeToString(raw)}
}

func (s *DurableStore) replaceLimitFiltersTx(ctx context.Context, tx bun.Tx, storeID, rowKey string, row controlplane.AccountingLimitStatusRow) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_authority_limit_filters WHERE store_id = ? AND row_key = ?`, storeID, rowKey); err != nil {
		return storeUnavailableError("replace limit filters", err)
	}
	filters := limitFiltersForRow(rowKey, row)
	var query strings.Builder
	query.WriteString(`INSERT INTO usage_authority_limit_filters(store_id, row_key, field_name, field_value) VALUES `)
	args := make([]any, 0, len(filters)*4)
	for i, filter := range filters {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteString(`(?,?,?,?)`)
		args = append(args, storeID, rowKey, filter.name, filter.value)
	}
	query.WriteString(` ON CONFLICT DO NOTHING`)
	if _, err := tx.ExecContext(ctx, query.String(), args...); err != nil {
		return storeUnavailableError("insert limit filters", err)
	}
	return nil
}

func (s *DurableStore) backfillLimitFilters(ctx context.Context) error {
	var after string
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT l.row_key, l.row_json FROM usage_authority_limit_rows l WHERE l.store_id = ? AND l.row_key > ? AND NOT EXISTS (SELECT 1 FROM usage_authority_limit_filters f WHERE f.store_id = l.store_id AND f.row_key = l.row_key AND f.field_name = ?) ORDER BY l.row_key ASC LIMIT ?`, s.c.storeID, after, limitFilterMarker, limitBackfillSize)
		if err != nil {
			return storeUnavailableError("backfill limit filters", err)
		}
		type item struct {
			key string
			row controlplane.AccountingLimitStatusRow
		}
		batch := make([]item, 0, limitBackfillSize)
		for rows.Next() {
			var current item
			var raw string
			if err := rows.Scan(&current.key, &raw); err != nil {
				_ = rows.Close()
				return storeUnavailableError("backfill limit filters scan", err)
			}
			if err := json.Unmarshal([]byte(raw), &current.row); err != nil {
				_ = rows.Close()
				return fmt.Errorf("authoritystore backfill limit decode: %w", err)
			}
			batch = append(batch, current)
			after = current.key
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return storeUnavailableError("backfill limit filters iter", err)
		}
		if err := rows.Close(); err != nil {
			return storeUnavailableError("backfill limit filters close", err)
		}
		if len(batch) == 0 {
			return nil
		}
		if err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			for _, item := range batch {
				if err := s.replaceLimitFiltersTx(ctx, tx, s.c.storeID, item.key, item.row); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
}

func (s *DurableStore) queryLimits(ctx context.Context, q controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	after, err := decodeLimitCursor(q)
	if err != nil {
		return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, err
	}
	filters := limitFiltersForQuery(q)
	args := make([]any, 0, 8+len(filters)*2)
	query := `SELECT l.row_key, l.row_json, sk.field_value FROM usage_authority_limit_rows l JOIN usage_authority_limit_filters sk ON sk.store_id = l.store_id AND sk.row_key = l.row_key AND sk.field_name = ?`
	args = append(args, limitSortKeyField)
	if len(filters) > 0 {
		query += ` JOIN usage_authority_limit_filters f0 ON f0.store_id = l.store_id AND f0.row_key = l.row_key AND f0.field_name = ? AND f0.field_value = ?`
		args = append(args, filters[0].name, filters[0].value)
	}
	query += ` WHERE l.store_id = ? AND sk.field_value > ?`
	args = append(args, s.c.storeID, after)
	if len(filters) > 1 {
		var extra strings.Builder
		for _, filter := range filters[1:] {
			extra.WriteString(` AND EXISTS (SELECT 1 FROM usage_authority_limit_filters fx WHERE fx.store_id = l.store_id AND fx.row_key = l.row_key AND fx.field_name = ? AND fx.field_value = ?)`)
			args = append(args, filter.name, filter.value)
		}
		query += extra.String()
	}
	if !q.Common.TimeRange.From.IsZero() {
		query += ` AND EXISTS (SELECT 1 FROM usage_authority_limit_filters we WHERE we.store_id = l.store_id AND we.row_key = l.row_key AND we.field_name = ? AND (we.field_value = '' OR we.field_value >= ?))`
		args = append(args, limitWindowEnd, encodeLimitTime(q.Common.TimeRange.From))
	}
	if !q.Common.TimeRange.To.IsZero() {
		query += ` AND EXISTS (SELECT 1 FROM usage_authority_limit_filters ws WHERE ws.store_id = l.store_id AND ws.row_key = l.row_key AND ws.field_name = ? AND (ws.field_value = '' OR ws.field_value <= ?))`
		args = append(args, limitWindowStart, encodeLimitTime(q.Common.TimeRange.To))
	}
	query += ` ORDER BY sk.field_value ASC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, storeUnavailableError("query limits", err)
	}
	defer func() { _ = rows.Close() }()
	type item struct {
		sortKey string
		row     controlplane.AccountingLimitStatusRow
	}
	items := make([]item, 0, limit+1)
	for rows.Next() {
		var current item
		var raw, rowKey string
		if err := rows.Scan(&rowKey, &raw, &current.sortKey); err != nil {
			return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, storeUnavailableError("query limits scan", err)
		}
		if err := json.Unmarshal([]byte(raw), &current.row); err != nil {
			return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, fmt.Errorf("authoritystore query limit decode: %w", err)
		}
		items = append(items, current)
	}
	if err := rows.Err(); err != nil {
		return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, storeUnavailableError("query limits iter", err)
	}
	page := controlplane.Page[controlplane.AccountingLimitStatusRow]{
		Visibility:  q.Visibility,
		Unsupported: limitStatusUnsupported(q),
	}
	if len(items) > limit {
		items = items[:limit]
		page.Next = encodeLimitCursor(q, items[len(items)-1].sortKey)
	}
	page.Items = make([]controlplane.AccountingLimitStatusRow, len(items))
	for i := range items {
		page.Items[i] = items[i].row
	}
	return page, nil
}
