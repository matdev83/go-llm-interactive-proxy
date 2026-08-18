package archtest

// EvaluateBillingCustomerOperatorIndependence rejects any customer-rating path
// that resolves or carries operator rates. Customer snapshot resolution, the
// customer join resolver, the customer rating inputs, and the customer
// post-usage worker must stay free of provider-cost data; only the provider
// join resolver may read operator rates.
func EvaluateBillingCustomerOperatorIndependence(root string) ([]RuleFinding, error) {
	var out []RuleFinding
	out = append(out, scanFuncBodyForbiddenIdents(
		root, "internal/infra/billingcompose/catalog.go", "CustomerRatingSnapshots",
		billingCorrectnessOperatorRateIdents,
		BillingCorrectnessRuleCustomerOperatorCoupling,
		"customer snapshot resolution must never look up or carry operator rates")...)
	out = append(out, scanFuncBodyForbiddenIdents(
		root, "internal/infra/billingcompose/resolver.go", "ResolveCallRating",
		billingCorrectnessOperatorRateIdents,
		BillingCorrectnessRuleCustomerOperatorCoupling,
		"customer join resolver must never resolve operator rates")...)
	out = append(out, scanFileForbiddenIdents(
		root, "internal/core/billing/call_post_usage_worker.go",
		billingCorrectnessOperatorRateIdents,
		BillingCorrectnessRuleCustomerOperatorCoupling,
		"customer post-usage worker must never depend on provider-cost resolution")...)
	out = append(out, scanStructFieldNamesForbidden(
		root, "internal/core/billing/call_rating.go", "CallRatingInput",
		"Operator", BillingCorrectnessRuleCustomerInputCarriesOperatorRates,
		"customer rating input must not carry operator-rate collections")...)
	out = append(out, requireProviderPathResolvesOperatorRate(root)...)
	return out, nil
}

// requireProviderPathResolvesOperatorRate locks the provider join resolver as
// the sole customer-independent consumer of operator rates.
func requireProviderPathResolvesOperatorRate(root string) []RuleFinding {
	rel := "internal/infra/billingcompose/resolver.go"
	f, err := parseProductionFile(root, rel)
	if err != nil {
		return []RuleFinding{billingCorrectnessRuleFinding(
			BillingCorrectnessRuleCustomerOperatorCoupling, rel,
			"provider cost resolver target failed to parse: "+err.Error())}
	}
	if f == nil {
		return []RuleFinding{billingCorrectnessRuleFinding(
			BillingCorrectnessRuleCustomerOperatorCoupling, rel,
			"provider cost resolver target is missing")}
	}
	fd := findFuncDecl(f, "ResolveProviderCost")
	if fd == nil {
		return []RuleFinding{billingCorrectnessRuleFinding(
			BillingCorrectnessRuleCustomerOperatorCoupling, rel,
			"provider cost resolver ResolveProviderCost is missing")}
	}
	names := collectIdentNames(fd.Body)
	if _, ok := names["OperatorRate"]; !ok {
		return []RuleFinding{billingCorrectnessRuleFinding(
			BillingCorrectnessRuleCustomerOperatorCoupling, rel,
			"provider cost resolution (the provider-only path) must read catalog.OperatorRate")}
	}
	return nil
}
