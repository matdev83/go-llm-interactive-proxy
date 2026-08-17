package archtest

// EvaluateBillingHoldAndStreamMoneyLock verifies the committed hold-deletion
// and no-stream-money ratchets remain active and green (requirement 8.7). It
// asserts the committed baseline flags are active, the deleted stream-time
// monetary declarations are still forbidden, the hold-lifecycle target
// inventory is intact, and the committed deletion/identity ratchets pass
// against the current tree.
func EvaluateBillingHoldAndStreamMoneyLock(root string) ([]RuleFinding, error) {
	var out []RuleFinding
	doc, err := LoadBillingExposureBaseline(root)
	if err != nil {
		return nil, err
	}
	if !doc.ForbidHoldSymbols {
		out = append(out, billingCorrectnessRuleFinding(
			BillingCorrectnessRuleHoldAndStreamMoneyLock, BillingExposureBaselineRelPath,
			"forbid_hold_symbols must stay active (7.1 hold deletion lock)"))
	}
	if !doc.RequireNetLOCReduction {
		out = append(out, billingCorrectnessRuleFinding(
			BillingCorrectnessRuleHoldAndStreamMoneyLock, BillingExposureBaselineRelPath,
			"require_net_loc_reduction must stay active (7.4 convergence lock)"))
	}

	inventory := make(map[string]struct{})
	for _, decl := range ForbiddenDeclarations {
		key := decl.Package + ":" + string(decl.Kind) + ":" + decl.Name
		inventory[key] = struct{}{}
	}
	for _, required := range []string{
		"internal/core/runtime:method:enrichUsageCost",
		"internal/core/runtime:method:recordTokenAccountingLedger",
		"internal/core/runtime:method:recordPartialTokenAccountingLedger",
		"internal/core/runtime:method:recordCancellationBillingMarker",
		"internal/core/runtime:method:rateMonetaryExposure",
		"internal/core/runtime:func:rateMonetaryExposure",
		"pkg/lipsdk/economics:type:RatingRequest",
		"pkg/lipsdk/economics:type:RatingResult",
		"pkg/lipsdk/economics:type:Rater",
	} {
		if _, ok := inventory[required]; !ok {
			out = append(out, billingCorrectnessRuleFinding(
				BillingCorrectnessRuleHoldAndStreamMoneyLock, "internal/archtest/symbol_rules.go",
				"stream-time monetary declaration must stay forbidden: "+required))
		}
	}
	for _, required := range []string{
		"Authorization", "AuthorizationStore", "authorization_holds", "reserved_nano",
		"hold_expiry", "hold_remainder", "hold_release", "JournalBookLegacyAuthorization",
	} {
		if !containsString(billingExposureHoldLifecycleTargetIDs, required) {
			out = append(out, billingCorrectnessRuleFinding(
				BillingCorrectnessRuleHoldAndStreamMoneyLock, "internal/archtest/billing_exposure_ratchet.go",
				"hold-lifecycle target must stay inventoried: "+required))
		}
	}

	// The committed deletion/identity ratchets must stay green against the
	// current tree (Requirement 8.7: ratchets remain active and green).
	del, err := EvaluateBillingExposureDeletionRatchet(root, doc)
	if err != nil {
		return nil, err
	}
	out = append(out, del...)
	ident, err := EvaluateBillingExposureIdentityRatchet(root, doc)
	if err != nil {
		return nil, err
	}
	out = append(out, ident...)
	return out, nil
}
