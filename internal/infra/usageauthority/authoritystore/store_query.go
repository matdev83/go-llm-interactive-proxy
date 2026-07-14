package authoritystore

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func (c *storeCore) limitStatus(q controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	rows := make([]controlplane.AccountingLimitStatusRow, 0, len(c.limits))
	unsupported := limitStatusUnsupported(q)
	for _, row := range c.limits {
		if !limitRowMatchesQuery(*row, q) {
			continue
		}
		cp := *row
		rows = append(rows, cp)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].WindowStart.Equal(rows[j].WindowStart) {
			return rows[i].WindowStart.Before(rows[j].WindowStart)
		}
		if rows[i].RuleID != rows[j].RuleID {
			return rows[i].RuleID < rows[j].RuleID
		}
		return rows[i].Correlation.RequestID < rows[j].Correlation.RequestID
	})
	return pageRows(rows, q.Limit, q.Cursor, q.Visibility, unsupported), nil
}

func (c *storeCore) decisionHistory(q controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	unsupported := make([]controlplane.UnsupportedFilter, 0, 1)
	if !q.Common.TimeRange.From.IsZero() || !q.Common.TimeRange.To.IsZero() {
		unsupported = append(unsupported, controlplane.UnsupportedFilter{Field: "time_range", Reason: "decision rows do not record time ranges"})
	}
	return pageDecisionRecords(c.decisions, q, unsupported)
}

func (c *storeCore) appendDecision(log MutationLog, snapshot commandSnapshot, row *controlplane.AccountingLimitStatusRow, reservationID, sourceKey string, outcome controlplane.AccountingOutcome, reason string, authority controlplane.AccountingAuthoritySource, settlementState controlplane.AccountingSettlementState, amount domain.Amount, released, overage, adjustment int64) {
	if row == nil {
		return
	}
	if sourceKey == "" {
		sourceKey = reservationID + "|" + reason
	}
	rec := decisionRecord{
		Seq:       c.nextDecisionSeq(),
		SourceKey: sourceKey,
		Row: controlplane.AccountingDecisionRow{
			Correlation:     snapshot.Correlation,
			Scope:           snapshot.Scope,
			RuleID:          row.RuleID,
			Outcome:         outcome,
			ReasonCode:      reason,
			Authority:       authority,
			ReservationID:   reservationID,
			SettlementState: settlementState,
			Unit:            string(amount.Unit),
			Currency:        amount.Currency,
			Limit:           row.Limit,
			Consumed:        row.Consumed,
			Reserved:        row.Reserved,
			Remaining:       row.Remaining,
			Released:        released,
			Overage:         overage,
			Adjustment:      adjustment,
			// Mirror the live limit row's window bounds so decision history shows the same
			// window context as the limit status. Rules without a window definition leave
			// these as the zero time.Time, matching AccountingLimitStatusRow's semantics.
			WindowStart:        row.WindowStart,
			WindowEnd:          row.WindowEnd,
			WindowResetAt:      row.WindowResetAt,
			EvidenceState:      controlplane.EvidenceRecorded,
			RedactionState:     controlplane.RedactionSummarized,
			AuthorityNamespace: row.AuthorityNamespace,
			Perspective:        controlplane.UsagePerspective(row.Perspective),
			LifecycleScope:     controlplane.UsageLifecycleScope(row.LifecycleScope),
			Basis:              row.Basis,
			RuleVersion:        row.RuleVersion,
			ReservationType:    controlplane.AuthorityHandleReservation,
			Surfaced:           snapshot.Surfaced,
			ParentRequestID:    snapshot.ParentRequestID,
			BoundPolicyVersion: snapshot.BoundPolicyVersion,
			BoundRatingVersion: snapshot.BoundRatingVersion,
		},
	}
	if rec.Row.BoundPolicyVersion.Version == "" && strings.TrimSpace(row.RuleVersion) != "" {
		rec.Row.BoundPolicyVersion = controlplane.VersionRef{ID: row.RuleID, Version: row.RuleVersion}
	}
	if rec.Row.Unit == "" {
		rec.Row.Unit = row.Unit
	}
	if rec.Row.Currency == "" {
		rec.Row.Currency = row.Currency
	}
	c.decisions = append(c.decisions, rec)
	log.CaptureDecisionAppend(rec)
}

func (c *storeCore) ensureWritable() error {
	switch c.readiness().State {
	case domain.AuthorityStateDisabled:
		return fmt.Errorf("%w: authority disabled", app.ErrDisabled)
	case domain.AuthorityStateDegraded:
		return fmt.Errorf("%w: authority degraded", app.ErrDegraded)
	case domain.AuthorityStateUnavailable:
		return fmt.Errorf("%w: authority unavailable", app.ErrUnavailable)
	case domain.AuthorityStateAdvisoryOnly, domain.AuthorityStateReady:
		return nil
	default:
		return fmt.Errorf("%w: authority unavailable", app.ErrUnavailable)
	}
}

func (c *storeCore) matchLimitRow(ruleID string, dims domain.Dimensions, at time.Time) (*controlplane.AccountingLimitStatusRow, string, bool) {
	candidate, key, ok := c.configuredLimitRow(ruleID, dims, at)
	if !ok {
		return nil, "", false
	}
	if row := c.limits[key]; row != nil {
		return row, key, true
	}
	c.limits[key] = &candidate
	return &candidate, key, true
}

func (c *storeCore) configuredLimitRow(ruleID string, dims domain.Dimensions, at time.Time) (controlplane.AccountingLimitStatusRow, string, bool) {
	for _, template := range c.limitTemplates[ruleID] {
		if !ScopeDimensionsMatch(template.Scope, dims) {
			continue
		}
		if !correlationFieldMatches(template.Correlation.BackendID, valueString(dims.Backend)) || !correlationFieldMatches(template.Correlation.Model, valueString(dims.Model)) {
			continue
		}

		candidate := template
		spec, configured := c.ruleWindows[ruleID]
		if configured && windowSpecConfigured(spec) {
			bounds, err := spec.Bounds(at)
			if err != nil {
				continue
			}
			candidate.WindowStart = bounds.Start
			candidate.WindowEnd = bounds.End
			candidate.WindowResetAt = bounds.End
		} else if !candidate.WindowEnd.IsZero() && !at.Before(candidate.WindowEnd) {
			// Preserve the legacy behavior for a row whose fixed window has no
			// configured rollover specification.
			continue
		}

		candidate.Consumed = 0
		candidate.Reserved = 0
		candidate.Adjustment = 0
		candidate.Remaining = maxInt64(0, candidate.Limit)
		return candidate, limitRowKey(candidate), true
	}
	return controlplane.AccountingLimitStatusRow{}, "", false
}

func windowSpecConfigured(spec domain.WindowSpec) bool {
	return spec.Algorithm != "" || spec.Size != 0 || !spec.Anchor.IsZero()
}

func limitRowMatchesQuery(row controlplane.AccountingLimitStatusRow, q controlplane.AccountingLimitStatusQuery) bool {
	if !commonFiltersMatch(
		commonQueryFields{Common: q.Common, RuleID: q.RuleID, Unit: q.Unit, Currency: q.Currency, Authority: q.Authority, EvidenceState: q.EvidenceState, RedactionState: q.RedactionState},
		commonRowFields{Correlation: row.Correlation, Scope: row.Scope, RuleID: row.RuleID, Unit: row.Unit, Currency: row.Currency, Authority: row.Authority, EvidenceState: row.EvidenceState, RedactionState: row.RedactionState},
	) {
		return false
	}
	if q.Perspective != "" && row.Perspective != string(q.Perspective) {
		return false
	}
	if q.LifecycleScope != "" && row.LifecycleScope != string(q.LifecycleScope) {
		return false
	}
	if basis := strings.TrimSpace(q.Basis); basis != "" && strings.TrimSpace(row.Basis) != basis {
		return false
	}
	if !q.Common.TimeRange.From.IsZero() && !row.WindowEnd.IsZero() && row.WindowEnd.Before(q.Common.TimeRange.From) {
		return false
	}
	if !q.Common.TimeRange.To.IsZero() && !row.WindowStart.IsZero() && row.WindowStart.After(q.Common.TimeRange.To) {
		return false
	}
	return true
}

func decisionRowMatchesQuery(row controlplane.AccountingDecisionRow, q controlplane.AccountingDecisionQuery) bool {
	if !commonFiltersMatch(
		commonQueryFields{Common: q.Common, RuleID: q.RuleID, Unit: q.Unit, Currency: q.Currency, Authority: q.Authority, EvidenceState: q.EvidenceState, RedactionState: q.RedactionState},
		commonRowFields{Correlation: row.Correlation, Scope: row.Scope, RuleID: row.RuleID, Unit: row.Unit, Currency: row.Currency, Authority: row.Authority, EvidenceState: row.EvidenceState, RedactionState: row.RedactionState},
	) {
		return false
	}
	if q.Perspective != "" && row.Perspective != q.Perspective {
		return false
	}
	if q.LifecycleScope != "" && row.LifecycleScope != q.LifecycleScope {
		return false
	}
	if basis := strings.TrimSpace(q.Basis); basis != "" && strings.TrimSpace(row.Basis) != basis {
		return false
	}
	if q.SettlementState != "" && row.SettlementState != q.SettlementState {
		return false
	}
	if q.Common.Outcome != "" && row.Outcome != controlplane.AccountingOutcome(q.Common.Outcome) {
		return false
	}
	if q.Common.ReasonCode != "" && row.ReasonCode != q.Common.ReasonCode {
		return false
	}
	return true
}

type commonQueryFields struct {
	Common         controlplane.CommonFilters
	RuleID         string
	Unit           string
	Currency       string
	Authority      controlplane.AccountingAuthoritySource
	EvidenceState  controlplane.EvidenceState
	RedactionState controlplane.RedactionState
}

type commonRowFields struct {
	Correlation    controlplane.Correlation
	Scope          controlplane.ScopeSnapshot
	RuleID         string
	Unit           string
	Currency       string
	Authority      controlplane.AccountingAuthoritySource
	EvidenceState  controlplane.EvidenceState
	RedactionState controlplane.RedactionState
}

// commonFiltersMatch checks the shared sibling field filters (RuleID, Unit,
// Currency, Authority, EvidenceState, RedactionState, Common correlation
// fields, Scope) against a row's common fields. It does not check
// row-type-specific fields (SettlementState, TimeRange, Outcome, ReasonCode)
// — those remain in the caller so each row type can reject only the filters
// it actually records.
func commonFiltersMatch(q commonQueryFields, r commonRowFields) bool {
	if q.RuleID != "" && r.RuleID != q.RuleID {
		return false
	}
	if q.Unit != "" && r.Unit != q.Unit {
		return false
	}
	if q.Currency != "" && !strings.EqualFold(r.Currency, q.Currency) {
		return false
	}
	if q.Authority != "" && r.Authority != q.Authority {
		return false
	}
	if q.EvidenceState != "" && r.EvidenceState != q.EvidenceState {
		return false
	}
	if q.RedactionState != "" && r.RedactionState != q.RedactionState {
		return false
	}
	if q.Common.BackendID != "" && r.Correlation.BackendID != q.Common.BackendID {
		return false
	}
	if q.Common.Model != "" && r.Correlation.Model != q.Common.Model {
		return false
	}
	if q.Common.FrontendID != "" && r.Correlation.FrontendID != q.Common.FrontendID {
		return false
	}
	if q.Common.TraceID != "" && r.Correlation.TraceID != q.Common.TraceID {
		return false
	}
	if q.Common.SessionID != "" && r.Correlation.SessionID != q.Common.SessionID {
		return false
	}
	if q.Common.ALegID != "" && r.Correlation.ALegID != q.Common.ALegID {
		return false
	}
	if q.Common.BLegID != "" && r.Correlation.BLegID != q.Common.BLegID {
		return false
	}
	if !ScopeMatches(q.Common.Scope, r.Scope) {
		return false
	}
	return true
}

func scopeValueMatches(filter, actual scope.Value) bool {
	if !filter.IsKnown() {
		return true
	}
	return filter.Equal(actual)
}

// ScopeMatches reports whether a row's scope satisfies every known field of
// the filter scope. Unknown filter fields are wildcards; known filter fields
// must match the target's value.
func ScopeMatches(filter controlplane.ScopeFilters, target controlplane.ScopeSnapshot) bool {
	return scopeValueMatches(filter.PrincipalID, target.PrincipalID) &&
		scopeValueMatches(filter.CredentialID, target.CredentialID) &&
		scopeValueMatches(filter.TenantID, target.TenantID) &&
		scopeValueMatches(filter.OrganizationID, target.OrganizationID) &&
		scopeValueMatches(filter.WorkspaceID, target.WorkspaceID) &&
		scopeValueMatches(filter.ProjectID, target.ProjectID) &&
		scopeValueMatches(filter.DepartmentID, target.DepartmentID) &&
		scopeValueMatches(filter.CostCenterID, target.CostCenterID)
}

// ScopeDimensionsMatch reports whether a stored limit row's scope snapshot
// satisfies the shared scope fields of an incoming domain.Dimensions
// target. It is the parallel helper to ScopeMatches for the resolver path,
// which works in domain.Dimensions rather than controlplane.ScopeFilters.
// Backend, Model, Route, and PolicyLabels are authority-specific dimensions
// and are matched separately by the caller. Credential is matched here so a
// rule targeting the credential authority dimension only reserves against a
// row seeded for the same credential (requirement 1.2). An unknown stored
// credential remains a wildcard so unconfigured rules still match (backward
// compat).
func ScopeDimensionsMatch(row controlplane.ScopeSnapshot, dims domain.Dimensions) bool {
	if !scopeValueMatches(row.PrincipalID, dims.Principal) ||
		!scopeValueMatches(row.CredentialID, dims.Credential) ||
		!scopeValueMatches(row.TenantID, dims.Tenant) ||
		!scopeValueMatches(row.OrganizationID, dims.Organization) ||
		!scopeValueMatches(row.WorkspaceID, dims.Workspace) ||
		!scopeValueMatches(row.ProjectID, dims.Project) ||
		!scopeValueMatches(row.DepartmentID, dims.Department) ||
		!scopeValueMatches(row.CostCenterID, dims.CostCenter) {
		return false
	}
	for key, rowValue := range row.Principal.PolicyLabels {
		actual, present := dims.PolicyLabels[key]
		if !present {
			actual = scope.Unknown()
		}
		if !scopeValueMatches(scopeValueFromString(rowValue), actual) {
			return false
		}
	}
	return true
}

func scopeValueFromString(value string) scope.Value {
	return scope.Known(value)
}

func correlationFieldMatches(rowValue, actual string) bool {
	if strings.TrimSpace(rowValue) == "" {
		return true
	}
	return rowValue == actual
}

func pageRows[T any](rows []T, limit int, cursor controlplane.Cursor, visibility controlplane.Visibility, unsupported []controlplane.UnsupportedFilter) controlplane.Page[T] {
	if limit <= 0 {
		limit = 100
	}
	offset := 0
	if cursor.Token != "" {
		if n, err := strconv.Atoi(cursor.Token); err == nil && n >= 0 {
			offset = n
		}
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	end := offset + limit
	end = min(end, len(rows))
	out := controlplane.Page[T]{
		Items:       append([]T(nil), rows[offset:end]...),
		Unsupported: append([]controlplane.UnsupportedFilter(nil), unsupported...),
		Visibility:  visibility,
	}
	if end < len(rows) {
		out.Next = controlplane.Cursor{Token: strconv.Itoa(end)}
	}
	return out
}
