package authoritystore

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

const defaultStoreID = "usage-authority"

// Config seeds a store with live rows and readiness posture.
type Config struct {
	StoreID string
	Backing domain.BackingCapability

	Readiness       domain.AuthorityStatus
	LimitRows       []controlplane.AccountingLimitStatusRow
	DecisionRows    []controlplane.AccountingDecisionRow
	reservationRows []reservationRecord
	Unsupported     []string
	StrictAtomic    bool
	AdvisoryOnly    bool
}

func (c Config) normalized() Config {
	if c.StoreID == "" {
		c.StoreID = defaultStoreID
	}
	if c.Backing == "" {
		c.Backing = domain.BackingCapabilityAtomic
	}
	if c.Readiness.State.IsKnown() {
		return c
	}
	c.Readiness = domain.StatusFromBacking(c.Backing)
	return c
}

type storeCore struct {
	storeID string
	cfg     Config
	state   domain.AuthorityStatus

	limits       map[string]*controlplane.AccountingLimitStatusRow
	decisions    []decisionRecord
	reservations map[string]*reservationRecord
	resBySource  map[string]string
	settleBySrc  map[string]string
	releaseBySrc map[string]string
	nextDecision int64
	unsupported  map[string]struct{}
}

type decisionRecord struct {
	Seq       int64                              `json:"seq"`
	SourceKey string                             `json:"source_key"`
	Row       controlplane.AccountingDecisionRow `json:"row"`
}

type reservationRecord struct {
	ReservationKey string                `json:"reservation_key"`
	SourceKey      string                `json:"source_key"`
	ReservationID  string                `json:"reservation_id"`
	RuleID         string                `json:"rule_id"`
	RuleType       string                `json:"rule_type"`
	Dimensions     domain.Dimensions     `json:"dimensions"`
	Request        domain.Amount         `json:"request"`
	Spend          domain.Amount         `json:"spend"`
	Authority      domain.AuthorityLevel `json:"authority"`
	Applied        bool                  `json:"applied"`
	ReservedAmount domain.Amount         `json:"reserved_amount"`
	Settled        bool                  `json:"settled"`
	Released       bool                  `json:"released"`
	CreatedAt      time.Time             `json:"created_at"`
	SettledAt      time.Time             `json:"settled_at"`
	ReleasedAt     time.Time             `json:"released_at"`
	SettlementKind app.SettlementKind    `json:"settlement_kind,omitempty"`
	ReleaseKind    app.ReleaseKind       `json:"release_kind,omitempty"`
}

type commandSnapshot struct {
	Correlation controlplane.Correlation
	Scope       controlplane.ScopeSnapshot
}

func newStoreCore(cfg Config) *storeCore {
	cfg = cfg.normalized()
	core := &storeCore{
		storeID:      cfg.StoreID,
		cfg:          cfg,
		state:        cfg.Readiness,
		limits:       make(map[string]*controlplane.AccountingLimitStatusRow, len(cfg.LimitRows)),
		reservations: make(map[string]*reservationRecord, len(cfg.reservationRows)),
		resBySource:  make(map[string]string, len(cfg.reservationRows)),
		settleBySrc:  make(map[string]string, len(cfg.reservationRows)),
		releaseBySrc: make(map[string]string, len(cfg.reservationRows)),
		unsupported:  make(map[string]struct{}, len(cfg.Unsupported)),
	}
	for _, field := range cfg.Unsupported {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		core.unsupported[field] = struct{}{}
	}
	for _, row := range cfg.LimitRows {
		cp := row
		core.limits[limitRowKey(cp)] = &cp
	}
	for _, rec := range cfg.reservationRows {
		cp := rec
		core.reservations[cp.ReservationKey] = &cp
		if cp.SourceKey != "" {
			core.resBySource[cp.SourceKey] = cp.ReservationKey
		}
	}
	for _, row := range cfg.DecisionRows {
		rec := decisionRecord{
			Seq:       core.nextDecision + 1,
			SourceKey: row.Correlation.RequestID,
			Row:       row,
		}
		core.nextDecision = rec.Seq
		core.decisions = append(core.decisions, rec)
	}
	if core.nextDecision == 0 {
		core.nextDecision = 1
	} else {
		core.nextDecision++
	}
	return core
}

func (c *storeCore) readiness() domain.AuthorityStatus {
	if c == nil {
		return domain.AuthorityStatus{State: domain.AuthorityStateDisabled, Reason: domain.StatusReasonDisabledByConfig}
	}
	if c.state.State.IsKnown() {
		return c.state
	}
	return domain.StatusFromBacking(c.cfg.Backing)
}

func (c *storeCore) seedLimitRows(rows []controlplane.AccountingLimitStatusRow) {
	for _, row := range rows {
		cp := row
		c.limits[limitRowKey(cp)] = &cp
	}
}

func (c *storeCore) cloneLimitRows() []controlplane.AccountingLimitStatusRow {
	out := make([]controlplane.AccountingLimitStatusRow, 0, len(c.limits))
	for _, row := range c.limits {
		cp := *row
		out = append(out, cp)
	}
	return out
}

func (c *storeCore) cloneDecisionRows() []decisionRecord {
	out := make([]decisionRecord, 0, len(c.decisions))
	for _, rec := range c.decisions {
		out = append(out, rec)
	}
	return out
}

func (c *storeCore) snapshotFromReserve(cmd app.ReserveCommand) commandSnapshot {
	return commandSnapshot{
		Correlation: correlationFromReservationKey(cmd.ReservationKey, cmd.Dimensions),
		Scope:       scopeSnapshotFromDimensions(cmd.Dimensions),
	}
}

func correlationFromReservationKey(k domain.ReservationKey, dims domain.Dimensions) controlplane.Correlation {
	return controlplane.Correlation{
		TraceID:    k.LogicalRequestID,
		RequestID:  k.LogicalRequestID,
		SessionID:  k.LogicalRequestID,
		ALegID:     k.ALegID,
		BLegID:     k.BLegID,
		AttemptSeq: k.Sequence,
		BackendID:  valueString(dims.Backend),
		Model:      valueString(dims.Model),
	}
}

func scopeSnapshotFromDimensions(d domain.Dimensions) controlplane.ScopeSnapshot {
	principal := scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectUnknown,
		PrincipalID:    d.Principal,
		CredentialID:   scope.Unknown(),
		TenantID:       d.Tenant,
		OrganizationID: d.Organization,
		WorkspaceID:    d.Workspace,
		ProjectID:      d.Project,
		DepartmentID:   d.Department,
		CostCenterID:   d.CostCenter,
		Origin:         scope.OriginInternal,
	}
	if d.PolicyLabels != nil {
		principal.PolicyLabels = make(map[string]string, len(d.PolicyLabels))
		for k, v := range d.PolicyLabels {
			principal.PolicyLabels[k] = v.String()
		}
	}
	return controlplane.ScopeSnapshot{
		Principal:      principal,
		PrincipalID:    d.Principal,
		CredentialID:   scope.Unknown(),
		TenantID:       d.Tenant,
		OrganizationID: d.Organization,
		WorkspaceID:    d.Workspace,
		ProjectID:      d.Project,
		DepartmentID:   d.Department,
		CostCenterID:   d.CostCenter,
	}
}

func valueString(v scope.Value) string {
	if v.IsUnknown() {
		return ""
	}
	return v.String()
}

func limitRowKey(row controlplane.AccountingLimitStatusRow) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%d|%d", row.RuleID, row.Correlation.RequestID, row.Correlation.ALegID, row.Correlation.BLegID, row.Correlation.BackendID, row.Correlation.Model, row.WindowStart.UTC().Format(time.RFC3339Nano), row.WindowEnd.UTC().Format(time.RFC3339Nano), row.WindowStart.UnixNano(), row.WindowEnd.UnixNano())
}

func (c *storeCore) nextDecisionSeq() int64 {
	seq := c.nextDecision
	c.nextDecision++
	return seq
}

func knownUnsupported(fields ...string) []controlplane.UnsupportedFilter {
	out := make([]controlplane.UnsupportedFilter, 0, len(fields))
	for _, field := range fields {
		out = append(out, controlplane.UnsupportedFilter{Field: field, Reason: "not recorded by this store"})
	}
	return out
}

func isUnsupportedField(fields map[string]struct{}, field string) bool {
	_, ok := fields[field]
	return ok
}

func marshalJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
