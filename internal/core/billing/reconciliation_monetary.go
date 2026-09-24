package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 12.2 exact E/Q/P monetary decomposition. It compares existing
// immutable valuations and computes the C4 cost-effect/residual formulas
// without rating, tolerance policy, operator selection or persistence.

var (
	// ErrMonetaryDiscrepancyInput identifies malformed, empty or out-of-bounds
	// decomposition input.
	ErrMonetaryDiscrepancyInput = errors.New("billing: invalid monetary discrepancy input")
	// ErrMonetaryDiscrepancyConflict identifies two valuations claiming one
	// E/Q/P role. The comparison fails closed instead of selecting one.
	ErrMonetaryDiscrepancyConflict = errors.New("billing: conflicting monetary valuation roles")
	// ErrMonetaryDiscrepancyRole identifies a valuation basis outside the E/Q/P
	// decomposition contract.
	ErrMonetaryDiscrepancyRole = errors.New("billing: unsupported monetary valuation role")
	// ErrMonetaryDiscrepancyOverflow identifies an exact difference outside the
	// bounded decimal/rational contract. No inexact fallback exists.
	ErrMonetaryDiscrepancyOverflow = errors.New("billing: monetary discrepancy arithmetic overflow")
)

const (
	// MaxMonetaryDiscrepancyValuations bounds the input to the three E/Q/P
	// roles; a larger slice always contains a duplicate or unsupported role.
	MaxMonetaryDiscrepancyValuations = 3
	// MaxMonetaryDiscrepancyRows bounds the output currency rows by the union
	// of three bounded valuation total sets.
	MaxMonetaryDiscrepancyRows = 3 * economics.MaxValuationTotals
)

// MonetaryDiscrepancyRole is one E/Q/P valuation plane.
type MonetaryDiscrepancyRole string

const (
	MonetaryRoleE MonetaryDiscrepancyRole = "e"
	MonetaryRoleQ MonetaryDiscrepancyRole = "q"
	MonetaryRoleP MonetaryDiscrepancyRole = "p"
)

// MonetaryDiscrepancyStatus is the overall decomposition state. It never
// declares incomplete or incomparable evidence reconciled.
type MonetaryDiscrepancyStatus string

const (
	MonetaryDiscrepancyComplete     MonetaryDiscrepancyStatus = "complete"
	MonetaryDiscrepancyPartial      MonetaryDiscrepancyStatus = "partial"
	MonetaryDiscrepancyIncomparable MonetaryDiscrepancyStatus = "incomparable"
	MonetaryDiscrepancyConflict     MonetaryDiscrepancyStatus = "conflict"
)

// MonetaryTermStatus is the state of one cost-effect/residual formula.
type MonetaryTermStatus string

const (
	MonetaryTermComplete     MonetaryTermStatus = "complete"
	MonetaryTermPartial      MonetaryTermStatus = "partial"
	MonetaryTermIncomparable MonetaryTermStatus = "incomparable"
	MonetaryTermMissing      MonetaryTermStatus = "missing"
)

func (s MonetaryDiscrepancyStatus) IsKnown() bool {
	switch s {
	case MonetaryDiscrepancyComplete, MonetaryDiscrepancyPartial, MonetaryDiscrepancyIncomparable, MonetaryDiscrepancyConflict:
		return true
	default:
		return false
	}
}

// MonetaryDiscrepancyReason is the typed explanation for a non-complete term
// or result. Every absent amount keeps an explicit reason.
type MonetaryDiscrepancyReason string

const (
	MonetaryReasonNone                         MonetaryDiscrepancyReason = ""
	MonetaryReasonMissingE                     MonetaryDiscrepancyReason = "missing_e"
	MonetaryReasonMissingQ                     MonetaryDiscrepancyReason = "missing_q"
	MonetaryReasonMissingP                     MonetaryDiscrepancyReason = "missing_p"
	MonetaryReasonAmountUnavailable            MonetaryDiscrepancyReason = "amount_unavailable"
	MonetaryReasonCurrencyMissing              MonetaryDiscrepancyReason = "currency_missing"
	MonetaryReasonSubjectMismatch              MonetaryDiscrepancyReason = "subject_mismatch"
	MonetaryReasonPayerMismatch                MonetaryDiscrepancyReason = "payer_mismatch"
	MonetaryReasonTariffMismatch               MonetaryDiscrepancyReason = "tariff_mismatch"
	MonetaryReasonContextMismatch              MonetaryDiscrepancyReason = "context_mismatch"
	MonetaryReasonCoverageMismatch             MonetaryDiscrepancyReason = "coverage_mismatch"
	MonetaryReasonCurrencyMismatch             MonetaryDiscrepancyReason = "currency_mismatch"
	MonetaryReasonValuationIncomplete          MonetaryDiscrepancyReason = "valuation_incomplete"
	MonetaryReasonValuationConflict            MonetaryDiscrepancyReason = "valuation_conflict"
	MonetaryReasonQuantityEvidencePartial      MonetaryDiscrepancyReason = "quantity_evidence_partial"
	MonetaryReasonQuantityEvidenceIncomparable MonetaryDiscrepancyReason = "quantity_evidence_incomparable"
	MonetaryReasonQuantityEvidenceConflict     MonetaryDiscrepancyReason = "quantity_evidence_conflict"
)

// IsKnown reports whether the reason is part of the supported vocabulary.
func (r MonetaryDiscrepancyReason) IsKnown() bool {
	switch r {
	case MonetaryReasonNone,
		MonetaryReasonMissingE, MonetaryReasonMissingQ, MonetaryReasonMissingP,
		MonetaryReasonAmountUnavailable, MonetaryReasonCurrencyMissing,
		MonetaryReasonSubjectMismatch, MonetaryReasonPayerMismatch, MonetaryReasonTariffMismatch,
		MonetaryReasonContextMismatch, MonetaryReasonCoverageMismatch, MonetaryReasonCurrencyMismatch,
		MonetaryReasonValuationIncomplete, MonetaryReasonValuationConflict,
		MonetaryReasonQuantityEvidencePartial, MonetaryReasonQuantityEvidenceIncomparable,
		MonetaryReasonQuantityEvidenceConflict:
		return true
	default:
		return false
	}
}

// MonetaryCause is a suspected, non-definitive attribution. A non-zero
// reported-price residual is always suspected_pricing_difference because this
// comparison holds no provider rate detail that could prove a tariff error.
type MonetaryCause string

const (
	MonetaryCauseNone                       MonetaryCause = ""
	MonetaryCauseQuantityDifference         MonetaryCause = "quantity_difference"
	MonetaryCauseSuspectedPricingDifference MonetaryCause = "suspected_pricing_difference"
)

// ErrInvalidMonetaryExactAmount identifies a noncanonical or unbounded
// exact monetary amount.
var ErrInvalidMonetaryExactAmount = errors.New("billing: invalid exact monetary amount")

// MonetaryExactAmount is one signed exact native-currency amount. Exactly one
// representation is present: a canonical bounded decimal, or reduced rational
// parts within the bounded exact-arithmetic contract. All Phase12 ingresses
// must call Validate (or Rat, which validates) before using a value.
type MonetaryExactAmount struct {
	Currency    string            `json:"currency"`
	Decimal     *metering.Decimal `json:"decimal,omitempty"`
	Numerator   string            `json:"numerator,omitempty"`
	Denominator string            `json:"denominator,omitempty"`
}

// Validate enforces the canonical exact-amount contract: a normalized
// uppercase currency code or a canonical lowercase unit key, exactly one
// decimal or rational representation, a canonical bounded decimal, and a
// gcd-reduced rational with canonical nonnegative-integer parts within the
// existing 128-digit bound.
func (a MonetaryExactAmount) Validate() error {
	if err := validateMonetaryAmountUnitKey(a.Currency); err != nil {
		return err
	}
	decimalPresent := a.Decimal != nil
	rationalPresent := a.Numerator != "" || a.Denominator != ""
	switch {
	case decimalPresent && rationalPresent:
		return fmt.Errorf("%w: decimal and rational representations are mutually exclusive", ErrInvalidMonetaryExactAmount)
	case !decimalPresent && !rationalPresent:
		return fmt.Errorf("%w: exactly one decimal or rational representation is required", ErrInvalidMonetaryExactAmount)
	case decimalPresent:
		if err := a.Decimal.Validate(); err != nil {
			return fmt.Errorf("%w: decimal: %v", ErrInvalidMonetaryExactAmount, err)
		}
		return nil
	default:
		return validateMonetaryAmountRationalParts(a.Numerator, a.Denominator)
	}
}

// NormalizeCanonical validates and returns the canonical representation. It
// never repairs a noncanonical value.
func (a MonetaryExactAmount) NormalizeCanonical() (MonetaryExactAmount, error) {
	if err := a.Validate(); err != nil {
		return MonetaryExactAmount{}, err
	}
	out := a
	if a.Decimal != nil {
		decimal, err := a.Decimal.Normalize()
		if err != nil {
			return MonetaryExactAmount{}, fmt.Errorf("%w: decimal: %v", ErrInvalidMonetaryExactAmount, err)
		}
		out.Decimal = &decimal
	}
	return out, nil
}

func validateMonetaryAmountUnitKey(key string) error {
	if !validEconomicIdentity(key, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: currency or unit key is required", ErrInvalidMonetaryExactAmount)
	}
	if key == strings.ToUpper(key) {
		if _, err := economics.NormalizeCurrency(key); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidMonetaryExactAmount, err)
		}
		return nil
	}
	if key != strings.ToLower(key) {
		return fmt.Errorf("%w: mixed-case unit key %q is not canonical", ErrInvalidMonetaryExactAmount, key)
	}
	return nil
}

func validateMonetaryAmountRationalParts(numerator, denominator string) error {
	if numerator == "" || denominator == "" {
		return fmt.Errorf("%w: numerator and denominator are required together", ErrInvalidMonetaryExactAmount)
	}
	parsedNumerator, ok := new(big.Int).SetString(numerator, 10)
	if !ok || parsedNumerator.String() != numerator {
		return fmt.Errorf("%w: numerator %q is not a canonical integer", ErrInvalidMonetaryExactAmount, numerator)
	}
	parsedDenominator, ok := new(big.Int).SetString(denominator, 10)
	if !ok || parsedDenominator.String() != denominator || parsedDenominator.Sign() <= 0 {
		return fmt.Errorf("%w: denominator %q must be a canonical positive integer", ErrInvalidMonetaryExactAmount, denominator)
	}
	if parsedNumerator.Sign() == 0 {
		return fmt.Errorf("%w: zero must use the canonical decimal representation", ErrInvalidMonetaryExactAmount)
	}
	if len(strings.TrimPrefix(numerator, "-")) > maxRationalDigits || len(denominator) > maxRationalDigits {
		return fmt.Errorf("%w: rational parts exceed %d digits", ErrInvalidMonetaryExactAmount, maxRationalDigits)
	}
	if new(big.Int).GCD(nil, nil, parsedNumerator, parsedDenominator).Cmp(big.NewInt(1)) != 0 {
		return fmt.Errorf("%w: rational must be reduced", ErrInvalidMonetaryExactAmount)
	}
	return nil
}

// Rat returns the exact rational value for further exact arithmetic. It
// validates first, so unvalidated or noncanonical input never reaches
// arithmetic.
func (a MonetaryExactAmount) Rat() (*big.Rat, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	if a.Decimal != nil {
		value, err := a.Decimal.ToRat()
		if err != nil {
			return nil, fmt.Errorf("%w: decimal: %v", ErrInvalidMonetaryExactAmount, err)
		}
		return value, nil
	}
	numerator, _ := new(big.Int).SetString(a.Numerator, 10)
	denominator, _ := new(big.Int).SetString(a.Denominator, 10)
	return new(big.Rat).SetFrac(numerator, denominator), nil
}

// MonetaryDiscrepancyTerm is one exact formula outcome. Amount is absent
// whenever the term is missing, incomparable or lacks a comparable value.
type MonetaryDiscrepancyTerm struct {
	Status MonetaryTermStatus        `json:"status"`
	Reason MonetaryDiscrepancyReason `json:"reason,omitempty"`
	Cause  MonetaryCause             `json:"cause,omitempty"`
	Amount *MonetaryExactAmount      `json:"amount,omitempty"`
}

// MonetaryDiscrepancyRow is one currency-scoped decomposition:
//
//	MeteringCostEffect    = Q - E
//	ReportedPriceResidual = P - Q
//	EndToEndCostDelta     = P - E
type MonetaryDiscrepancyRow struct {
	Currency              string                  `json:"currency"`
	MeteringCostEffect    MonetaryDiscrepancyTerm `json:"metering_cost_effect"`
	ReportedPriceResidual MonetaryDiscrepancyTerm `json:"reported_price_residual"`
	EndToEndCostDelta     MonetaryDiscrepancyTerm `json:"end_to_end_cost_delta"`
}

// MonetaryValuationEvidence preserves one alternative E/Q/P valuation with its
// role, immutable source refs and completeness labels.
type MonetaryValuationEvidence struct {
	Role      MonetaryDiscrepancyRole `json:"role"`
	Valuation economics.Valuation     `json:"valuation"`
}

// MonetaryDiscrepancyInput is the frozen valuation set for one economic
// subject. QuantityComparison is the optional pure Task 12.1 comparison for
// the same subject; it cannot supply monetary terms but its status prevents a
// partial/missing quantity evidence set from being reported reconciled.
type MonetaryDiscrepancyInput struct {
	Valuations         []economics.Valuation
	QuantityComparison *ComponentQuantityComparison
}

// MonetaryDiscrepancyComparison is the bounded, deterministic result. All
// alternative valuations remain stored; no source-selection field exists here.
type MonetaryDiscrepancyComparison struct {
	Status     MonetaryDiscrepancyStatus    `json:"status"`
	Reason     MonetaryDiscrepancyReason    `json:"reason,omitempty"`
	Subject    metering.SubjectRef          `json:"subject"`
	Rows       []MonetaryDiscrepancyRow     `json:"rows"`
	Valuations []MonetaryValuationEvidence  `json:"valuations"`
	Quantity   *ComponentQuantityComparison `json:"quantity,omitempty"`
}

// DecomposeMonetaryDiscrepancies compares exact E/Q/P valuation totals per
// native currency. Terms are computed only when the compared valuations are
// present, have exact amounts and share subject, payer, coverage, currency and
// (for E/Q) frozen tariff/rater/measurement context. Anything else returns a
// typed partial/incomparable outcome; missing values are never zero-filled.
func DecomposeMonetaryDiscrepancies(in MonetaryDiscrepancyInput) (MonetaryDiscrepancyComparison, error) {
	if len(in.Valuations) == 0 {
		return MonetaryDiscrepancyComparison{}, fmt.Errorf("%w: at least one E/Q/P valuation required", ErrMonetaryDiscrepancyInput)
	}
	if len(in.Valuations) > MaxMonetaryDiscrepancyValuations {
		return MonetaryDiscrepancyComparison{}, fmt.Errorf("%w: valuations=%d max=%d", ErrMonetaryDiscrepancyInput, len(in.Valuations), MaxMonetaryDiscrepancyValuations)
	}

	byRole := make(map[MonetaryDiscrepancyRole]economics.Valuation, MaxMonetaryDiscrepancyValuations)
	for i := range in.Valuations {
		valuation := in.Valuations[i]
		if err := valuation.Validate(); err != nil {
			return MonetaryDiscrepancyComparison{}, fmt.Errorf("%w: valuation %d: %v", ErrMonetaryDiscrepancyInput, i, err)
		}
		role, known := monetaryRoleForBasis(valuation.Basis)
		if !known {
			return MonetaryDiscrepancyComparison{}, fmt.Errorf("%w: basis %q", ErrMonetaryDiscrepancyRole, valuation.Basis)
		}
		if _, exists := byRole[role]; exists {
			return MonetaryDiscrepancyComparison{}, fmt.Errorf("%w: duplicate role %q", ErrMonetaryDiscrepancyConflict, role)
		}
		byRole[role] = valuation
	}

	result := MonetaryDiscrepancyComparison{}
	facts := make(map[MonetaryDiscrepancyRole]*monetaryValuationFacts, MaxMonetaryDiscrepancyValuations)
	for _, role := range []MonetaryDiscrepancyRole{MonetaryRoleE, MonetaryRoleQ, MonetaryRoleP} {
		valuation, ok := byRole[role]
		if !ok {
			continue
		}
		cloned := valuation.Clone()
		result.Valuations = append(result.Valuations, MonetaryValuationEvidence{Role: role, Valuation: cloned})
		if result.Subject.Kind == "" {
			result.Subject = cloned.Subject
		}
		fact, err := newMonetaryValuationFacts(role, cloned)
		if err != nil {
			return MonetaryDiscrepancyComparison{}, err
		}
		facts[role] = fact
	}
	if in.QuantityComparison != nil {
		quantity := cloneComponentQuantityComparison(*in.QuantityComparison)
		result.Quantity = &quantity
	}

	currencySet := make(map[string]struct{})
	for _, role := range []MonetaryDiscrepancyRole{MonetaryRoleE, MonetaryRoleQ, MonetaryRoleP} {
		if fact := facts[role]; fact != nil {
			for _, currency := range fact.currencies {
				currencySet[currency] = struct{}{}
			}
		}
	}
	if len(currencySet) > MaxMonetaryDiscrepancyRows {
		return MonetaryDiscrepancyComparison{}, fmt.Errorf("%w: currency rows=%d max=%d", ErrMonetaryDiscrepancyInput, len(currencySet), MaxMonetaryDiscrepancyRows)
	}
	currencies := make([]string, 0, len(currencySet))
	for currency := range currencySet {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)

	formulas := []monetaryFormula{
		newMonetaryFormula(monetaryMeteringCostEffect, facts[MonetaryRoleQ], facts[MonetaryRoleE], MonetaryReasonMissingQ, MonetaryReasonMissingE),
		newMonetaryFormula(monetaryReportedPriceResidual, facts[MonetaryRoleP], facts[MonetaryRoleQ], MonetaryReasonMissingP, MonetaryReasonMissingQ),
		newMonetaryFormula(monetaryEndToEndCostDelta, facts[MonetaryRoleP], facts[MonetaryRoleE], MonetaryReasonMissingP, MonetaryReasonMissingE),
	}
	for _, currency := range currencies {
		row := MonetaryDiscrepancyRow{Currency: currency}
		for _, formula := range formulas {
			term, err := formula.term(currency)
			if err != nil {
				return MonetaryDiscrepancyComparison{}, err
			}
			switch formula.kind {
			case monetaryMeteringCostEffect:
				row.MeteringCostEffect = term
			case monetaryReportedPriceResidual:
				row.ReportedPriceResidual = term
			case monetaryEndToEndCostDelta:
				row.EndToEndCostDelta = term
			}
		}
		result.Rows = append(result.Rows, row)
	}
	finalizeMonetaryDiscrepancy(&result)
	return result, nil
}

func monetaryRoleForBasis(basis economics.ValuationBasis) (MonetaryDiscrepancyRole, bool) {
	switch basis {
	case economics.BasisLocalExpected:
		return MonetaryRoleE, true
	case economics.BasisProviderQuantityLocal:
		return MonetaryRoleQ, true
	case economics.BasisProviderReported:
		return MonetaryRoleP, true
	default:
		return "", false
	}
}

// monetaryExactTotal is one valuation total's exact native value.
type monetaryExactTotal struct {
	rat   *big.Rat
	exact bool
}

// monetaryValuationFacts is a validated valuation projected to the comparison
// identity fields and exact currency totals.
type monetaryValuationFacts struct {
	role         MonetaryDiscrepancyRole
	value        economics.Valuation
	completeness economics.Completeness
	totals       map[string]monetaryExactTotal
	currencies   []string
	frozenKey    string
	contextKey   string
	coverageKey  string
}

func newMonetaryValuationFacts(role MonetaryDiscrepancyRole, value economics.Valuation) (*monetaryValuationFacts, error) {
	facts := &monetaryValuationFacts{
		role: role, value: value, completeness: value.Completeness,
		totals: make(map[string]monetaryExactTotal, len(value.Totals)),
	}
	for i, total := range value.Totals {
		currency, err := economics.NormalizeCurrency(total.Currency)
		if err != nil {
			return nil, fmt.Errorf("%w: %s total %d currency: %v", ErrMonetaryDiscrepancyInput, role, i, err)
		}
		rat, exact, err := monetaryTotalRat(total)
		if err != nil {
			return nil, fmt.Errorf("%w: %s total %d: %v", ErrMonetaryDiscrepancyInput, role, i, err)
		}
		facts.totals[currency] = monetaryExactTotal{rat: rat, exact: exact}
		facts.currencies = append(facts.currencies, currency)
	}
	sort.Strings(facts.currencies)

	coverageKey, err := canonicalReconciliationCoverageKey(value.CoverageRefs)
	if err != nil {
		return nil, fmt.Errorf("%w: %s coverage: %v", ErrMonetaryDiscrepancyInput, role, err)
	}
	facts.coverageKey = coverageKey
	qualifiersKey, err := canonicalReconciliationQualifierKey(value.EffectiveQualifiers)
	if err != nil {
		return nil, fmt.Errorf("%w: %s measurement context: %v", ErrMonetaryDiscrepancyInput, role, err)
	}
	frozen, err := json.Marshal(monetaryFrozenContext{
		Tariff: value.Tariff, TariffContent: value.TariffContent,
		Rater: value.Rater, RaterContent: value.RaterContent,
		Policy: value.Policy, PolicyContent: value.PolicyContent,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s frozen context: %v", ErrMonetaryDiscrepancyInput, role, err)
	}
	facts.frozenKey = string(frozen)
	context, err := json.Marshal(monetaryMeasurementContext{
		Scope: value.Scope, Qualifiers: qualifiersKey,
		QualifierSnapshot: value.QualifierSnapshot, QualifierSnapshotRef: value.QualifierSnapshotRef,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s measurement context: %v", ErrMonetaryDiscrepancyInput, role, err)
	}
	facts.contextKey = string(context)
	return facts, nil
}

type monetaryFrozenContext struct {
	Tariff        economics.RatingSnapshotRef   `json:"tariff"`
	TariffContent *economics.SnapshotContentRef `json:"tariff_content,omitempty"`
	Rater         economics.RatingSnapshotRef   `json:"rater"`
	RaterContent  *economics.SnapshotContentRef `json:"rater_content,omitempty"`
	Policy        economics.PolicySnapshotRef   `json:"policy"`
	PolicyContent *economics.SnapshotContentRef `json:"policy_content,omitempty"`
}

type monetaryMeasurementContext struct {
	Scope                string                        `json:"scope,omitempty"`
	Qualifiers           string                        `json:"qualifiers,omitempty"`
	QualifierSnapshot    string                        `json:"qualifier_snapshot,omitempty"`
	QualifierSnapshotRef *economics.SnapshotContentRef `json:"qualifier_snapshot_ref,omitempty"`
}

func monetaryTotalRat(total economics.CurrencyTotal) (*big.Rat, bool, error) {
	if total.Amount != nil {
		rat, err := total.Amount.ToRat()
		if err != nil {
			return nil, false, err
		}
		return rat, true, nil
	}
	if total.AmountNumerator == "" || total.AmountDenominator == "" {
		return nil, false, nil
	}
	numerator, ok := new(big.Int).SetString(total.AmountNumerator, 10)
	if !ok {
		return nil, false, fmt.Errorf("numerator %q is not an integer", total.AmountNumerator)
	}
	denominator, ok := new(big.Int).SetString(total.AmountDenominator, 10)
	if !ok || denominator.Sign() <= 0 {
		return nil, false, fmt.Errorf("denominator %q is not positive", total.AmountDenominator)
	}
	return new(big.Rat).SetFrac(numerator, denominator), true, nil
}

func newMonetaryExactAmount(currency string, value *big.Rat) (MonetaryExactAmount, error) {
	if value == nil {
		return MonetaryExactAmount{}, fmt.Errorf("%w: nil exact amount", ErrMonetaryDiscrepancyOverflow)
	}
	if err := boundedRat(value); err != nil {
		return MonetaryExactAmount{}, fmt.Errorf("%w: %v", ErrMonetaryDiscrepancyOverflow, err)
	}
	var out MonetaryExactAmount
	if decimal, rational, terminating := decimalFromRat(value); terminating {
		out = MonetaryExactAmount{Currency: currency, Decimal: &decimal}
	} else {
		out = MonetaryExactAmount{Currency: currency, Numerator: rational.Num().String(), Denominator: rational.Denom().String()}
	}
	if err := out.Validate(); err != nil {
		return MonetaryExactAmount{}, fmt.Errorf("%w: %v", ErrMonetaryDiscrepancyOverflow, err)
	}
	return out, nil
}

type monetaryFormulaKind int

const (
	monetaryMeteringCostEffect monetaryFormulaKind = iota
	monetaryReportedPriceResidual
	monetaryEndToEndCostDelta
)

// monetaryFormula is one exact B - A formula over two valuation roles.
type monetaryFormula struct {
	kind          monetaryFormulaKind
	left          *monetaryValuationFacts
	right         *monetaryValuationFacts
	leftMissing   MonetaryDiscrepancyReason
	rightMissing  MonetaryDiscrepancyReason
	contextReason MonetaryDiscrepancyReason
	comparable    bool
}

func newMonetaryFormula(kind monetaryFormulaKind, left, right *monetaryValuationFacts, leftMissing, rightMissing MonetaryDiscrepancyReason) monetaryFormula {
	formula := monetaryFormula{kind: kind, left: left, right: right, leftMissing: leftMissing, rightMissing: rightMissing}
	if left != nil && right != nil {
		reason, comparable := monetaryPairContextReason(left, right)
		formula.contextReason = reason
		formula.comparable = comparable
	}
	return formula
}

// monetaryPairContextReason enforces the C4 grouping for every comparable
// pair: economic subject/charge, payer, coverage, effective measurement
// scope/qualifier/snapshot context and a shared currency. Only the E/Q pair
// additionally requires the frozen local tariff/rater/policy identity; a
// P-side provider tariff detail may legitimately differ, but only after the
// economic identity and measurement context match.
func monetaryPairContextReason(left, right *monetaryValuationFacts) (MonetaryDiscrepancyReason, bool) {
	if !sameSubject(left.value.Subject, right.value.Subject) {
		return MonetaryReasonSubjectMismatch, false
	}
	if left.value.Payer != right.value.Payer {
		return MonetaryReasonPayerMismatch, false
	}
	if left.coverageKey != right.coverageKey {
		return MonetaryReasonCoverageMismatch, false
	}
	if left.contextKey != right.contextKey {
		return MonetaryReasonContextMismatch, false
	}
	if monetaryFrozenPair(left.role, right.role) && left.frozenKey != right.frozenKey {
		return MonetaryReasonTariffMismatch, false
	}
	if len(left.currencies) > 0 && len(right.currencies) > 0 && !monetarySharedCurrency(left, right) {
		return MonetaryReasonCurrencyMismatch, false
	}
	return MonetaryReasonNone, true
}

func monetaryFrozenPair(a, b MonetaryDiscrepancyRole) bool {
	return (a == MonetaryRoleE && b == MonetaryRoleQ) || (a == MonetaryRoleQ && b == MonetaryRoleE)
}

func monetarySharedCurrency(left, right *monetaryValuationFacts) bool {
	for _, currency := range left.currencies {
		if _, ok := right.totals[currency]; ok {
			return true
		}
	}
	return false
}

func (f monetaryFormula) term(currency string) (MonetaryDiscrepancyTerm, error) {
	if f.left == nil {
		return MonetaryDiscrepancyTerm{Status: MonetaryTermMissing, Reason: f.leftMissing}, nil
	}
	if f.right == nil {
		return MonetaryDiscrepancyTerm{Status: MonetaryTermMissing, Reason: f.rightMissing}, nil
	}
	if !f.comparable {
		return MonetaryDiscrepancyTerm{Status: MonetaryTermIncomparable, Reason: f.contextReason}, nil
	}
	leftTotal, leftPresent := f.left.totals[currency]
	rightTotal, rightPresent := f.right.totals[currency]
	switch {
	case len(f.left.totals) == 0 || len(f.right.totals) == 0:
		return MonetaryDiscrepancyTerm{Status: MonetaryTermPartial, Reason: MonetaryReasonAmountUnavailable}, nil
	case !leftPresent || !rightPresent:
		return MonetaryDiscrepancyTerm{Status: MonetaryTermMissing, Reason: MonetaryReasonCurrencyMissing}, nil
	case !leftTotal.exact || !rightTotal.exact:
		return MonetaryDiscrepancyTerm{Status: MonetaryTermPartial, Reason: MonetaryReasonAmountUnavailable}, nil
	}
	if f.left.completeness == economics.CompletenessConflict || f.right.completeness == economics.CompletenessConflict {
		return MonetaryDiscrepancyTerm{Status: MonetaryTermIncomparable, Reason: MonetaryReasonValuationConflict}, nil
	}
	delta := new(big.Rat).Sub(leftTotal.rat, rightTotal.rat)
	amount, err := newMonetaryExactAmount(currency, delta)
	if err != nil {
		return MonetaryDiscrepancyTerm{}, err
	}
	term := MonetaryDiscrepancyTerm{Amount: &amount}
	if f.left.completeness == economics.CompletenessComplete && f.right.completeness == economics.CompletenessComplete {
		term.Status = MonetaryTermComplete
	} else {
		term.Status = MonetaryTermPartial
		term.Reason = MonetaryReasonValuationIncomplete
	}
	if delta.Sign() != 0 {
		switch f.kind {
		case monetaryMeteringCostEffect:
			term.Cause = MonetaryCauseQuantityDifference
		case monetaryReportedPriceResidual:
			term.Cause = MonetaryCauseSuspectedPricingDifference
		}
	}
	return term, nil
}

// monetaryDiscrepancyRollup derives the overall monetary status and reason from
// the validated terms and optional quantity integration. It is the single
// authoritative derivation used by the producer and by retention validation.
func monetaryDiscrepancyRollup(result *MonetaryDiscrepancyComparison) (MonetaryDiscrepancyStatus, MonetaryDiscrepancyReason) {
	severity := monetarySeverityComplete
	reason := MonetaryReasonNone
	if len(result.Rows) == 0 {
		severity = monetarySeverityPartial
		reason = MonetaryReasonAmountUnavailable
	}
	for _, row := range result.Rows {
		for _, term := range []MonetaryDiscrepancyTerm{row.MeteringCostEffect, row.ReportedPriceResidual, row.EndToEndCostDelta} {
			termSeverity, termReason := monetaryTermSeverity(term)
			if termSeverity > severity {
				severity, reason = termSeverity, termReason
			} else if termSeverity == severity && reason == MonetaryReasonNone && termReason != MonetaryReasonNone {
				reason = termReason
			}
		}
	}
	if result.Quantity != nil {
		quantitySeverity, quantityReason := monetaryQuantitySeverity(result.Quantity)
		switch {
		case quantitySeverity > severity:
			severity, reason = quantitySeverity, quantityReason
		case quantitySeverity == severity && quantitySeverity > 0 && reason == MonetaryReasonNone:
			reason = quantityReason
		}
	}
	switch severity {
	case monetarySeverityConflict:
		return MonetaryDiscrepancyConflict, reason
	case monetarySeverityIncomparable:
		return MonetaryDiscrepancyIncomparable, reason
	case monetarySeverityPartial:
		return MonetaryDiscrepancyPartial, reason
	default:
		return MonetaryDiscrepancyComplete, reason
	}
}

func finalizeMonetaryDiscrepancy(result *MonetaryDiscrepancyComparison) {
	result.Status, result.Reason = monetaryDiscrepancyRollup(result)
}

type monetarySeverity int

const (
	monetarySeverityComplete monetarySeverity = iota
	monetarySeverityPartial
	monetarySeverityIncomparable
	monetarySeverityConflict
)

func monetaryTermSeverity(term MonetaryDiscrepancyTerm) (monetarySeverity, MonetaryDiscrepancyReason) {
	switch term.Status {
	case MonetaryTermIncomparable:
		return monetarySeverityIncomparable, term.Reason
	case MonetaryTermPartial, MonetaryTermMissing:
		return monetarySeverityPartial, term.Reason
	default:
		return monetarySeverityComplete, MonetaryReasonNone
	}
}

func monetaryQuantitySeverity(quantity *ComponentQuantityComparison) (monetarySeverity, MonetaryDiscrepancyReason) {
	switch quantity.Status {
	case ReconciliationStatusConflict:
		return monetarySeverityConflict, MonetaryReasonQuantityEvidenceConflict
	case ReconciliationStatusIncomparable:
		return monetarySeverityIncomparable, MonetaryReasonQuantityEvidenceIncomparable
	case ReconciliationStatusPartial, ReconciliationStatusMissingLocal, ReconciliationStatusMissingProvider:
		return monetarySeverityPartial, MonetaryReasonQuantityEvidencePartial
	case ReconciliationStatusMatched, ReconciliationStatusDiscrepant:
		if quantity.Complete {
			return monetarySeverityComplete, MonetaryReasonNone
		}
		return monetarySeverityPartial, MonetaryReasonQuantityEvidencePartial
	default:
		return monetarySeverityPartial, MonetaryReasonQuantityEvidencePartial
	}
}

// cloneComponentQuantityComparison preserves the optional Task 12.1 evidence
// independently of caller-owned memory.
func cloneComponentQuantityComparison(in ComponentQuantityComparison) ComponentQuantityComparison {
	out := in
	if in.Items != nil {
		out.Items = make([]ComponentQuantityComparisonItem, len(in.Items))
		for i := range in.Items {
			out.Items[i] = cloneComponentQuantityComparisonItem(in.Items[i])
		}
	}
	return out
}

func cloneComponentQuantityComparisonItem(in ComponentQuantityComparisonItem) ComponentQuantityComparisonItem {
	out := in
	out.Key = in.Key.Clone()
	out.Local = cloneReconciliationQuantityEvidence(in.Local)
	out.Provider = cloneReconciliationQuantityEvidence(in.Provider)
	if in.SignedDelta != nil {
		value := *in.SignedDelta
		out.SignedDelta = &value
	}
	if in.AbsoluteDelta != nil {
		value := *in.AbsoluteDelta
		out.AbsoluteDelta = &value
	}
	return out
}

func cloneReconciliationQuantityEvidence(in []ReconciliationQuantityEvidence) []ReconciliationQuantityEvidence {
	if in == nil {
		return nil
	}
	out := make([]ReconciliationQuantityEvidence, len(in))
	for i, evidence := range in {
		out[i] = evidence
		out[i].Key = evidence.Key.Clone()
		if evidence.Value != nil {
			value := *evidence.Value
			out[i].Value = &value
		}
	}
	return out
}
