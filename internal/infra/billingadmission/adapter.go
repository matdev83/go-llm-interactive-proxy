package billingadmission

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

type (
	RoutePricing   func(ctx context.Context, backend, model string) (billing.PricingSnapshot, error)
	ModelMaxOutput func(ctx context.Context, backend, model string) (max int64, present bool, err error)
	// TariffResolver returns the immutable customer tariff bound to the call's
	// policy. A nil resolver keeps the legacy scalar pricing path.
	TariffResolver func(ctx context.Context, call lipapi.Call) (economics.TariffSnapshot, error)
	// ModelTariffResolver optionally overrides the base tariff per candidate
	// route. An empty snapshot selects the base tariff for that route.
	ModelTariffResolver func(ctx context.Context, backend, model string) (economics.TariffSnapshot, error)
	// ComponentBoundsResolver returns finite enforceable candidate/work upper
	// bounds for richer customer offers. Required when BaseTariff is set.
	ComponentBoundsResolver func(ctx context.Context, call lipapi.Call) ([]billing.RichComponentBound, error)
	// RouteCapabilitiesResolver returns provable evidence capabilities per route.
	RouteCapabilitiesResolver func(ctx context.Context, backend, model string) ([]string, error)
	Config                    struct {
		ExposureStore        billing.ExposureAdmissionStore
		Identity             coreruntime.BillingIdentity
		Currency             string
		Policy               func(context.Context, lipapi.Call) (billing.ChargePolicy, error)
		Pricing              RoutePricing
		ModelMaxOutput       ModelMaxOutput
		ClientMaxOutput      func(context.Context, lipapi.Call) *int64
		Strict               bool
		ConservativeCeiling  *billing.Money
		BaseTariff           TariffResolver
		ModelTariff          ModelTariffResolver
		ComponentBounds      ComponentBoundsResolver
		RequiredCapabilities []string
		RouteCapabilities    RouteCapabilitiesResolver
	}
)

type Adapter struct {
	cfg Config
}

func NewAdapter(cfg Config) (*Adapter, error) {
	if cfg.ExposureStore == nil {
		return nil, fmt.Errorf("billingadmission: exposure store is required")
	}
	if cfg.Identity.AccountID == nil {
		return nil, fmt.Errorf("billingadmission: account identity resolver is required")
	}
	if cfg.Policy == nil || cfg.Pricing == nil {
		return nil, fmt.Errorf("billingadmission: policy and pricing resolvers are required")
	}
	if strings.TrimSpace(cfg.Currency) == "" {
		return nil, fmt.Errorf("billingadmission: currency is required")
	}
	return &Adapter{cfg: cfg}, nil
}

var _ coreruntime.BillingExposureAdmission = (*Adapter)(nil)

func (a *Adapter) Quote(ctx context.Context, in coreruntime.BillingAdmissionInput) (billing.MaxCostBound, error) {
	if a == nil {
		return billing.MaxCostBound{}, fmt.Errorf("%w: nil admission adapter", billing.ErrEstimateInvalid)
	}
	if a.cfg.BaseTariff != nil {
		return a.richQuote(ctx, in)
	}
	estimate, err := a.maxChargeInput(ctx, in)
	if err != nil {
		return billing.MaxCostBound{}, err
	}
	return billing.EstimateMaxCustomerCharge(estimate)
}

func (a *Adapter) richQuote(ctx context.Context, in coreruntime.BillingAdmissionInput) (billing.MaxCostBound, error) {
	policy, err := a.cfg.Policy(ctx, in.Call)
	if err != nil {
		return billing.MaxCostBound{}, err
	}
	baseTariff, err := a.cfg.BaseTariff(ctx, in.Call)
	if err != nil {
		return billing.MaxCostBound{}, err
	}
	if a.cfg.ComponentBounds == nil {
		return billing.MaxCostBound{}, fmt.Errorf("%w: component bounds resolver is required with customer tariff", billing.ErrEstimateInvalid)
	}
	bounds, err := a.cfg.ComponentBounds(ctx, in.Call)
	if err != nil {
		return billing.MaxCostBound{}, err
	}
	leaves := collectPlannedLeaves(in.Route)
	if len(leaves) == 0 {
		return billing.MaxCostBound{}, fmt.Errorf("%w: route plan has no chargeable leaves", billing.ErrEstimateUnbounded)
	}
	routes := make([]billing.RichQuoteRoute, 0, len(leaves))
	for _, leaf := range leaves {
		route := billing.RichQuoteRoute{ID: leaf.Key, Backend: leaf.Backend, Model: leaf.Model}
		if a.cfg.ModelTariff != nil {
			tariff, err := a.cfg.ModelTariff(ctx, leaf.Backend, leaf.Model)
			if err != nil {
				return billing.MaxCostBound{}, err
			}
			if strings.TrimSpace(tariff.Ref.ID) != "" || strings.TrimSpace(tariff.Ref.Version) != "" || len(tariff.Rules) != 0 || strings.TrimSpace(tariff.Currency) != "" {
				route.Tariff = tariff
			}
		}
		if a.cfg.RouteCapabilities != nil {
			capabilities, err := a.cfg.RouteCapabilities(ctx, leaf.Backend, leaf.Model)
			if err != nil {
				return billing.MaxCostBound{}, err
			}
			route.Capabilities = capabilities
		}
		routes = append(routes, route)
	}
	return billing.EstimateRichCustomerCharge(billing.RichQuoteInput{
		Currency: a.cfg.Currency, Policy: policy, BaseTariff: baseTariff,
		Routes: routes, Bounds: bounds,
		RequiredCapabilities: append([]string(nil), a.cfg.RequiredCapabilities...),
	})
}

func (a *Adapter) maxChargeInput(ctx context.Context, in coreruntime.BillingAdmissionInput) (billing.MaxChargeInput, error) {
	policy, err := a.cfg.Policy(ctx, in.Call)
	if err != nil {
		return billing.MaxChargeInput{}, err
	}
	routes, err := a.chargeRoutes(ctx, in)
	if err != nil {
		return billing.MaxChargeInput{}, err
	}
	return billing.MaxChargeInput{
		Currency:            a.cfg.Currency,
		InputTokens:         in.RequestSize.Tokens,
		InputTokensPresent:  in.RequestSize.Available,
		Policy:              policy,
		Routes:              routes,
		Strict:              a.cfg.Strict,
		ConservativeCeiling: a.cfg.ConservativeCeiling,
	}, nil
}

func (a *Adapter) Admit(ctx context.Context, in coreruntime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
	// F3: runtime composition chooses the posting owner from the durable
	// marker per StoreID/deployment at admission. When the underlying store
	// exposes version-aware admission plus a cutover reader, the active
	// marker selects V2; all pre-active states retain the legacy V1 default.
	// Stores without those ports (test doubles, legacy) keep V1 behavior.
	// The admitted exposure+pin retains the owner durably through the call
	// lifecycle; terminal evidence validates against that admitted record,
	// never a fresh global reclassification, and the owner never changes
	// mid-call.
	if owner, ok := a.versionedAdmissionOwner(ctx); ok && owner == billing.PostingOwnerV2 {
		return a.admitWithOwner(ctx, in, billing.PostingOwnerV2)
	}
	return a.admitV1(ctx, in)
}

// AdmitWithOwner is the explicit version-aware admission entrypoint. Empty
// owner preserves the legacy V1 default; V2 requires v2_active authorization
// from the durable marker and pins V2 ownership at start.
func (a *Adapter) AdmitWithOwner(ctx context.Context, in coreruntime.BillingExposureAdmissionInput, owner string) (billing.CallExposure, error) {
	trimmed := strings.TrimSpace(owner)
	if trimmed == "" || trimmed == billing.PostingOwnerV1 {
		return a.admitV1(ctx, in)
	}
	if trimmed != billing.PostingOwnerV2 {
		return billing.CallExposure{}, fmt.Errorf("%w: %w: unknown posting owner %q", billing.ErrPostingOwnershipInvalid, billing.ErrInvalidRecord, owner)
	}
	return a.admitWithOwner(ctx, in, billing.PostingOwnerV2)
}

// AdmitV2 is the explicit V2 admission entrypoint. Before v2_active it is
// rejected; in active it pins V2 ownership at start.
func (a *Adapter) AdmitV2(ctx context.Context, in coreruntime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
	return a.admitWithOwner(ctx, in, billing.PostingOwnerV2)
}

// versionedExposureStore is the explicit version-aware admission port F3
// consumes. DurableStore implements it; test doubles without it keep legacy
// V1 behavior.
type versionedExposureStore interface {
	AdmitExposureWithOwner(context.Context, billing.AdmitExposureInput, string) (billing.CallExposure, error)
}

// cutoverReader observes the durable marker per StoreID/deployment at
// admission. DurableStore implements it; the owner is chosen from this
// snapshot and retained via the admitted pin, never reclassified mid-call.
type cutoverReader interface {
	GetAccountingCutover(context.Context) (billing.AccountingCutoverMarker, error)
}

// versionedAdmissionOwner returns the durable-marker owner for this
// admission: V2 only in v2_active, V1 otherwise. ok=false when the store
// lacks the versioned ports (legacy V1 path).
func (a *Adapter) versionedAdmissionOwner(ctx context.Context) (string, bool) {
	if a == nil || a.cfg.ExposureStore == nil {
		return "", false
	}
	if _, ok := a.cfg.ExposureStore.(versionedExposureStore); !ok {
		return "", false
	}
	reader, ok := a.cfg.ExposureStore.(cutoverReader)
	if !ok {
		return "", false
	}
	marker, err := reader.GetAccountingCutover(ctx)
	if err != nil {
		// Absent marker (legacy store): V1 default via legacy path.
		return "", false
	}
	if billing.IsV2NewWorkAuthorized(marker.State) {
		return billing.PostingOwnerV2, true
	}
	return billing.PostingOwnerV1, true
}

func (a *Adapter) admitWithOwner(ctx context.Context, in coreruntime.BillingExposureAdmissionInput, owner string) (billing.CallExposure, error) {
	if a == nil || a.cfg.ExposureStore == nil {
		return billing.CallExposure{}, fmt.Errorf("%w: exposure store is required", billing.ErrExposureInvalid)
	}
	callID := strings.TrimSpace(in.CallID)
	if callID == "" {
		return billing.CallExposure{}, fmt.Errorf("%w: BillingCallID is required", billing.ErrExposureInvalid)
	}
	bound, err := a.Quote(ctx, in.BillingAdmissionInput)
	if err != nil {
		return billing.CallExposure{}, err
	}
	accountID := strings.TrimSpace(a.cfg.Identity.AccountID(ctx, in.Call))
	if accountID == "" && in.AccountID != "" {
		accountID = strings.TrimSpace(in.AccountID)
	}
	if accountID == "" && in.Scope.PrincipalID.IsKnown() {
		accountID = strings.TrimSpace(in.Scope.PrincipalID.String())
	}
	if accountID == "" {
		return billing.CallExposure{}, fmt.Errorf("%w: account identity is required", billing.ErrExposureInvalid)
	}
	input := billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID, Max: bound.Amount,
		PricingRef: bound.PricingRef, ChargePolicyRef: bound.ChargePolicyRef,
		RouteTariffs: bound.RouteTariffs,
	}
	if versioned, ok := a.cfg.ExposureStore.(versionedExposureStore); ok && versioned != nil {
		return versioned.AdmitExposureWithOwner(ctx, input, owner)
	}
	if owner == billing.PostingOwnerV2 {
		return billing.CallExposure{}, fmt.Errorf("%w: store does not support V2 admission",
			billing.ErrCutoverV2NotAuthorized)
	}
	return a.cfg.ExposureStore.AdmitExposure(ctx, input)
}

func (a *Adapter) admitV1(ctx context.Context, in coreruntime.BillingExposureAdmissionInput) (billing.CallExposure, error) {
	if a == nil || a.cfg.ExposureStore == nil {
		return billing.CallExposure{}, fmt.Errorf("%w: exposure store is required", billing.ErrExposureInvalid)
	}
	callID := strings.TrimSpace(in.CallID)
	if callID == "" {
		return billing.CallExposure{}, fmt.Errorf("%w: BillingCallID is required", billing.ErrExposureInvalid)
	}
	bound, err := a.Quote(ctx, in.BillingAdmissionInput)
	if err != nil {
		return billing.CallExposure{}, err
	}
	accountID := strings.TrimSpace(a.cfg.Identity.AccountID(ctx, in.Call))
	if accountID == "" && in.AccountID != "" {
		accountID = strings.TrimSpace(in.AccountID)
	}
	if accountID == "" && in.Scope.PrincipalID.IsKnown() {
		accountID = strings.TrimSpace(in.Scope.PrincipalID.String())
	}
	if accountID == "" {
		return billing.CallExposure{}, fmt.Errorf("%w: account identity is required", billing.ErrExposureInvalid)
	}
	return a.cfg.ExposureStore.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID, Max: bound.Amount,
		PricingRef: bound.PricingRef, ChargePolicyRef: bound.ChargePolicyRef,
		RouteTariffs: bound.RouteTariffs,
	})
}

func (a *Adapter) chargeRoutes(ctx context.Context, in coreruntime.BillingAdmissionInput) ([]billing.ChargeRoute, error) {
	if in.Route == nil {
		return nil, fmt.Errorf("%w: route plan is required", billing.ErrEstimateInvalid)
	}
	leaves := collectPlannedLeaves(in.Route)
	if len(leaves) == 0 {
		return nil, fmt.Errorf("%w: route plan has no chargeable leaves", billing.ErrEstimateUnbounded)
	}
	var clientMax *int64
	if a.cfg.ClientMaxOutput != nil {
		clientMax = a.cfg.ClientMaxOutput(ctx, in.Call)
	} else if in.MaxOutputTokens != nil {
		v := int64(*in.MaxOutputTokens)
		clientMax = &v
	} else if in.Call.Options.MaxOutputTokens != nil {
		v := int64(*in.Call.Options.MaxOutputTokens)
		clientMax = &v
	}
	out := make([]billing.ChargeRoute, 0, len(leaves))
	for _, leaf := range leaves {
		pricing, err := a.cfg.Pricing(ctx, leaf.Backend, leaf.Model)
		if err != nil {
			return nil, err
		}
		route := billing.ChargeRoute{
			ID: leaf.Key, Pricing: pricing, ClientMaxOutputTokens: clientMax,
		}
		if a.cfg.ModelMaxOutput != nil {
			max, present, maxErr := a.cfg.ModelMaxOutput(ctx, leaf.Backend, leaf.Model)
			if maxErr != nil {
				return nil, maxErr
			}
			route.ModelMaxOutputTokens = max
			route.ModelMaxOutputTokensPresent = present
		}
		out = append(out, route)
	}
	return out, nil
}

type plannedLeaf struct {
	Key     string
	Backend string
	Model   string
}

func collectPlannedLeaves(sel *routing.Selector) []plannedLeaf {
	if sel == nil {
		return nil
	}
	var out []plannedLeaf
	add := func(p routing.Primary) {
		out = append(out, plannedLeaf{Key: p.String(), Backend: p.Backend, Model: p.Model})
	}
	for _, alt := range sel.Alternatives {
		switch {
		case alt.Primary != nil:
			add(*alt.Primary)
		case alt.Weighted != nil:
			for _, branch := range alt.Weighted.Branches {
				if branch.Parallel != nil {
					for _, parallelBranch := range branch.Parallel.Branches {
						add(parallelBranch.Target)
					}
					continue
				}
				add(branch.Target)
			}
		case alt.Parallel != nil:
			for _, branch := range alt.Parallel.Branches {
				add(branch.Target)
			}
		}
	}
	return out
}
