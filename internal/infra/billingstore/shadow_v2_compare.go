package billingstore

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// Shadow V2 semantic comparison for Task 17.2.
//
// The comparator classifies one V1-versus-actual-V2 delta against an
// independently derived expected V2 result. A nonzero delta carrying an
// allowlisted reason is not sufficient for an intended-fix verdict: only
// exact agreement between actual and expected V2 may be an expected fix, and
// only exact V1/V2 agreement with no claimed fix may be a match. Wrong
// magnitude or direction is drift. Inputs are bounded synthetic or
// captured-safe aggregates only. Raw prompts, message content, credentials,
// paths, and provider payloads are never accepted on this seam.

// ErrShadowV2ComparisonInvalid identifies a malformed comparison input.
var ErrShadowV2ComparisonInvalid = errors.New("billingstore: invalid shadow V2 comparison")

// ShadowV2ComparisonVerdict is the closed classification vocabulary.
type ShadowV2ComparisonVerdict string

const (
	// ShadowV2Match means V1 and actual V2 agree exactly with no claimed fix.
	ShadowV2Match ShadowV2ComparisonVerdict = "match"
	// ShadowV2ExpectedFix means actual V2 exactly equals the independently
	// derived expected V2 under an allowlisted defect-fix reason.
	ShadowV2ExpectedFix ShadowV2ComparisonVerdict = "expected_fix"
	// ShadowV2UnexpectedDrift means actual V2 differs from expectation, or a
	// claimed fix is inconsistent with the observed amounts.
	ShadowV2UnexpectedDrift ShadowV2ComparisonVerdict = "unexpected_drift"
)

// ShadowV2ComparisonInput is one comparison point. Label is a strict stable
// token (never raw content). V1Nano is the documented legacy amount,
// V2Nano is the actual persisted V2 amount, and ExpectedV2Nano is the
// independently derived expected V2 amount. ReasonCode is empty when no fix
// is claimed; otherwise it must name an allowlisted defect-fix rule.
type ShadowV2ComparisonInput struct {
	Label          string
	Currency       string
	V1Nano         int64
	V2Nano         int64
	ExpectedV2Nano int64
	ReasonCode     string
}

// ShadowV2ComparisonResult carries the verdict, signed actual-minus-V1 delta,
// and a sanitized detail string over validated tokens only.
type ShadowV2ComparisonResult struct {
	Verdict   ShadowV2ComparisonVerdict
	DeltaNano int64
	Detail    string
}

const (
	shadowV2LabelLimit   = 64
	shadowV2ReasonLimit  = 64
	shadowV2CurrencySize = 3
)

// shadowV2AllowedCurrencies is the closed currency vocabulary for synthetic
// comparison vectors.
var shadowV2AllowedCurrencies = map[string]struct{}{
	"USD": {}, "EUR": {}, "GBP": {}, "JPY": {}, "CHF": {}, "CAD": {}, "AUD": {}, "PLN": {},
}

// ShadowV2ExpectedFixReasonCodes is the closed allowlist of intended V1 defect
// fixes. Each code maps to a design acceptance vector; a code alone never
// certifies a delta without exact expected agreement.
func ShadowV2ExpectedFixReasonCodes() []string {
	return []string{
		"reasoning-in-output-once",
		"cache-partition-exclusive",
		"inclusive-input-total-once",
		"uncached-quantity-derivation",
		"aggregate-no-double-count",
		"eqp-quantity-cost-effect",
	}
}

func shadowV2ExpectedFixReason(code string) bool {
	return slices.Contains(ShadowV2ExpectedFixReasonCodes(), code)
}

func shadowV2ValidLabel(label string) bool {
	if len(label) == 0 || len(label) > shadowV2LabelLimit {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		first := i == 0
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && !first:
		case (c == '-' || c == '_') && !first:
		default:
			return false
		}
	}
	return true
}

func checkedSubNano(a, b int64) (int64, error) {
	if (b > 0 && a < math.MinInt64+b) || (b < 0 && a > math.MaxInt64+b) {
		return 0, fmt.Errorf("%w: unrepresentable delta", ErrShadowV2ComparisonInvalid)
	}
	return a - b, nil
}

// CompareShadowV2Semantics classifies one actual-V2 against its independent
// expectation. Error paths never echo unsafe input; detail strings quote only
// validated label and reason tokens.
func CompareShadowV2Semantics(in ShadowV2ComparisonInput) (ShadowV2ComparisonResult, error) {
	if !shadowV2ValidLabel(in.Label) {
		return ShadowV2ComparisonResult{}, fmt.Errorf("%w: comparison label is not a stable token", ErrShadowV2ComparisonInvalid)
	}
	currency := strings.TrimSpace(in.Currency)
	if len(currency) != shadowV2CurrencySize {
		return ShadowV2ComparisonResult{}, fmt.Errorf("%w: comparison currency is not supported", ErrShadowV2ComparisonInvalid)
	}
	upper := strings.ToUpper(currency)
	if currency != upper {
		return ShadowV2ComparisonResult{}, fmt.Errorf("%w: comparison currency is not supported", ErrShadowV2ComparisonInvalid)
	}
	if _, ok := shadowV2AllowedCurrencies[currency]; !ok {
		return ShadowV2ComparisonResult{}, fmt.Errorf("%w: comparison currency is not supported", ErrShadowV2ComparisonInvalid)
	}
	if len(in.ReasonCode) > shadowV2ReasonLimit {
		return ShadowV2ComparisonResult{}, fmt.Errorf("%w: comparison reason is not supported", ErrShadowV2ComparisonInvalid)
	}
	if in.ReasonCode != "" && !shadowV2ExpectedFixReason(in.ReasonCode) {
		return ShadowV2ComparisonResult{}, fmt.Errorf("%w: comparison reason is not supported", ErrShadowV2ComparisonInvalid)
	}
	if in.V1Nano < 0 || in.V2Nano < 0 || in.ExpectedV2Nano < 0 {
		return ShadowV2ComparisonResult{}, fmt.Errorf("%w: comparison amounts must be nonnegative", ErrShadowV2ComparisonInvalid)
	}
	delta, err := checkedSubNano(in.V2Nano, in.V1Nano)
	if err != nil {
		return ShadowV2ComparisonResult{}, err
	}
	if in.V2Nano != in.ExpectedV2Nano {
		return ShadowV2ComparisonResult{
			Verdict:   ShadowV2UnexpectedDrift,
			DeltaNano: delta,
			Detail:    fmt.Sprintf("vector %q drifts from expectation in %s", in.Label, currency),
		}, nil
	}
	switch {
	case delta == 0 && in.ReasonCode == "":
		return ShadowV2ComparisonResult{
			Verdict:   ShadowV2Match,
			DeltaNano: 0,
			Detail:    fmt.Sprintf("vector %q matches in %s", in.Label, currency),
		}, nil
	case delta != 0 && in.ReasonCode != "":
		return ShadowV2ComparisonResult{
			Verdict:   ShadowV2ExpectedFix,
			DeltaNano: delta,
			Detail:    fmt.Sprintf("vector %q applies intended fix %q in %s", in.Label, in.ReasonCode, currency),
		}, nil
	default:
		return ShadowV2ComparisonResult{
			Verdict:   ShadowV2UnexpectedDrift,
			DeltaNano: delta,
			Detail:    fmt.Sprintf("vector %q drifts from expectation in %s", in.Label, currency),
		}, nil
	}
}
