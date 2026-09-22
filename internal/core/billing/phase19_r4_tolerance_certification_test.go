package billing

import (
	"errors"
	"testing"
)

// TestPhase19R4VersionedToleranceCertification certifies Requirement 12.4
// against the production reconciliation policy: versioned absolute and
// relative tolerances with max() threshold semantics, boundary equality,
// distinct policy content versions, explicit zero-denominator behavior, and
// typed incomparable/partial/discrepant decision results with retained deltas.
//
// It complements the pre-existing unit suites
// (TestReconciliationToleranceExactFormula, TestReconciliationToleranceZeroDenominator,
// TestReconciliationToleranceNoMatchingRuleAndUnitMismatch,
// TestReconciliationTolerancePolicyValidation) by proving every 12.4 criterion
// in one production-path certification the traceability matrix can cite.
func TestPhase19R4VersionedToleranceCertification(t *testing.T) {
	t.Parallel()

	target := ReconciliationToleranceTarget{Currency: "USD"}

	t.Run("absolute and relative threshold semantics with max", func(t *testing.T) {
		t.Parallel()
		// Relative governs: max(0.05, 0.10*1.00)=0.10; delta 0.10 is within.
		relPolicy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.05"), toleranceLimit(t, "0.10")))
		evaluation, err := EvaluateReconciliationTolerance(relPolicy, target, toleranceAmount(t, "USD", "1.00"), toleranceAmount(t, "USD", "1.10"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusWithinTolerance || !evaluation.WithinTolerance {
			t.Fatalf("status = %q within=%v, want within_tolerance (relative governs)", evaluation.Status, evaluation.WithinTolerance)
		}
		assertToleranceAmount(t, "threshold", evaluation.Threshold, "USD", "1/1")
		if !evaluation.RelativePresent || evaluation.ZeroDenominator {
			t.Fatalf("relative presence = %v zero denominator = %v, want present/non-zero", evaluation.RelativePresent, evaluation.ZeroDenominator)
		}
		// Deltas are retained even when within tolerance (Req 12.4).
		assertToleranceAmount(t, "signed delta", evaluation.SignedDelta, "USD", "1/1")
		assertToleranceAmount(t, "absolute delta", evaluation.AbsoluteDelta, "USD", "1/1")

		// Absolute governs: max(0.20, 0.01*1.00)=0.20; delta 0.10 is within.
		absPolicy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.20"), toleranceLimit(t, "0.01")))
		evaluation, err = EvaluateReconciliationTolerance(absPolicy, target, toleranceAmount(t, "USD", "1.00"), toleranceAmount(t, "USD", "1.10"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusWithinTolerance {
			t.Fatalf("status = %q, want within_tolerance (absolute governs)", evaluation.Status)
		}
		assertToleranceAmount(t, "threshold", evaluation.Threshold, "USD", "2/1")

		// Beyond max threshold is a typed discrepant decision.
		evaluation, err = EvaluateReconciliationTolerance(relPolicy, target, toleranceAmount(t, "USD", "1.00"), toleranceAmount(t, "USD", "1.11"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusDiscrepant || evaluation.WithinTolerance {
			t.Fatalf("status = %q within=%v, want discrepant", evaluation.Status, evaluation.WithinTolerance)
		}
		assertToleranceAmount(t, "signed delta", evaluation.SignedDelta, "USD", "11/2")
	})

	t.Run("boundary equality is within tolerance", func(t *testing.T) {
		t.Parallel()
		// Delta exactly equals the absolute limit: abs(P-E) <= threshold.
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.10"), nil))
		evaluation, err := EvaluateReconciliationTolerance(policy, target, toleranceAmount(t, "USD", "1.00"), toleranceAmount(t, "USD", "1.10"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusWithinTolerance || !evaluation.WithinTolerance {
			t.Fatalf("boundary status = %q within=%v, want within_tolerance (delta == threshold)", evaluation.Status, evaluation.WithinTolerance)
		}
		// Delta exactly equals the relative threshold: 0.10*2.00=0.20.
		relPolicy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, nil, toleranceLimit(t, "0.10")))
		evaluation, err = EvaluateReconciliationTolerance(relPolicy, target, toleranceAmount(t, "USD", "2.00"), toleranceAmount(t, "USD", "2.20"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusWithinTolerance {
			t.Fatalf("relative boundary status = %q, want within_tolerance", evaluation.Status)
		}
		assertToleranceAmount(t, "relative threshold", evaluation.Threshold, "USD", "2/1")
	})

	t.Run("different policy content versions are distinguished", func(t *testing.T) {
		t.Parallel()
		v1 := ReconciliationTolerancePolicy{
			Version: ReconciliationTolerancePolicyV1,
			Ref:     VersionRef{ID: "tolerance-policy", Version: "v1"},
			Rules:   []ReconciliationToleranceRule{toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.10"), nil)},
		}
		v2 := ReconciliationTolerancePolicy{
			Version: ReconciliationTolerancePolicyV1,
			Ref:     VersionRef{ID: "tolerance-policy", Version: "v2"},
			Rules:   []ReconciliationToleranceRule{toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.50"), nil)},
		}
		if err := v1.Validate(); err != nil {
			t.Fatalf("v1 Validate: %v", err)
		}
		if err := v2.Validate(); err != nil {
			t.Fatalf("v2 Validate: %v", err)
		}
		first, err := EvaluateReconciliationTolerance(v1, target, toleranceAmount(t, "USD", "1.00"), toleranceAmount(t, "USD", "1.30"))
		if err != nil {
			t.Fatalf("v1 Evaluate: %v", err)
		}
		second, err := EvaluateReconciliationTolerance(v2, target, toleranceAmount(t, "USD", "1.00"), toleranceAmount(t, "USD", "1.30"))
		if err != nil {
			t.Fatalf("v2 Evaluate: %v", err)
		}
		if first.PolicyVersion != "v1" || second.PolicyVersion != "v2" {
			t.Fatalf("policy provenance = %q/%q, want v1/v2", first.PolicyVersion, second.PolicyVersion)
		}
		if first.Status != ReconciliationStatusDiscrepant {
			t.Fatalf("v1 status = %q, want discrepant (0.30 > 0.10)", first.Status)
		}
		if second.Status != ReconciliationStatusWithinTolerance {
			t.Fatalf("v2 status = %q, want within_tolerance (0.30 <= 0.50)", second.Status)
		}
		// Unsupported policy format version fails closed.
		bad := v1
		bad.Version = 99
		if err := bad.Validate(); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("format-version 99 error = %v, want ErrReconciliationToleranceInvalid", err)
		}
		if _, err := EvaluateReconciliationTolerance(bad, target, toleranceAmount(t, "USD", "1"), toleranceAmount(t, "USD", "1")); !errors.Is(err, ErrReconciliationToleranceInvalid) {
			t.Fatalf("evaluate format-version 99 error = %v, want ErrReconciliationToleranceInvalid", err)
		}
	})

	t.Run("zero denominators are explicit", func(t *testing.T) {
		t.Parallel()
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.1"), toleranceLimit(t, "0.5")))
		// Within absolute limit: relative difference absent, reason typed.
		evaluation, err := EvaluateReconciliationTolerance(policy, target, toleranceAmount(t, "USD", "0"), toleranceAmount(t, "USD", "0.05"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusWithinTolerance {
			t.Fatalf("status = %q, want within_tolerance", evaluation.Status)
		}
		if evaluation.RelativePresent || !evaluation.ZeroDenominator || evaluation.Reason != ReconciliationReasonZeroDenominator {
			t.Fatalf("zero-denominator state = %+v", evaluation)
		}
		if evaluation.RelativeDifference != nil {
			t.Fatalf("relative difference must be absent: %+v", evaluation.RelativeDifference)
		}
		assertToleranceAmount(t, "absolute delta", evaluation.AbsoluteDelta, "USD", "5/2")
		// Beyond absolute limit: discrepant but still zero-denominator typed.
		evaluation, err = EvaluateReconciliationTolerance(policy, target, toleranceAmount(t, "USD", "0"), toleranceAmount(t, "USD", "0.2"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusDiscrepant || evaluation.Reason != ReconciliationReasonZeroDenominator {
			t.Fatalf("status/reason = %q/%q, want discrepant/zero_denominator", evaluation.Status, evaluation.Reason)
		}
		// Relative-only rule cannot classify a zero expected value: partial.
		relOnly := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, nil, toleranceLimit(t, "0.5")))
		evaluation, err = EvaluateReconciliationTolerance(relOnly, target, toleranceAmount(t, "USD", "0"), toleranceAmount(t, "USD", "0.2"))
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
		// Zero against zero is an exact match retaining the typed reason.
		evaluation, err = EvaluateReconciliationTolerance(policy, target, toleranceAmount(t, "USD", "0"), toleranceAmount(t, "USD", "0"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusMatched || evaluation.Reason != ReconciliationReasonZeroDenominator {
			t.Fatalf("status/reason = %q/%q, want matched/zero_denominator", evaluation.Status, evaluation.Reason)
		}
	})

	t.Run("typed incomparable and missing-policy decisions", func(t *testing.T) {
		t.Parallel()
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.1"), nil))
		// Currency/unit mismatch is incomparable, never a numeric decision.
		evaluation, err := EvaluateReconciliationTolerance(policy, target, toleranceAmount(t, "USD", "1"), toleranceAmount(t, "EUR", "1.5"))
		if err != nil {
			t.Fatalf("EvaluateReconciliationTolerance: %v", err)
		}
		if evaluation.Status != ReconciliationStatusIncomparable || evaluation.Reason != ReconciliationReasonUnitMismatch {
			t.Fatalf("status/reason = %q/%q, want incomparable/unit_mismatch", evaluation.Status, evaluation.Reason)
		}
		// No matching rule is partial with retained deltas and no threshold.
		evaluation, err = EvaluateReconciliationTolerance(policy, ReconciliationToleranceTarget{Currency: "EUR"}, toleranceAmount(t, "EUR", "1"), toleranceAmount(t, "EUR", "1.5"))
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

	t.Run("aggregate retains decisions gross and signed", func(t *testing.T) {
		t.Parallel()
		policy := toleranceTestPolicy(toleranceTestRule("usd", ReconciliationToleranceScope{Currency: "USD"}, toleranceLimit(t, "0.10"), nil))
		findings := []ReconciliationFinding{
			{
				ID: "offset-a", Scope: "phase19-r4", Currency: "USD",
				Status:   ReconciliationStatusDiscrepant,
				Expected: func() *MonetaryExactAmount { amount := toleranceAmount(t, "USD", "1.00"); return &amount }(),
				Reported: func() *MonetaryExactAmount { amount := toleranceAmount(t, "USD", "1.30"); return &amount }(),
			},
			{
				ID: "offset-b", Scope: "phase19-r4", Currency: "USD",
				Status:   ReconciliationStatusDiscrepant,
				Expected: func() *MonetaryExactAmount { amount := toleranceAmount(t, "USD", "1.00"); return &amount }(),
				Reported: func() *MonetaryExactAmount { amount := toleranceAmount(t, "USD", "0.70"); return &amount }(),
			},
		}
		aggregate, err := AggregateReconciliationFindings(policy, findings)
		if err != nil {
			t.Fatalf("AggregateReconciliationFindings: %v", err)
		}
		if aggregate.Policy.ID != "tolerance-policy" || aggregate.Policy.Version != "v1" {
			t.Fatalf("aggregate policy = %+v, want tolerance-policy/v1", aggregate.Policy)
		}
		if len(aggregate.Rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(aggregate.Rows))
		}
		row := aggregate.Rows[0]
		// Gross absolute keeps 0.30+0.30=0.60 while the signed net is 0.30-0.30=0.
		assertToleranceAmount(t, "gross", row.GrossAbsoluteDiscrepancy, "USD", "6/1")
		assertToleranceAmount(t, "net", row.NetSignedDiscrepancy, "USD", "0/0")
		if row.AffectedCount != 2 {
			t.Fatalf("affected = %d, want 2 (offsetting errors must not disappear)", row.AffectedCount)
		}
		// Every evaluated finding retains its signed/absolute deltas.
		for _, finding := range aggregate.Findings {
			if finding.Evaluation == nil || finding.Evaluation.SignedDelta == nil || finding.Evaluation.AbsoluteDelta == nil {
				t.Fatalf("finding %q lost its retained deltas: %+v", finding.ID, finding.Evaluation)
			}
			if finding.EvaluatedStatus != ReconciliationStatusDiscrepant {
				t.Fatalf("finding %q status = %q, want discrepant", finding.ID, finding.EvaluatedStatus)
			}
		}
	})
}
