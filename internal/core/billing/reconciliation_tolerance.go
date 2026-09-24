package billing

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Task 12.3A pure versioned tolerance policy. It applies the approved C4
// comparison rule with exact arithmetic and no persistence, selection or
// posting behavior.

var (
	// ErrReconciliationToleranceInvalid identifies a malformed or ambiguous
	// policy, or an evaluation input that cannot be compared at all.
	ErrReconciliationToleranceInvalid = errors.New("billing: invalid reconciliation tolerance policy or input")
	// ErrReconciliationToleranceOverflow identifies an exact tolerance
	// intermediate outside the bounded decimal/rational contract.
	ErrReconciliationToleranceOverflow = errors.New("billing: reconciliation tolerance arithmetic overflow")
)

const (
	// ReconciliationTolerancePolicyV1 is the first supported policy format.
	ReconciliationTolerancePolicyV1 uint32 = 1
	// MaxReconciliationToleranceRules bounds one policy's disjoint rule set.
	MaxReconciliationToleranceRules = 64
)

// Additive reconciliation vocabulary shared with Tasks 12.1/12.2. Declaring
// these here keeps the earlier comparator files untouched.
const (
	// ReconciliationStatusWithinTolerance is distinct from an exact match.
	ReconciliationStatusWithinTolerance ReconciliationComparisonStatus = "within_tolerance"

	ReconciliationReasonZeroDenominator        ReconciliationComparisonReason = "zero_denominator"
	ReconciliationReasonUnitMismatch           ReconciliationComparisonReason = "unit_mismatch"
	ReconciliationReasonTolerancePolicyMissing ReconciliationComparisonReason = "tolerance_policy_missing"
	ReconciliationReasonEstimatedNotExact      ReconciliationComparisonReason = "estimated_not_exact"
)

// ReconciliationTolerancePolicy is a versioned set of disjoint tolerance
// rules. Scopes must be unambiguous: no rule may overlap another.
type ReconciliationTolerancePolicy struct {
	Version uint32                        `json:"version"`
	Ref     VersionRef                    `json:"ref"`
	Rules   []ReconciliationToleranceRule `json:"rules"`
}

// ReconciliationToleranceRule bounds one scope. At least one limit is
// required; limits are exact non-negative bounded decimals.
type ReconciliationToleranceRule struct {
	ID            string                       `json:"id"`
	Scope         ReconciliationToleranceScope `json:"scope"`
	AbsoluteLimit *metering.Decimal            `json:"absolute_limit,omitempty"`
	RelativeLimit *metering.Decimal            `json:"relative_limit,omitempty"`
}

// ReconciliationToleranceScope is a wildcard pattern: an empty field matches
// any target value, a non-empty field must match exactly.
type ReconciliationToleranceScope struct {
	Unit      string `json:"unit,omitempty"`
	Currency  string `json:"currency,omitempty"`
	Component string `json:"component,omitempty"`
	SchemaID  string `json:"schema_id,omitempty"`
	Context   string `json:"context,omitempty"`
}

// ReconciliationToleranceTarget is the evaluation identity a policy matches.
type ReconciliationToleranceTarget = ReconciliationToleranceScope

// ReconciliationToleranceEvaluation retains the exact signed and absolute
// deltas even when the difference is within tolerance.
type ReconciliationToleranceEvaluation struct {
	PolicyID      string                        `json:"policy_id"`
	PolicyVersion string                        `json:"policy_version"`
	RuleID        string                        `json:"rule_id,omitempty"`
	Target        ReconciliationToleranceTarget `json:"target"`

	Expected *MonetaryExactAmount `json:"expected,omitempty"`
	Reported *MonetaryExactAmount `json:"reported,omitempty"`

	SignedDelta   *MonetaryExactAmount `json:"signed_delta,omitempty"`
	AbsoluteDelta *MonetaryExactAmount `json:"absolute_delta,omitempty"`

	RelativeDifference *MonetaryExactAmount `json:"relative_difference,omitempty"`
	RelativePresent    bool                 `json:"relative_present"`
	ZeroDenominator    bool                 `json:"zero_denominator"`

	AbsoluteLimit *metering.Decimal    `json:"absolute_limit,omitempty"`
	RelativeLimit *metering.Decimal    `json:"relative_limit,omitempty"`
	Threshold     *MonetaryExactAmount `json:"threshold,omitempty"`

	WithinTolerance bool                           `json:"within_tolerance"`
	Status          ReconciliationComparisonStatus `json:"status"`
	Reason          ReconciliationComparisonReason `json:"reason,omitempty"`
}

// Validate rejects duplicate rules, ambiguous overlap, missing or negative
// limits and limits outside the bounded exact decimal contract.
func (p ReconciliationTolerancePolicy) Validate() error {
	if p.Version != ReconciliationTolerancePolicyV1 {
		return fmt.Errorf("%w: unsupported version %d", ErrReconciliationToleranceInvalid, p.Version)
	}
	if !validEconomicIdentity(p.Ref.ID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: policy id required", ErrReconciliationToleranceInvalid)
	}
	if !validEconomicIdentity(p.Ref.Version, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("%w: policy version required", ErrReconciliationToleranceInvalid)
	}
	if len(p.Rules) == 0 {
		return fmt.Errorf("%w: at least one tolerance rule required", ErrReconciliationToleranceInvalid)
	}
	if len(p.Rules) > MaxReconciliationToleranceRules {
		return fmt.Errorf("%w: rules=%d max=%d", ErrReconciliationToleranceInvalid, len(p.Rules), MaxReconciliationToleranceRules)
	}
	seenIDs := make(map[string]struct{}, len(p.Rules))
	for i := range p.Rules {
		rule := p.Rules[i]
		if err := rule.validate(); err != nil {
			return fmt.Errorf("%w: rule %d: %v", ErrReconciliationToleranceInvalid, i, err)
		}
		if _, exists := seenIDs[rule.ID]; exists {
			return fmt.Errorf("%w: duplicate rule id %q", ErrReconciliationToleranceInvalid, rule.ID)
		}
		seenIDs[rule.ID] = struct{}{}
	}
	for i := range p.Rules {
		for j := i + 1; j < len(p.Rules); j++ {
			left, right := p.Rules[i].Scope, p.Rules[j].Scope
			if !toleranceScopesOverlap(left, right) {
				continue
			}
			if left == right {
				return fmt.Errorf("%w: duplicate scope for rules %q and %q", ErrReconciliationToleranceInvalid, p.Rules[i].ID, p.Rules[j].ID)
			}
			return fmt.Errorf("%w: ambiguous overlapping scopes for rules %q and %q", ErrReconciliationToleranceInvalid, p.Rules[i].ID, p.Rules[j].ID)
		}
	}
	return nil
}

func (r ReconciliationToleranceRule) validate() error {
	if !validEconomicIdentity(r.ID, metering.MaxSchemaIDBytes) {
		return fmt.Errorf("rule id required")
	}
	for name, value := range map[string]string{
		"rule scope unit": r.Scope.Unit, "rule scope currency": r.Scope.Currency,
		"rule scope component": r.Scope.Component, "rule scope schema": r.Scope.SchemaID,
		"rule scope context": r.Scope.Context,
	} {
		if value != "" && !validEconomicIdentity(value, metering.MaxSchemaIDBytes) {
			return fmt.Errorf("%s is not a bounded identity", name)
		}
	}
	if r.AbsoluteLimit == nil && r.RelativeLimit == nil {
		return fmt.Errorf("rule %q requires an absolute or relative limit", r.ID)
	}
	for name, limit := range map[string]*metering.Decimal{"absolute limit": r.AbsoluteLimit, "relative limit": r.RelativeLimit} {
		if err := validateToleranceLimit(name, limit); err != nil {
			return fmt.Errorf("rule %q: %w", r.ID, err)
		}
	}
	return nil
}

// validateToleranceLimit enforces the shared exact non-negative bounded limit
// contract used by policy rules and by retained tolerance evaluations.
func validateToleranceLimit(name string, limit *metering.Decimal) error {
	if limit == nil {
		return nil
	}
	if err := limit.Validate(); err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	rat, err := limit.ToRat()
	if err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	if rat.Sign() < 0 {
		return fmt.Errorf("%s must be non-negative", name)
	}
	return nil
}

func toleranceScopesOverlap(left, right ReconciliationToleranceScope) bool {
	return toleranceScopeFieldOverlaps(left.Unit, right.Unit) &&
		toleranceScopeFieldOverlaps(left.Currency, right.Currency) &&
		toleranceScopeFieldOverlaps(left.Component, right.Component) &&
		toleranceScopeFieldOverlaps(left.SchemaID, right.SchemaID) &&
		toleranceScopeFieldOverlaps(left.Context, right.Context)
}

func toleranceScopeFieldOverlaps(pattern, value string) bool {
	return pattern == "" || value == "" || pattern == value
}

func (p ReconciliationTolerancePolicy) matchingRule(target ReconciliationToleranceTarget) (ReconciliationToleranceRule, bool) {
	for _, rule := range p.Rules {
		if rule.Scope.matches(target) {
			return rule, true
		}
	}
	return ReconciliationToleranceRule{}, false
}

func (s ReconciliationToleranceScope) matches(target ReconciliationToleranceTarget) bool {
	return (s.Unit == "" || s.Unit == target.Unit) &&
		(s.Currency == "" || s.Currency == target.Currency) &&
		(s.Component == "" || s.Component == target.Component) &&
		(s.SchemaID == "" || s.SchemaID == target.SchemaID) &&
		(s.Context == "" || s.Context == target.Context)
}

// EvaluateReconciliationTolerance applies
// abs(P-E) <= max(absolute_limit, relative_limit*abs(E)) with exact
// arithmetic. When E is zero only the absolute limit applies and the relative
// difference is absent with a typed zero_denominator reason.
func EvaluateReconciliationTolerance(policy ReconciliationTolerancePolicy, target ReconciliationToleranceTarget, expected, reported MonetaryExactAmount) (ReconciliationToleranceEvaluation, error) {
	if err := policy.Validate(); err != nil {
		return ReconciliationToleranceEvaluation{}, err
	}
	evaluation := ReconciliationToleranceEvaluation{
		PolicyID: policy.Ref.ID, PolicyVersion: policy.Ref.Version, Target: target,
		Expected: cloneExactAmount(&expected), Reported: cloneExactAmount(&reported),
	}
	if expected.Currency == "" || reported.Currency == "" || expected.Currency != reported.Currency {
		evaluation.Status = ReconciliationStatusIncomparable
		evaluation.Reason = ReconciliationReasonUnitMismatch
		return evaluation, nil
	}
	expectedRat, err := expected.Rat()
	if err != nil {
		return ReconciliationToleranceEvaluation{}, fmt.Errorf("%w: expected: %w", ErrReconciliationToleranceInvalid, err)
	}
	reportedRat, err := reported.Rat()
	if err != nil {
		return ReconciliationToleranceEvaluation{}, fmt.Errorf("%w: reported: %w", ErrReconciliationToleranceInvalid, err)
	}
	unit := expected.Currency
	signedRat := new(big.Rat).Sub(reportedRat, expectedRat)
	absoluteRat := new(big.Rat).Abs(signedRat)
	signed, err := toleranceExactAmount(unit, signedRat)
	if err != nil {
		return ReconciliationToleranceEvaluation{}, err
	}
	evaluation.SignedDelta = &signed
	absolute, err := toleranceExactAmount(unit, absoluteRat)
	if err != nil {
		return ReconciliationToleranceEvaluation{}, err
	}
	evaluation.AbsoluteDelta = &absolute
	if expectedRat.Sign() == 0 {
		evaluation.ZeroDenominator = true
		evaluation.Reason = ReconciliationReasonZeroDenominator
	}

	rule, matched := policy.matchingRule(target)
	if !matched {
		evaluation.Status = ReconciliationStatusPartial
		evaluation.Reason = ReconciliationReasonTolerancePolicyMissing
		return evaluation, nil
	}
	evaluation.RuleID = rule.ID
	evaluation.AbsoluteLimit = cloneDecimal(rule.AbsoluteLimit)
	evaluation.RelativeLimit = cloneDecimal(rule.RelativeLimit)

	var threshold *big.Rat
	if rule.AbsoluteLimit != nil {
		threshold, err = rule.AbsoluteLimit.ToRat()
		if err != nil {
			return ReconciliationToleranceEvaluation{}, fmt.Errorf("%w: absolute limit: %v", ErrReconciliationToleranceInvalid, err)
		}
	}
	if expectedRat.Sign() != 0 && rule.RelativeLimit != nil {
		relativeLimit, err := rule.RelativeLimit.ToRat()
		if err != nil {
			return ReconciliationToleranceEvaluation{}, fmt.Errorf("%w: relative limit: %v", ErrReconciliationToleranceInvalid, err)
		}
		relativeComponent := new(big.Rat).Mul(relativeLimit, new(big.Rat).Abs(expectedRat))
		if threshold == nil || relativeComponent.Cmp(threshold) > 0 {
			threshold = relativeComponent
		}
	}
	if threshold == nil {
		// A relative-only rule cannot classify a zero expected value; the
		// deltas remain retained but tolerance is unknown.
		evaluation.Status = ReconciliationStatusPartial
		evaluation.Reason = ReconciliationReasonZeroDenominator
		evaluation.ZeroDenominator = true
		return evaluation, nil
	}
	thresholdAmount, err := toleranceExactAmount(unit, threshold)
	if err != nil {
		return ReconciliationToleranceEvaluation{}, err
	}
	evaluation.Threshold = &thresholdAmount
	if expectedRat.Sign() != 0 {
		relative := new(big.Rat).Quo(absoluteRat, new(big.Rat).Abs(expectedRat))
		relativeAmount, err := toleranceExactAmount(unit, relative)
		if err != nil {
			return ReconciliationToleranceEvaluation{}, err
		}
		evaluation.RelativeDifference = &relativeAmount
		evaluation.RelativePresent = true
	}
	switch {
	case signedRat.Sign() == 0:
		evaluation.Status = ReconciliationStatusMatched
		evaluation.WithinTolerance = true
	case absoluteRat.Cmp(threshold) <= 0:
		evaluation.Status = ReconciliationStatusWithinTolerance
		evaluation.WithinTolerance = true
	default:
		evaluation.Status = ReconciliationStatusDiscrepant
	}
	return evaluation, nil
}

func toleranceExactAmount(unit string, value *big.Rat) (MonetaryExactAmount, error) {
	amount, err := newMonetaryExactAmount(unit, value)
	if err != nil {
		return MonetaryExactAmount{}, fmt.Errorf("%w: %v", ErrReconciliationToleranceOverflow, err)
	}
	return amount, nil
}

// cloneExactAmount deep-copies an exact amount without aliasing caller memory.
func cloneExactAmount(in *MonetaryExactAmount) *MonetaryExactAmount {
	if in == nil {
		return nil
	}
	out := *in
	if in.Decimal != nil {
		decimal := *in.Decimal
		out.Decimal = &decimal
	}
	return &out
}
