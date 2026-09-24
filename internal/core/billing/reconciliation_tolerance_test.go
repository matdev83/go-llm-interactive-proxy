package billing

import (
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func toleranceLimit(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	value, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", raw, err)
	}
	if _, err := value.Normalize(); err != nil {
		t.Fatalf("Normalize(%q): %v", raw, err)
	}
	return &value
}

func toleranceTestPolicy(rules ...ReconciliationToleranceRule) ReconciliationTolerancePolicy {
	return ReconciliationTolerancePolicy{
		Version: ReconciliationTolerancePolicyV1,
		Ref:     VersionRef{ID: "tolerance-policy", Version: "v1"},
		Rules:   rules,
	}
}

func toleranceTestRule(id string, scope ReconciliationToleranceScope, absolute, relative *metering.Decimal) ReconciliationToleranceRule {
	return ReconciliationToleranceRule{ID: id, Scope: scope, AbsoluteLimit: absolute, RelativeLimit: relative}
}

func toleranceAmount(t *testing.T, unit, raw string) MonetaryExactAmount {
	t.Helper()
	decimal, err := metering.ParseDecimal(raw)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", raw, err)
	}
	rat, err := decimal.ToRat()
	if err != nil {
		t.Fatalf("ToRat(%q): %v", raw, err)
	}
	amount, err := newMonetaryExactAmount(unit, rat)
	if err != nil {
		t.Fatalf("newMonetaryExactAmount(%q, %s): %v", unit, raw, err)
	}
	return amount
}

func assertToleranceAmount(t *testing.T, label string, amount *MonetaryExactAmount, unit, canonical string) {
	t.Helper()
	if amount == nil {
		t.Fatalf("%s is absent, want %s %s", label, unit, canonical)
	}
	if amount.Currency != unit {
		t.Fatalf("%s unit = %q, want %q", label, amount.Currency, unit)
	}
	if amount.Decimal == nil {
		t.Fatalf("%s is not a terminating decimal: %+v", label, amount)
	}
	if got := amount.Decimal.CanonicalString(); got != canonical {
		t.Fatalf("%s = %s, want %s", label, got, canonical)
	}
}

// TestReconciliationTolerancePolicyValidation locks the versioned policy
// contract: duplicates, ambiguous overlap, missing/negative limits and
// unbounded precision all fail closed.
func TestReconciliationTolerancePolicyValidation(t *testing.T) {
	t.Parallel()

	usd := ReconciliationToleranceScope{Currency: "USD"}
	tokenUSD := ReconciliationToleranceScope{Currency: "USD", Unit: "token"}
	eur := ReconciliationToleranceScope{Currency: "EUR"}

	t.Run("valid disjoint scopes", func(t *testing.T) {
		policy := toleranceTestPolicy(
			toleranceTestRule("usd", usd, toleranceLimit(t, "0.01"), toleranceLimit(t, "0.001")),
			toleranceTestRule("eur", eur, toleranceLimit(t, "0.02"), nil),
		)
		if err := policy.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("duplicate scope", func(t *testing.T) {
		policy := toleranceTestPolicy(
			toleranceTestRule("first", usd, toleranceLimit(t, "0.01"), nil),
			toleranceTestRule("second", usd, toleranceLimit(t, "0.02"), nil),
		)
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})

	t.Run("ambiguous overlap", func(t *testing.T) {
		policy := toleranceTestPolicy(
			toleranceTestRule("broad", usd, toleranceLimit(t, "0.01"), nil),
			toleranceTestRule("narrow", tokenUSD, toleranceLimit(t, "0.02"), nil),
		)
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})

	t.Run("duplicate rule id", func(t *testing.T) {
		policy := toleranceTestPolicy(
			toleranceTestRule("same", usd, toleranceLimit(t, "0.01"), nil),
			toleranceTestRule("same", eur, toleranceLimit(t, "0.02"), nil),
		)
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})

	t.Run("rule without limits", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("empty", usd, nil, nil))
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})

	t.Run("negative limit", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("negative", usd, toleranceLimit(t, "-0.01"), nil))
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})

	t.Run("unbounded coefficient precision", func(t *testing.T) {
		limit := metering.Decimal{Coefficient: strings.Repeat("9", 40)}
		policy := toleranceTestPolicy(toleranceTestRule("wide", usd, &limit, nil))
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})

	t.Run("unbounded scale", func(t *testing.T) {
		limit := metering.Decimal{Coefficient: "1", Scale: 255}
		policy := toleranceTestPolicy(toleranceTestRule("deep", usd, &limit, nil))
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})

	t.Run("unsupported version", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", usd, toleranceLimit(t, "0.01"), nil))
		policy.Version = 99
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})

	t.Run("empty and malformed policy identity", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", usd, toleranceLimit(t, "0.01"), nil))
		policy.Rules = nil
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("empty rules error = %v, want ErrReconciliationToleranceInvalid", err)
		}
		policy = toleranceTestPolicy(toleranceTestRule("usd", usd, toleranceLimit(t, "0.01"), nil))
		policy.Ref.ID = ""
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("empty policy id error = %v, want ErrReconciliationToleranceInvalid", err)
		}
		policy = toleranceTestPolicy(toleranceTestRule("", usd, toleranceLimit(t, "0.01"), nil))
		if err := policy.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("empty rule id error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})
}

// TestReconciliationToleranceExactFormula locks
// abs(P-E) <= max(absolute_limit, relative_limit*abs(E)) with exact arithmetic
// and retained signed/absolute deltas.
func TestReconciliationToleranceExactFormula(t *testing.T) {
	t.Parallel()

	t.Run("relative limit governs", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.05"), toleranceLimit(t, "0.10")))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, toleranceAmount(t, "USD", "1.00"), toleranceAmount(t, "USD", "1.10"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusWithinTolerance || !evaluation.WithinTolerance {
			t.Fatalf("status = %q within=%v, want within_tolerance", evaluation.Status, evaluation.WithinTolerance)
		}
		if evaluation.RuleID != "usd" || evaluation.PolicyID != "tolerance-policy" || evaluation.PolicyVersion != "v1" {
			t.Fatalf("policy provenance lost: %+v", evaluation)
		}
		assertToleranceAmount(t, "threshold", evaluation.Threshold, "USD", "1/1")
		assertToleranceAmount(t, "signed delta", evaluation.SignedDelta, "USD", "1/1")
		assertToleranceAmount(t, "absolute delta", evaluation.AbsoluteDelta, "USD", "1/1")
		if !evaluation.RelativePresent || evaluation.ZeroDenominator {
			t.Fatalf("relative presence = %v zero denominator = %v", evaluation.RelativePresent, evaluation.ZeroDenominator)
		}
		assertToleranceAmount(t, "relative difference", evaluation.RelativeDifference, "USD", "1/1")
	})

	t.Run("absolute limit governs", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.20"), toleranceLimit(t, "0.01")))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, toleranceAmount(t, "USD", "1.00"), toleranceAmount(t, "USD", "1.10"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusWithinTolerance {
			t.Fatalf("status = %q, want within_tolerance", evaluation.Status)
		}
		assertToleranceAmount(t, "threshold", evaluation.Threshold, "USD", "2/1")
	})

	t.Run("beyond threshold is discrepant", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.05"), toleranceLimit(t, "0.10")))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, toleranceAmount(t, "USD", "1.00"), toleranceAmount(t, "USD", "1.11"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusDiscrepant || evaluation.WithinTolerance {
			t.Fatalf("status = %q within=%v, want discrepant", evaluation.Status, evaluation.WithinTolerance)
		}
		assertToleranceAmount(t, "signed delta", evaluation.SignedDelta, "USD", "11/2")
		assertToleranceAmount(t, "absolute delta", evaluation.AbsoluteDelta, "USD", "11/2")
	})

	t.Run("exact match", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0"), nil))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, toleranceAmount(t, "USD", "1.00"), toleranceAmount(t, "USD", "1.00"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusMatched {
			t.Fatalf("status = %q, want matched", evaluation.Status)
		}
		assertToleranceAmount(t, "signed delta", evaluation.SignedDelta, "USD", "0/0")
		assertToleranceAmount(t, "absolute delta", evaluation.AbsoluteDelta, "USD", "0/0")
	})

	t.Run("negative expected uses absolute magnitude", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0"), toleranceLimit(t, "0.30")))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, toleranceAmount(t, "USD", "-2"), toleranceAmount(t, "USD", "-1.5"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusWithinTolerance {
			t.Fatalf("status = %q, want within_tolerance", evaluation.Status)
		}
		assertToleranceAmount(t, "threshold", evaluation.Threshold, "USD", "6/1")
		assertToleranceAmount(t, "relative difference", evaluation.RelativeDifference, "USD", "25/2")
	})
}

// TestReconciliationToleranceZeroDenominator proves E=0 keeps only the
// absolute limit and reports the relative difference as absent.
func TestReconciliationToleranceZeroDenominator(t *testing.T) {
	t.Parallel()

	t.Run("absolute limit applies when expected is zero", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.1"), toleranceLimit(t, "0.5")))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, toleranceAmount(t, "USD", "0"), toleranceAmount(t, "USD", "0.05"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusWithinTolerance {
			t.Fatalf("status = %q, want within_tolerance", evaluation.Status)
		}
		if evaluation.RelativePresent || !evaluation.ZeroDenominator || evaluation.Reason != ReconciliationReasonZeroDenominator {
			t.Fatalf("relative/zero-denominator state = %+v", evaluation)
		}
		if evaluation.RelativeDifference != nil {
			t.Fatalf("relative difference must be absent: %+v", evaluation.RelativeDifference)
		}
		assertToleranceAmount(t, "absolute delta", evaluation.AbsoluteDelta, "USD", "5/2")
		assertToleranceAmount(t, "threshold", evaluation.Threshold, "USD", "1/1")
	})

	t.Run("absolute limit exceeded when expected is zero", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.1"), toleranceLimit(t, "0.5")))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, toleranceAmount(t, "USD", "0"), toleranceAmount(t, "USD", "0.2"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusDiscrepant {
			t.Fatalf("status = %q, want discrepant", evaluation.Status)
		}
		if evaluation.Reason != ReconciliationReasonZeroDenominator {
			t.Fatalf("reason = %q, want zero_denominator", evaluation.Reason)
		}
	})

	t.Run("relative-only policy cannot classify zero expected", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, nil, toleranceLimit(t, "0.5")))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, toleranceAmount(t, "USD", "0"), toleranceAmount(t, "USD", "0.2"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusPartial || evaluation.Reason != ReconciliationReasonZeroDenominator {
			t.Fatalf("status/reason = %q/%q, want partial/zero_denominator", evaluation.Status, evaluation.Reason)
		}
		if evaluation.Threshold != nil {
			t.Fatalf("threshold should be absent: %+v", evaluation.Threshold)
		}
		assertToleranceAmount(t, "absolute delta", evaluation.AbsoluteDelta, "USD", "2/1")
	})

	t.Run("zero expected and zero reported", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.1"), nil))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, toleranceAmount(t, "USD", "0"), toleranceAmount(t, "USD", "0"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusMatched || evaluation.Reason != ReconciliationReasonZeroDenominator {
			t.Fatalf("status/reason = %q/%q, want matched/zero_denominator", evaluation.Status, evaluation.Reason)
		}
	})
}

// TestReconciliationToleranceNoMatchingRuleAndUnitMismatch proves unmatched
// scopes and mismatched units fail closed instead of comparing.
func TestReconciliationToleranceNoMatchingRuleAndUnitMismatch(t *testing.T) {
	t.Parallel()

	t.Run("no matching rule is partial", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.1"), nil))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "EUR"}, toleranceAmount(t, "EUR", "1"), toleranceAmount(t, "EUR", "1.5"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusPartial || evaluation.Reason != ReconciliationReasonTolerancePolicyMissing {
			t.Fatalf("status/reason = %q/%q, want partial/tolerance_policy_missing", evaluation.Status, evaluation.Reason)
		}
		if evaluation.Threshold != nil {
			t.Fatalf("unmatched policy must not produce a threshold: %+v", evaluation.Threshold)
		}
		assertToleranceAmount(t, "signed delta", evaluation.SignedDelta, "EUR", "5/1")
		assertToleranceAmount(t, "absolute delta", evaluation.AbsoluteDelta, "EUR", "5/1")
	})

	t.Run("unit mismatch is incomparable", func(t *testing.T) {
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.1"), nil))
		evaluation, err := EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "USD"}, toleranceAmount(t, "USD", "1"), toleranceAmount(t, "EUR", "1.5"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusIncomparable || evaluation.Reason != ReconciliationReasonUnitMismatch {
			t.Fatalf("status/reason = %q/%q, want incomparable/unit_mismatch", evaluation.Status, evaluation.Reason)
		}
	})
}
