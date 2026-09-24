package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 13.2 statement matching domain contracts. The matcher consumes
// normalized imported statements (13.1) plus an explicitly eligible retained
// charge/reconciliation evidence set and produces typed per-line outcomes and
// immutable coverage links. It performs no SQL, vendor parsing, UI, rating,
// posting, head transition, journal write or worker behavior, and it retains
// no request/A-leg/B-leg lineage that could become a guessed allocation.

const (
	// MaxStatementMatchStatements bounds one matching batch.
	MaxStatementMatchStatements = 128
	// MaxStatementMatchEvidence bounds the eligible retained evidence set.
	MaxStatementMatchEvidence = 4096
	// MaxStatementMatchCoverageRefs bounds coverage references on one claim.
	MaxStatementMatchCoverageRefs = 4096
)

var (
	// ErrStatementMatchInvalid identifies malformed, duplicated or overbound
	// matching input. Invalid input fails closed and produces no match result.
	ErrStatementMatchInvalid = errors.New("billing: invalid statement match input")
	// ErrStatementMatchAmbiguous identifies input that contains more than one
	// revision of one statement identity. Choosing a current revision is not
	// this component's policy, so no guessed coverage is produced.
	ErrStatementMatchAmbiguous = errors.New("billing: ambiguous statement match input")
)

// StatementMatchAmbiguityError reports statement identities that appear with
// more than one revision in one matching batch.
type StatementMatchAmbiguityError struct {
	StatementIDs []string
}

func (e *StatementMatchAmbiguityError) Error() string {
	if e == nil {
		return ErrStatementMatchAmbiguous.Error()
	}
	return fmt.Sprintf("%s: statement ids %s", ErrStatementMatchAmbiguous, strings.Join(e.StatementIDs, ","))
}

func (e *StatementMatchAmbiguityError) Unwrap() error { return ErrStatementMatchAmbiguous }

// StatementEvidenceState records whether retained evidence is currently
// eligible to be covered by an imported statement. Ineligible evidence is
// retained and reported, never silently covered.
type StatementEvidenceState string

const (
	StatementEvidenceEligible   StatementEvidenceState = "eligible"
	StatementEvidenceIneligible StatementEvidenceState = "ineligible"
)

// IsKnown reports whether s is a documented evidence state.
func (s StatementEvidenceState) IsKnown() bool {
	return s == StatementEvidenceEligible || s == StatementEvidenceIneligible
}

// Validate checks the evidence state vocabulary.
func (s StatementEvidenceState) Validate() error {
	if !s.IsKnown() {
		return fmt.Errorf("%w: unknown statement evidence state %q", ErrStatementMatchInvalid, s)
	}
	return nil
}

// StatementChargeEvidence is one eligible retained charge or reconciliation
// record that a statement line may reference explicitly or cover completely by
// account/period/SKU aggregate scope. It contains no request, A-leg or B-leg
// lineage: the covered charge identity and the retained reconciliation
// identity are the only linkage retained.
type StatementChargeEvidence struct {
	Ref                metering.ChargeRef
	State              StatementEvidenceState
	ReconciliationID   string
	TenantID           string
	ProviderAccountKey string
	PeriodID           string
	Kind               metering.ChargeKind
	Component          *metering.ComponentKey
	Currency           string
	Payer              metering.PaymentParty
	// Covers is the retained charge's own coverage graph. Inclusive edges are
	// used to detect duplicate parent/child coverage; they are never expanded
	// into new per-request charges.
	Covers []metering.ChargeCoverageRef
}

// Validate checks one bounded eligible evidence entry.
func (e StatementChargeEvidence) Validate() error {
	if err := e.Ref.Validate(); err != nil {
		return fmt.Errorf("%w: statement match evidence: %v", ErrStatementMatchInvalid, err)
	}
	if !validEconomicIdentity(e.ProviderAccountKey, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: statement match evidence provider account required", ErrStatementMatchInvalid)
	}
	if !validEconomicIdentity(e.PeriodID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: statement match evidence period required", ErrStatementMatchInvalid)
	}
	if e.TenantID != "" && !validEconomicIdentity(e.TenantID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: statement match evidence tenant is not a bounded identity", ErrStatementMatchInvalid)
	}
	if !e.Kind.IsKnown() {
		return fmt.Errorf("%w: statement match evidence kind %q is not recognized", ErrStatementMatchInvalid, e.Kind)
	}
	if e.Component != nil {
		if err := e.Component.Validate(); err != nil {
			return fmt.Errorf("%w: statement match evidence component: %v", ErrStatementMatchInvalid, err)
		}
	}
	if e.Currency != "" {
		normalized, err := economics.NormalizeCurrency(e.Currency)
		if err != nil || normalized != e.Currency {
			return fmt.Errorf("%w: statement match evidence currency %q is not normalized", ErrStatementMatchInvalid, e.Currency)
		}
	}
	if err := e.Payer.Validate(); err != nil {
		return fmt.Errorf("%w: statement match evidence payer: %v", ErrStatementMatchInvalid, err)
	}
	if e.ReconciliationID != "" {
		if err := economics.ValidateSafeRef("statement match evidence reconciliation id", e.ReconciliationID); err != nil {
			return fmt.Errorf("%w: %v", ErrStatementMatchInvalid, err)
		}
	}
	if err := e.State.Validate(); err != nil {
		return err
	}
	if len(e.Covers) > MaxStatementMatchCoverageRefs {
		return fmt.Errorf("%w: statement match evidence coverage bound exceeded", ErrStatementMatchInvalid)
	}
	seen := make(map[string]struct{}, len(e.Covers))
	for i, edge := range e.Covers {
		if err := edge.Validate(); err != nil {
			return fmt.Errorf("%w: statement match evidence covers[%d]: %v", ErrStatementMatchInvalid, i, err)
		}
		if edge.Ref.StoreID != e.Ref.StoreID {
			return fmt.Errorf("%w: statement match evidence covers[%d] crosses store scope", ErrStatementMatchInvalid, i)
		}
		key := statementMatchChargeRefKey(edge.Ref)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: statement match evidence covers[%d] duplicates a charge reference", ErrStatementMatchInvalid, i)
		}
		seen[key] = struct{}{}
		if edge.Ref.StoreID == e.Ref.StoreID && edge.Ref.ObservationID == e.Ref.ObservationID &&
			edge.Ref.Revision == e.Ref.Revision && edge.Ref.ChargeItemID == e.Ref.ChargeItemID {
			return fmt.Errorf("%w: statement match evidence covers[%d] self-references its charge", ErrStatementMatchInvalid, i)
		}
	}
	return nil
}

// StatementMatchKind distinguishes the two permitted matching mechanisms.
type StatementMatchKind string

const (
	// StatementMatchKindExplicitCharge is an exact explicit charge-ID match.
	StatementMatchKindExplicitCharge StatementMatchKind = "explicit_charge"
	// StatementMatchKindAggregateSKU is complete compatible account/period/SKU
	// aggregate coverage. It retains every covered charge without asserting a
	// per-request allocation.
	StatementMatchKindAggregateSKU StatementMatchKind = "aggregate_sku"
)

// IsKnown reports whether k is a documented match kind.
func (k StatementMatchKind) IsKnown() bool {
	return k == StatementMatchKindExplicitCharge || k == StatementMatchKindAggregateSKU
}

// StatementMatchStatus is the typed per-line matching state. It is separate
// from posting and selection state.
type StatementMatchStatus string

const (
	// StatementMatchStatusMatched is a complete, unambiguous coverage link.
	StatementMatchStatusMatched StatementMatchStatus = "matched"
	// StatementMatchStatusPartial is incomplete coverage; it is never matched.
	StatementMatchStatusPartial StatementMatchStatus = "partial"
	// StatementMatchStatusIncomparable is incompatible scope/currency/unit/SKU
	// granularity; it is never matched.
	StatementMatchStatusIncomparable StatementMatchStatus = "incomparable"
	// StatementMatchStatusConflict is duplicate, overlapping or ambiguous
	// coverage; it is never matched.
	StatementMatchStatusConflict StatementMatchStatus = "conflict"
	// StatementMatchStatusUnmatched is retained without any attachment.
	StatementMatchStatusUnmatched StatementMatchStatus = "unmatched"
)

// IsKnown reports whether s is a documented match status.
func (s StatementMatchStatus) IsKnown() bool {
	switch s {
	case StatementMatchStatusMatched, StatementMatchStatusPartial, StatementMatchStatusIncomparable,
		StatementMatchStatusConflict, StatementMatchStatusUnmatched:
		return true
	default:
		return false
	}
}

// StatementMatchReason is the typed explanation of one match status.
type StatementMatchReason string

const (
	StatementMatchReasonNone                   StatementMatchReason = ""
	StatementMatchReasonExplicitCharge         StatementMatchReason = "explicit_charge"
	StatementMatchReasonAggregateSKUCoverage   StatementMatchReason = "aggregate_sku_coverage"
	StatementMatchReasonNoChargeLink           StatementMatchReason = "no_charge_link"
	StatementMatchReasonChargeNotFound         StatementMatchReason = "charge_not_found"
	StatementMatchReasonAccountScopedTotal     StatementMatchReason = "account_scoped_total"
	StatementMatchReasonEvidenceIneligible     StatementMatchReason = "evidence_ineligible"
	StatementMatchReasonPartialSKUCoverage     StatementMatchReason = "partial_sku_coverage"
	StatementMatchReasonDanglingCoverageRef    StatementMatchReason = "dangling_coverage_ref"
	StatementMatchReasonStoreMismatch          StatementMatchReason = "store_mismatch"
	StatementMatchReasonTenantMismatch         StatementMatchReason = "tenant_mismatch"
	StatementMatchReasonAccountMismatch        StatementMatchReason = "account_mismatch"
	StatementMatchReasonPeriodMismatch         StatementMatchReason = "period_mismatch"
	StatementMatchReasonCurrencyMismatch       StatementMatchReason = "currency_mismatch"
	StatementMatchReasonUnitMismatch           StatementMatchReason = "unit_mismatch"
	StatementMatchReasonSKUMismatch            StatementMatchReason = "sku_mismatch"
	StatementMatchReasonGranularityMismatch    StatementMatchReason = "granularity_mismatch"
	StatementMatchReasonCrossRevisionAmbiguity StatementMatchReason = "cross_revision_ambiguity"
	StatementMatchReasonDuplicateParentChild   StatementMatchReason = "duplicate_parent_child"
	StatementMatchReasonOverlappingCoverage    StatementMatchReason = "overlapping_coverage"
)

// IsKnown reports whether r is a documented match reason.
func (r StatementMatchReason) IsKnown() bool {
	switch r {
	case StatementMatchReasonNone, StatementMatchReasonExplicitCharge, StatementMatchReasonAggregateSKUCoverage,
		StatementMatchReasonNoChargeLink, StatementMatchReasonChargeNotFound, StatementMatchReasonAccountScopedTotal,
		StatementMatchReasonEvidenceIneligible, StatementMatchReasonPartialSKUCoverage, StatementMatchReasonDanglingCoverageRef,
		StatementMatchReasonStoreMismatch, StatementMatchReasonTenantMismatch, StatementMatchReasonAccountMismatch,
		StatementMatchReasonPeriodMismatch, StatementMatchReasonCurrencyMismatch, StatementMatchReasonUnitMismatch,
		StatementMatchReasonSKUMismatch, StatementMatchReasonGranularityMismatch, StatementMatchReasonCrossRevisionAmbiguity,
		StatementMatchReasonDuplicateParentChild, StatementMatchReasonOverlappingCoverage:
		return true
	default:
		return false
	}
}

// StatementCoverageLink is the immutable coverage link of one statement line:
// every covered statement line identity plus every covered retained charge and
// evidence identity. It deliberately contains no request/B-leg allocation.
type StatementCoverageLink struct {
	StatementKey      string
	StatementID       string
	StatementRevision uint64
	Kind              StatementMatchKind
	LineKeys          []string
	LineIDs           []string
	Charges           []metering.ChargeRef
	EvidenceIDs       []string
}

// Key returns a deterministic bounded immutable coverage identity. Every
// semantic member participates in the SHA-256 preimage.
func (l StatementCoverageLink) Key() string {
	if !l.Kind.IsKnown() || len(l.LineKeys) == 0 || len(l.Charges) == 0 {
		return ""
	}
	chargeKeys := make([]string, 0, len(l.Charges))
	for _, charge := range l.Charges {
		chargeKeys = append(chargeKeys, statementMatchChargeRefKey(charge))
	}
	encoded, err := json.Marshal(struct {
		Version           string             `json:"version"`
		StatementKey      string             `json:"statement_key"`
		StatementID       string             `json:"statement_id"`
		StatementRevision uint64             `json:"statement_revision"`
		Kind              StatementMatchKind `json:"kind"`
		LineKeys          []string           `json:"line_keys"`
		Charges           []string           `json:"charges"`
		EvidenceIDs       []string           `json:"evidence_ids"`
	}{
		Version: "statement-coverage-link:v1", StatementKey: l.StatementKey,
		StatementID: l.StatementID, StatementRevision: l.StatementRevision, Kind: l.Kind,
		LineKeys: l.LineKeys, Charges: chargeKeys, EvidenceIDs: l.EvidenceIDs,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "statement-coverage-link:v1:" + hex.EncodeToString(sum[:])
}

// Equal reports whether two links describe the same immutable coverage.
func (l StatementCoverageLink) Equal(other StatementCoverageLink) bool {
	left, right := l.Clone(), other.Clone()
	leftCharges, rightCharges := make([]string, 0, len(left.Charges)), make([]string, 0, len(right.Charges))
	for _, charge := range left.Charges {
		leftCharges = append(leftCharges, statementMatchChargeRefKey(charge))
	}
	for _, charge := range right.Charges {
		rightCharges = append(rightCharges, statementMatchChargeRefKey(charge))
	}
	return left.StatementKey == right.StatementKey && left.StatementID == right.StatementID &&
		left.StatementRevision == right.StatementRevision && left.Kind == right.Kind &&
		slices.Equal(left.LineKeys, right.LineKeys) && slices.Equal(left.LineIDs, right.LineIDs) &&
		slices.Equal(leftCharges, rightCharges) && slices.Equal(left.EvidenceIDs, right.EvidenceIDs)
}

// Clone returns a detached copy safe to hand to another owner.
func (l StatementCoverageLink) Clone() StatementCoverageLink {
	out := l
	out.LineKeys = slices.Clone(l.LineKeys)
	out.LineIDs = slices.Clone(l.LineIDs)
	out.Charges = slices.Clone(l.Charges)
	out.EvidenceIDs = slices.Clone(l.EvidenceIDs)
	return out
}

// StatementMatchLine is one typed statement-line matching outcome.
type StatementMatchLine struct {
	LineID          string
	LineKey         string
	LineRevision    uint64
	Status          StatementMatchStatus
	Reason          StatementMatchReason
	UnmatchedReason string
	LinkKey         string
}

// StatementMatchResult is the deterministic per-statement matching outcome.
// Unmatched lines and their declared reasons are preserved.
type StatementMatchResult struct {
	StatementKey      string
	StatementID       string
	StatementRevision uint64
	Lines             []StatementMatchLine
	Links             []StatementCoverageLink
}

// StatementMatchSet is the deterministic result of one matching batch.
type StatementMatchSet struct {
	Results []StatementMatchResult
}

func statementMatchChargeRefKey(ref metering.ChargeRef) string {
	return ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.ChargeItemID
}

func statementMatchComponentEqual(left, right *metering.ComponentKey) (equal bool, unitOnlyDifference bool) {
	if left == nil || right == nil {
		return left == nil && right == nil, false
	}
	a, errA := left.Normalize()
	b, errB := right.Normalize()
	if errA != nil || errB != nil {
		return false, false
	}
	if a.Direction != b.Direction || a.Component != b.Component || a.SchemaID != b.SchemaID || !slices.Equal(a.Dimensions, b.Dimensions) {
		return false, false
	}
	if a.Unit != b.Unit {
		return false, true
	}
	return true, false
}
