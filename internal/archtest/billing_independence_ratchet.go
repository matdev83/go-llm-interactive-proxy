package archtest

// EvaluateBillingCustomerOperatorIndependence rejects any customer-rating path
// that resolves or carries operator rates. Customer snapshot resolution, the
// customer join resolver, the customer rating inputs, and the customer
// post-usage worker must stay free of provider-cost data. Task 18.1 retired
// the scalar token-to-money fallback, so the provider join resolver must not
// read operator rates either; estimates belong to the V2 provider-quantity
// valuation and the operator-rate catalog remains for historical references.
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
	out = append(out, forbidProviderPathOperatorRate(root)...)
	return out, nil
}

// forbidProviderPathOperatorRate locks the Task 18.1 retirement: the provider
// join resolver must not read operator rates for live estimation. Only
// provider-reported authoritative V1 money resolves there; token-only evidence
// stays unreconciled and V2 provider-quantity valuation owns estimates.
func forbidProviderPathOperatorRate(root string) []RuleFinding {
	return scanFuncBodyForbiddenIdents(
		root, "internal/infra/billingcompose/resolver.go", "ResolveProviderCost",
		billingCorrectnessOperatorRateIdents,
		BillingCorrectnessRuleCustomerOperatorCoupling,
		"provider cost resolution must not read operator rates: scalar fallback is retired")
}
