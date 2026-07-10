package authoritystore

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

var (
	_ app.StateStore = (*MemoryStore)(nil)
	_ app.StateStore = (*DurableStore)(nil)
)

// MemoryStore is a race-safe in-memory authority store.
type MemoryStore struct {
	mu sync.Mutex
	c  *storeCore
}

// NewMemory returns an empty in-memory store seeded with the provided config.
func NewMemory(cfg Config) *MemoryStore {
	return &MemoryStore{c: newStoreCore(cfg)}
}

// Close implements a no-op close so the memory store matches the durable store shape.
func (s *MemoryStore) Close() error { return nil }

// CheckReadiness reports the configured readiness posture.
func (s *MemoryStore) CheckReadiness(ctx context.Context) (domain.AuthorityStatus, error) {
	if err := ctx.Err(); err != nil {
		return domain.AuthorityStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.readiness(), nil
}

// Reserve atomically records a reservation against a matching live limit row.
func (s *MemoryStore) Reserve(ctx context.Context, cmd app.ReserveCommand) (app.ReserveResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ReserveResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.reserve(cmd, discardMutationLog{})
}

// Settle reconciles final or partial usage against a matching reservation.
func (s *MemoryStore) Settle(ctx context.Context, cmd app.SettleCommand) (app.SettleResult, error) {
	if err := ctx.Err(); err != nil {
		return app.SettleResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.settle(cmd, discardMutationLog{})
}

// Release releases unused reservation capacity for swallowed or losing attempts.
func (s *MemoryStore) Release(ctx context.Context, cmd app.ReleaseCommand) (app.ReleaseResult, error) {
	if err := ctx.Err(); err != nil {
		return app.ReleaseResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.release(cmd, discardMutationLog{})
}

// LimitStatus returns bounded live limit rows.
func (s *MemoryStore) LimitStatus(ctx context.Context, q controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	if err := ctx.Err(); err != nil {
		return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.limitStatus(q)
}

// DecisionHistory returns bounded decision rows.
func (s *MemoryStore) DecisionHistory(ctx context.Context, q controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	if err := ctx.Err(); err != nil {
		return controlplane.Page[controlplane.AccountingDecisionRow]{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.decisionHistory(q)
}

func (c *storeCore) reserve(cmd app.ReserveCommand, log MutationLog) (app.ReserveResult, error) {
	if out, ok := c.reserveExisting(cmd.SourceKey); ok {
		return out, nil
	}
	if err := c.ensureWritable(); err != nil {
		return app.ReserveResult{}, err
	}
	if cmd.EstimateOnly {
		return app.ReserveResult{
			Applied:       false,
			ReservationID: cmd.ReservationKey.String(),
			RuleID:        cmd.RuleID,
			RuleType:      cmd.RuleType,
		}, nil
	}

	row, key, ok := c.matchLimitRow(cmd.RuleID, cmd.Dimensions, cmd.At)
	if !ok {
		return app.ReserveResult{}, wrapUnavailable("reserve", "matching limit not found")
	}

	amount := reserveAmount(cmd)
	if amount.Value == 0 {
		return app.ReserveResult{}, fmt.Errorf("%w: empty reserve amount", app.ErrReservationConflict)
	}

	if c.cfg.Backing.StrictCapable() {
		remaining := row.Limit - row.Consumed - row.Reserved
		if amount.Value > remaining {
			c.appendDecision(log, commandSnapshot{
				Correlation: correlationFromReservationKey(cmd.ReservationKey, cmd.Dimensions),
				Scope:       scopeSnapshotFromDimensions(cmd.Dimensions),
			}, row, cmd.ReservationKey.String(), controlplane.AccountingOutcomeDeny, "reservation_conflict", controlplane.AccountingAuthoritySourceUnavailable, controlplane.AccountingSettlementUnavailable, amount, 0, 0, 0)
			return app.ReserveResult{}, fmt.Errorf("%w: strict reservation would exceed remaining capacity", app.ErrReservationConflict)
		}
	}

	row.Reserved += amount.Value
	row.Remaining = maxInt64(0, row.Limit-row.Consumed-row.Reserved)
	c.limits[key] = row
	log.CaptureLimitUpdate(key, row)

	// The reservation ID is the reservation key string: deterministic per
	// logical request so repeated admission for the same (request, attempt,
	// rule) tuple converges on one reservation idempotently.
	record := &reservationRecord{
		ReservationKey: cmd.ReservationKey.String(),
		SourceKey:      cmd.SourceKey,
		ReservationID:  cmd.ReservationKey.String(),
		RuleID:         cmd.RuleID,
		RuleType:       cmd.RuleType,
		Dimensions:     cmd.Dimensions,
		Request:        cmd.Request,
		Spend:          cmd.Spend,
		Authority:      cmd.Authority,
		Applied:        true,
		ReservedAmount: amount,
		CreatedAt:      cmd.At,
	}
	c.reservations[record.ReservationKey] = record
	if cmd.SourceKey != "" {
		c.resBySource[cmd.SourceKey] = record.ReservationKey
	}
	log.CaptureReservationUpsert(record.ReservationKey, record)
	c.appendDecision(log, commandSnapshot{
		Correlation: correlationFromReservationKey(cmd.ReservationKey, cmd.Dimensions),
		Scope:       scopeSnapshotFromDimensions(cmd.Dimensions),
	}, row, record.ReservationID, controlplane.AccountingOutcomeReserve, "reserved", controlplane.AccountingAuthoritySourceReserved, controlplane.AccountingSettlementPending, amount, 0, 0, 0)

	return app.ReserveResult{
		Applied:        true,
		ReservationID:  record.ReservationID,
		ReservedAmount: amount,
		RuleID:         cmd.RuleID,
		RuleType:       cmd.RuleType,
	}, nil
}

func (c *storeCore) reserveExisting(sourceKey string) (app.ReserveResult, bool) {
	if sourceKey == "" {
		return app.ReserveResult{}, false
	}
	reservationKey, ok := c.resBySource[sourceKey]
	if !ok {
		return app.ReserveResult{}, false
	}
	rec := c.reservations[reservationKey]
	if rec == nil {
		return app.ReserveResult{}, false
	}
	return app.ReserveResult{
		Applied:        false,
		ReservationID:  rec.ReservationID,
		ReservedAmount: rec.ReservedAmount,
		RuleID:         rec.RuleID,
		RuleType:       rec.RuleType,
	}, true
}

func (c *storeCore) settle(cmd app.SettleCommand, log MutationLog) (app.SettleResult, error) {
	if out, ok := c.settleExisting(cmd.SourceKey); ok {
		return out, nil
	}
	if err := c.ensureWritable(); err != nil {
		return app.SettleResult{}, err
	}
	rec, ok := c.reservations[cmd.ReservationKey.String()]
	if !ok {
		return app.SettleResult{}, fmt.Errorf("%w: reservation not found", app.ErrReservationConflict)
	}
	row, key, ok := c.matchLimitRow(rec.RuleID, rec.Dimensions, cmd.At)
	if !ok {
		return app.SettleResult{}, wrapUnavailable("settle", "matching limit not found")
	}
	if rec.Settled {
		return app.SettleResult{
			Applied:         false,
			ReservationID:   rec.ReservationID,
			ReleasedDelta:   domain.Amount{Unit: cmd.ReservedUsage.Unit},
			OverageDelta:    domain.Amount{Unit: cmd.ReservedUsage.Unit},
			AdjustmentDelta: domain.Amount{Unit: cmd.ReservedUsage.Unit},
		}, nil
	}

	actual := cmd.FinalUsage
	if cmd.Kind != app.SettlementKindFinal && actual.Value == 0 {
		actual = cmd.EstimatedUsage
	}
	released := int64(0)
	overage := int64(0)
	if actual.Value < cmd.ReservedUsage.Value {
		released = cmd.ReservedUsage.Value - actual.Value
	} else if actual.Value > cmd.ReservedUsage.Value {
		overage = actual.Value - cmd.ReservedUsage.Value
	}
	adjustment := released - overage

	row.Consumed += actual.Value
	row.Reserved = maxInt64(0, row.Reserved-cmd.ReservedUsage.Value)
	row.Adjustment += adjustment
	row.Remaining = maxInt64(0, row.Limit-row.Consumed-row.Reserved)
	c.limits[key] = row
	log.CaptureLimitUpdate(key, row)

	rec.Settled = true
	rec.SettledAt = cmd.At
	rec.SettlementKind = cmd.Kind
	c.settleBySrc[cmd.SourceKey] = rec.ReservationKey
	c.reservations[rec.ReservationKey] = rec
	log.CaptureReservationUpsert(rec.ReservationKey, rec)

	c.appendDecision(log, commandSnapshot{
		Correlation: correlationFromReservationKey(cmd.ReservationKey, rec.Dimensions),
		Scope:       scopeSnapshotFromDimensions(rec.Dimensions),
	}, row, rec.ReservationID, controlplane.AccountingOutcomeReconcile, "reconciled", controlplane.AccountingAuthoritySourceReconciled, settlementStateForCommand(cmd.Kind), cmd.FinalUsage, released, overage, adjustment)

	return app.SettleResult{
		Applied:         true,
		ReservationID:   rec.ReservationID,
		ReleasedDelta:   domain.Amount{Unit: cmd.ReservedUsage.Unit, Value: released, Currency: cmd.ReservedUsage.Currency},
		OverageDelta:    domain.Amount{Unit: cmd.FinalUsage.Unit, Value: overage, Currency: cmd.FinalUsage.Currency},
		AdjustmentDelta: domain.Amount{Unit: cmd.FinalUsage.Unit, Value: adjustment, Currency: cmd.FinalUsage.Currency},
	}, nil
}

func (c *storeCore) settleExisting(sourceKey string) (app.SettleResult, bool) {
	if sourceKey == "" {
		return app.SettleResult{}, false
	}
	reservationKey, ok := c.settleBySrc[sourceKey]
	if !ok {
		return app.SettleResult{}, false
	}
	rec := c.reservations[reservationKey]
	if rec == nil {
		return app.SettleResult{}, false
	}
	return app.SettleResult{
		Applied:       false,
		ReservationID: rec.ReservationID,
	}, true
}

func (c *storeCore) release(cmd app.ReleaseCommand, log MutationLog) (app.ReleaseResult, error) {
	if out, ok := c.releaseExisting(cmd.SourceKey); ok {
		return out, nil
	}
	if err := c.ensureWritable(); err != nil {
		return app.ReleaseResult{}, err
	}
	rec, ok := c.reservations[cmd.ReservationKey.String()]
	if !ok {
		return app.ReleaseResult{}, fmt.Errorf("%w: reservation not found", app.ErrReservationConflict)
	}
	row, key, ok := c.matchLimitRow(rec.RuleID, rec.Dimensions, cmd.At)
	if !ok {
		return app.ReleaseResult{}, wrapUnavailable("release", "matching limit not found")
	}
	if rec.Released {
		return app.ReleaseResult{Applied: false, ReservationID: rec.ReservationID}, nil
	}

	released := cmd.Amount.Value
	if released > rec.ReservedAmount.Value {
		released = rec.ReservedAmount.Value
	}
	row.Reserved = maxInt64(0, row.Reserved-released)
	row.Adjustment += released
	row.Remaining = maxInt64(0, row.Limit-row.Consumed-row.Reserved)
	c.limits[key] = row
	log.CaptureLimitUpdate(key, row)

	rec.Released = true
	rec.ReleasedAt = cmd.At
	rec.ReleaseKind = cmd.Kind
	c.releaseBySrc[cmd.SourceKey] = rec.ReservationKey
	c.reservations[rec.ReservationKey] = rec
	log.CaptureReservationUpsert(rec.ReservationKey, rec)

	c.appendDecision(log, commandSnapshot{
		Correlation: correlationFromReservationKey(cmd.ReservationKey, rec.Dimensions),
		Scope:       scopeSnapshotFromDimensions(rec.Dimensions),
	}, row, rec.ReservationID, controlplane.AccountingOutcomeReconcile, "released", controlplane.AccountingAuthoritySourceReserved, controlplane.AccountingSettlementReleased, cmd.Amount, released, 0, released)

	return app.ReleaseResult{
		Applied:       true,
		ReservationID: rec.ReservationID,
		ReleasedDelta: domain.Amount{Unit: cmd.Amount.Unit, Value: released, Currency: cmd.Amount.Currency},
	}, nil
}

func (c *storeCore) releaseExisting(sourceKey string) (app.ReleaseResult, bool) {
	if sourceKey == "" {
		return app.ReleaseResult{}, false
	}
	reservationKey, ok := c.releaseBySrc[sourceKey]
	if !ok {
		return app.ReleaseResult{}, false
	}
	rec := c.reservations[reservationKey]
	if rec == nil {
		return app.ReleaseResult{}, false
	}
	return app.ReleaseResult{
		Applied:       false,
		ReservationID: rec.ReservationID,
	}, true
}

func (c *storeCore) limitStatus(q controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	rows := make([]controlplane.AccountingLimitStatusRow, 0, len(c.limits))
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
	rows := make([]controlplane.AccountingDecisionRow, 0, len(c.decisions))
	unsupported := make([]controlplane.UnsupportedFilter, 0, 1)
	if !q.Common.TimeRange.From.IsZero() || !q.Common.TimeRange.To.IsZero() {
		unsupported = append(unsupported, controlplane.UnsupportedFilter{Field: "time_range", Reason: "decision rows do not record time ranges"})
	}
	for _, rec := range c.decisions {
		if !decisionRowMatchesQuery(rec.Row, q) {
			continue
		}
		rows = append(rows, rec.Row)
	}
	return pageRows(rows, q.Limit, q.Cursor, q.Visibility, unsupported), nil
}

func (c *storeCore) appendDecision(log MutationLog, snapshot commandSnapshot, row *controlplane.AccountingLimitStatusRow, reservationID string, outcome controlplane.AccountingOutcome, reason string, authority controlplane.AccountingAuthoritySource, settlementState controlplane.AccountingSettlementState, amount domain.Amount, released, overage, adjustment int64) {
	if row == nil {
		return
	}
	rec := decisionRecord{
		Seq:       c.nextDecisionSeq(),
		SourceKey: reservationID + "|" + reason,
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
			WindowStart:    row.WindowStart,
			WindowEnd:      row.WindowEnd,
			WindowResetAt:  row.WindowResetAt,
			EvidenceState:  controlplane.EvidenceRecorded,
			RedactionState: controlplane.RedactionSummarized,
		},
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
	var (
		matchKey string
		match    *controlplane.AccountingLimitStatusRow
	)
	for key, row := range c.limits {
		if row.RuleID != ruleID {
			continue
		}
		if !ScopeDimensionsMatch(row.Scope, dims) {
			continue
		}
		if !correlationFieldMatches(row.Correlation.BackendID, valueString(dims.Backend)) || !correlationFieldMatches(row.Correlation.Model, valueString(dims.Model)) {
			continue
		}
		if !row.WindowStart.IsZero() && at.Before(row.WindowStart) {
			continue
		}
		if !row.WindowEnd.IsZero() && !at.Before(row.WindowEnd) {
			continue
		}
		matchKey = key
		match = row
		break
	}
	return match, matchKey, match != nil
}

func limitRowMatchesQuery(row controlplane.AccountingLimitStatusRow, q controlplane.AccountingLimitStatusQuery) bool {
	if !commonFiltersMatch(
		commonQueryFields{Common: q.Common, RuleID: q.RuleID, Unit: q.Unit, Currency: q.Currency, Authority: q.Authority, EvidenceState: q.EvidenceState, RedactionState: q.RedactionState},
		commonRowFields{Correlation: row.Correlation, Scope: row.Scope, RuleID: row.RuleID, Unit: row.Unit, Currency: row.Currency, Authority: row.Authority, EvidenceState: row.EvidenceState, RedactionState: row.RedactionState},
	) {
		return false
	}
	if !q.Common.TimeRange.From.IsZero() && row.WindowEnd.Before(q.Common.TimeRange.From) {
		return false
	}
	if !q.Common.TimeRange.To.IsZero() && row.WindowStart.After(q.Common.TimeRange.To) {
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
// satisfies the 7 shared scope fields of an incoming domain.Dimensions
// target. It is the parallel helper to ScopeMatches for the resolver path,
// which works in domain.Dimensions rather than controlplane.ScopeFilters.
// Backend, Model, Route, and PolicyLabels are authority-specific dimensions
// and are matched separately by the caller.
func ScopeDimensionsMatch(row controlplane.ScopeSnapshot, dims domain.Dimensions) bool {
	return scopeValueMatches(row.PrincipalID, dims.Principal) &&
		scopeValueMatches(row.TenantID, dims.Tenant) &&
		scopeValueMatches(row.OrganizationID, dims.Organization) &&
		scopeValueMatches(row.WorkspaceID, dims.Workspace) &&
		scopeValueMatches(row.ProjectID, dims.Project) &&
		scopeValueMatches(row.DepartmentID, dims.Department) &&
		scopeValueMatches(row.CostCenterID, dims.CostCenter)
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
	if end > len(rows) {
		end = len(rows)
	}
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

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func reserveAmount(cmd app.ReserveCommand) domain.Amount {
	if cmd.Request.Value != 0 {
		return cmd.Request
	}
	return cmd.Spend
}

func settlementStateForCommand(kind app.SettlementKind) controlplane.AccountingSettlementState {
	switch kind {
	case app.SettlementKindPartial, app.SettlementKindUnavailable, app.SettlementKindCancellation:
		return controlplane.AccountingSettlementUnavailable
	case app.SettlementKindSwallowed, app.SettlementKindLosing:
		return controlplane.AccountingSettlementReleased
	default:
		return controlplane.AccountingSettlementSettled
	}
}

func wrapUnavailable(op, reason string) error {
	return fmt.Errorf("authoritystore %s: %w: %s", op, app.ErrUnavailable, reason)
}
