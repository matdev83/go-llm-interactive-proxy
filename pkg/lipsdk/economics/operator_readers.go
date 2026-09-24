package economics

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 16.2A typed public operator reader contracts: bounded paginated
// views over reconciliation discrepancies, provider allowance history,
// unmatched statement lines with aggregate scope, and selected-cost
// adjustments. These are protected operator-reader DTOs only; no supplier
// or operator economics may be placed in frontend response DTOs.
//
// Comparison, selection and posting states travel as independent typed
// fields; readers never infer one plane from another. Native currencies,
// source references, incomparability/missing reasons and aggregate scope
// are preserved verbatim. DTOs contain no SQL, persistence handles,
// provider SDKs, or raw prompts, payloads, headers or secrets.

const (
	// OperatorPageDefaultLimit bounds one operator page when the caller
	// omits a limit.
	OperatorPageDefaultLimit = 100
	// OperatorPageMaxLimit is the hard bounded page maximum.
	OperatorPageMaxLimit = 500
	// MaxOperatorCursorBytes bounds one operator continuation cursor. It is
	// larger than MaxValuationTextBytes because a journal-backed operator
	// cursor authenticates and wraps an opaque inner source position; it stays
	// bounded so a caller cannot submit unbounded continuation state.
	MaxOperatorCursorBytes = 8192
	// MaxDiscrepancyAggregateIDs bounds one aggregate plane's retained
	// missing/incomparable/conflict evidence identity lists. It matches the
	// canonical aggregate finding bound so a durable aggregate can always be
	// projected without truncation, while a caller cannot submit unbounded
	// identity state.
	MaxDiscrepancyAggregateIDs = 4096
)

// ValidateOperatorCursorSize rejects a raw operator continuation cursor whose
// transport bytes exceed MaxOperatorCursorBytes. It inspects only the raw byte
// count, so it is safe to call before any delimiter split, Base64 decode, MAC
// verification or inner parse, and it never echoes caller content. Operator
// cursor transport is ASCII, so raw bytes are the allocation boundary and
// Unicode codepoints are deliberately not counted here. An empty cursor is not
// rejected: the caller decides whether a continuation is required.
func ValidateOperatorCursorSize(field, value string) error {
	if len(value) > MaxOperatorCursorBytes {
		return fmt.Errorf("economics: %s exceeds %d bytes", field, MaxOperatorCursorBytes)
	}
	return nil
}

// validateOperatorCursor validates a bounded operator continuation cursor. It
// remains a safe printable reference; only its size bound is larger than an
// ordinary public reference because it may wrap an opaque inner position.
func validateOperatorCursor(field, value string) error {
	if err := ValidateOperatorCursorSize(field, value); err != nil {
		return err
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("economics: %s contains invalid UTF-8", field)
	}
	if err := ValidateSafeRef(field, value); err != nil {
		return err
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("economics: %s must not have surrounding whitespace", field)
	}
	return nil
}

var (
	// ErrOperatorQueryInvalid identifies a malformed operator query: missing
	// scope, unbounded filters, unknown vocabulary or over-bound pages.
	ErrOperatorQueryInvalid = errors.New("economics: invalid operator query")
	// ErrOperatorScopeMismatch identifies evidence outside the trusted query
	// scope. Reads fail closed rather than mixing scopes.
	ErrOperatorScopeMismatch = errors.New("economics: operator scope mismatch")
	// ErrOperatorNotFound identifies an unknown scoped subject. Absent data
	// is not an error; unknown scope roots are.
	ErrOperatorNotFound = errors.New("economics: operator subject not found")
	// ErrOperatorBoundExceeded identifies a scope whose retained rows exceed
	// the bounded read contract.
	ErrOperatorBoundExceeded = errors.New("economics: operator page bound exceeded")
	// ErrOperatorCursorInvalid identifies a malformed, tampered or
	// cross-scope replayed page cursor.
	ErrOperatorCursorInvalid = errors.New("economics: invalid operator cursor")
	// ErrOperatorCursorStale identifies a continuation whose frozen full-scope
	// snapshot changed since the cursor was issued. The token is not a valid
	// position in the current snapshot; reusing it cannot produce a consistent
	// traversal, so the caller must restart pagination.
	ErrOperatorCursorStale = errors.New("economics: operator cursor snapshot changed")
)

// OperatorScope is the explicit trusted scope of one operator read. StoreID
// is always required; at least one of TenantID or AccountID is required so
// no query runs under a store-only scope. Per-reader rules narrow further.
type OperatorScope struct {
	StoreID   string `json:"store_id"`
	TenantID  string `json:"tenant_id,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

// Validate checks the trusted scope without normalizing caller memory.
func (s OperatorScope) Validate() error {
	if err := validatePublicRef("operator store id", s.StoreID); err != nil {
		return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if s.TenantID != "" {
		if err := validatePublicRef("operator tenant id", s.TenantID); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if s.AccountID != "" {
		if err := validatePublicRef("operator account id", s.AccountID); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if s.TenantID == "" && s.AccountID == "" {
		return fmt.Errorf("%w: trusted tenant or account scope required", ErrOperatorQueryInvalid)
	}
	return nil
}

// DiscrepancyStatus is the public comparison outcome vocabulary. Spellings
// match the durable reconciliation vocabulary exactly; readers pass them
// through without reinterpretation.
type DiscrepancyStatus string

const (
	DiscrepancyMatched          DiscrepancyStatus = "matched"
	DiscrepancyWithinTolerance  DiscrepancyStatus = "within_tolerance"
	DiscrepancyDiscrepant       DiscrepancyStatus = "discrepant"
	DiscrepancyPartial          DiscrepancyStatus = "partial"
	DiscrepancyIncomparable     DiscrepancyStatus = "incomparable"
	DiscrepancyMissingLocal     DiscrepancyStatus = "missing_local"
	DiscrepancyMissingProvider  DiscrepancyStatus = "missing_provider"
	DiscrepancyPendingStatement DiscrepancyStatus = "pending_statement"
	DiscrepancyConflict         DiscrepancyStatus = "conflict"
)

// IsKnown reports whether s is a documented discrepancy status.
func (s DiscrepancyStatus) IsKnown() bool {
	switch s {
	case DiscrepancyMatched, DiscrepancyWithinTolerance, DiscrepancyDiscrepant,
		DiscrepancyPartial, DiscrepancyIncomparable, DiscrepancyMissingLocal,
		DiscrepancyMissingProvider, DiscrepancyPendingStatement, DiscrepancyConflict:
		return true
	default:
		return false
	}
}

// MonetaryState is the public monetary decomposition outcome. It is a
// separate field from the quantity comparison status by design.
type MonetaryState string

const (
	MonetaryComplete     MonetaryState = "complete"
	MonetaryPartial      MonetaryState = "partial"
	MonetaryIncomparable MonetaryState = "incomparable"
	MonetaryConflict     MonetaryState = "conflict"
)

// IsKnown reports whether s is a documented monetary state.
func (s MonetaryState) IsKnown() bool {
	switch s {
	case MonetaryComplete, MonetaryPartial, MonetaryIncomparable, MonetaryConflict:
		return true
	default:
		return false
	}
}

// DiscrepancyReason is the public typed explanation for a non-complete
// comparison term. Spellings match the durable reason vocabulary exactly.
type DiscrepancyReason string

const (
	DiscrepancyReasonNone                         DiscrepancyReason = ""
	DiscrepancyReasonSubjectMismatch              DiscrepancyReason = "subject_mismatch"
	DiscrepancyReasonPeriodMismatch               DiscrepancyReason = "period_mismatch"
	DiscrepancyReasonPayerMismatch                DiscrepancyReason = "payer_mismatch"
	DiscrepancyReasonCurrencyMismatch             DiscrepancyReason = "currency_mismatch"
	DiscrepancyReasonScopeMismatch                DiscrepancyReason = "scope_mismatch"
	DiscrepancyReasonChargeMismatch               DiscrepancyReason = "charge_mismatch"
	DiscrepancyReasonCoverageMismatch             DiscrepancyReason = "coverage_mismatch"
	DiscrepancyReasonContextMismatch              DiscrepancyReason = "measurement_context_mismatch"
	DiscrepancyReasonSchemaMismatch               DiscrepancyReason = "schema_mismatch"
	DiscrepancyReasonQualifierMismatch            DiscrepancyReason = "qualifier_mismatch"
	DiscrepancyReasonTokenizerMismatch            DiscrepancyReason = "tokenizer_mismatch"
	DiscrepancyReasonTokenizerMissing             DiscrepancyReason = "tokenizer_missing"
	DiscrepancyReasonTokenizerRequired            DiscrepancyReason = "tokenizer_required"
	DiscrepancyReasonSemanticsMismatch            DiscrepancyReason = "semantics_mismatch"
	DiscrepancyReasonValueUnavailableLocal        DiscrepancyReason = "value_unavailable_local"
	DiscrepancyReasonValueUnavailableProvider     DiscrepancyReason = "value_unavailable_provider"
	DiscrepancyReasonValueUnavailableBoth         DiscrepancyReason = "value_unavailable_both"
	DiscrepancyReasonDuplicateLocal               DiscrepancyReason = "duplicate_local"
	DiscrepancyReasonDuplicateProvider            DiscrepancyReason = "duplicate_provider"
	DiscrepancyReasonConflictingLocal             DiscrepancyReason = "conflicting_local"
	DiscrepancyReasonConflictingProvider          DiscrepancyReason = "conflicting_provider"
	DiscrepancyReasonZeroDenominator              DiscrepancyReason = "zero_denominator"
	DiscrepancyReasonUnitMismatch                 DiscrepancyReason = "unit_mismatch"
	DiscrepancyReasonTolerancePolicyMissing       DiscrepancyReason = "tolerance_policy_missing"
	DiscrepancyReasonEstimatedNotExact            DiscrepancyReason = "estimated_not_exact"
	DiscrepancyReasonMissingE                     DiscrepancyReason = "missing_e"
	DiscrepancyReasonMissingQ                     DiscrepancyReason = "missing_q"
	DiscrepancyReasonMissingP                     DiscrepancyReason = "missing_p"
	DiscrepancyReasonAmountUnavailable            DiscrepancyReason = "amount_unavailable"
	DiscrepancyReasonCurrencyMissing              DiscrepancyReason = "currency_missing"
	DiscrepancyReasonTariffMismatch               DiscrepancyReason = "tariff_mismatch"
	DiscrepancyReasonValuationIncomplete          DiscrepancyReason = "valuation_incomplete"
	DiscrepancyReasonValuationConflict            DiscrepancyReason = "valuation_conflict"
	DiscrepancyReasonQuantityEvidencePartial      DiscrepancyReason = "quantity_evidence_partial"
	DiscrepancyReasonQuantityEvidenceIncomparable DiscrepancyReason = "quantity_evidence_incomparable"
	DiscrepancyReasonQuantityEvidenceConflict     DiscrepancyReason = "quantity_evidence_conflict"
)

// IsKnown reports whether r is a documented discrepancy reason.
func (r DiscrepancyReason) IsKnown() bool {
	switch r {
	case DiscrepancyReasonNone,
		DiscrepancyReasonSubjectMismatch, DiscrepancyReasonPeriodMismatch,
		DiscrepancyReasonPayerMismatch, DiscrepancyReasonCurrencyMismatch,
		DiscrepancyReasonScopeMismatch, DiscrepancyReasonChargeMismatch,
		DiscrepancyReasonCoverageMismatch, DiscrepancyReasonContextMismatch,
		DiscrepancyReasonSchemaMismatch, DiscrepancyReasonQualifierMismatch,
		DiscrepancyReasonTokenizerMismatch, DiscrepancyReasonTokenizerMissing,
		DiscrepancyReasonTokenizerRequired, DiscrepancyReasonSemanticsMismatch,
		DiscrepancyReasonValueUnavailableLocal, DiscrepancyReasonValueUnavailableProvider,
		DiscrepancyReasonValueUnavailableBoth, DiscrepancyReasonDuplicateLocal,
		DiscrepancyReasonDuplicateProvider, DiscrepancyReasonConflictingLocal,
		DiscrepancyReasonConflictingProvider, DiscrepancyReasonZeroDenominator,
		DiscrepancyReasonUnitMismatch, DiscrepancyReasonTolerancePolicyMissing,
		DiscrepancyReasonEstimatedNotExact, DiscrepancyReasonMissingE,
		DiscrepancyReasonMissingQ, DiscrepancyReasonMissingP,
		DiscrepancyReasonAmountUnavailable, DiscrepancyReasonCurrencyMissing,
		DiscrepancyReasonTariffMismatch, DiscrepancyReasonValuationIncomplete,
		DiscrepancyReasonValuationConflict, DiscrepancyReasonQuantityEvidencePartial,
		DiscrepancyReasonQuantityEvidenceIncomparable, DiscrepancyReasonQuantityEvidenceConflict:
		return true
	default:
		return false
	}
}

// DiscrepancyDiagnostic is one safe bounded operational signal. It never
// contains prompts, media, credentials or raw provider payloads.
type DiscrepancyDiagnostic struct {
	Code   string            `json:"code"`
	Reason DiscrepancyReason `json:"reason,omitempty"`
	Detail string            `json:"detail,omitempty"`
}

// Validate checks the diagnostic without mutating it.
func (d DiscrepancyDiagnostic) Validate() error {
	if err := validatePublicRef("discrepancy diagnostic code", d.Code); err != nil {
		return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if !d.Reason.IsKnown() {
		return fmt.Errorf("%w: unknown discrepancy reason %q", ErrOperatorQueryInvalid, d.Reason)
	}
	if len(d.Detail) > 512 {
		return fmt.Errorf("%w: discrepancy diagnostic detail exceeds 512 bytes", ErrOperatorQueryInvalid)
	}
	for _, r := range d.Detail {
		if r < 0x20 || r == 0x7f || !unicode.IsPrint(r) {
			return fmt.Errorf("%w: discrepancy diagnostic detail contains unsafe characters", ErrOperatorQueryInvalid)
		}
	}
	return nil
}

// DiscrepancyAggregate is the public typed projection of one retained
// aggregate reconciliation plane. It preserves the bounded classification
// outcome and the identity of the evidence that could not be reconciled; it is
// an independent plane that never substitutes for, and never carries or
// implies, a quantity or monetary amount. Status is the most severe retained
// classification (conflict, incomparable, missing_provider, missing_local,
// partial, discrepant, within_tolerance, matched) or empty when the aggregate
// carries no finding at all. Complete is true exactly when no retained
// classification is missing, incomparable, conflicting or partial.
type DiscrepancyAggregate struct {
	Status          DiscrepancyStatus `json:"status,omitempty"`
	Complete        bool              `json:"complete"`
	AffectedCount   int               `json:"affected_count,omitempty"`
	MissingIDs      []string          `json:"missing_ids,omitempty"`
	IncomparableIDs []string          `json:"incomparable_ids,omitempty"`
	ConflictIDs     []string          `json:"conflict_ids,omitempty"`
}

// Validate checks the aggregate projection without mutating it.
func (a DiscrepancyAggregate) Validate() error {
	if a.Status != "" && !a.Status.IsKnown() {
		return fmt.Errorf("%w: unknown aggregate status %q", ErrOperatorQueryInvalid, a.Status)
	}
	if a.AffectedCount < 0 {
		return fmt.Errorf("%w: aggregate affected count cannot be negative", ErrOperatorQueryInvalid)
	}
	for _, group := range []struct {
		field string
		ids   []string
	}{
		{field: "aggregate missing id", ids: a.MissingIDs},
		{field: "aggregate incomparable id", ids: a.IncomparableIDs},
		{field: "aggregate conflict id", ids: a.ConflictIDs},
	} {
		if len(group.ids) > MaxDiscrepancyAggregateIDs {
			return fmt.Errorf("%w: %s bound exceeded", ErrOperatorQueryInvalid, group.field)
		}
		for _, id := range group.ids {
			if err := validatePublicRef(group.field, id); err != nil {
				return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
			}
		}
	}
	if a.Complete {
		switch a.Status {
		case "", DiscrepancyMatched, DiscrepancyWithinTolerance, DiscrepancyDiscrepant:
		default:
			return fmt.Errorf("%w: complete aggregate cannot carry status %q", ErrOperatorQueryInvalid, a.Status)
		}
		if len(a.MissingIDs) > 0 || len(a.IncomparableIDs) > 0 || len(a.ConflictIDs) > 0 {
			return fmt.Errorf("%w: complete aggregate cannot carry unresolved evidence", ErrOperatorQueryInvalid)
		}
	}
	return nil
}

// Clone deep-copies the aggregate projection.
func (a DiscrepancyAggregate) Clone() DiscrepancyAggregate {
	out := a
	out.MissingIDs = append([]string(nil), a.MissingIDs...)
	out.IncomparableIDs = append([]string(nil), a.IncomparableIDs...)
	out.ConflictIDs = append([]string(nil), a.ConflictIDs...)
	return out
}

// DiscrepancyView is one retained reconciliation summary. Quantity, monetary
// and aggregate planes are independent fields; an absent plane is nil-like
// (empty status) and never reads as reconciled.
type DiscrepancyView struct {
	ID               string                    `json:"id"`
	Revision         uint64                    `json:"revision"`
	Subject          metering.SubjectRef       `json:"subject"`
	Scope            string                    `json:"scope,omitempty"`
	Payer            metering.PaymentParty     `json:"payer,omitzero"`
	PolicyID         string                    `json:"policy_id"`
	PolicyVersion    string                    `json:"policy_version"`
	InputSetHash     string                    `json:"input_set_hash,omitempty"`
	ValuationIDs     []string                  `json:"valuation_ids,omitempty"`
	ObservationRefs  []metering.ObservationRef `json:"observation_refs,omitempty"`
	QuantityStatus   DiscrepancyStatus         `json:"quantity_status,omitempty"`
	QuantityComplete bool                      `json:"quantity_complete"`
	MonetaryState    MonetaryState             `json:"monetary_state,omitempty"`
	MonetaryReason   DiscrepancyReason         `json:"monetary_reason,omitempty"`
	Aggregate        *DiscrepancyAggregate     `json:"aggregate,omitempty"`
	Diagnostics      []DiscrepancyDiagnostic   `json:"diagnostics,omitempty"`
	CreatedAt        time.Time                 `json:"created_at"`
}

// Validate checks the view without mutating it.
func (v DiscrepancyView) Validate() error {
	if err := validatePublicRef("discrepancy id", v.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if v.Revision == 0 {
		return fmt.Errorf("%w: discrepancy revision required", ErrOperatorQueryInvalid)
	}
	if err := v.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: discrepancy subject: %v", ErrOperatorQueryInvalid, err)
	}
	if v.Scope != "" {
		if err := validatePublicRef("discrepancy scope", v.Scope); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if err := v.Payer.Validate(); err != nil {
		return fmt.Errorf("%w: discrepancy payer: %v", ErrOperatorQueryInvalid, err)
	}
	if err := validatePublicRef("discrepancy policy id", v.PolicyID); err != nil {
		return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if err := validatePublicRef("discrepancy policy version", v.PolicyVersion); err != nil {
		return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if v.QuantityStatus != "" && !v.QuantityStatus.IsKnown() {
		return fmt.Errorf("%w: unknown quantity status %q", ErrOperatorQueryInvalid, v.QuantityStatus)
	}
	if v.MonetaryState != "" && !v.MonetaryState.IsKnown() {
		return fmt.Errorf("%w: unknown monetary state %q", ErrOperatorQueryInvalid, v.MonetaryState)
	}
	if !v.MonetaryReason.IsKnown() {
		return fmt.Errorf("%w: unknown monetary reason %q", ErrOperatorQueryInvalid, v.MonetaryReason)
	}
	if v.Aggregate != nil {
		if err := v.Aggregate.Validate(); err != nil {
			return fmt.Errorf("%w: aggregate: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if v.QuantityStatus == "" && v.MonetaryState == "" && v.Aggregate == nil {
		return fmt.Errorf("%w: at least one comparison plane required", ErrOperatorQueryInvalid)
	}
	if len(v.Diagnostics) > 64 {
		return fmt.Errorf("%w: diagnostic bound exceeded", ErrOperatorQueryInvalid)
	}
	for i, diagnostic := range v.Diagnostics {
		if err := diagnostic.Validate(); err != nil {
			return fmt.Errorf("%w: diagnostic %d: %v", ErrOperatorQueryInvalid, i, err)
		}
	}
	if v.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at required", ErrOperatorQueryInvalid)
	}
	return nil
}

// Clone deep-copies the view for consumers that need local sorting.
func (v DiscrepancyView) Clone() DiscrepancyView {
	out := v
	out.Subject = v.Subject.Clone()
	out.ValuationIDs = append([]string(nil), v.ValuationIDs...)
	out.ObservationRefs = append([]metering.ObservationRef(nil), v.ObservationRefs...)
	if v.Aggregate != nil {
		aggregate := v.Aggregate.Clone()
		out.Aggregate = &aggregate
	}
	out.Diagnostics = append([]DiscrepancyDiagnostic(nil), v.Diagnostics...)
	return out
}

// DiscrepancyQuery identifies a bounded retained-reconciliation window.
// Subject kind and ID narrow the window; both must be supplied together.
type DiscrepancyQuery struct {
	Scope       OperatorScope        `json:"scope"`
	SubjectKind metering.SubjectKind `json:"subject_kind,omitempty"`
	SubjectID   string               `json:"subject_id,omitempty"`
	Limit       int                  `json:"limit,omitempty"`
	Cursor      string               `json:"cursor,omitempty"`
}

// Normalize trims scope, applies the default limit and rejects unbounded or
// ambiguous queries.
func (q DiscrepancyQuery) Normalize() (DiscrepancyQuery, error) {
	out := q
	out.Scope.StoreID = strings.TrimSpace(q.Scope.StoreID)
	out.Scope.TenantID = strings.TrimSpace(q.Scope.TenantID)
	out.Scope.AccountID = strings.TrimSpace(q.Scope.AccountID)
	out.SubjectID = strings.TrimSpace(q.SubjectID)
	out.Cursor = strings.TrimSpace(q.Cursor)
	if err := ValidateOperatorCursorSize("discrepancy cursor", out.Cursor); err != nil {
		return DiscrepancyQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if err := out.Scope.Validate(); err != nil {
		return DiscrepancyQuery{}, err
	}
	if (out.SubjectKind == "") != (out.SubjectID == "") {
		return DiscrepancyQuery{}, fmt.Errorf("%w: subject kind and id must be supplied together", ErrOperatorQueryInvalid)
	}
	if out.SubjectKind != "" && !out.SubjectKind.IsKnown() {
		return DiscrepancyQuery{}, fmt.Errorf("%w: unknown subject kind %q", ErrOperatorQueryInvalid, out.SubjectKind)
	}
	if out.SubjectID != "" {
		if err := validatePublicRef("discrepancy subject id", out.SubjectID); err != nil {
			return DiscrepancyQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if out.Limit == 0 {
		out.Limit = OperatorPageDefaultLimit
	}
	if out.Limit < 1 || out.Limit > OperatorPageMaxLimit {
		return DiscrepancyQuery{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrOperatorQueryInvalid, OperatorPageMaxLimit)
	}
	return out, nil
}

// DiscrepancyPage is one deterministic page of retained reconciliation
// summaries in durable order.
type DiscrepancyPage struct {
	Items      []DiscrepancyView `json:"items,omitempty"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// Validate checks page bounds without mutating the page.
func (p DiscrepancyPage) Validate() error {
	if len(p.Items) > OperatorPageMaxLimit {
		return fmt.Errorf("%w: discrepancy page bound exceeded", ErrOperatorQueryInvalid)
	}
	for i, item := range p.Items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("%w: item %d: %v", ErrOperatorQueryInvalid, i, err)
		}
	}
	if p.NextCursor != "" {
		if err := validateOperatorCursor("discrepancy next cursor", p.NextCursor); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	return nil
}

// DiscrepancyReader reads immutable retained reconciliation summaries and
// never performs rating, posting or provider calls.
type DiscrepancyReader interface {
	QueryDiscrepancies(ctx context.Context, query DiscrepancyQuery) (DiscrepancyPage, error)
}

// AllowanceQuery identifies a bounded provider allowance-window history
// window. The provider account is mandatory: history without that bound
// would scan unrelated supplier accounts.
type AllowanceQuery struct {
	Scope              OperatorScope `json:"scope"`
	ProviderAccountKey string        `json:"provider_account_key"`
	PoolID             string        `json:"pool_id,omitempty"`
	WindowID           string        `json:"window_id,omitempty"`
	Limit              int           `json:"limit,omitempty"`
	Cursor             string        `json:"cursor,omitempty"`
}

// Normalize trims scope, applies the default limit and rejects unbounded or
// ambiguously authorized queries. Allowance history is provider-side gauge
// evidence under tenant authority; a customer account scope must not
// authorize it.
func (q AllowanceQuery) Normalize() (AllowanceQuery, error) {
	out := q
	out.Scope.StoreID = strings.TrimSpace(q.Scope.StoreID)
	out.Scope.TenantID = strings.TrimSpace(q.Scope.TenantID)
	out.Scope.AccountID = strings.TrimSpace(q.Scope.AccountID)
	out.ProviderAccountKey = strings.TrimSpace(q.ProviderAccountKey)
	out.PoolID = strings.TrimSpace(q.PoolID)
	out.WindowID = strings.TrimSpace(q.WindowID)
	out.Cursor = strings.TrimSpace(q.Cursor)
	if err := ValidateOperatorCursorSize("allowance cursor", out.Cursor); err != nil {
		return AllowanceQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if err := validatePublicRef("allowance store id", out.Scope.StoreID); err != nil {
		return AllowanceQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if out.Scope.TenantID == "" {
		return AllowanceQuery{}, fmt.Errorf("%w: trusted tenant scope required", ErrOperatorQueryInvalid)
	}
	if err := validatePublicRef("allowance tenant id", out.Scope.TenantID); err != nil {
		return AllowanceQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if out.Scope.AccountID != "" {
		return AllowanceQuery{}, fmt.Errorf("%w: customer account scope cannot authorize provider allowance history", ErrOperatorQueryInvalid)
	}
	if out.ProviderAccountKey == "" {
		return AllowanceQuery{}, fmt.Errorf("%w: provider account bound required", ErrOperatorQueryInvalid)
	}
	if err := validatePublicRef("allowance provider account", out.ProviderAccountKey); err != nil {
		return AllowanceQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	for name, value := range map[string]string{"allowance pool id": out.PoolID, "allowance window id": out.WindowID} {
		if value != "" {
			if err := validatePublicRef(name, value); err != nil {
				return AllowanceQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
			}
		}
	}
	if out.Limit == 0 {
		out.Limit = OperatorPageDefaultLimit
	}
	if out.Limit < 1 || out.Limit > OperatorPageMaxLimit {
		return AllowanceQuery{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrOperatorQueryInvalid, OperatorPageMaxLimit)
	}
	return out, nil
}

// AllowancePage is one deterministic page of immutable allowance gauge
// history in effective observed-at order. Gauges are never summed: each
// observation stands alone and no per-request debit is inferred.
type AllowancePage struct {
	Observations []metering.Observation `json:"observations,omitempty"`
	NextCursor   string                 `json:"next_cursor,omitempty"`
}

// Validate checks page bounds and observation validity without mutating it.
func (p AllowancePage) Validate() error {
	if len(p.Observations) > OperatorPageMaxLimit {
		return fmt.Errorf("%w: allowance page bound exceeded", ErrOperatorQueryInvalid)
	}
	for i, observation := range p.Observations {
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("%w: observation %d: %v", ErrOperatorQueryInvalid, i, err)
		}
		if observation.Subject.Kind != metering.SubjectAccountWindow {
			return fmt.Errorf("%w: observation %d is not account-window evidence", ErrOperatorQueryInvalid, i)
		}
	}
	if p.NextCursor != "" {
		if err := validateOperatorCursor("allowance next cursor", p.NextCursor); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	return nil
}

// AllowanceReader reads immutable provider allowance history and never
// performs rating, posting or provider calls.
type AllowanceReader interface {
	QueryAllowances(ctx context.Context, query AllowanceQuery) (AllowancePage, error)
}

// StatementLineView is one retained normalized statement line. The
// statement/period/provider scope is the aggregate identity: unmatched
// lines keep it and never gain a request linkage. Matched lines carry
// their explicit charge linkage; nothing is guessed.
type StatementLineView struct {
	StatementID        string                  `json:"statement_id"`
	LineID             string                  `json:"line_id"`
	Revision           uint64                  `json:"revision"`
	PeriodID           string                  `json:"period_id"`
	ProviderAccountKey string                  `json:"provider_account_key"`
	Outcome            StatementLineOutcome    `json:"outcome,omitempty"`
	UnmatchedReason    string                  `json:"unmatched_reason,omitempty"`
	ChargeItemID       string                  `json:"charge_item_id,omitempty"`
	Observation        metering.ObservationRef `json:"observation,omitzero"`
}

// Validate checks the view without mutating it.
func (v StatementLineView) Validate() error {
	for name, value := range map[string]string{
		"statement line statement id": v.StatementID, "statement line id": v.LineID,
		"statement line period id": v.PeriodID, "statement line provider account": v.ProviderAccountKey,
	} {
		if err := validatePublicRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if v.Revision == 0 {
		return fmt.Errorf("%w: statement line revision required", ErrOperatorQueryInvalid)
	}
	if v.Outcome != "" && !v.Outcome.IsKnown() {
		return fmt.Errorf("%w: unknown statement line outcome %q", ErrOperatorQueryInvalid, v.Outcome)
	}
	if v.Outcome == StatementLineUnmatched {
		if err := validatePublicRef("statement unmatched reason", v.UnmatchedReason); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
		if v.ChargeItemID != "" || !isZeroObservationRef(v.Observation) {
			return fmt.Errorf("%w: unmatched statement line cannot carry a charge linkage", ErrOperatorQueryInvalid)
		}
		return nil
	}
	if v.UnmatchedReason != "" {
		return fmt.Errorf("%w: matched statement line cannot carry unmatched reason", ErrOperatorQueryInvalid)
	}
	if v.Outcome == StatementLineMatched {
		if err := v.Observation.Validate(); err != nil {
			return fmt.Errorf("%w: statement line observation: %v", ErrOperatorQueryInvalid, err)
		}
		return validatePublicRef("statement charge item id", v.ChargeItemID)
	}
	return nil
}

// StatementLineQuery identifies a bounded retained statement-line window.
// At least one narrow filter (provider account, statement, period or line) is
// required so the read stays index-backed instead of scanning a tenant.
type StatementLineQuery struct {
	Scope              OperatorScope        `json:"scope"`
	ProviderAccountKey string               `json:"provider_account_key,omitempty"`
	StatementID        string               `json:"statement_id,omitempty"`
	PeriodID           string               `json:"period_id,omitempty"`
	LineID             string               `json:"line_id,omitempty"`
	Outcome            StatementLineOutcome `json:"outcome,omitempty"`
	Limit              int                  `json:"limit,omitempty"`
	Cursor             string               `json:"cursor,omitempty"`
}

// Normalize trims scope, applies the default limit and rejects unbounded or
// ambiguously authorized queries. Customer account scope must not authorize
// provider statement history.
func (q StatementLineQuery) Normalize() (StatementLineQuery, error) {
	out := q
	out.Scope.StoreID = strings.TrimSpace(q.Scope.StoreID)
	out.Scope.TenantID = strings.TrimSpace(q.Scope.TenantID)
	out.Scope.AccountID = strings.TrimSpace(q.Scope.AccountID)
	out.ProviderAccountKey = strings.TrimSpace(q.ProviderAccountKey)
	out.StatementID = strings.TrimSpace(q.StatementID)
	out.PeriodID = strings.TrimSpace(q.PeriodID)
	out.LineID = strings.TrimSpace(q.LineID)
	out.Cursor = strings.TrimSpace(q.Cursor)
	if err := ValidateOperatorCursorSize("statement line cursor", out.Cursor); err != nil {
		return StatementLineQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if err := validatePublicRef("statement store id", out.Scope.StoreID); err != nil {
		return StatementLineQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if out.Scope.TenantID == "" {
		return StatementLineQuery{}, fmt.Errorf("%w: trusted tenant scope required", ErrOperatorQueryInvalid)
	}
	if err := validatePublicRef("statement tenant id", out.Scope.TenantID); err != nil {
		return StatementLineQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if out.Scope.AccountID != "" {
		return StatementLineQuery{}, fmt.Errorf("%w: customer account scope cannot authorize provider statement history", ErrOperatorQueryInvalid)
	}
	for name, value := range map[string]string{
		"statement provider account": out.ProviderAccountKey, "statement id": out.StatementID, "statement period id": out.PeriodID,
		"statement line id": out.LineID,
	} {
		if value != "" {
			if err := validatePublicRef(name, value); err != nil {
				return StatementLineQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
			}
		}
	}
	if out.Outcome != "" && !out.Outcome.IsKnown() {
		return StatementLineQuery{}, fmt.Errorf("%w: unknown statement line outcome %q", ErrOperatorQueryInvalid, out.Outcome)
	}
	if out.ProviderAccountKey == "" && out.StatementID == "" && out.PeriodID == "" && out.LineID == "" {
		return StatementLineQuery{}, fmt.Errorf("%w: provider account, statement, period or line filter required", ErrOperatorQueryInvalid)
	}
	if out.Limit == 0 {
		out.Limit = OperatorPageDefaultLimit
	}
	if out.Limit < 1 || out.Limit > OperatorPageMaxLimit {
		return StatementLineQuery{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrOperatorQueryInvalid, OperatorPageMaxLimit)
	}
	return out, nil
}

// StatementLinePage is one deterministic page of retained statement lines
// in line-key order.
type StatementLinePage struct {
	Lines      []StatementLineView `json:"lines,omitempty"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

// Validate checks page bounds without mutating the page.
func (p StatementLinePage) Validate() error {
	if len(p.Lines) > OperatorPageMaxLimit {
		return fmt.Errorf("%w: statement line page bound exceeded", ErrOperatorQueryInvalid)
	}
	for i, line := range p.Lines {
		if err := line.Validate(); err != nil {
			return fmt.Errorf("%w: line %d: %v", ErrOperatorQueryInvalid, i, err)
		}
	}
	if p.NextCursor != "" {
		if err := validateOperatorCursor("statement line next cursor", p.NextCursor); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	return nil
}

// StatementLineReader reads immutable retained statement lines and never
// performs matching, rating, posting or provider calls. Import stays on
// StatementImporter; this reader cannot authorize imports.
type StatementLineReader interface {
	QueryStatementLines(ctx context.Context, query StatementLineQuery) (StatementLinePage, error)
}

// ExactAmount is one signed exact native-currency amount for adjustment
// deltas. Exactly one representation is present: a canonical bounded
// decimal, or reduced rational parts. Downward corrections carry negative
// deltas; the balanced journal reverses debit/credit sides instead of
// recording a negative gross amount.
type ExactAmount struct {
	Currency    string            `json:"currency"`
	Decimal     *metering.Decimal `json:"decimal,omitempty"`
	Numerator   string            `json:"numerator,omitempty"`
	Denominator string            `json:"denominator,omitempty"`
}

// Validate enforces the canonical exact-amount contract.
func (a ExactAmount) Validate() error {
	currency, err := NormalizeCurrency(a.Currency)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	_ = currency
	decimalPresent := a.Decimal != nil
	rationalPresent := a.Numerator != "" || a.Denominator != ""
	switch {
	case decimalPresent && rationalPresent:
		return fmt.Errorf("%w: decimal and rational representations are mutually exclusive", ErrOperatorQueryInvalid)
	case !decimalPresent && !rationalPresent:
		return fmt.Errorf("%w: exactly one decimal or rational representation is required", ErrOperatorQueryInvalid)
	case decimalPresent:
		if _, err := a.Decimal.Normalize(); err != nil {
			return fmt.Errorf("%w: decimal: %v", ErrOperatorQueryInvalid, err)
		}
		return nil
	default:
		if a.Numerator == "" || a.Denominator == "" {
			return fmt.Errorf("%w: numerator and denominator are required together", ErrOperatorQueryInvalid)
		}
		if err := validateRationalParts("delta amount", a.Numerator, a.Denominator); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
		return nil
	}
}

// Clone deep-copies the amount.
func (a ExactAmount) Clone() ExactAmount {
	out := a
	if a.Decimal != nil {
		decimal := *a.Decimal
		out.Decimal = &decimal
	}
	return out
}

// AdjustmentTransitionStatus is the correction outcome plane. Only applied,
// no-op and replay carry effects; pending, stale and conflict never do.
type AdjustmentTransitionStatus string

const (
	AdjustmentApplied  AdjustmentTransitionStatus = "applied"
	AdjustmentNoOp     AdjustmentTransitionStatus = "no_op"
	AdjustmentReplay   AdjustmentTransitionStatus = "replay"
	AdjustmentPending  AdjustmentTransitionStatus = "pending"
	AdjustmentStale    AdjustmentTransitionStatus = "stale"
	AdjustmentConflict AdjustmentTransitionStatus = "conflict"
)

// IsKnown reports whether s is a documented transition status.
func (s AdjustmentTransitionStatus) IsKnown() bool {
	switch s {
	case AdjustmentApplied, AdjustmentNoOp, AdjustmentReplay,
		AdjustmentPending, AdjustmentStale, AdjustmentConflict:
		return true
	default:
		return false
	}
}

// AdjustmentComparison is the monetary comparability plane, separate from
// the transition outcome.
type AdjustmentComparison string

const (
	AdjustmentComparisonNotEvaluated AdjustmentComparison = "not_evaluated"
	AdjustmentComparisonComparable   AdjustmentComparison = "comparable"
	AdjustmentComparisonPending      AdjustmentComparison = "pending"
	AdjustmentComparisonIncomparable AdjustmentComparison = "incomparable"
)

// IsKnown reports whether s is a documented comparison state.
func (s AdjustmentComparison) IsKnown() bool {
	switch s {
	case AdjustmentComparisonNotEvaluated, AdjustmentComparisonComparable,
		AdjustmentComparisonPending, AdjustmentComparisonIncomparable:
		return true
	default:
		return false
	}
}

// AdjustmentPosting is the financial posting plane, separate from selection
// and comparison.
type AdjustmentPosting string

const (
	AdjustmentPostingUnposted AdjustmentPosting = "unposted"
	AdjustmentPostingPending  AdjustmentPosting = "pending"
	AdjustmentPostingApplied  AdjustmentPosting = "applied"
	AdjustmentPostingReplayed AdjustmentPosting = "replayed"
)

// IsKnown reports whether s is a documented posting state.
func (s AdjustmentPosting) IsKnown() bool {
	switch s {
	case AdjustmentPostingUnposted, AdjustmentPostingPending,
		AdjustmentPostingApplied, AdjustmentPostingReplayed:
		return true
	default:
		return false
	}
}

// AdjustmentSelection is the selection outcome plane carried by the
// adjustment. Source selection alone is never labelled reconciliation.
type AdjustmentSelection string

const (
	AdjustmentSelectionFinal              AdjustmentSelection = "final"
	AdjustmentSelectionProvisional        AdjustmentSelection = "provisional"
	AdjustmentSelectionKnownZero          AdjustmentSelection = "known_zero"
	AdjustmentSelectionUnknown            AdjustmentSelection = "unknown"
	AdjustmentSelectionIncomparable       AdjustmentSelection = "incomparable"
	AdjustmentSelectionConflict           AdjustmentSelection = "conflict"
	AdjustmentSelectionNotOperatorPayable AdjustmentSelection = "not_operator_payable"
)

// IsKnown reports whether s is a documented selection outcome.
func (s AdjustmentSelection) IsKnown() bool {
	switch s {
	case AdjustmentSelectionFinal, AdjustmentSelectionProvisional,
		AdjustmentSelectionKnownZero, AdjustmentSelectionUnknown,
		AdjustmentSelectionIncomparable, AdjustmentSelectionConflict,
		AdjustmentSelectionNotOperatorPayable:
		return true
	default:
		return false
	}
}

// AdjustmentValuationRef is the immutable identity of one frozen selected
// valuation revision in the correction chain.
type AdjustmentValuationRef struct {
	ValuationID  string `json:"valuation_id"`
	Revision     uint64 `json:"revision"`
	InputSetHash string `json:"input_set_hash"`
}

// Validate checks the immutable valuation identity.
func (r AdjustmentValuationRef) Validate() error {
	for name, value := range map[string]string{
		"adjustment valuation id": r.ValuationID, "adjustment input set hash": r.InputSetHash,
	} {
		if err := validatePublicRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if r.Revision == 0 {
		return fmt.Errorf("%w: adjustment valuation revision required", ErrOperatorQueryInvalid)
	}
	return nil
}

// AdjustmentView is one immutable selected-cost correction with its
// comparison, selection and posting planes carried as independent fields.
type AdjustmentView struct {
	OperationKey         string                     `json:"operation_key"`
	LinkKey              string                     `json:"link_key"`
	Fingerprint          string                     `json:"fingerprint"`
	AccountID            string                     `json:"account_id"`
	CallID               string                     `json:"call_id"`
	HeadKey              string                     `json:"head_key"`
	Status               AdjustmentTransitionStatus `json:"status"`
	Comparison           AdjustmentComparison       `json:"comparison"`
	Posting              AdjustmentPosting          `json:"posting"`
	SelectionStatus      AdjustmentSelection        `json:"selection_status"`
	SelectionReason      string                     `json:"selection_reason,omitempty"`
	Previous             *AdjustmentValuationRef    `json:"previous,omitempty"`
	Current              AdjustmentValuationRef     `json:"current"`
	Currency             string                     `json:"currency"`
	Delta                *ExactAmount               `json:"delta,omitempty"`
	JournalTransactionID string                     `json:"journal_transaction_id,omitempty"`
	CreatedAt            time.Time                  `json:"created_at"`
}

// Validate checks the view without mutating it.
func (v AdjustmentView) Validate() error {
	for name, value := range map[string]string{
		"adjustment operation key": v.OperationKey, "adjustment link key": v.LinkKey,
		"adjustment fingerprint": v.Fingerprint, "adjustment account id": v.AccountID,
		"adjustment call id": v.CallID, "adjustment head key": v.HeadKey,
	} {
		if err := validatePublicRef(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if !v.Status.IsKnown() {
		return fmt.Errorf("%w: unknown adjustment status %q", ErrOperatorQueryInvalid, v.Status)
	}
	if !v.Comparison.IsKnown() {
		return fmt.Errorf("%w: unknown adjustment comparison %q", ErrOperatorQueryInvalid, v.Comparison)
	}
	if !v.Posting.IsKnown() {
		return fmt.Errorf("%w: unknown adjustment posting %q", ErrOperatorQueryInvalid, v.Posting)
	}
	if !v.SelectionStatus.IsKnown() {
		return fmt.Errorf("%w: unknown adjustment selection %q", ErrOperatorQueryInvalid, v.SelectionStatus)
	}
	if v.SelectionReason != "" {
		if err := validatePublicRef("adjustment selection reason", v.SelectionReason); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if v.Previous != nil {
		if err := v.Previous.Validate(); err != nil {
			return fmt.Errorf("%w: previous: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if err := v.Current.Validate(); err != nil {
		return fmt.Errorf("%w: current: %v", ErrOperatorQueryInvalid, err)
	}
	if _, err := NormalizeCurrency(v.Currency); err != nil {
		return fmt.Errorf("%w: currency: %v", ErrOperatorQueryInvalid, err)
	}
	if v.Delta != nil {
		if err := v.Delta.Validate(); err != nil {
			return fmt.Errorf("%w: delta: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if v.JournalTransactionID != "" {
		if err := validatePublicRef("adjustment journal transaction id", v.JournalTransactionID); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	if v.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at required", ErrOperatorQueryInvalid)
	}
	return nil
}

// Clone deep-copies the view.
func (v AdjustmentView) Clone() AdjustmentView {
	out := v
	if v.Previous != nil {
		previous := *v.Previous
		out.Previous = &previous
	}
	if v.Delta != nil {
		delta := v.Delta.Clone()
		out.Delta = &delta
	}
	return out
}

// AdjustmentQuery identifies a bounded selected-cost correction window for
// one customer account. Call and head narrow the window; all three must
// agree with the retained rows.
type AdjustmentQuery struct {
	Scope   OperatorScope `json:"scope"`
	CallID  string        `json:"call_id,omitempty"`
	HeadKey string        `json:"head_key,omitempty"`
	Limit   int           `json:"limit,omitempty"`
	Cursor  string        `json:"cursor,omitempty"`
}

// Normalize trims scope, applies the default limit and rejects unbounded or
// ambiguously authorized queries.
func (q AdjustmentQuery) Normalize() (AdjustmentQuery, error) {
	out := q
	out.Scope.StoreID = strings.TrimSpace(q.Scope.StoreID)
	out.Scope.TenantID = strings.TrimSpace(q.Scope.TenantID)
	out.Scope.AccountID = strings.TrimSpace(q.Scope.AccountID)
	out.CallID = strings.TrimSpace(q.CallID)
	out.HeadKey = strings.TrimSpace(q.HeadKey)
	out.Cursor = strings.TrimSpace(q.Cursor)
	if err := ValidateOperatorCursorSize("adjustment cursor", out.Cursor); err != nil {
		return AdjustmentQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if err := validatePublicRef("adjustment store id", out.Scope.StoreID); err != nil {
		return AdjustmentQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if out.Scope.AccountID == "" {
		return AdjustmentQuery{}, fmt.Errorf("%w: trusted account scope required", ErrOperatorQueryInvalid)
	}
	if err := validatePublicRef("adjustment account id", out.Scope.AccountID); err != nil {
		return AdjustmentQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
	}
	if out.Scope.TenantID != "" {
		if err := validatePublicRef("adjustment tenant id", out.Scope.TenantID); err != nil {
			return AdjustmentQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	for name, value := range map[string]string{"adjustment call id": out.CallID, "adjustment head key": out.HeadKey} {
		if value != "" {
			if err := validatePublicRef(name, value); err != nil {
				return AdjustmentQuery{}, fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
			}
		}
	}
	if out.Limit == 0 {
		out.Limit = OperatorPageDefaultLimit
	}
	if out.Limit < 1 || out.Limit > OperatorPageMaxLimit {
		return AdjustmentQuery{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrOperatorQueryInvalid, OperatorPageMaxLimit)
	}
	return out, nil
}

// AdjustmentPage is one deterministic page of immutable corrections in
// (call, head, revision) order.
type AdjustmentPage struct {
	Adjustments []AdjustmentView `json:"adjustments,omitempty"`
	NextCursor  string           `json:"next_cursor,omitempty"`
}

// Validate checks page bounds without mutating the page.
func (p AdjustmentPage) Validate() error {
	if len(p.Adjustments) > OperatorPageMaxLimit {
		return fmt.Errorf("%w: adjustment page bound exceeded", ErrOperatorQueryInvalid)
	}
	for i, adjustment := range p.Adjustments {
		if err := adjustment.Validate(); err != nil {
			return fmt.Errorf("%w: adjustment %d: %v", ErrOperatorQueryInvalid, i, err)
		}
	}
	if p.NextCursor != "" {
		if err := validateOperatorCursor("adjustment next cursor", p.NextCursor); err != nil {
			return fmt.Errorf("%w: %v", ErrOperatorQueryInvalid, err)
		}
	}
	return nil
}

// AdjustmentReader reads immutable selected-cost corrections and never
// performs selection, posting or provider calls.
type AdjustmentReader interface {
	QueryAdjustments(ctx context.Context, query AdjustmentQuery) (AdjustmentPage, error)
}

// OperatorReader is the combined protected operator surface. Adapters may
// implement readers individually; the combination exists so composition
// roots can require the full 16.2A surface with one bound.
type OperatorReader interface {
	DiscrepancyReader
	AllowanceReader
	StatementLineReader
	AdjustmentReader
}

var (
	_ DiscrepancyReader   = (*operatorReaderStub)(nil)
	_ AllowanceReader     = (*operatorReaderStub)(nil)
	_ StatementLineReader = (*operatorReaderStub)(nil)
	_ AdjustmentReader    = (*operatorReaderStub)(nil)
	_ OperatorReader      = (*operatorReaderStub)(nil)
)

// operatorReaderStub documents the method set each reader must provide. It
// carries no state and is never used for production reads.
type operatorReaderStub struct{}

func (operatorReaderStub) QueryDiscrepancies(context.Context, DiscrepancyQuery) (DiscrepancyPage, error) {
	return DiscrepancyPage{}, errors.New("economics: operator reader stub")
}

func (operatorReaderStub) QueryAllowances(context.Context, AllowanceQuery) (AllowancePage, error) {
	return AllowancePage{}, errors.New("economics: operator reader stub")
}

func (operatorReaderStub) QueryStatementLines(context.Context, StatementLineQuery) (StatementLinePage, error) {
	return StatementLinePage{}, errors.New("economics: operator reader stub")
}

func (operatorReaderStub) QueryAdjustments(context.Context, AdjustmentQuery) (AdjustmentPage, error) {
	return AdjustmentPage{}, errors.New("economics: operator reader stub")
}

// SortDiscrepancies returns a copy sorted by stable identity. It is a
// helper for operator consumers and does not mutate caller-owned slices.
func SortDiscrepancies(in []DiscrepancyView) []DiscrepancyView {
	out := append([]DiscrepancyView(nil), in...)
	slices.SortFunc(out, func(a, b DiscrepancyView) int {
		if a.ID != b.ID {
			return strings.Compare(a.ID, b.ID)
		}
		if a.Revision != b.Revision {
			if a.Revision < b.Revision {
				return -1
			}
			return 1
		}
		return 0
	})
	return out
}
