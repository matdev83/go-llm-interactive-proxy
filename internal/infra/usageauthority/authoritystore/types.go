package authoritystore

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

const defaultStoreID = "usage-authority"

// Config seeds a store with live rows and readiness posture.
//
// RuleWindows maps rule IDs to their fixed-window spec so the store can resolve
// the configured current window on demand after a rollover (requirement 3.5).
// Rules absent from the map (or with an unconfigured spec) keep the legacy
// behavior: an expired window stays unavailable.
type Config struct {
	StoreID string
	Backing domain.BackingCapability

	Readiness       domain.AuthorityStatus
	LimitRows       []controlplane.AccountingLimitStatusRow
	DecisionRows    []controlplane.AccountingDecisionRow
	RuleWindows     map[string]domain.WindowSpec
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
	// applyUsageBySrc tracks advisory/no-reservation usage source keys that have
	// already been applied so replays are no-ops (requirement 7.7, 7.8). Unlike
	// settle/release, applyUsage creates no reservation record, so idempotency is
	// tracked here and hydrated from the decision ledger on durable load.
	applyUsageBySrc map[string]struct{}
	// unreservedUsageFacts stores the latest usage fact for each logical
	// source/rule pair. A partial estimate and a later final/provider fact use
	// the same source key; replacing the fact applies only the delta, so
	// unreserved windows cannot double-count reconciliation updates.
	unreservedUsageFacts map[string]unreservedUsageFact
	nextDecision         int64
	unsupported          map[string]struct{}

	// ruleWindows carries the per-rule fixed-window spec used to resolve the
	// current configured row after rollover. It is copied from Config at
	// construction so mutations to the caller's map never affect the store.
	ruleWindows map[string]domain.WindowSpec
	// limitTemplates contains the current configured row shape for each rule.
	// Persisted rows are retained in limits for history and reservation
	// settlement, but new admissions resolve only through these templates so a
	// removed or superseded row cannot become active after a restart.
	limitTemplates map[string][]controlplane.AccountingLimitStatusRow
}

type decisionRecord struct {
	Seq       int64                              `json:"seq"`
	SourceKey string                             `json:"source_key"`
	Row       controlplane.AccountingDecisionRow `json:"row"`
}

type reservationRecord struct {
	ReservationKey string                `json:"reservation_key"`
	LimitRowKey    string                `json:"limit_row_key,omitempty"`
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
	// SettledActual remembers the enforceable amount consumed by the prior
	// settlement so a later authoritative re-settlement (new source key) can
	// compute and apply an adjustment delta instead of becoming a permanent
	// no-op (requirement 7.6, 8.4-8.6). It is in the reservation's enforceable
	// unit (money for budget/spend-cap rules, tokens/requests otherwise) and
	// persists via the existing JSON flush.
	SettledActual     domain.Amount            `json:"settled_actual"`
	SettledAuthority  app.MeasurementAuthority `json:"settled_authority"`
	SettlementSources []string                 `json:"settlement_sources,omitempty"`
	Released          bool                     `json:"released"`
	ReleaseSources    []string                 `json:"release_sources,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	SettledAt         time.Time                `json:"settled_at"`
	ReleasedAt        time.Time                `json:"released_at"`
	SettlementKind    app.SettlementKind       `json:"settlement_kind,omitempty"`
	ReleaseKind       app.ReleaseKind          `json:"release_kind,omitempty"`
}

type unreservedUsageFact struct {
	SourceKey            string                   `json:"source_key"`
	RuleID               string                   `json:"rule_id"`
	LimitRowKey          string                   `json:"limit_row_key"`
	Amount               domain.Amount            `json:"amount"`
	Authority            domain.AuthorityLevel    `json:"authority"`
	MeasurementAuthority app.MeasurementAuthority `json:"measurement_authority"`
	Kind                 app.SettlementKind       `json:"kind"`
	At                   time.Time                `json:"at"`
}

type commandSnapshot struct {
	Correlation        controlplane.Correlation
	Scope              controlplane.ScopeSnapshot
	Surfaced           controlplane.UsageSurfaced
	ParentRequestID    string
	BoundPolicyVersion controlplane.VersionRef
	BoundRatingVersion controlplane.VersionRef
}

func newStoreCore(cfg Config) *storeCore {
	cfg = cfg.normalized()
	core := &storeCore{
		storeID:              cfg.StoreID,
		cfg:                  cfg,
		state:                cfg.Readiness,
		limits:               make(map[string]*controlplane.AccountingLimitStatusRow, len(cfg.LimitRows)),
		reservations:         make(map[string]*reservationRecord, len(cfg.reservationRows)),
		resBySource:          make(map[string]string, len(cfg.reservationRows)),
		settleBySrc:          make(map[string]string, len(cfg.reservationRows)),
		releaseBySrc:         make(map[string]string, len(cfg.reservationRows)),
		applyUsageBySrc:      make(map[string]struct{}),
		unreservedUsageFacts: make(map[string]unreservedUsageFact),
		unsupported:          make(map[string]struct{}, len(cfg.Unsupported)),
		ruleWindows:          make(map[string]domain.WindowSpec, len(cfg.RuleWindows)),
		limitTemplates:       make(map[string][]controlplane.AccountingLimitStatusRow),
	}
	maps.Copy(core.ruleWindows, cfg.RuleWindows)
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
		core.limitTemplates[cp.RuleID] = append(core.limitTemplates[cp.RuleID], cp)
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
		c.limitTemplates[cp.RuleID] = append(c.limitTemplates[cp.RuleID], cp)
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
	out = append(out, c.decisions...)
	return out
}

// clone returns an isolated projection for one transactional mutation. The
// store core applies a complete reservation/settlement/release set to the
// clone and publishes it only after every descriptor validates successfully.
// This is the memory-store transaction boundary and also gives the durable
// adapter a projection it can safely discard before rollback.
func (c *storeCore) clone() *storeCore {
	if c == nil {
		return nil
	}
	out := *c
	out.limits = make(map[string]*controlplane.AccountingLimitStatusRow, len(c.limits))
	for key, row := range c.limits {
		if row == nil {
			continue
		}
		cp := *row
		cp.Scope = cloneScopeSnapshot(row.Scope)
		out.limits[key] = &cp
	}
	out.decisions = append([]decisionRecord(nil), c.decisions...)
	out.reservations = make(map[string]*reservationRecord, len(c.reservations))
	for key, rec := range c.reservations {
		if rec == nil {
			continue
		}
		cp := *rec
		cp.Dimensions = cloneDimensions(rec.Dimensions)
		cp.SettlementSources = append([]string(nil), rec.SettlementSources...)
		cp.ReleaseSources = append([]string(nil), rec.ReleaseSources...)
		out.reservations[key] = &cp
	}
	out.resBySource = cloneStringMap(c.resBySource)
	out.settleBySrc = cloneStringMap(c.settleBySrc)
	out.releaseBySrc = cloneStringMap(c.releaseBySrc)
	out.applyUsageBySrc = cloneSet(c.applyUsageBySrc)
	out.unreservedUsageFacts = cloneUnreservedUsageFacts(c.unreservedUsageFacts)
	out.unsupported = cloneSet(c.unsupported)
	out.ruleWindows = cloneRuleWindows(c.ruleWindows)
	out.limitTemplates = cloneLimitTemplates(c.limitTemplates)
	return &out
}

// reconcileLimitRows overlays current configuration onto persisted rows while
// preserving accounting facts. Configuration is authoritative for the row
// shape and limit; consumed/reserved/adjustment are durable facts and survive
// an operator edit to the rule.
func (c *storeCore) reconcileLimitRows(rows map[string]*controlplane.AccountingLimitStatusRow) map[string]*controlplane.AccountingLimitStatusRow {
	if rows == nil {
		rows = make(map[string]*controlplane.AccountingLimitStatusRow)
	}
	for _, templates := range c.limitTemplates {
		for _, template := range templates {
			key := limitRowKey(template)
			persisted := rows[key]
			if persisted == nil {
				cp := template
				cp.Remaining = max(0, cp.Limit-cp.Consumed-cp.Reserved)
				rows[key] = &cp
				continue
			}
			merged := template
			merged.Consumed = persisted.Consumed
			merged.Reserved = persisted.Reserved
			merged.Adjustment = persisted.Adjustment
			merged.Remaining = max(0, merged.Limit-merged.Consumed-merged.Reserved)
			rows[key] = &merged
		}
	}
	return rows
}

func cloneDimensions(in domain.Dimensions) domain.Dimensions {
	out := in
	if len(in.PolicyLabels) > 0 {
		out.PolicyLabels = make(map[string]scope.Value, len(in.PolicyLabels))
		maps.Copy(out.PolicyLabels, in.PolicyLabels)
	}
	return out
}

func cloneScopeSnapshot(in controlplane.ScopeSnapshot) controlplane.ScopeSnapshot {
	out := in
	out.Principal = in.Principal.Clone()
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return make(map[string]string)
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func cloneSet(in map[string]struct{}) map[string]struct{} {
	if len(in) == 0 {
		return make(map[string]struct{})
	}
	out := make(map[string]struct{}, len(in))
	for key := range in {
		out[key] = struct{}{}
	}
	return out
}

func cloneUnreservedUsageFacts(in map[string]unreservedUsageFact) map[string]unreservedUsageFact {
	if len(in) == 0 {
		return make(map[string]unreservedUsageFact)
	}
	out := make(map[string]unreservedUsageFact, len(in))
	maps.Copy(out, in)
	return out
}

func cloneRuleWindows(in map[string]domain.WindowSpec) map[string]domain.WindowSpec {
	return maps.Clone(in)
}

func cloneLimitTemplates(in map[string][]controlplane.AccountingLimitStatusRow) map[string][]controlplane.AccountingLimitStatusRow {
	out := make(map[string][]controlplane.AccountingLimitStatusRow, len(in))
	for ruleID, rows := range in {
		out[ruleID] = append([]controlplane.AccountingLimitStatusRow(nil), rows...)
	}
	return out
}

func (c *storeCore) snapshotFromReserve(cmd app.ReserveCommand) commandSnapshot {
	return enrichCommandSnapshot(commandSnapshot{
		Correlation:        cmd.Correlation,
		Scope:              scopeSnapshotFromDimensionsWithFallback(cmd.Scope, cmd.Dimensions),
		Surfaced:           cmd.Surfaced,
		ParentRequestID:    strings.TrimSpace(cmd.ParentRequestID),
		BoundPolicyVersion: cmd.BoundPolicyVersion,
		BoundRatingVersion: cmd.BoundRatingVersion,
	})
}

func mutationSnapshot(correlation controlplane.Correlation, view scope.PrincipalScopeView, dims domain.Dimensions) commandSnapshot {
	return enrichCommandSnapshot(commandSnapshot{
		Correlation: correlation,
		Scope:       scopeSnapshotFromDimensionsWithFallback(view, dims),
	})
}

func enrichCommandSnapshot(snap commandSnapshot) commandSnapshot {
	if strings.TrimSpace(snap.ParentRequestID) == "" {
		snap.ParentRequestID = strings.TrimSpace(snap.Correlation.RequestID)
	}
	if snap.Surfaced == "" {
		snap.Surfaced = controlplane.UsageSurfacedUnknown
	}
	return snap
}

func scopeSnapshotFromDimensionsWithFallback(view scope.PrincipalScopeView, dims domain.Dimensions) controlplane.ScopeSnapshot {
	if !scopeViewEmpty(view) {
		return scopeSnapshotFromView(view)
	}
	return scopeSnapshotFromDimensions(dims)
}

func scopeViewEmpty(view scope.PrincipalScopeView) bool {
	return view.PrincipalID.IsUnknown() && view.CredentialID.IsUnknown() && view.TenantID.IsUnknown() &&
		view.OrganizationID.IsUnknown() && view.WorkspaceID.IsUnknown() && view.ProjectID.IsUnknown() &&
		view.DepartmentID.IsUnknown() && view.CostCenterID.IsUnknown() && len(view.PolicyLabels) == 0
}

func scopeSnapshotFromView(view scope.PrincipalScopeView) controlplane.ScopeSnapshot {
	return controlplane.ScopeSnapshot{
		Principal:      view.Clone(),
		PrincipalID:    view.PrincipalID,
		CredentialID:   view.CredentialID,
		TenantID:       view.TenantID,
		OrganizationID: view.OrganizationID,
		WorkspaceID:    view.WorkspaceID,
		ProjectID:      view.ProjectID,
		DepartmentID:   view.DepartmentID,
		CostCenterID:   view.CostCenterID,
	}
}

func scopeSnapshotFromDimensions(d domain.Dimensions) controlplane.ScopeSnapshot {
	principal := scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectUnknown,
		PrincipalID:    d.Principal,
		CredentialID:   d.Credential,
		TenantID:       d.Tenant,
		OrganizationID: d.Organization,
		WorkspaceID:    d.Workspace,
		ProjectID:      d.Project,
		DepartmentID:   d.Department,
		CostCenterID:   d.CostCenter,
		Origin:         scope.OriginInternal,
	}
	if len(d.PolicyLabels) > 0 {
		principal.PolicyLabels = make(map[string]string, len(d.PolicyLabels))
		for k, v := range d.PolicyLabels {
			// A missing map entry is the safe representation of an unknown
			// label. A present empty string remains known-empty, so do not
			// collapse the two states while projecting into ScopeSnapshot.
			if v.IsKnown() {
				principal.PolicyLabels[k] = v.String()
			}
		}
		if len(principal.PolicyLabels) == 0 {
			principal.PolicyLabels = nil
		}
	}
	return controlplane.ScopeSnapshot{
		Principal:      principal,
		PrincipalID:    d.Principal,
		CredentialID:   d.Credential,
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

func effectiveAuthorityNamespace(ns string) string {
	if strings.TrimSpace(ns) == "" {
		return domain.NamespaceLegacy
	}
	return strings.TrimSpace(ns)
}

func limitRowKey(row controlplane.AccountingLimitStatusRow) string {
	// Include every identity dimension in the key. In particular, credential
	// and policy-label dimensions must not collide when two rules share the same
	// correlation and window. JSON gives us stable map-key ordering while the
	// fallback keeps the key usable if a future identity field becomes
	// non-serializable.
	identity := struct {
		RuleID             string                     `json:"rule_id"`
		AuthorityNamespace string                     `json:"authority_namespace"`
		Perspective        string                     `json:"perspective"`
		LifecycleScope     string                     `json:"lifecycle_scope"`
		Basis              string                     `json:"basis"`
		RuleVersion        string                     `json:"rule_version"`
		Correlation        controlplane.Correlation   `json:"correlation"`
		Scope              controlplane.ScopeSnapshot `json:"scope"`
		Unit               string                     `json:"unit"`
		Currency           string                     `json:"currency"`
		WindowStart        time.Time                  `json:"window_start"`
		WindowEnd          time.Time                  `json:"window_end"`
	}{
		RuleID:             row.RuleID,
		AuthorityNamespace: effectiveAuthorityNamespace(row.AuthorityNamespace),
		Perspective:        row.Perspective,
		LifecycleScope:     row.LifecycleScope,
		Basis:              row.Basis,
		RuleVersion:        row.RuleVersion,
		Correlation:        row.Correlation,
		Scope:              row.Scope,
		Unit:               row.Unit,
		Currency:           row.Currency,
		WindowStart:        row.WindowStart.UTC(),
		WindowEnd:          row.WindowEnd.UTC(),
	}
	if raw, err := json.Marshal(identity); err == nil {
		return string(raw)
	}
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
