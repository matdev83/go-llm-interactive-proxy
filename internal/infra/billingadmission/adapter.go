package billingadmission

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type (
	RoutePricing   func(ctx context.Context, backend, model string) (billing.PricingSnapshot, error)
	ModelMaxOutput func(ctx context.Context, backend, model string) (max int64, present bool, err error)
	Config         struct {
		ExposureStore       billing.ExposureAdmissionStore
		Identity            coreruntime.BillingIdentity
		Currency            string
		Policy              func(context.Context, lipapi.Call) (billing.ChargePolicy, error)
		Pricing             RoutePricing
		ModelMaxOutput      ModelMaxOutput
		ClientMaxOutput     func(context.Context, lipapi.Call) *int64
		Strict              bool
		ConservativeCeiling *billing.Money
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
	estimate, err := a.maxChargeInput(ctx, in)
	if err != nil {
		return billing.MaxCostBound{}, err
	}
	return billing.EstimateMaxCustomerCharge(estimate)
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
	if accountID == "" {
		return billing.CallExposure{}, fmt.Errorf("%w: account identity is required", billing.ErrExposureInvalid)
	}
	return a.cfg.ExposureStore.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID, Max: bound.Amount,
		PricingRef: bound.PricingRef, ChargePolicyRef: bound.ChargePolicyRef,
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
