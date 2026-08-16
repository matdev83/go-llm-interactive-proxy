package billingcompose

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var (
	ErrSnapshotImmutable      = errors.New("billingcompose: published snapshot is immutable")
	ErrSnapshotNotFound       = errors.New("billingcompose: snapshot not found")
	ErrPolicyPricingMismatch  = errors.New("billingcompose: charge policy pricing ref does not match customer pricing")
	ErrBindingImmutable       = errors.New("billingcompose: route binding is immutable")
	errNilSnapshotCatalog     = errors.New("billingcompose: nil snapshot catalog")
	errBackendModelRequired   = errors.New("billingcompose: backend and model are required")
	errCatalogDefaultsMissing = errors.New("billingcompose: catalog defaults are not set")
)

type versionKey struct {
	id      string
	version string
}
type routeKey struct {
	backend string
	model   string
}
type SnapshotCatalog struct {
	mu               sync.RWMutex
	pricing          map[versionKey]billing.PricingSnapshot
	policies         map[versionKey]billing.ChargePolicy
	operatorRates    map[versionKey]billing.OperatorRateSnapshot
	defaultPricing   versionKey
	defaultPolicy    versionKey
	hasDefaults      bool
	routePricing     map[routeKey]versionKey
	operatorBindings map[routeKey]versionKey
}

func NewSnapshotCatalog() *SnapshotCatalog {
	return &SnapshotCatalog{
		pricing:          make(map[versionKey]billing.PricingSnapshot),
		policies:         make(map[versionKey]billing.ChargePolicy),
		operatorRates:    make(map[versionKey]billing.OperatorRateSnapshot),
		routePricing:     make(map[routeKey]versionKey),
		operatorBindings: make(map[routeKey]versionKey),
	}
}

func (c *SnapshotCatalog) PutPricing(snapshot billing.PricingSnapshot) error {
	if c == nil {
		return errNilSnapshotCatalog
	}
	if err := snapshot.Validate(snapshot.Currency); err != nil {
		return fmt.Errorf("billingcompose: pricing snapshot: %w", err)
	}
	key := keyOf(snapshot.Ref)
	cloned := clonePricing(snapshot)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.pricing[key]; ok {
		if pricingReplayEqual(existing, cloned) {
			return nil
		}
		return ErrSnapshotImmutable
	}
	c.pricing[key] = cloned
	return nil
}

func (c *SnapshotCatalog) PutPolicy(snapshot billing.ChargePolicy) error {
	if c == nil {
		return errNilSnapshotCatalog
	}
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("billingcompose: charge policy: %w", err)
	}
	key := keyOf(snapshot.Ref)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.policies[key]; ok {
		if policyReplayEqual(existing, snapshot) {
			return nil
		}
		return ErrSnapshotImmutable
	}
	c.policies[key] = snapshot
	return nil
}

func (c *SnapshotCatalog) PutOperatorRate(snapshot billing.OperatorRateSnapshot) error {
	if c == nil {
		return errNilSnapshotCatalog
	}
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("billingcompose: operator rate: %w", err)
	}
	key := keyOf(snapshot.Ref)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.operatorRates[key]; ok {
		if operatorRateReplayEqual(existing, snapshot) {
			return nil
		}
		return ErrSnapshotImmutable
	}
	c.operatorRates[key] = snapshot
	return nil
}

func (c *SnapshotCatalog) SetDefaults(CustomerPricing, ChargePolicy billing.VersionRef) error {
	if c == nil {
		return errNilSnapshotCatalog
	}
	pricingKey := keyOf(CustomerPricing)
	policyKey := keyOf(ChargePolicy)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.pricing[pricingKey]; !ok {
		return ErrSnapshotNotFound
	}
	policy, ok := c.policies[policyKey]
	if !ok {
		return ErrSnapshotNotFound
	}
	if keyOf(policy.PricingRef) != pricingKey {
		return ErrPolicyPricingMismatch
	}
	c.defaultPricing = pricingKey
	c.defaultPolicy = policyKey
	c.hasDefaults = true
	return nil
}

func (c *SnapshotCatalog) HasDefaults() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hasDefaults
}

func (c *SnapshotCatalog) SetRoutePricing(backend, model string, ref billing.VersionRef) error {
	if c == nil {
		return errNilSnapshotCatalog
	}
	rk, err := parseRoute(backend, model)
	if err != nil {
		return err
	}
	key := keyOf(ref)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.pricing[key]; !ok {
		return ErrSnapshotNotFound
	}
	if existing, bound := c.routePricing[rk]; bound {
		if existing == key {
			return nil
		}
		return ErrBindingImmutable
	}
	c.routePricing[rk] = key
	return nil
}

func (c *SnapshotCatalog) SetOperatorRateBinding(backend, model string, ref billing.VersionRef) error {
	if c == nil {
		return errNilSnapshotCatalog
	}
	rk, err := parseRoute(backend, model)
	if err != nil {
		return err
	}
	key := keyOf(ref)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.operatorRates[key]; !ok {
		return ErrSnapshotNotFound
	}
	if existing, bound := c.operatorBindings[rk]; bound {
		if existing == key {
			return nil
		}
		return ErrBindingImmutable
	}
	c.operatorBindings[rk] = key
	return nil
}

func (c *SnapshotCatalog) SnapshotsFor(record billing.TurnUsageRecord) (pricing billing.PricingSnapshot, policy billing.ChargePolicy, rates []billing.OperatorRateSnapshot, modelPricing []billing.ModelCustomerPricing, err error) {
	if c == nil {
		return billing.PricingSnapshot{}, billing.ChargePolicy{}, nil, nil, errNilSnapshotCatalog
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	pricing, ok := c.pricing[keyOf(record.CustomerPricingRef)]
	if !ok {
		return billing.PricingSnapshot{}, billing.ChargePolicy{}, nil, nil, lookupMiss("customer pricing")
	}
	policy, ok = c.policies[keyOf(record.ChargePolicyRef)]
	if !ok {
		return billing.PricingSnapshot{}, billing.ChargePolicy{}, nil, nil, lookupMiss("charge policy")
	}
	seenRates := make(map[versionKey]struct{})
	for _, leg := range record.Legs {
		if emptyRef(leg.OperatorRateRef) {
			continue
		}
		rk := keyOf(leg.OperatorRateRef)
		if _, seen := seenRates[rk]; seen {
			continue
		}
		rate, found := c.operatorRates[rk]
		if !found {
			return billing.PricingSnapshot{}, billing.ChargePolicy{}, nil, nil, lookupMiss("operator rate")
		}
		seenRates[rk] = struct{}{}
		rates = append(rates, rate)
	}
	modelPricing, err = c.modelPricingForRecord(record, pricing)
	if err != nil {
		return billing.PricingSnapshot{}, billing.ChargePolicy{}, nil, nil, err
	}
	return clonePricing(pricing), policy, rates, modelPricing, nil
}

func (c *SnapshotCatalog) RoutePricing(ctx context.Context, backend, model string) (billing.PricingSnapshot, error) {
	if err := catalogCtxErr(ctx, c); err != nil {
		return billing.PricingSnapshot{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasDefaults {
		return billing.PricingSnapshot{}, errCatalogDefaultsMissing
	}
	identity, found := c.pricing[c.defaultPricing]
	if !found {
		return billing.PricingSnapshot{}, lookupMiss("customer pricing")
	}
	if key, ok := c.routePricing[routeOf(backend, model)]; ok {
		body, found := c.pricing[key]
		if !found {
			return billing.PricingSnapshot{}, lookupMiss("route pricing")
		}
		return pricingWithCatalogRef(body, identity.Ref), nil
	}
	return clonePricing(identity), nil
}

func (c *SnapshotCatalog) Policy(ctx context.Context, _ lipapi.Call) (billing.ChargePolicy, error) {
	if err := catalogCtxErr(ctx, c); err != nil {
		return billing.ChargePolicy{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasDefaults {
		return billing.ChargePolicy{}, errCatalogDefaultsMissing
	}
	policy, found := c.policies[c.defaultPolicy]
	if !found {
		return billing.ChargePolicy{}, lookupMiss("charge policy")
	}
	return policy, nil
}

func (c *SnapshotCatalog) CustomerPricingRef(_ context.Context, _ lipapi.Call) billing.VersionRef {
	if c == nil {
		return billing.VersionRef{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasDefaults {
		return billing.VersionRef{}
	}
	if body, ok := c.pricing[c.defaultPricing]; ok {
		return body.Ref
	}
	return billing.VersionRef{}
}

func (c *SnapshotCatalog) ChargePolicyRef(_ context.Context, _ lipapi.Call) billing.VersionRef {
	if c == nil {
		return billing.VersionRef{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasDefaults {
		return billing.VersionRef{}
	}
	if body, ok := c.policies[c.defaultPolicy]; ok {
		return body.Ref
	}
	return billing.VersionRef{}
}

func (c *SnapshotCatalog) OperatorRate(ref billing.VersionRef) (billing.OperatorRateSnapshot, error) {
	if c == nil {
		return billing.OperatorRateSnapshot{}, errNilSnapshotCatalog
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	rate, ok := c.operatorRates[keyOf(ref)]
	if !ok {
		return billing.OperatorRateSnapshot{}, lookupMiss("operator rate")
	}
	return rate, nil
}

func (c *SnapshotCatalog) OperatorRateRef(_ context.Context, backend, model string) billing.VersionRef {
	if c == nil {
		return billing.VersionRef{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	key, ok := c.operatorBindings[routeOf(backend, model)]
	if !ok {
		return billing.VersionRef{}
	}
	if body, found := c.operatorRates[key]; found {
		return body.Ref
	}
	return billing.VersionRef{}
}

func (c *SnapshotCatalog) modelPricingForRecord(record billing.TurnUsageRecord, customer billing.PricingSnapshot) ([]billing.ModelCustomerPricing, error) {
	anyOverride := false
	for _, leg := range record.Legs {
		if _, found := c.routePricing[routeOf(leg.BackendID, leg.ModelID)]; found {
			anyOverride = true
			break
		}
	}
	if !anyOverride {
		return nil, nil
	}
	var cards []billing.ModelCustomerPricing
	seenRoutes := make(map[routeKey]struct{})
	for _, leg := range record.Legs {
		rk := routeOf(leg.BackendID, leg.ModelID)
		if _, seen := seenRoutes[rk]; seen {
			continue
		}
		seenRoutes[rk] = struct{}{}
		body := customer
		if overrideKey, found := c.routePricing[rk]; found {
			overrideBody, found := c.pricing[overrideKey]
			if !found {
				return nil, lookupMiss("route pricing")
			}
			body = overrideBody
		}
		cards = append(cards, billing.ModelCustomerPricing{
			BackendID: rk.backend,
			ModelID:   rk.model,
			Pricing:   pricingWithCatalogRef(body, customer.Ref),
		})
	}
	return cards, nil
}

func catalogCtxErr(ctx context.Context, c *SnapshotCatalog) error {
	if c == nil {
		return errNilSnapshotCatalog
	}
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

func pricingWithCatalogRef(body billing.PricingSnapshot, ref billing.VersionRef) billing.PricingSnapshot {
	out := clonePricing(body)
	out.Ref = ref
	return out
}

func lookupMiss(kind string) error {
	return fmt.Errorf("%w: %w: %s", billing.ErrRatingSnapshotMismatch, ErrSnapshotNotFound, kind)
}

func keyOf(ref billing.VersionRef) versionKey {
	return versionKey{id: strings.TrimSpace(ref.ID), version: strings.TrimSpace(ref.Version)}
}

func routeOf(backend, model string) routeKey {
	return routeKey{backend: strings.TrimSpace(backend), model: strings.TrimSpace(model)}
}

func parseRoute(backend, model string) (routeKey, error) {
	rk := routeOf(backend, model)
	if rk.backend == "" || rk.model == "" {
		return routeKey{}, errBackendModelRequired
	}
	return rk, nil
}

func emptyRef(ref billing.VersionRef) bool {
	return strings.TrimSpace(ref.ID) == "" && strings.TrimSpace(ref.Version) == ""
}

func clonePricing(p billing.PricingSnapshot) billing.PricingSnapshot {
	out := p
	if p.FixedCharges != nil {
		out.FixedCharges = slices.Clone(p.FixedCharges)
	}
	if p.ResourceCharges != nil {
		out.ResourceCharges = slices.Clone(p.ResourceCharges)
	}
	return out
}

func pricingReplayEqual(a, b billing.PricingSnapshot) bool {
	if keyOf(a.Ref) != keyOf(b.Ref) {
		return false
	}
	if a.Currency != b.Currency ||
		a.InputPerMillionNano != b.InputPerMillionNano ||
		a.OutputPerMillionNano != b.OutputPerMillionNano ||
		a.InputRatePresent != b.InputRatePresent ||
		a.OutputRatePresent != b.OutputRatePresent {
		return false
	}
	return slices.Equal(a.FixedCharges, b.FixedCharges) &&
		slices.Equal(a.ResourceCharges, b.ResourceCharges)
}

func policyReplayEqual(a, b billing.ChargePolicy) bool {
	return keyOf(a.Ref) == keyOf(b.Ref) &&
		keyOf(a.PricingRef) == keyOf(b.PricingRef) &&
		a.Scope == b.Scope &&
		a.IncludeInputTokens == b.IncludeInputTokens &&
		a.IncludeOutputTokens == b.IncludeOutputTokens &&
		a.IncludeFixedCharges == b.IncludeFixedCharges &&
		a.IncludeResourceCharges == b.IncludeResourceCharges
}

func operatorRateReplayEqual(a, b billing.OperatorRateSnapshot) bool {
	if keyOf(a.Ref) != keyOf(b.Ref) || a.Currency != b.Currency {
		return false
	}
	return a.InputPerMillionNano == b.InputPerMillionNano &&
		a.OutputPerMillionNano == b.OutputPerMillionNano &&
		a.CacheReadPerMillionNano == b.CacheReadPerMillionNano &&
		a.CacheWritePerMillionNano == b.CacheWritePerMillionNano &&
		a.ReasoningPerMillionNano == b.ReasoningPerMillionNano &&
		a.InputRatePresent == b.InputRatePresent &&
		a.OutputRatePresent == b.OutputRatePresent &&
		a.CacheReadRatePresent == b.CacheReadRatePresent &&
		a.CacheWriteRatePresent == b.CacheWriteRatePresent &&
		a.ReasoningRatePresent == b.ReasoningRatePresent
}
