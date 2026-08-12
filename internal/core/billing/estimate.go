package billing

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrEstimateInvalid   = errors.New("billing: invalid max-charge estimate input")
	ErrEstimateUnbounded = errors.New("billing: charge exposure is unknown or unbounded")
	ErrEstimateCurrency  = errors.New("billing: max-charge currency mismatch")
	ErrEstimateOverflow  = errors.New("billing: max-charge arithmetic overflow")
	ErrEstimateSnapshot  = errors.New("billing: max-charge snapshot mismatch")
)

// ChargePolicyScope controls which route legs may contribute to the customer
// hold. Failover alternatives are normally one logical surfaced turn; a
// parallel/pass-through policy may explicitly charge every supplied leg.
type ChargePolicyScope string

const (
	ChargeSurfacedTurn     ChargePolicyScope = "surfaced_turn"
	ChargeAllPotentialLegs ChargePolicyScope = "all_potential_legs"
)

// ChargePolicy is an immutable customer charging-policy snapshot. It contains
// no provider or wire types and is reused by post-turn rating in later phases.
type ChargePolicy struct {
	Ref                    VersionRef
	PricingRef             VersionRef
	Scope                  ChargePolicyScope
	IncludeInputTokens     bool
	IncludeOutputTokens    bool
	IncludeFixedCharges    bool
	IncludeResourceCharges bool
}

// PricingSnapshot is the customer-facing immutable price snapshot. Rates are
// non-negative nano-units per one million tokens. A zero rate is valid and is
// distinct from an unavailable rate through the corresponding Present flag.
type PricingSnapshot struct {
	Ref                  VersionRef
	Currency             string
	InputPerMillionNano  int64
	OutputPerMillionNano int64
	InputRatePresent     bool
	OutputRatePresent    bool
	FixedCharges         []ChargeComponent
	ResourceCharges      []ChargeComponent
}

// ChargeComponent is a fixed/resource amount already normalized by the
// side-effect-free preflight path. The estimator only adds it; it never calls a
// provider to discover a price or quantity.
type ChargeComponent struct {
	Name   string
	Amount Money
}

// ChargeRoute is one planned route/model alternative. ModelMaxOutputTokens is
// required for a finite bound; ClientMaxOutputTokens, when present and valid,
// narrows that bound when it is lower than the model maximum.
type ChargeRoute struct {
	ID                          string
	Pricing                     PricingSnapshot
	ModelMaxOutputTokens        int64
	ModelMaxOutputTokensPresent bool
	ClientMaxOutputTokens       *int64
	FixedCharges                []ChargeComponent
	ResourceCharges             []ChargeComponent
}

// MaxChargeInput contains deterministic preflight quantities and all candidate
// route pricing needed to calculate one conservative customer hold.
type MaxChargeInput struct {
	Currency           string
	InputTokens        int64
	InputTokensPresent bool
	Policy             ChargePolicy
	Routes             []ChargeRoute
	// ConservativeCeiling is an optional explicit finite safety ceiling for
	// unknown input/output/rate exposure. It is only used when Strict is true.
	ConservativeCeiling *Money
	Strict              bool
}

// BoundComponent explains the immutable components included in a maximum.
type BoundComponent struct {
	RouteID string
	Kind    string
	Name    string
	Amount  Money
}

// MaxCostBound is the exact, snapshot-bound pessimistic customer exposure.
type MaxCostBound struct {
	Amount          Money
	PricingRef      VersionRef
	ChargePolicyRef VersionRef
	Basis           []BoundComponent
}

// MaxCustomerCharge is the public terminology alias for MaxCostBound.
type MaxCustomerCharge = MaxCostBound

func (p ChargePolicy) Validate() error {
	if strings.TrimSpace(p.Ref.ID) == "" || strings.TrimSpace(p.Ref.Version) == "" {
		return fmt.Errorf("%w: policy snapshot reference is required", ErrEstimateInvalid)
	}
	if strings.TrimSpace(p.PricingRef.ID) == "" || strings.TrimSpace(p.PricingRef.Version) == "" {
		return fmt.Errorf("%w: pricing snapshot reference is required", ErrEstimateInvalid)
	}
	if p.Scope != ChargeSurfacedTurn && p.Scope != ChargeAllPotentialLegs {
		return fmt.Errorf("%w: unsupported charging scope %q", ErrEstimateInvalid, p.Scope)
	}
	if !p.IncludeInputTokens && !p.IncludeOutputTokens && !p.IncludeFixedCharges && !p.IncludeResourceCharges {
		return fmt.Errorf("%w: policy has no chargeable dimensions", ErrEstimateInvalid)
	}
	return nil
}

func (p PricingSnapshot) Validate(currency string) error {
	if strings.TrimSpace(p.Ref.ID) == "" || strings.TrimSpace(p.Ref.Version) == "" {
		return fmt.Errorf("%w: pricing snapshot reference is required", ErrEstimateInvalid)
	}
	if p.Ref != (VersionRef{}) && (p.Ref.ID == "" || p.Ref.Version == "") {
		return fmt.Errorf("%w: malformed pricing snapshot reference", ErrEstimateInvalid)
	}
	if strings.TrimSpace(p.Currency) == "" || p.Currency != currency {
		return fmt.Errorf("%w: pricing currency %q want %q", ErrEstimateCurrency, p.Currency, currency)
	}
	if p.InputRatePresent && p.InputPerMillionNano < 0 || p.OutputRatePresent && p.OutputPerMillionNano < 0 {
		return fmt.Errorf("%w: negative rate", ErrEstimateInvalid)
	}
	return nil
}

// EstimateMaxCustomerCharge performs no I/O, provider work, or account
// mutation. Unknown/unbounded exposure fails closed in strict mode unless the
// caller supplied a finite conservative ceiling.
func EstimateMaxCustomerCharge(in MaxChargeInput) (MaxCostBound, error) {
	if err := in.Policy.Validate(); err != nil {
		return MaxCostBound{}, err
	}
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		return MaxCostBound{}, fmt.Errorf("%w: currency is required", ErrEstimateInvalid)
	}
	if in.InputTokens < 0 {
		return MaxCostBound{}, fmt.Errorf("%w: quantities cannot be negative", ErrEstimateInvalid)
	}
	if in.Policy.IncludeInputTokens && !in.InputTokensPresent {
		return ceilingOrError(in, ErrEstimateUnbounded)
	}
	if len(in.Routes) == 0 {
		return ceilingOrError(in, ErrEstimateUnbounded)
	}
	var pricingRef VersionRef
	for i, route := range in.Routes {
		if err := route.Pricing.Validate(currency); err != nil {
			return MaxCostBound{}, fmt.Errorf("%w: route %d: %v", ErrEstimateSnapshot, i, err)
		}
		if route.Pricing.Ref != in.Policy.PricingRef {
			return MaxCostBound{}, fmt.Errorf("%w: route %q pricing %v does not match policy %v", ErrEstimateSnapshot, route.ID, route.Pricing.Ref, in.Policy.PricingRef)
		}
		if i == 0 {
			pricingRef = route.Pricing.Ref
		}
		if route.ModelMaxOutputTokens < 0 {
			return MaxCostBound{}, fmt.Errorf("%w: route %q has negative model output bound", ErrEstimateInvalid, route.ID)
		}
		if in.Policy.IncludeOutputTokens && !route.ModelMaxOutputTokensPresent {
			return ceilingOrError(in, fmt.Errorf("%w: route %q model output bound is unavailable", ErrEstimateUnbounded, route.ID))
		}
		if route.ClientMaxOutputTokens != nil && *route.ClientMaxOutputTokens < 0 {
			return MaxCostBound{}, fmt.Errorf("%w: route %q has negative client output bound", ErrEstimateInvalid, route.ID)
		}
	}
	if pricingRef != in.Policy.PricingRef {
		return MaxCostBound{}, ErrEstimateSnapshot
	}

	bounds := make([]MaxCostBound, 0, len(in.Routes))
	for _, route := range in.Routes {
		bound, err := estimateRoute(in, route, currency)
		if err != nil {
			return ceilingOrError(in, err)
		}
		bounds = append(bounds, bound)
	}

	selected := bounds[0]
	if in.Policy.Scope == ChargeAllPotentialLegs {
		var total int64
		basis := make([]BoundComponent, 0)
		for _, b := range bounds {
			var err error
			total, err = addNonNegative(total, b.Amount.Nano)
			if err != nil {
				return ceilingOrError(in, ErrEstimateOverflow)
			}
			basis = append(basis, b.Basis...)
		}
		selected = MaxCostBound{Amount: Money{Nano: total, Currency: currency}, PricingRef: in.Policy.PricingRef, ChargePolicyRef: in.Policy.Ref, Basis: basis}
	} else {
		for _, b := range bounds[1:] {
			if b.Amount.Nano > selected.Amount.Nano {
				selected = b
			}
		}
		selected.PricingRef = in.Policy.PricingRef
		selected.ChargePolicyRef = in.Policy.Ref
	}
	return selected, nil
}

func estimateRoute(in MaxChargeInput, route ChargeRoute, currency string) (MaxCostBound, error) {
	var total int64
	basis := make([]BoundComponent, 0, 4+len(route.FixedCharges)+len(route.ResourceCharges))
	add := func(kind, name string, value int64) error {
		if value < 0 {
			return ErrEstimateInvalid
		}
		var err error
		total, err = addNonNegative(total, value)
		if err != nil {
			return err
		}
		if value > 0 {
			basis = append(basis, BoundComponent{RouteID: route.ID, Kind: kind, Name: name, Amount: Money{Nano: value, Currency: currency}})
		}
		return nil
	}
	if in.Policy.IncludeInputTokens {
		if !route.Pricing.InputRatePresent {
			return MaxCostBound{}, ErrEstimateUnbounded
		}
		value, err := tokensAtRate(in.InputTokens, route.Pricing.InputPerMillionNano)
		if err != nil {
			return MaxCostBound{}, err
		}
		if err := add("input_tokens", "input", value); err != nil {
			return MaxCostBound{}, err
		}
	}
	if in.Policy.IncludeOutputTokens {
		if !route.Pricing.OutputRatePresent {
			return MaxCostBound{}, ErrEstimateUnbounded
		}
		maxOutput := route.ModelMaxOutputTokens
		if route.ClientMaxOutputTokens != nil && *route.ClientMaxOutputTokens < maxOutput {
			maxOutput = *route.ClientMaxOutputTokens
		}
		value, err := tokensAtRate(maxOutput, route.Pricing.OutputPerMillionNano)
		if err != nil {
			return MaxCostBound{}, err
		}
		if err := add("output_tokens", "output_max", value); err != nil {
			return MaxCostBound{}, err
		}
	}
	if in.Policy.IncludeFixedCharges {
		// PricingSnapshot charges are the rating authority; route-level charges are
		// additional conservative preflight components. Include both so admission
		// never under-reserves relative to post-turn rating.
		for _, component := range route.Pricing.FixedCharges {
			value, err := componentAmount(component, currency)
			if err != nil {
				return MaxCostBound{}, err
			}
			if err := add("fixed", component.Name, value); err != nil {
				return MaxCostBound{}, err
			}
		}
		for _, component := range route.FixedCharges {
			value, err := componentAmount(component, currency)
			if err != nil {
				return MaxCostBound{}, err
			}
			if err := add("fixed", component.Name, value); err != nil {
				return MaxCostBound{}, err
			}
		}
	}
	if in.Policy.IncludeResourceCharges {
		for _, component := range route.Pricing.ResourceCharges {
			value, err := componentAmount(component, currency)
			if err != nil {
				return MaxCostBound{}, err
			}
			if err := add("resource", component.Name, value); err != nil {
				return MaxCostBound{}, err
			}
		}
		for _, component := range route.ResourceCharges {
			value, err := componentAmount(component, currency)
			if err != nil {
				return MaxCostBound{}, err
			}
			if err := add("resource", component.Name, value); err != nil {
				return MaxCostBound{}, err
			}
		}
	}
	return MaxCostBound{Amount: Money{Nano: total, Currency: currency}, PricingRef: in.Policy.PricingRef, ChargePolicyRef: in.Policy.Ref, Basis: basis}, nil
}

func componentAmount(component ChargeComponent, currency string) (int64, error) {
	if err := component.Amount.Validate(); err != nil {
		return 0, err
	}
	if component.Amount.Currency != currency {
		return 0, ErrEstimateCurrency
	}
	if component.Amount.Nano < 0 {
		return 0, ErrEstimateInvalid
	}
	return component.Amount.Nano, nil
}

func ceilingOrError(in MaxChargeInput, err error) (MaxCostBound, error) {
	if in.Strict && in.ConservativeCeiling != nil {
		ceiling := *in.ConservativeCeiling
		if ceiling.Nano < 0 || strings.TrimSpace(ceiling.Currency) == "" {
			return MaxCostBound{}, fmt.Errorf("%w: invalid conservative ceiling", ErrEstimateInvalid)
		}
		if in.Currency != "" && ceiling.Currency != in.Currency {
			return MaxCostBound{}, ErrEstimateCurrency
		}
		return MaxCostBound{Amount: ceiling, PricingRef: in.Policy.PricingRef, ChargePolicyRef: in.Policy.Ref, Basis: []BoundComponent{{Kind: "ceiling", Name: "conservative_per_call_ceiling", Amount: ceiling}}}, nil
	}
	return MaxCostBound{}, err
}

// tokensAtRate is the admission/hold arithmetic: floor(tokens*rate/1e6), then
// +1 nano when a fractional nano remains so holds never under-cover exact rating.
func tokensAtRate(tokens, rate int64) (int64, error) {
	amount, err := tokensTimesRatePerMillion(tokens, rate)
	if err != nil {
		return 0, err
	}
	if tokens == 0 || rate == 0 {
		return amount, nil
	}
	r := uint64(tokens) % 1_000_000
	rateR := uint64(rate) % 1_000_000
	if (r*rateR)%1_000_000 != 0 {
		return addNonNegative(amount, 1)
	}
	return amount, nil
}

// exactTokensAtRate is post-turn rating arithmetic: floor(tokens*rate/1e6).
// It must not invent nanos; admission uses tokensAtRate for a conservative ceiling.
func exactTokensAtRate(tokens, rate int64) (int64, error) {
	return tokensTimesRatePerMillion(tokens, rate)
}

func tokensTimesRatePerMillion(tokens, rate int64) (int64, error) {
	if tokens < 0 || rate < 0 {
		return 0, ErrEstimateInvalid
	}
	if tokens == 0 || rate == 0 {
		return 0, nil
	}
	q := uint64(tokens) / 1_000_000
	r := uint64(tokens) % 1_000_000
	if q > math.MaxInt64/uint64(rate) {
		return 0, ErrEstimateOverflow
	}
	total := q * uint64(rate)
	// Decompose the remainder product so r*rate cannot overflow uint64:
	// r*(rateQ*1e6+rateR)/1e6 = r*rateQ + r*rateR/1e6.
	rateQ, rateR := uint64(rate)/1_000_000, uint64(rate)%1_000_000
	part := r * rateQ
	rem := r * rateR
	part += rem / 1_000_000
	if total > math.MaxInt64-part {
		return 0, ErrEstimateOverflow
	}
	return int64(total + part), nil
}

func addNonNegative(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, ErrEstimateOverflow
	}
	return a + b, nil
}
