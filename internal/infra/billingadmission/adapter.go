package billingadmission

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// RoutePricing resolves the exact customer pricing snapshot for one planned
// backend:model leaf. Missing rates must fail closed through the estimator.
type RoutePricing func(ctx context.Context, backend, model string) (billing.PricingSnapshot, error)

// ModelMaxOutput resolves the finite model output bound for one planned leaf.
type ModelMaxOutput func(ctx context.Context, backend, model string) (max int64, present bool, err error)

// Config wires identity and economic snapshot resolvers for one admission call.
type Config struct {
	Store               billing.AuthorizationStore
	Releaser            billing.HoldReleaser
	Identity            coreruntime.BillingIdentity
	Currency            string
	Policy              func(context.Context, lipapi.Call) (billing.ChargePolicy, error)
	Pricing             RoutePricing
	ModelMaxOutput      ModelMaxOutput
	ClientMaxOutput     func(context.Context, lipapi.Call) *int64
	Strict              bool
	ConservativeCeiling *billing.Money
	HoldTTL             time.Duration
	Now                 func() time.Time
}

// Adapter is the production BillingAdmission implementation for composition.
type Adapter struct {
	cfg Config
}

// NewAdapter constructs a runtime BillingAdmission adapter. Store, Releaser,
// Identity account/authorization resolvers, Policy, and Pricing are required.
func NewAdapter(cfg Config) (*Adapter, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("billingadmission: authorization store is required")
	}
	if cfg.Releaser == nil {
		return nil, fmt.Errorf("billingadmission: hold releaser is required")
	}
	if cfg.Identity.AccountID == nil || cfg.Identity.AuthorizationID == nil {
		return nil, fmt.Errorf("billingadmission: account and authorization identity resolvers are required")
	}
	if cfg.Policy == nil || cfg.Pricing == nil {
		return nil, fmt.Errorf("billingadmission: policy and pricing resolvers are required")
	}
	if strings.TrimSpace(cfg.Currency) == "" {
		return nil, fmt.Errorf("billingadmission: currency is required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.HoldTTL <= 0 {
		cfg.HoldTTL = billing.DefaultHoldTTL
	}
	return &Adapter{cfg: cfg}, nil
}

// SetHoldTTL replaces the authorization hold lifetime. Zero or negative is ignored.
func (a *Adapter) SetHoldTTL(d time.Duration) {
	if a == nil || d <= 0 {
		return
	}
	a.cfg.HoldTTL = d
}

// HoldTTL returns the configured authorization hold lifetime.
func (a *Adapter) HoldTTL() time.Duration {
	if a == nil {
		return 0
	}
	return a.cfg.HoldTTL
}

var (
	_ coreruntime.BillingAdmission        = (*Adapter)(nil)
	_ coreruntime.BillingAdmissionCleanup = (*Adapter)(nil)
)

// Authorize estimates MaxCustomerCharge from the planned route and atomically
// creates the durable authorization hold before any provider/connector work.
func (a *Adapter) Authorize(ctx context.Context, in coreruntime.BillingAdmissionInput) (billing.Authorization, error) {
	if a == nil {
		return billing.Authorization{}, fmt.Errorf("%w: nil admission adapter", billing.ErrAuthorizationUnavailable)
	}
	accountID := strings.TrimSpace(a.cfg.Identity.AccountID(ctx, in.Call))
	aLegID := strings.TrimSpace(in.ALegID)
	authID := strings.TrimSpace(a.cfg.Identity.AuthorizationID(ctx, in.Call, aLegID))
	if accountID == "" || authID == "" || aLegID == "" {
		return billing.Authorization{}, fmt.Errorf("%w: account, authorization, and A-leg identities are required", billing.ErrAuthorizationInvalid)
	}
	turKey, err := billing.TURKey(accountID, aLegID)
	if err != nil {
		return billing.Authorization{}, fmt.Errorf("%w: %v", billing.ErrAuthorizationInvalid, err)
	}

	policy, err := a.cfg.Policy(ctx, in.Call)
	if err != nil {
		return billing.Authorization{}, err
	}
	routes, err := a.chargeRoutes(ctx, in)
	if err != nil {
		return billing.Authorization{}, err
	}
	estimate := billing.MaxChargeInput{
		Currency:            a.cfg.Currency,
		InputTokens:         in.RequestSize.Tokens,
		InputTokensPresent:  in.RequestSize.Available,
		Policy:              policy,
		Routes:              routes,
		Strict:              a.cfg.Strict,
		ConservativeCeiling: a.cfg.ConservativeCeiling,
	}
	service := billing.AdmissionService{Store: a.cfg.Store}
	auth, _, err := service.Authorize(ctx, billing.AdmissionRequest{
		AccountID: accountID, TURKey: turKey, AuthorizationID: authID,
		Estimate: estimate, ExpiresAt: a.cfg.Now().Add(a.cfg.HoldTTL),
	})
	return auth, err
}

// ReleaseUnused implements coreruntime.BillingAdmissionCleanup for Execute
// aborts that never reach a stream terminal handoff.
func (a *Adapter) ReleaseUnused(ctx context.Context, in coreruntime.BillingAdmissionInput) error {
	if a == nil {
		return fmt.Errorf("%w: nil admission adapter", billing.ErrAuthorizationUnavailable)
	}
	if a.cfg.Releaser == nil {
		return fmt.Errorf("%w: hold releaser is required", billing.ErrAuthorizationUnavailable)
	}
	accountID := strings.TrimSpace(a.cfg.Identity.AccountID(ctx, in.Call))
	aLegID := strings.TrimSpace(in.ALegID)
	authID := strings.TrimSpace(a.cfg.Identity.AuthorizationID(ctx, in.Call, aLegID))
	turKey, err := billing.TURKey(accountID, aLegID)
	if err != nil {
		return fmt.Errorf("%w: %v", billing.ErrAuthorizationInvalid, err)
	}
	if accountID == "" || authID == "" || aLegID == "" {
		return fmt.Errorf("%w: account, authorization, and A-leg identities are required", billing.ErrAuthorizationInvalid)
	}
	_, err = a.cfg.Releaser.ReleaseAuthorization(ctx, billing.ReleaseAuthorizationInput{
		AccountID: accountID, AuthorizationID: authID, TURKey: turKey,
		FullClose: true, Reason: billing.ReleaseExecutionNotStarted,
		SourceKey: "execution_not_started:" + authID,
	})
	return err
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
	seen := make(map[string]struct{})
	var out []plannedLeaf
	add := func(p routing.Primary) {
		key := p.String()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, plannedLeaf{Key: key, Backend: p.Backend, Model: p.Model})
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
