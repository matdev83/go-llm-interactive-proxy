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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
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
	mu                sync.RWMutex
	pricing           map[versionKey]billing.PricingSnapshot
	tariffs           map[versionKey]economics.TariffSnapshot
	policies          map[versionKey]billing.ChargePolicy
	operatorRates     map[versionKey]billing.OperatorRateSnapshot
	selectionPolicies map[versionKey]billing.OperatorCostSelectionPolicy
	defaultPricing    versionKey
	defaultPolicy     versionKey
	hasDefaults       bool
	routePricing      map[routeKey]versionKey
	operatorBindings  map[routeKey]versionKey
}

var _ economics.RatingSnapshotSource = (*SnapshotCatalog)(nil)

func NewSnapshotCatalog() *SnapshotCatalog {
	return &SnapshotCatalog{
		pricing:           make(map[versionKey]billing.PricingSnapshot),
		tariffs:           make(map[versionKey]economics.TariffSnapshot),
		policies:          make(map[versionKey]billing.ChargePolicy),
		operatorRates:     make(map[versionKey]billing.OperatorRateSnapshot),
		selectionPolicies: make(map[versionKey]billing.OperatorCostSelectionPolicy),
		routePricing:      make(map[routeKey]versionKey),
		operatorBindings:  make(map[routeKey]versionKey),
	}
}

func (c *SnapshotCatalog) PutPricing(snapshot billing.PricingSnapshot) error {
	return c.putPricingTariff(snapshot, nil)
}

// PutPricingWithSchemas atomically publishes a scalar pricing card and the
// matching opt-in schema-bearing tariff, so a frozen default or route tariff
// is reachable through the public catalog. Schemas are caller-supplied and
// never inferred; empty schemas is exactly PutPricing.
func (c *SnapshotCatalog) PutPricingWithSchemas(snapshot billing.PricingSnapshot, schemas []metering.ComponentSchema) error {
	return c.putPricingTariff(snapshot, schemas)
}

func (c *SnapshotCatalog) putPricingTariff(snapshot billing.PricingSnapshot, schemas []metering.ComponentSchema) error {
	if c == nil {
		return errNilSnapshotCatalog
	}
	if err := snapshot.Validate(snapshot.Currency); err != nil {
		return fmt.Errorf("billingcompose: pricing snapshot: %w", err)
	}
	tariff, err := billing.PricingSnapshotToTariff(snapshot)
	if err != nil {
		return fmt.Errorf("billingcompose: legacy tariff snapshot: %w", err)
	}
	if len(schemas) > 0 {
		tariff.Schemas = schemas
		tariff.Content = economics.SnapshotContentRef{}
		if tariff, err = tariff.Canonical(); err != nil {
			return fmt.Errorf("billingcompose: schema tariff snapshot: %w", err)
		}
		if _, err = billing.NewReferenceRater(tariff); err != nil {
			return fmt.Errorf("billingcompose: tariff rules: %w", err)
		}
	}
	key := keyOf(snapshot.Ref)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existingTariff, tariffOK := c.tariffs[key]; tariffOK &&
		(existingTariff.ContentHash() != tariff.ContentHash() || existingTariff.Ref.RaterID != tariff.Ref.RaterID) {
		return ErrSnapshotImmutable
	}
	if existing, ok := c.pricing[key]; ok {
		if pricingReplayEqual(existing, snapshot) {
			if existingTariff, tariffOK := c.tariffs[key]; tariffOK && existingTariff.ContentHash() != tariff.ContentHash() {
				return ErrSnapshotImmutable
			}
			return nil
		}
		return ErrSnapshotImmutable
	}
	c.pricing[key] = clonePricing(snapshot)
	c.tariffs[key] = tariff
	return nil
}

// PutTariff publishes a generic immutable tariff alongside the legacy scalar
// catalog. The identity is the VersionRef portion of the rating snapshot;
// content and rater identity remain part of the immutable body.
func (c *SnapshotCatalog) PutTariff(snapshot economics.TariffSnapshot) error {
	if c == nil {
		return errNilSnapshotCatalog
	}
	canonical, err := snapshot.Canonical()
	if err != nil {
		return fmt.Errorf("billingcompose: tariff snapshot: %w", err)
	}
	// Publish only a rule set the billing reference evaluator can select
	// deterministically. The catalog remains an input adapter; validation is
	// delegated to the billing domain rather than duplicated in infrastructure.
	if _, err := billing.NewReferenceRater(canonical); err != nil {
		return fmt.Errorf("billingcompose: tariff rules: %w", err)
	}
	key := keyOfEconomicsRatingRef(canonical.Ref)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.tariffs[key]; ok {
		if existing.ContentHash() == canonical.ContentHash() && existing.Ref.RaterID == canonical.Ref.RaterID {
			return nil
		}
		return ErrSnapshotImmutable
	}
	c.tariffs[key] = canonical.Clone()
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
	c.policies[key] = snapshot.Clone()
	return nil
}

// PutOperatorRate publishes a historical V1 operator-rate body (Migration
// Strategy step 8, Task 18.2). Live scalar fallback is retired: no production
// money path reads these bodies for estimates. Retained for historical replay
// and OperatorRateRef lineage. Operator migration: do not publish new rates
// for live rating; V2 tariffs own estimates.
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

// SetOperatorRateBinding binds a historical V1 operator-rate ref for
// OperatorRateRef lineage stamping (Task 18.2). Live money never resolves
// rate bodies through this binding; V2 tariffs own estimates. Retained so
// sealed B-legs keep explicit historical refs.
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

// CustomerRatingSnapshots is the complete immutable input set customer rating
// resolves for one call. It deliberately carries no OperatorRateSnapshot
// values: customer settlement must never depend on provider-cost readiness.
type CustomerRatingSnapshots struct {
	DefaultPricing billing.PricingSnapshot
	Policy         billing.ChargePolicy
	ModelPricing   []billing.ModelCustomerPricing
	// DefaultTariff and ModelTariffs are the frozen component-rating material
	// corresponding to the scalar cards above. They are additive so existing
	// scalar callers continue to resolve exactly as before.
	DefaultTariff economics.TariffSnapshot
	ModelTariffs  []billing.ModelCustomerTariff
}

// CustomerRatingSnapshots resolves customer pricing/policy/model cards only.
// It never looks up, validates, or loads operator-rate snapshots, so missing,
// invalid, stale, or unreconciled provider-cost data cannot block customer
// settlement or leave operational exposure open (requirements 5.1-5.6).
//
// The model cards mirror what admission quotes: a route/model override binds a
// versioned immutable pricing body; a route without an override keeps the
// configured default card. When any override exists for the call and an
// override body is missing this fails closed rather than substituting an
// unrelated model price.
func (c *SnapshotCatalog) CustomerRatingSnapshots(call billing.CallUsageRecord, legs []billing.CallLegUsageRecord) (CustomerRatingSnapshots, error) {
	if c == nil {
		return CustomerRatingSnapshots{}, errNilSnapshotCatalog
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	pricing, ok := c.pricing[keyOf(call.CustomerPricingRef)]
	if !ok {
		return CustomerRatingSnapshots{}, lookupMiss("customer pricing")
	}
	policy, ok := c.policies[keyOf(call.ChargePolicyRef)]
	if !ok {
		return CustomerRatingSnapshots{}, lookupMiss("charge policy")
	}
	modelPricing, err := c.modelPricingForLegs(legs, pricing)
	if err != nil {
		return CustomerRatingSnapshots{}, err
	}
	tariff, ok := c.tariffs[keyOf(call.CustomerPricingRef)]
	if !ok {
		return CustomerRatingSnapshots{}, lookupMiss("customer tariff")
	}
	modelTariffs, err := c.modelTariffsForLegs(legs, pricing)
	if err != nil {
		return CustomerRatingSnapshots{}, err
	}
	return CustomerRatingSnapshots{
		DefaultPricing: clonePricing(pricing),
		Policy:         policy.Clone(),
		ModelPricing:   modelPricing,
		DefaultTariff:  tariff.Clone(),
		ModelTariffs:   modelTariffs,
	}, nil
}

// Tariff resolves immutable generic material by its stable ID/version. The
// returned body is a deep copy; callers cannot mutate replay behavior.
func (c *SnapshotCatalog) Tariff(ref billing.VersionRef) (economics.TariffSnapshot, error) {
	if c == nil {
		return economics.TariffSnapshot{}, errNilSnapshotCatalog
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	tariff, ok := c.tariffs[keyOf(ref)]
	if !ok {
		return economics.TariffSnapshot{}, lookupMiss("tariff")
	}
	return tariff.Clone(), nil
}

// ResolveTariff is the context-aware catalog port used by post-usage workers.
// Context cancellation is checked before resolving immutable local material.
func (c *SnapshotCatalog) ResolveTariff(ctx context.Context, ref billing.VersionRef) (economics.TariffSnapshot, error) {
	if err := catalogCtxErr(ctx, c); err != nil {
		return economics.TariffSnapshot{}, err
	}
	return c.Tariff(ref)
}

// RouteTariff resolves the generic tariff bound to a route, or the configured
// default when no route override exists.
func (c *SnapshotCatalog) RouteTariff(ctx context.Context, backend, model string) (economics.TariffSnapshot, error) {
	if err := catalogCtxErr(ctx, c); err != nil {
		return economics.TariffSnapshot{}, err
	}
	rk, err := parseRoute(backend, model)
	if err != nil {
		return economics.TariffSnapshot{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasDefaults {
		return economics.TariffSnapshot{}, errCatalogDefaultsMissing
	}
	key := c.defaultPricing
	if override, found := c.routePricing[rk]; found {
		key = override
	}
	tariff, found := c.tariffs[key]
	if !found {
		return economics.TariffSnapshot{}, lookupMiss("route tariff")
	}
	return tariff.Clone(), nil
}

// DefaultTariff resolves the configured default component tariff. It is used
// for direct provider-reported P valuations, whose amount does not depend on
// local tariff arithmetic but still needs a stable evaluator instance.
func (c *SnapshotCatalog) DefaultTariff(ctx context.Context) (economics.TariffSnapshot, error) {
	if err := catalogCtxErr(ctx, c); err != nil {
		return economics.TariffSnapshot{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasDefaults {
		return economics.TariffSnapshot{}, errCatalogDefaultsMissing
	}
	tariff, ok := c.tariffs[c.defaultPricing]
	if !ok {
		return economics.TariffSnapshot{}, lookupMiss("customer tariff")
	}
	return tariff.Clone(), nil
}

// Snapshot implements the public rating source for the configured default
// card. Refresh still belongs to the host snapshot controller; this method
// only exposes a stable, content-addressable source view.
func (c *SnapshotCatalog) Snapshot(ctx context.Context) (economics.Snapshot[economics.RatingCatalogView], error) {
	if err := catalogCtxErr(ctx, c); err != nil {
		return economics.Snapshot[economics.RatingCatalogView]{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.hasDefaults {
		return economics.Snapshot[economics.RatingCatalogView]{State: economics.SnapshotUnavailable}, errCatalogDefaultsMissing
	}
	pricing, found := c.pricing[c.defaultPricing]
	if !found {
		return economics.Snapshot[economics.RatingCatalogView]{State: economics.SnapshotUnavailable}, lookupMiss("customer pricing")
	}
	tariff, found := c.tariffs[c.defaultPricing]
	if !found {
		return economics.Snapshot[economics.RatingCatalogView]{State: economics.SnapshotUnavailable}, lookupMiss("customer tariff")
	}
	tariff = tariff.Clone()
	rules := make([]economics.RatingRule, len(tariff.Rules))
	for i, rule := range tariff.Rules {
		rules[i] = rule.Clone()
	}
	return economics.Snapshot[economics.RatingCatalogView]{
		ID: tariff.Ref.ID, Version: tariff.Ref.Version,
		EffectiveAt: tariff.Ref.EffectiveAt, FetchedAt: tariff.Ref.FetchedAt,
		State: economics.SnapshotReady,
		Value: economics.RatingCatalogView{
			Currency: pricing.Currency, CatalogVersion: tariff.CatalogVersion,
			Rules:               rules,
			EffectiveQualifiers: append([]metering.Dimension(nil), tariff.EffectiveQualifiers...),
			LegacySemantics:     tariff.LegacySemantics,
			Schemas:             tariff.Schemas,
		},
	}, nil
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
	return policy.Clone(), nil
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

// OperatorRate returns a historical V1 operator-rate body for replay only
// (Task 18.2). No live production money path calls it after the Task 18.1
// scalar-fallback retirement; V2 provider-quantity valuation owns estimates.
// Operator migration: query historical bodies for audit, never for live rating.
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

// modelPricingForLegs builds the effective per backend/model customer pricing
// cards for the legs of one call. Cards are emitted only when at least one
// route/model override exists for the call; a route without an override keeps
// the customer default pricing (requirement 4.4). An override binding whose
// immutable body is missing fails closed (requirement 4.5) instead of rating
// with an unrelated model price.
func (c *SnapshotCatalog) modelPricingForLegs(legs []billing.CallLegUsageRecord, customer billing.PricingSnapshot) ([]billing.ModelCustomerPricing, error) {
	anyOverride := false
	for _, leg := range legs {
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
	for _, leg := range legs {
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

func (c *SnapshotCatalog) modelTariffsForLegs(legs []billing.CallLegUsageRecord, customer billing.PricingSnapshot) ([]billing.ModelCustomerTariff, error) {
	anyOverride := false
	for _, leg := range legs {
		if _, found := c.routePricing[routeOf(leg.BackendID, leg.ModelID)]; found {
			anyOverride = true
			break
		}
	}
	if !anyOverride {
		return nil, nil
	}
	defaultTariff, found := c.tariffs[keyOf(customer.Ref)]
	if !found {
		return nil, lookupMiss("customer tariff")
	}
	cards := make([]billing.ModelCustomerTariff, 0, len(legs))
	seenRoutes := make(map[routeKey]struct{})
	for _, leg := range legs {
		rk := routeOf(leg.BackendID, leg.ModelID)
		if _, seen := seenRoutes[rk]; seen {
			continue
		}
		seenRoutes[rk] = struct{}{}
		tariff := defaultTariff
		if overrideKey, overridden := c.routePricing[rk]; overridden {
			var ok bool
			tariff, ok = c.tariffs[overrideKey]
			if !ok {
				return nil, lookupMiss("route tariff")
			}
		}
		cards = append(cards, billing.ModelCustomerTariff{BackendID: rk.backend, ModelID: rk.model, Tariff: tariff.Clone()})
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

func keyOfEconomicsRatingRef(ref economics.RatingSnapshotRef) versionKey {
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
	if !retailPolicyReplayEqual(a.Retail, b.Retail) {
		return false
	}
	return keyOf(a.Ref) == keyOf(b.Ref) &&
		keyOf(a.PricingRef) == keyOf(b.PricingRef) &&
		a.Scope == b.Scope &&
		a.IncludeInputTokens == b.IncludeInputTokens &&
		a.IncludeOutputTokens == b.IncludeOutputTokens &&
		a.IncludeFixedCharges == b.IncludeFixedCharges &&
		a.IncludeResourceCharges == b.IncludeResourceCharges
}

func retailPolicyReplayEqual(a, b *billing.RetailSelectionPolicy) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Mode != b.Mode || a.Basis != b.Basis || !slices.Equal(a.OutcomeSubset, b.OutcomeSubset) {
		return false
	}
	if a.CostPassThrough == nil || b.CostPassThrough == nil {
		return a.CostPassThrough == b.CostPassThrough
	}
	if a.CostPassThrough.MissingCost != b.CostPassThrough.MissingCost || a.CostPassThrough.AllowLateAdjustment != b.CostPassThrough.AllowLateAdjustment {
		return false
	}
	if a.CostPassThrough.SafeBound == nil || b.CostPassThrough.SafeBound == nil {
		return a.CostPassThrough.SafeBound == b.CostPassThrough.SafeBound
	}
	return *a.CostPassThrough.SafeBound == *b.CostPassThrough.SafeBound
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
