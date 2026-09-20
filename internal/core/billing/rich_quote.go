package billing

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// RichComponentBound is one finite enforceable candidate/work upper bound for a
// concrete component instance. Upper must be non-negative and Enforceable must
// be true: a configuration number without an enforceable execution limit is not
// proof of a bound on uninterruptible provider work and must not be quoted.
type RichComponentBound struct {
	Key         metering.ComponentKey
	Upper       metering.Decimal
	Enforceable bool
}

// RichQuoteRoute is one candidate execution leg for a richer customer offer.
// An empty Tariff means the base tariff applies to this route. Backend and
// Model identify the route for tariff resolution and binding; ID carries the
// planned leaf key for basis attribution. Capabilities are the route's
// provable evidence capabilities (for example tool-count or duration
// capture); a route missing a required capability cannot be bounded.
type RichQuoteRoute struct {
	ID           string
	Backend      string
	Model        string
	Tariff       economics.TariffSnapshot
	Capabilities []string
}

// RichQuoteInput conservatively quotes unit, fixed, minimum, credit and
// resource charges from the same immutable tariff semantics used for
// settlement. Bounds are finite enforceable work limits and
// RequiredCapabilities are evidence capabilities every candidate route must
// prove. Effective qualifiers come only from each canonical frozen tariff
// snapshot: this path accepts no external qualifier overlay, so conditional
// rule selection provably matches terminal settlement under the same tariff.
type RichQuoteInput struct {
	Currency             string
	Policy               ChargePolicy
	BaseTariff           economics.TariffSnapshot
	Routes               []RichQuoteRoute
	Bounds               []RichComponentBound
	RequiredCapabilities []string
}

// EstimateRichCustomerCharge quotes a richer customer offer using the same
// rule evaluation, rounding and price-version binding as post-usage settlement.
// Unknown duration/tool counts (missing or non-enforceable bounds), missing
// required evidence capabilities, period/conversion uncertainty, and precision
// or overflow uncertainty all fail closed with a typed error before provider
// execution. A bare configured money ceiling is not an enforceable work bound
// and never converts these failures into admission; this path takes no
// Strict/ceiling fallback by design (the legacy scalar path keeps its own
// pre-existing ceiling behavior). It never fabricates a bound.
func EstimateRichCustomerCharge(in RichQuoteInput) (MaxCostBound, error) {
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		return MaxCostBound{}, fmt.Errorf("%w: currency is required", ErrEstimateInvalid)
	}
	if err := in.Policy.Validate(); err != nil {
		return MaxCostBound{}, err
	}
	retail, err := ResolveRetailSelectionPolicy(in.Policy)
	if err != nil {
		return MaxCostBound{}, err
	}
	baseTariff, err := in.BaseTariff.Canonical()
	if err != nil {
		return MaxCostBound{}, fmt.Errorf("%w: base tariff: %v", ErrEstimateInvalid, err)
	}
	if !strings.EqualFold(baseTariff.Currency, currency) {
		return MaxCostBound{}, fmt.Errorf("%w: base tariff currency %q want %q", ErrEstimateCurrency, baseTariff.Currency, currency)
	}
	if baseTariff.Ref.ID != in.Policy.PricingRef.ID || baseTariff.Ref.Version != in.Policy.PricingRef.Version {
		return MaxCostBound{}, fmt.Errorf("%w: base tariff %s@%s does not match policy pricing %s@%s", ErrEstimateSnapshot, baseTariff.Ref.ID, baseTariff.Ref.Version, in.Policy.PricingRef.ID, in.Policy.PricingRef.Version)
	}
	if len(in.Routes) == 0 {
		return MaxCostBound{}, fmt.Errorf("%w: no candidate routes", ErrEstimateUnbounded)
	}
	// The only effective qualifiers are the snapshot-embedded ones, already
	// covered by the canonical tariff content hash and route binding.
	effective := append([]metering.Dimension(nil), baseTariff.EffectiveQualifiers...)
	bounds, err := validatedRichBounds(in.Bounds)
	if err != nil {
		return MaxCostBound{}, err
	}
	required, err := validatedRichCapabilities(in.RequiredCapabilities)
	if err != nil {
		return MaxCostBound{}, err
	}
	baseRater, err := NewReferenceRater(baseTariff)
	if err != nil {
		return MaxCostBound{}, fmt.Errorf("%w: base tariff: %v", ErrEstimateInvalid, err)
	}
	// Fixed call/submission fees apply once per call, not per leg, matching
	// post-usage retail composition.
	fixedLines, err := richFixedLines(baseRater, baseTariff, effective, currency)
	if err != nil {
		return MaxCostBound{}, err
	}
	type routeEvaluation struct {
		id      string
		lines   []economics.LineItem
		binding RouteTariffBinding
	}
	evaluations := make([]routeEvaluation, 0, len(in.Routes))
	for idx, route := range in.Routes {
		routeTariff := baseTariff
		routeRater := baseRater
		if strings.TrimSpace(route.Tariff.Ref.ID) != "" || strings.TrimSpace(route.Tariff.Ref.Version) != "" || len(route.Tariff.Rules) != 0 {
			canonical, canonicalErr := route.Tariff.Canonical()
			if canonicalErr != nil {
				return MaxCostBound{}, fmt.Errorf("%w: route %q tariff: %v", ErrEstimateInvalid, route.ID, canonicalErr)
			}
			if !strings.EqualFold(canonical.Currency, currency) {
				return MaxCostBound{}, fmt.Errorf("%w: route %q tariff currency %q want %q", ErrEstimateCurrency, route.ID, canonical.Currency, currency)
			}
			routeRater, err = NewReferenceRater(canonical)
			if err != nil {
				return MaxCostBound{}, fmt.Errorf("%w: route %q tariff: %v", ErrEstimateInvalid, route.ID, err)
			}
			routeTariff = canonical
		}
		if routeTariff.Content.ContentHash == "" {
			return MaxCostBound{}, fmt.Errorf("%w: route %q tariff content hash is required", ErrEstimateInvalid, route.ID)
		}
		if strings.TrimSpace(route.Model) == "" {
			return MaxCostBound{}, fmt.Errorf("%w: route %q model is required for tariff binding", ErrEstimateInvalid, route.ID)
		}
		binding := RouteTariffBinding{
			RouteID:  RouteTariffKey(route.Backend, route.Model),
			TariffID: routeTariff.Ref.ID, TariffVersion: routeTariff.Ref.Version,
			ContentHash: routeTariff.Content.ContentHash,
		}
		if err := binding.Validate(); err != nil {
			return MaxCostBound{}, fmt.Errorf("%w: route %q binding: %v", ErrEstimateInvalid, route.ID, err)
		}
		if err := checkRichRouteCapabilities(route, required); err != nil {
			return MaxCostBound{}, fmt.Errorf("%w: route %q: %v", ErrEstimateUnbounded, route.ID, err)
		}
		routeEffective := append([]metering.Dimension(nil), routeTariff.EffectiveQualifiers...)
		scopeKey := fmt.Sprintf("quote:%d:%s", idx, strings.TrimSpace(route.ID))
		lines, err := richComponentLines(routeRater, routeTariff, bounds, routeEffective, currency, scopeKey)
		if err != nil {
			return MaxCostBound{}, fmt.Errorf("route %q: %w", route.ID, err)
		}
		evaluations = append(evaluations, routeEvaluation{id: route.ID, lines: lines, binding: binding})
	}
	routeBindings := make([]RouteTariffBinding, 0, len(evaluations))
	for _, evaluation := range evaluations {
		routeBindings = append(routeBindings, evaluation.binding)
	}
	sort.Slice(routeBindings, func(i, j int) bool { return routeBindings[i].RouteID < routeBindings[j].RouteID })
	deduped := routeBindings[:0]
	for _, binding := range routeBindings {
		if n := len(deduped); n > 0 && deduped[n-1].RouteID == binding.RouteID {
			if deduped[n-1] != binding {
				return MaxCostBound{}, fmt.Errorf("%w: conflicting tariffs quoted for route %q", ErrEstimateInvalid, binding.RouteID)
			}
			continue
		}
		deduped = append(deduped, binding)
	}
	routeBindings = deduped
	sumAll := in.Policy.Scope == ChargeAllPotentialLegs || retail.Mode == RetailSelectionAllAttributable
	if sumAll {
		allLines := append([]economics.LineItem(nil), fixedLines...)
		for _, evaluation := range evaluations {
			allLines = append(allLines, evaluation.lines...)
		}
		totals, err := totalsFromLines(allLines, currency)
		if err != nil {
			return MaxCostBound{}, err
		}
		amount, err := richSingleTotal(totals, currency)
		if err != nil {
			return MaxCostBound{}, err
		}
		var basis []BoundComponent
		for _, evaluation := range evaluations {
			basis = append(basis, richBasis(evaluation.id, evaluation.lines, nil, currency)...)
		}
		basis = append(basis, richBasis("", fixedLines, nil, currency)...)
		return MaxCostBound{Amount: amount, PricingRef: in.Policy.PricingRef, ChargePolicyRef: in.Policy.Ref, Basis: basis, RouteTariffs: routeBindings}, nil
	}
	var selected MaxCostBound
	for i, evaluation := range evaluations {
		combined := append(append([]economics.LineItem(nil), evaluation.lines...), fixedLines...)
		totals, err := totalsFromLines(combined, currency)
		if err != nil {
			return MaxCostBound{}, fmt.Errorf("route %q: %w", evaluation.id, err)
		}
		amount, err := richSingleTotal(totals, currency)
		if err != nil {
			return MaxCostBound{}, fmt.Errorf("route %q: %w", evaluation.id, err)
		}
		basis := richBasis(evaluation.id, evaluation.lines, fixedLines, currency)
		candidate := MaxCostBound{Amount: amount, PricingRef: in.Policy.PricingRef, ChargePolicyRef: in.Policy.Ref, Basis: basis, RouteTariffs: routeBindings}
		if i == 0 || candidate.Amount.Nano > selected.Amount.Nano {
			selected = candidate
		}
	}
	return selected, nil
}

func validatedRichBounds(bounds []RichComponentBound) ([]RichComponentBound, error) {
	out := make([]RichComponentBound, 0, len(bounds))
	for i, bound := range bounds {
		key, err := bound.Key.Normalize()
		if err != nil {
			return nil, fmt.Errorf("%w: bound %d key: %v", ErrEstimateInvalid, i, err)
		}
		upper, err := bound.Upper.Normalize()
		if err != nil {
			return nil, fmt.Errorf("%w: bound %d upper: %v", ErrEstimateInvalid, i, err)
		}
		rational, err := upper.ToRat()
		if err != nil {
			return nil, fmt.Errorf("%w: bound %d upper: %v", ErrEstimateInvalid, i, err)
		}
		if rational.Sign() < 0 {
			return nil, fmt.Errorf("%w: bound %d quantities cannot be negative", ErrEstimateInvalid, i)
		}
		if !bound.Enforceable {
			return nil, fmt.Errorf("%w: bound %d for %s is not enforceable", ErrEstimateUnbounded, i, key.CanonicalKey())
		}
		out = append(out, RichComponentBound{Key: key, Upper: upper, Enforceable: true})
	}
	return out, nil
}

func validatedRichCapabilities(capabilities []string) ([]string, error) {
	out := make([]string, 0, len(capabilities))
	for i, capability := range capabilities {
		trimmed := strings.TrimSpace(capability)
		if trimmed == "" {
			return nil, fmt.Errorf("%w: required capability %d is required", ErrEstimateInvalid, i)
		}
		out = append(out, trimmed)
	}
	return out, nil
}

func checkRichRouteCapabilities(route RichQuoteRoute, required []string) error {
	if len(required) == 0 {
		return nil
	}
	provided := make(map[string]struct{}, len(route.Capabilities))
	for _, capability := range route.Capabilities {
		trimmed := strings.TrimSpace(capability)
		if trimmed == "" {
			continue
		}
		provided[trimmed] = struct{}{}
	}
	for _, want := range required {
		if _, ok := provided[want]; !ok {
			return fmt.Errorf("missing required evidence capability %q", want)
		}
	}
	return nil
}

func richFixedLines(rater *ReferenceRater, tariff economics.TariffSnapshot, qualifiers []metering.Dimension, currency string) ([]economics.LineItem, error) {
	values := make(map[string]string, len(qualifiers))
	for _, qualifier := range qualifiers {
		values[qualifier.Name] = qualifier.Value
	}
	var lines []economics.LineItem
	for _, rule := range tariff.Rules {
		if rule.FixedAmount == nil {
			continue
		}
		switch rule.FixedScope {
		case economics.FixedFeeScopeCall, economics.FixedFeeScopeSubmission:
		case economics.FixedFeeScopePeriod:
			return nil, fmt.Errorf("%w: fixed rule %q requires period valuation", ErrEstimateUnbounded, rule.ID)
		default:
			return nil, fmt.Errorf("%w: fixed rule %q has unsupported scope %q", ErrEstimateInvalid, rule.ID, rule.FixedScope)
		}
		matched := true
		for _, condition := range rule.Conditions {
			value, ok := values[condition.Name]
			if !ok {
				return nil, fmt.Errorf("%w: fixed rule %q requires qualifier %q", ErrEstimateUnbounded, rule.ID, condition.Name)
			}
			if value != condition.Value {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if !strings.EqualFold(rule.Currency, currency) {
			return nil, fmt.Errorf("%w: fixed rule %q uses %s, want %s", ErrEstimateCurrency, rule.ID, rule.Currency, currency)
		}
		amount, err := rule.FixedAmount.ToRat()
		if err != nil {
			return nil, fmt.Errorf("%w: fixed rule %q amount: %v", ErrRatePrecision, rule.ID, err)
		}
		line := economics.LineItem{
			ID: "fixed:" + rule.ID, RuleID: rule.ID, ItemID: rule.ID,
			FixedFee: &economics.FixedFeeIdentity{ID: rule.ID, Scope: rule.FixedScope, Version: tariff.Ref.Version},
			Unit:     metering.UnitCount, RoundingScope: economics.RoundingScopeLine, RoundingPolicy: defaultRounding,
			Status: economics.RatingLineRated,
		}
		if rule.RoundingScope != "" {
			line.RoundingScope = rule.RoundingScope
		}
		if rule.RoundingPolicy != economics.RoundingUnspecified {
			line.RoundingPolicy = rule.RoundingPolicy
		}
		if err := setExactAmount(&line, amount, currency); err != nil {
			return nil, fmt.Errorf("%w: fixed rule %q: %v", ErrRatePrecision, rule.ID, err)
		}
		lines = append(lines, line)
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].ID < lines[j].ID })
	return lines, nil
}

func richComponentLines(rater *ReferenceRater, tariff economics.TariffSnapshot, bounds []RichComponentBound, qualifiers []metering.Dimension, currency, scopeKey string) ([]economics.LineItem, error) {
	contextTotals := richContextTotals(bounds)
	lines := make([]economics.LineItem, 0, len(bounds))
	for _, bound := range bounds {
		rule, err := rater.resolveRule(bound.Key, qualifiers)
		if err != nil {
			if errors.Is(err, ErrRateMissing) {
				continue
			}
			if errors.Is(err, ErrQualifierMissing) {
				return nil, fmt.Errorf("%w: component %s: %v", ErrEstimateUnbounded, bound.Key.CanonicalKey(), err)
			}
			return nil, fmt.Errorf("%w: component %s: %v", ErrEstimateInvalid, bound.Key.CanonicalKey(), err)
		}
		if !strings.EqualFold(rule.Currency, currency) {
			return nil, fmt.Errorf("%w: rule %q uses %s, want %s", ErrEstimateCurrency, rule.ID, rule.Currency, currency)
		}
		if rule.Kind == economics.RatingRuleConversion {
			return nil, fmt.Errorf("%w: conversion rule %q requires an explicit transform adapter", ErrEstimateUnbounded, rule.ID)
		}
		if rule.SelectionScope == economics.SelectionPeriod || rule.RoundingScope == economics.RoundingScopePeriod {
			return nil, fmt.Errorf("%w: rule %q requires period valuation", ErrEstimateUnbounded, rule.ID)
		}
		upper, err := bound.Upper.ToRat()
		if err != nil {
			return nil, fmt.Errorf("%w: bound %s: %v", ErrEstimateInvalid, bound.Key.CanonicalKey(), err)
		}
		contextQuantity := new(big.Rat).Set(upper)
		if rule.SelectionScope == economics.SelectionWholeContext {
			if total, ok := contextTotals[richContextKey(bound.Key)]; ok && total != nil {
				contextQuantity = new(big.Rat).Set(total)
			}
		}
		amount, err := richConservativeAmount(rule, upper, contextQuantity)
		if err != nil {
			return nil, err
		}
		line := economics.LineItem{
			ID:     "measure:" + bound.Key.CanonicalKey() + ":" + scopeDigest(scopeKey+bound.Key.CanonicalKey()),
			RuleID: rule.ID, ItemID: bound.Key.CanonicalKey(), Component: componentPtr(bound.Key),
			Quantity: decimalPtrFromRat(upper), Unit: bound.Key.Unit,
			RoundingScope: economics.RoundingScopeLine, RoundingPolicy: defaultRounding,
			Status: economics.RatingLineRated,
		}
		if rule.RoundingScope != "" {
			line.RoundingScope = rule.RoundingScope
		}
		if rule.RoundingPolicy != economics.RoundingUnspecified {
			line.RoundingPolicy = rule.RoundingPolicy
		}
		if err := setExactAmount(&line, amount, currency); err != nil {
			return nil, fmt.Errorf("%w: rule %q: %v", ErrRatePrecision, rule.ID, err)
		}
		lines = append(lines, line)
	}
	if err := richRequireBoundsForApplicableRules(rater, tariff, bounds, qualifiers); err != nil {
		return nil, err
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].ID < lines[j].ID })
	return lines, nil
}

func richContextKey(key metering.ComponentKey) string {
	return string(key.Direction) + "\x00" + key.Unit + "\x00" + key.SchemaID
}

func richContextTotals(bounds []RichComponentBound) map[string]*big.Rat {
	totals := make(map[string]*big.Rat)
	for _, bound := range bounds {
		rational, err := bound.Upper.ToRat()
		if err != nil || rational.Sign() < 0 {
			continue
		}
		key := richContextKey(bound.Key)
		total := totals[key]
		if total == nil {
			total = new(big.Rat)
			totals[key] = total
		}
		total.Add(total, rational)
	}
	return totals
}

// richConservativeAmount evaluates the same quantity rule as settlement but at
// a finite upper bound. All-units tiers use the maximum reachable tier price so
// a bound inside a cheap high-volume tier cannot understate a smaller actual
// quantity billed at a more expensive tier boundary.
func richConservativeAmount(rule economics.RatingRule, quantity, contextQuantity *big.Rat) (*big.Rat, error) {
	amount, _, err := evaluateQuantityRule(rule, quantity, contextQuantity, false)
	if err != nil {
		if errors.Is(err, ErrPeriodScopeRequired) {
			return nil, fmt.Errorf("%w: rule %q requires period valuation", ErrEstimateUnbounded, rule.ID)
		}
		if errors.Is(err, ErrRateUnsupported) && rule.Kind == economics.RatingRuleConversion {
			return nil, fmt.Errorf("%w: rule %q requires an explicit transform adapter", ErrEstimateUnbounded, rule.ID)
		}
		return nil, err
	}
	if rule.Kind != economics.RatingRuleAllUnits && (rule.TierMode != economics.TierAllUnits || len(rule.Tiers) == 0) {
		return amount, nil
	}
	if rule.SelectionScope != economics.SelectionWholeContext && rule.SelectionScope != "" && rule.SelectionScope != economics.SelectionBillableQuantity {
		return amount, nil
	}
	best := new(big.Rat).Set(amount)
	for _, tier := range rule.Tiers {
		price, err := priceFromTier(tier)
		if err != nil {
			continue
		}
		candidate := new(big.Rat).Mul(new(big.Rat).Set(quantity), price.rat)
		if err := boundedRat(candidate); err != nil {
			return nil, err
		}
		if candidate.Cmp(best) > 0 {
			best = candidate
		}
	}
	return best, nil
}

// richRequireBoundsForApplicableRules fails closed when a tariff prices a
// component that has no finite enforceable bound. Conditional rules whose
// qualifiers do not match are inapplicable and need no bound; rules needing a
// missing qualifier are unbounded rather than guessed.
func richRequireBoundsForApplicableRules(rater *ReferenceRater, tariff economics.TariffSnapshot, bounds []RichComponentBound, qualifiers []metering.Dimension) error {
	values := make(map[string]string, len(qualifiers))
	for _, qualifier := range qualifiers {
		values[qualifier.Name] = qualifier.Value
	}
	for _, rule := range tariff.Rules {
		if rule.Component == nil {
			continue
		}
		matched := true
		for _, condition := range rule.Conditions {
			value, ok := values[condition.Name]
			if !ok {
				return fmt.Errorf("%w: rule %q requires qualifier %q", ErrEstimateUnbounded, rule.ID, condition.Name)
			}
			if value != condition.Value {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		found := false
		for _, bound := range bounds {
			if ruleComponentMatches(*rule.Component, bound.Key) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: component %s has no finite enforceable bound", ErrEstimateUnbounded, rule.Component.CanonicalKey())
		}
	}
	return nil
}

func richSingleTotal(totals []economics.CurrencyTotal, currency string) (Money, error) {
	if len(totals) != 1 {
		return Money{}, fmt.Errorf("%w: expected one currency total, got %d", ErrEstimateInvalid, len(totals))
	}
	total := totals[0]
	if !strings.EqualFold(total.Currency, currency) {
		return Money{}, fmt.Errorf("%w: total currency %q want %q", ErrEstimateCurrency, total.Currency, currency)
	}
	if !total.RoundedAmount.Present {
		return Money{}, fmt.Errorf("%w: rounded total is unavailable", ErrEstimateInvalid)
	}
	return Money{Nano: total.RoundedAmount.NanoUnits, Currency: total.Currency}, nil
}

func richBasis(routeID string, componentLines, fixedLines []economics.LineItem, currency string) []BoundComponent {
	basis := make([]BoundComponent, 0, len(componentLines)+len(fixedLines))
	for _, line := range componentLines {
		if line.RoundedAmount == nil || !line.RoundedAmount.Present {
			continue
		}
		basis = append(basis, BoundComponent{RouteID: routeID, Kind: "component", Name: line.RuleID, Amount: Money{Nano: line.RoundedAmount.NanoUnits, Currency: line.RoundedAmount.Currency}})
	}
	for _, line := range fixedLines {
		if line.RoundedAmount == nil || !line.RoundedAmount.Present {
			continue
		}
		basis = append(basis, BoundComponent{RouteID: routeID, Kind: "fixed", Name: line.RuleID, Amount: Money{Nano: line.RoundedAmount.NanoUnits, Currency: line.RoundedAmount.Currency}})
	}
	sort.Slice(basis, func(i, j int) bool {
		if basis[i].RouteID != basis[j].RouteID {
			return basis[i].RouteID < basis[j].RouteID
		}
		if basis[i].Kind != basis[j].Kind {
			return basis[i].Kind < basis[j].Kind
		}
		return basis[i].Name < basis[j].Name
	})
	return basis
}
