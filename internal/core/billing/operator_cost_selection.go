package billing

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 12.4 pure operator cost selection. It chooses one evidence basis for an
// operator COGS view without mutating or erasing any reconciliation state. It
// performs no posting, cost-head compare-and-swap or journal work.

var (
	// ErrOperatorCostSelectionInput identifies a malformed policy, input or
	// out-of-bounds candidate set.
	ErrOperatorCostSelectionInput = errors.New("billing: invalid operator cost selection input")
	// ErrOperatorCostSelectionConflict identifies duplicate candidate roles;
	// the selection fails closed instead of choosing one silently.
	ErrOperatorCostSelectionConflict = errors.New("billing: conflicting operator cost selection candidates")
)

const (
	// OperatorCostSelectionPolicyV1 is the first supported policy format.
	OperatorCostSelectionPolicyV1 uint32 = 1
	// MaxOperatorCostSelectionRules bounds the ordered policy.
	MaxOperatorCostSelectionRules = 16
	// MaxOperatorCostCandidates bounds the candidate set (one per basis).
	MaxOperatorCostCandidates = 4
	// MaxOperatorCostSelectionRefs bounds retained source refs per candidate.
	MaxOperatorCostSelectionRefs = 1024
)

// OperatorCostSelectionBasis is one comparable evidence plane.
type OperatorCostSelectionBasis string

const (
	OperatorCostBasisNone OperatorCostSelectionBasis = ""
	OperatorCostBasisE    OperatorCostSelectionBasis = "e"
	OperatorCostBasisQ    OperatorCostSelectionBasis = "q"
	OperatorCostBasisP    OperatorCostSelectionBasis = "p"
	OperatorCostBasisS    OperatorCostSelectionBasis = "s"
)

func (b OperatorCostSelectionBasis) IsKnown() bool {
	switch b {
	case OperatorCostBasisE, OperatorCostBasisQ, OperatorCostBasisP, OperatorCostBasisS:
		return true
	default:
		return false
	}
}

// OperatorCostSelectionStatus is the selection outcome. It is deliberately
// separate from comparison status, evidence completeness and posting state.
type OperatorCostSelectionStatus string

const (
	OperatorCostSelectionStatusFinal              OperatorCostSelectionStatus = "final"
	OperatorCostSelectionStatusProvisional        OperatorCostSelectionStatus = "provisional"
	OperatorCostSelectionStatusKnownZero          OperatorCostSelectionStatus = "known_zero"
	OperatorCostSelectionStatusUnknown            OperatorCostSelectionStatus = "unknown"
	OperatorCostSelectionStatusIncomparable       OperatorCostSelectionStatus = "incomparable"
	OperatorCostSelectionStatusConflict           OperatorCostSelectionStatus = "conflict"
	OperatorCostSelectionStatusNotOperatorPayable OperatorCostSelectionStatus = "not_operator_payable"
)

// OperatorCostPostingState records that this task never posts. It is a separate
// field from the selection status.
type OperatorCostPostingState string

const (
	OperatorCostPostingUnposted OperatorCostPostingState = "unposted"
	OperatorCostPostingPending  OperatorCostPostingState = "pending"
)

// OperatorCostSelectionReason is the typed explanation of a non-final
// selection.
type OperatorCostSelectionReason string

const (
	OperatorCostReasonNone                   OperatorCostSelectionReason = ""
	OperatorCostReasonNoPayableEvidence      OperatorCostSelectionReason = "no_payable_evidence"
	OperatorCostReasonKnownZeroAuthorized    OperatorCostSelectionReason = "known_zero_authorized"
	OperatorCostReasonKnownZeroNotAuthorized OperatorCostSelectionReason = "known_zero_not_authorized"
	OperatorCostReasonPayerCustomerBYOK      OperatorCostSelectionReason = "payer_customer_byok"
	OperatorCostReasonPayerUnallocated       OperatorCostSelectionReason = "payer_unallocated"
	OperatorCostReasonPayerNotOperator       OperatorCostSelectionReason = "payer_not_operator"
	OperatorCostReasonCurrencyMismatch       OperatorCostSelectionReason = "currency_mismatch"
	OperatorCostReasonCandidateIncompatible  OperatorCostSelectionReason = "candidate_incompatible"
	OperatorCostReasonComparisonConflict     OperatorCostSelectionReason = "comparison_conflict"
	OperatorCostReasonIncomparableComparison OperatorCostSelectionReason = "incomparable_comparison"
	OperatorCostReasonIncompleteEvidence     OperatorCostSelectionReason = "incomplete_evidence"
)

// OperatorCostPayerClass is the trusted credential payer classification.
type OperatorCostPayerClass string

const (
	OperatorCostPayerOperator     OperatorCostPayerClass = "operator"
	OperatorCostPayerCustomerBYOK OperatorCostPayerClass = "customer_byok"
	OperatorCostPayerUnallocated  OperatorCostPayerClass = "unallocated"
	OperatorCostPayerUnknown      OperatorCostPayerClass = "unknown"
)

func (c OperatorCostPayerClass) IsKnown() bool {
	switch c {
	case OperatorCostPayerOperator, OperatorCostPayerCustomerBYOK, OperatorCostPayerUnallocated, OperatorCostPayerUnknown:
		return true
	default:
		return false
	}
}

// OperatorCostProvenance is the explicit execution basis of the work.
type OperatorCostProvenance string

const (
	OperatorCostProvenanceAttempted    OperatorCostProvenance = "attempted"
	OperatorCostProvenanceNeverStarted OperatorCostProvenance = "never_started"
	OperatorCostProvenanceNotBillable  OperatorCostProvenance = "not_billable"
)

func (p OperatorCostProvenance) IsKnown() bool {
	switch p {
	case OperatorCostProvenanceAttempted, OperatorCostProvenanceNeverStarted, OperatorCostProvenanceNotBillable:
		return true
	default:
		return false
	}
}

func (p OperatorCostProvenance) IsExplicitKnownZero() bool {
	return p == OperatorCostProvenanceNeverStarted || p == OperatorCostProvenanceNotBillable
}

// OperatorCostFXBasis is one explicit frozen conversion basis. It binds the
// exact source and destination currencies, direction and bounded positive exact
// rate material; there is no implicit conversion.
type OperatorCostFXBasis struct {
	ID           string            `json:"id"`
	Version      string            `json:"version"`
	FromCurrency string            `json:"from_currency"`
	ToCurrency   string            `json:"to_currency"`
	Rate         *metering.Decimal `json:"rate"`
}

func (b OperatorCostFXBasis) Validate() error {
	if !validEconomicIdentity(b.ID, metering.MaxSchemaIDBytes) || !validEconomicIdentity(b.Version, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: FX basis id and version are required", ErrOperatorCostSelectionInput)
	}
	from, err := economics.NormalizeCurrency(b.FromCurrency)
	if err != nil {
		return fmt.Errorf("%w: FX from currency: %v", ErrOperatorCostSelectionInput, err)
	}
	to, err := economics.NormalizeCurrency(b.ToCurrency)
	if err != nil {
		return fmt.Errorf("%w: FX to currency: %v", ErrOperatorCostSelectionInput, err)
	}
	if from == to {
		return fmt.Errorf("%w: FX basis requires distinct from/to currencies", ErrOperatorCostSelectionInput)
	}
	if b.Rate == nil {
		return fmt.Errorf("%w: FX rate material is required", ErrOperatorCostSelectionInput)
	}
	if err := b.Rate.Validate(); err != nil {
		return fmt.Errorf("%w: FX rate: %v", ErrOperatorCostSelectionInput, err)
	}
	rat, err := b.Rate.ToRat()
	if err != nil {
		return fmt.Errorf("%w: FX rate: %v", ErrOperatorCostSelectionInput, err)
	}
	if rat.Sign() <= 0 {
		return fmt.Errorf("%w: FX rate must be positive", ErrOperatorCostSelectionInput)
	}
	return nil
}

func (b OperatorCostFXBasis) Clone() *OperatorCostFXBasis {
	out := b
	if b.Rate != nil {
		rate := *b.Rate
		out.Rate = &rate
	}
	return &out
}

func sameOperatorCostFX(left, right *OperatorCostFXBasis) bool {
	if left == nil || right == nil {
		return false
	}
	if left.ID != right.ID || left.Version != right.Version ||
		left.FromCurrency != right.FromCurrency || left.ToCurrency != right.ToCurrency {
		return false
	}
	switch {
	case left.Rate == nil && right.Rate == nil:
		return true
	case left.Rate == nil || right.Rate == nil:
		return false
	default:
		return left.Rate.CanonicalString() == right.Rate.CanonicalString()
	}
}

// OperatorCostReconciliationRef preserves the durable reconciliation identity.
type OperatorCostReconciliationRef struct {
	ID          string `json:"id,omitempty"`
	Version     uint64 `json:"version,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// OperatorCostReconciliationState binds comparison status and completeness
// separately from the selection outcome.
type OperatorCostReconciliationState struct {
	Ref      OperatorCostReconciliationRef  `json:"ref"`
	Status   ReconciliationComparisonStatus `json:"status"`
	Complete bool                           `json:"complete"`
}

// OperatorCostCandidate is one frozen alternative. The amount stays exact.
type OperatorCostCandidate struct {
	Basis            OperatorCostSelectionBasis `json:"basis"`
	ValuationID      string                     `json:"valuation_id"`
	ValuationVersion uint32                     `json:"valuation_version"`
	Currency         string                     `json:"currency"`
	Amount           *MonetaryExactAmount       `json:"amount"`
	Completeness     economics.Completeness     `json:"completeness"`
	Payer            metering.PaymentParty      `json:"payer,omitzero"`
	Scope            string                     `json:"scope,omitempty"`
	CoverageKey      string                     `json:"coverage_key,omitempty"`
	ContextKey       string                     `json:"context_key,omitempty"`
	FX               *OperatorCostFXBasis       `json:"fx,omitempty"`
	SourceRefs       []metering.ObservationRef  `json:"source_refs,omitempty"`
}

// OperatorCostSelectionRule is one ordered, conditional policy step. The first
// matching rule and candidate wins.
type OperatorCostSelectionRule struct {
	ID                          string                      `json:"id"`
	Basis                       OperatorCostSelectionBasis  `json:"basis"`
	Status                      OperatorCostSelectionStatus `json:"status"`
	RequireOperatorPayer        bool                        `json:"require_operator_payer"`
	AllowPartialEvidence        bool                        `json:"allow_partial_evidence"`
	RequireComparableComparison bool                        `json:"require_comparable_comparison"`
}

// OperatorCostSelectionPolicy is the versioned immutable selection policy.
// A final rule must require complete evidence; known-zero is only authorized by
// an explicit provenance list.
type OperatorCostSelectionPolicy struct {
	Version             uint32                      `json:"version"`
	Ref                 VersionRef                  `json:"ref"`
	Rules               []OperatorCostSelectionRule `json:"rules"`
	KnownZeroProvenance []OperatorCostProvenance    `json:"known_zero_provenance,omitempty"`
}

// Validate rejects malformed versions, duplicate rule ids, unknown bases or
// statuses, final rules that accept partial evidence and malformed known-zero
// authorization.
func (p OperatorCostSelectionPolicy) Validate() error {
	if p.Version != OperatorCostSelectionPolicyV1 {
		return fmt.Errorf("%w: unsupported version %d", ErrOperatorCostSelectionInput, p.Version)
	}
	if !validEconomicIdentity(p.Ref.ID, metering.MaxSchemaIDBytes) || !validEconomicIdentity(p.Ref.Version, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: policy id and version are required", ErrOperatorCostSelectionInput)
	}
	if len(p.Rules) == 0 {
		return fmt.Errorf("%w: at least one selection rule required", ErrOperatorCostSelectionInput)
	}
	if len(p.Rules) > MaxOperatorCostSelectionRules {
		return fmt.Errorf("%w: rules=%d max=%d", ErrOperatorCostSelectionInput, len(p.Rules), MaxOperatorCostSelectionRules)
	}
	seenRules := make(map[string]struct{}, len(p.Rules))
	for i := range p.Rules {
		rule := p.Rules[i]
		if !validEconomicIdentity(rule.ID, metering.MaxSchemaIDBytes) {
			return fmt.Errorf("%w: rule %d id is required", ErrOperatorCostSelectionInput, i)
		}
		if _, exists := seenRules[rule.ID]; exists {
			return fmt.Errorf("%w: duplicate rule id %q", ErrOperatorCostSelectionInput, rule.ID)
		}
		seenRules[rule.ID] = struct{}{}
		if !rule.Basis.IsKnown() {
			return fmt.Errorf("%w: rule %q unknown basis %q", ErrOperatorCostSelectionInput, rule.ID, rule.Basis)
		}
		if rule.Status != OperatorCostSelectionStatusFinal && rule.Status != OperatorCostSelectionStatusProvisional {
			return fmt.Errorf("%w: rule %q status must be final or provisional", ErrOperatorCostSelectionInput, rule.ID)
		}
		if rule.Status == OperatorCostSelectionStatusFinal && rule.AllowPartialEvidence {
			return fmt.Errorf("%w: final rule %q cannot accept partial evidence", ErrOperatorCostSelectionInput, rule.ID)
		}
	}
	seenKnownZero := make(map[OperatorCostProvenance]struct{}, len(p.KnownZeroProvenance))
	for i, provenance := range p.KnownZeroProvenance {
		if !provenance.IsExplicitKnownZero() {
			return fmt.Errorf("%w: known-zero provenance %d must be never_started or not_billable", ErrOperatorCostSelectionInput, i)
		}
		if _, exists := seenKnownZero[provenance]; exists {
			return fmt.Errorf("%w: duplicate known-zero provenance %q", ErrOperatorCostSelectionInput, provenance)
		}
		seenKnownZero[provenance] = struct{}{}
	}
	return nil
}

// Clone makes the policy independent of caller memory.
func (p OperatorCostSelectionPolicy) Clone() OperatorCostSelectionPolicy {
	out := p
	out.Rules = append([]OperatorCostSelectionRule(nil), p.Rules...)
	out.KnownZeroProvenance = append([]OperatorCostProvenance(nil), p.KnownZeroProvenance...)
	return out
}

// OperatorCostSelectionInput is the frozen selection context.
type OperatorCostSelectionInput struct {
	Subject        metering.SubjectRef             `json:"subject"`
	Scope          string                          `json:"scope,omitempty"`
	CoverageKey    string                          `json:"coverage_key,omitempty"`
	ContextKey     string                          `json:"context_key,omitempty"`
	Currency       string                          `json:"currency"`
	FX             *OperatorCostFXBasis            `json:"fx,omitempty"`
	PayerClass     OperatorCostPayerClass          `json:"payer_class"`
	Provenance     OperatorCostProvenance          `json:"provenance"`
	Reconciliation OperatorCostReconciliationState `json:"reconciliation"`
	Candidates     []OperatorCostCandidate         `json:"candidates,omitempty"`
	AsOf           time.Time                       `json:"as_of,omitzero"`
}

// OperatorCostSelectionResult preserves all alternatives and all state planes.
type OperatorCostSelectionResult struct {
	Policy             VersionRef                     `json:"policy"`
	Subject            metering.SubjectRef            `json:"subject"`
	Scope              string                         `json:"scope,omitempty"`
	PayerClass         OperatorCostPayerClass         `json:"payer_class"`
	Provenance         OperatorCostProvenance         `json:"provenance"`
	Currency           string                         `json:"currency"`
	FX                 *OperatorCostFXBasis           `json:"fx,omitempty"`
	Status             OperatorCostSelectionStatus    `json:"status"`
	Basis              OperatorCostSelectionBasis     `json:"basis,omitempty"`
	Reason             OperatorCostSelectionReason    `json:"reason,omitempty"`
	Amount             *MonetaryExactAmount           `json:"amount,omitempty"`
	NativeAmount       *MonetaryExactAmount           `json:"native_amount,omitempty"`
	KnownZeroBasis     OperatorCostProvenance         `json:"known_zero_basis,omitempty"`
	ComparisonStatus   ReconciliationComparisonStatus `json:"comparison_status"`
	ComparisonComplete bool                           `json:"comparison_complete"`
	Reconciliation     OperatorCostReconciliationRef  `json:"reconciliation"`
	PostingState       OperatorCostPostingState       `json:"posting_state"`
	Candidates         []OperatorCostCandidate        `json:"candidates"`
	AsOf               time.Time                      `json:"as_of,omitzero"`
}

// SelectOperatorCost chooses one basis under the explicit ordered policy. It is
// pure: it never mutates the policy or input, never deletes alternatives and
// never claims reconciliation success from source selection alone.
func SelectOperatorCost(policy OperatorCostSelectionPolicy, input OperatorCostSelectionInput) (OperatorCostSelectionResult, error) {
	if err := policy.Validate(); err != nil {
		return OperatorCostSelectionResult{}, err
	}
	if err := validateOperatorCostSelectionInput(input); err != nil {
		return OperatorCostSelectionResult{}, err
	}
	if err := operatorCostCandidateRolesUnique(input.Candidates); err != nil {
		return OperatorCostSelectionResult{}, err
	}

	currency, err := economics.NormalizeCurrency(input.Currency)
	if err != nil {
		return OperatorCostSelectionResult{}, fmt.Errorf("%w: currency: %v", ErrOperatorCostSelectionInput, err)
	}
	result := OperatorCostSelectionResult{
		Policy:             policy.Ref,
		Subject:            input.Subject,
		Scope:              input.Scope,
		PayerClass:         input.PayerClass,
		Provenance:         input.Provenance,
		Currency:           currency,
		FX:                 cloneOperatorCostFX(input.FX),
		Status:             OperatorCostSelectionStatusUnknown,
		ComparisonStatus:   input.Reconciliation.Status,
		ComparisonComplete: input.Reconciliation.Complete,
		Reconciliation:     input.Reconciliation.Ref,
		PostingState:       OperatorCostPostingUnposted,
		Candidates:         canonicalOperatorCostCandidates(input.Candidates),
		AsOf:               input.AsOf,
	}
	if input.Reconciliation.Status == ReconciliationStatusConflict {
		result.Status = OperatorCostSelectionStatusConflict
		result.Reason = OperatorCostReasonComparisonConflict
		return result, nil
	}
	if input.PayerClass == OperatorCostPayerCustomerBYOK {
		result.Status = OperatorCostSelectionStatusNotOperatorPayable
		result.Reason = OperatorCostReasonPayerCustomerBYOK
		return result, nil
	}
	if input.PayerClass == OperatorCostPayerUnallocated || input.PayerClass == OperatorCostPayerUnknown {
		result.Status = OperatorCostSelectionStatusUnknown
		result.Reason = OperatorCostReasonPayerUnallocated
		return result, nil
	}

	comparisonComparable := operatorCostComparisonComparable(input.Reconciliation)
	currencyBlocked, incompatibleBlocked, payerBlocked, incompleteBlocked, comparisonBlocked := false, false, false, false, false
	candidates := canonicalOperatorCostCandidates(input.Candidates)
	for ruleIndex := range policy.Rules {
		rule := policy.Rules[ruleIndex]
		for candidateIndex := range candidates {
			candidate := candidates[candidateIndex]
			if candidate.Basis != rule.Basis {
				continue
			}
			if !operatorCostCandidateCompatible(input, candidate) {
				incompatibleBlocked = true
				continue
			}
			if !operatorCostCurrencyComparable(input, candidate) {
				currencyBlocked = true
				continue
			}
			if !operatorCostCandidatePayerEligible(rule, candidate.Payer) {
				payerBlocked = true
				continue
			}
			if !rule.AllowPartialEvidence && candidate.Completeness != economics.CompletenessComplete {
				incompleteBlocked = true
				continue
			}
			if rule.RequireComparableComparison && !comparisonComparable {
				comparisonBlocked = true
				continue
			}
			result.Status = rule.Status
			result.Basis = candidate.Basis
			if candidate.Currency != result.Currency {
				native := cloneExactAmount(candidate.Amount)
				converted, err := convertOperatorCostAmount(candidate, result.Currency)
				if err != nil {
					return OperatorCostSelectionResult{}, err
				}
				result.NativeAmount = native
				result.Amount = converted
				result.FX = cloneOperatorCostFX(candidate.FX)
			} else {
				result.Amount = cloneExactAmount(candidate.Amount)
			}
			if rule.Status == OperatorCostSelectionStatusProvisional {
				result.PostingState = OperatorCostPostingPending
			}
			return result, nil
		}
	}

	switch {
	case input.Provenance.IsExplicitKnownZero() && operatorCostKnownZeroAuthorized(policy, input.Provenance):
		zero, err := newMonetaryExactAmount(result.Currency, new(big.Rat))
		if err != nil {
			return OperatorCostSelectionResult{}, fmt.Errorf("%w: known zero amount: %v", ErrOperatorCostSelectionInput, err)
		}
		result.Status = OperatorCostSelectionStatusKnownZero
		result.Reason = OperatorCostReasonKnownZeroAuthorized
		result.KnownZeroBasis = input.Provenance
		result.Amount = &zero
	case input.Provenance.IsExplicitKnownZero():
		result.Status = OperatorCostSelectionStatusUnknown
		result.Reason = OperatorCostReasonKnownZeroNotAuthorized
	case len(input.Candidates) == 0:
		result.Status = OperatorCostSelectionStatusUnknown
		result.Reason = OperatorCostReasonNoPayableEvidence
	case currencyBlocked:
		result.Status = OperatorCostSelectionStatusIncomparable
		result.Reason = OperatorCostReasonCurrencyMismatch
	case incompatibleBlocked:
		result.Status = OperatorCostSelectionStatusIncomparable
		result.Reason = OperatorCostReasonCandidateIncompatible
	case comparisonBlocked:
		result.Status = OperatorCostSelectionStatusUnknown
		result.Reason = OperatorCostReasonIncomparableComparison
	case incompleteBlocked:
		result.Status = OperatorCostSelectionStatusUnknown
		result.Reason = OperatorCostReasonIncompleteEvidence
	case payerBlocked:
		result.Status = OperatorCostSelectionStatusUnknown
		result.Reason = OperatorCostReasonPayerNotOperator
	default:
		result.Status = OperatorCostSelectionStatusUnknown
		result.Reason = OperatorCostReasonNoPayableEvidence
	}
	return result, nil
}

func operatorCostKnownZeroAuthorized(policy OperatorCostSelectionPolicy, provenance OperatorCostProvenance) bool {
	for _, authorized := range policy.KnownZeroProvenance {
		if authorized == provenance {
			return true
		}
	}
	return false
}

func validateOperatorCostSelectionInput(input OperatorCostSelectionInput) error {
	if err := input.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %v", ErrOperatorCostSelectionInput, err)
	}
	for name, value := range map[string]string{
		"scope": input.Scope, "coverage key": input.CoverageKey, "context key": input.ContextKey,
	} {
		if value != "" && !validEconomicIdentity(value, metering.MaxSchemaIDBytes) {
			return fmt.Errorf("%w: %s is not a bounded identity", ErrOperatorCostSelectionInput, name)
		}
	}
	if _, err := economics.NormalizeCurrency(input.Currency); err != nil {
		return fmt.Errorf("%w: currency: %v", ErrOperatorCostSelectionInput, err)
	}
	if input.FX != nil {
		if err := input.FX.Validate(); err != nil {
			return err
		}
		currency, _ := economics.NormalizeCurrency(input.Currency)
		if input.FX.FromCurrency == currency || input.FX.ToCurrency != currency {
			return fmt.Errorf("%w: input FX basis must convert into the view currency %q", ErrOperatorCostSelectionInput, currency)
		}
	}
	if !input.PayerClass.IsKnown() {
		return fmt.Errorf("%w: unknown payer class %q", ErrOperatorCostSelectionInput, input.PayerClass)
	}
	if !input.Provenance.IsKnown() {
		return fmt.Errorf("%w: unknown provenance %q", ErrOperatorCostSelectionInput, input.Provenance)
	}
	if !reconciliationFindingStatusKnown(input.Reconciliation.Status) {
		return fmt.Errorf("%w: unknown comparison status %q", ErrOperatorCostSelectionInput, input.Reconciliation.Status)
	}
	ref := input.Reconciliation.Ref
	if !validEconomicIdentity(ref.ID, metering.MaxSchemaIDBytes) || ref.Version == 0 || !validOperatorCostFingerprint(ref.Fingerprint) {
		return fmt.Errorf("%w: durable reconciliation ref requires id, version and 64-hex fingerprint", ErrOperatorCostSelectionInput)
	}
	if len(input.Candidates) > MaxOperatorCostCandidates {
		return fmt.Errorf("%w: candidates=%d max=%d", ErrOperatorCostSelectionInput, len(input.Candidates), MaxOperatorCostCandidates)
	}
	for i := range input.Candidates {
		if err := validateOperatorCostCandidate(input.Candidates[i]); err != nil {
			return fmt.Errorf("%w: candidate %d: %w", ErrOperatorCostSelectionInput, i, err)
		}
	}
	return nil
}

func validateOperatorCostCandidate(candidate OperatorCostCandidate) error {
	if !candidate.Basis.IsKnown() {
		return fmt.Errorf("unknown basis %q", candidate.Basis)
	}
	if !validEconomicIdentity(candidate.ValuationID, metering.MaxSchemaIDBytes) || candidate.ValuationVersion == 0 {
		return fmt.Errorf("valuation id and version are required")
	}
	currency, err := economics.NormalizeCurrency(candidate.Currency)
	if err != nil {
		return fmt.Errorf("currency: %v", err)
	}
	if candidate.Amount == nil {
		return fmt.Errorf("exact amount is required")
	}
	if candidate.Amount.Currency != currency {
		return fmt.Errorf("amount currency %q does not match candidate currency %q", candidate.Amount.Currency, currency)
	}
	if _, err := candidate.Amount.Rat(); err != nil {
		return fmt.Errorf("amount: %w", err)
	}
	if !candidate.Completeness.IsKnown() {
		return fmt.Errorf("unknown completeness %q", candidate.Completeness)
	}
	if err := candidate.Payer.Validate(); err != nil {
		return fmt.Errorf("payer: %v", err)
	}
	for name, value := range map[string]string{
		"scope": candidate.Scope, "coverage key": candidate.CoverageKey, "context key": candidate.ContextKey,
	} {
		if value != "" && !validEconomicIdentity(value, metering.MaxSchemaIDBytes) {
			return fmt.Errorf("%s is not a bounded identity", name)
		}
	}
	if candidate.FX != nil {
		if err := candidate.FX.Validate(); err != nil {
			return err
		}
		if candidate.FX.FromCurrency != currency {
			return fmt.Errorf("FX basis source %q must match candidate currency %q", candidate.FX.FromCurrency, currency)
		}
	}
	if len(candidate.SourceRefs) > MaxOperatorCostSelectionRefs {
		return fmt.Errorf("source refs exceed %d", MaxOperatorCostSelectionRefs)
	}
	for i, ref := range candidate.SourceRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("source ref %d: %v", i, err)
		}
	}
	return nil
}

func operatorCostCandidateRolesUnique(candidates []OperatorCostCandidate) error {
	seen := make(map[OperatorCostSelectionBasis]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, exists := seen[candidate.Basis]; exists {
			return fmt.Errorf("%w: duplicate candidate basis %q", ErrOperatorCostSelectionConflict, candidate.Basis)
		}
		seen[candidate.Basis] = struct{}{}
	}
	return nil
}

func operatorCostCandidateCompatible(input OperatorCostSelectionInput, candidate OperatorCostCandidate) bool {
	return candidate.Scope == input.Scope &&
		candidate.CoverageKey == input.CoverageKey &&
		candidate.ContextKey == input.ContextKey
}

func operatorCostCurrencyComparable(input OperatorCostSelectionInput, candidate OperatorCostCandidate) bool {
	inputCurrency, err := economics.NormalizeCurrency(input.Currency)
	if err != nil {
		return false
	}
	if candidate.Currency == inputCurrency {
		return true
	}
	if !sameOperatorCostFX(input.FX, candidate.FX) {
		return false
	}
	return candidate.FX.FromCurrency == candidate.Currency && candidate.FX.ToCurrency == inputCurrency
}

// convertOperatorCostAmount applies the frozen exact rate to the native amount.
// It never rounds and never mutates the native value: non-terminating exact
// results stay rational within the bounded contract.
func convertOperatorCostAmount(candidate OperatorCostCandidate, toCurrency string) (*MonetaryExactAmount, error) {
	if candidate.FX == nil || candidate.FX.Rate == nil {
		return nil, fmt.Errorf("%w: cross-currency selection requires frozen FX rate material", ErrOperatorCostSelectionInput)
	}
	native, err := candidate.Amount.Rat()
	if err != nil {
		return nil, fmt.Errorf("%w: native amount: %v", ErrOperatorCostSelectionInput, err)
	}
	rate, err := candidate.FX.Rate.ToRat()
	if err != nil {
		return nil, fmt.Errorf("%w: FX rate: %v", ErrOperatorCostSelectionInput, err)
	}
	converted, err := newMonetaryExactAmount(toCurrency, new(big.Rat).Mul(native, rate))
	if err != nil {
		return nil, fmt.Errorf("%w: converted amount: %v", ErrOperatorCostSelectionInput, err)
	}
	return &converted, nil
}

// operatorCostCandidatePayerEligible enforces the payer contract per rule: an
// explicit operator payer always satisfies it, an absent payer satisfies only a
// rule that explicitly does not require operator payability, and every other
// classification (unknown, unallocated, customer) is never operator payable.
func operatorCostCandidatePayerEligible(rule OperatorCostSelectionRule, payer metering.PaymentParty) bool {
	switch payer.Kind {
	case metering.PaymentPartyOperator:
		return true
	case "":
		return !rule.RequireOperatorPayer
	default:
		return false
	}
}

// validOperatorCostFingerprint requires the canonical lowercase SHA-256 hex
// form used by durable reconciliation identities.
func validOperatorCostFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func operatorCostComparisonComparable(state OperatorCostReconciliationState) bool {
	if !state.Complete {
		return false
	}
	switch state.Status {
	case ReconciliationStatusMatched, ReconciliationStatusWithinTolerance, ReconciliationStatusDiscrepant:
		return true
	default:
		return false
	}
}

func cloneOperatorCostFX(fx *OperatorCostFXBasis) *OperatorCostFXBasis {
	if fx == nil {
		return nil
	}
	return fx.Clone()
}

var operatorCostBasisRank = map[OperatorCostSelectionBasis]int{
	OperatorCostBasisE: 0,
	OperatorCostBasisQ: 1,
	OperatorCostBasisP: 2,
	OperatorCostBasisS: 3,
}

func canonicalOperatorCostCandidates(in []OperatorCostCandidate) []OperatorCostCandidate {
	if len(in) == 0 {
		return nil
	}
	out := make([]OperatorCostCandidate, len(in))
	for i, candidate := range in {
		out[i] = candidate
		out[i].Amount = cloneExactAmount(candidate.Amount)
		out[i].FX = cloneOperatorCostFX(candidate.FX)
		out[i].SourceRefs = canonicalReconciliationObservationRefs(candidate.SourceRefs)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if operatorCostBasisRank[out[i].Basis] != operatorCostBasisRank[out[j].Basis] {
			return operatorCostBasisRank[out[i].Basis] < operatorCostBasisRank[out[j].Basis]
		}
		return out[i].Basis < out[j].Basis
	})
	return out
}
