package billingcompose

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

type JoinRatingResolver struct {
	catalog *SnapshotCatalog
}

var (
	_ billing.CallRatingResolver           = (*JoinRatingResolver)(nil)
	_ billing.OwnerAwareCallRatingResolver = (*JoinRatingResolver)(nil)
	_ billing.ProviderCostResolver         = (*ProviderCostJoinResolver)(nil)
	_ billing.PostUsageRater               = (*JoinRatingResolver)(nil)
)

func NewCallRatingResolver(catalog *SnapshotCatalog) (billing.CallRatingResolver, error) {
	if catalog == nil {
		return nil, errNilSnapshotCatalog
	}
	return &JoinRatingResolver{catalog: catalog}, nil
}

// ResolveCallRatingForOwner is the production owner-aware customer-rating
// port (Phase 18 blocker 1). The durable B1 pin owner selects V1 drain vs
// V2 component semantics inside billing.RateCall: V2-owned work rates
// exclusively from canonical V2 component quantities (legacy tariffs as
// explicitly mapped component material where supported) and fails closed
// before money when V2 evidence is missing or unsupported. The scalar live
// engine is unreachable for V2 owner/token and new live V2 calls. The
// complete bound valuation (subject/scope, currency, amount, result
// identity) is enforced before returning so a scalar wearing an ID cannot
// leave this boundary.
func (r *JoinRatingResolver) ResolveCallRatingForOwner(_ context.Context, complete billing.CompleteCall, exposure billing.CallExposure, owner string) (billing.CallRatingResult, error) {
	call := complete.Closure
	// Customer rating resolves customer pricing/policy/model cards only. The
	// combined provider-compose method is gone: no operator-rate lookup happens
	// here, so missing provider-cost data can never block customer settlement.
	snapshots, err := r.catalog.CustomerRatingSnapshots(call, complete.Legs)
	if err != nil {
		return billing.CallRatingResult{}, fmt.Errorf("billingcompose: customer rating snapshots: %w", err)
	}
	result, err := billing.RateCall(billing.CallRatingInput{
		Call:              call,
		Legs:              complete.Legs,
		MaxCustomerCharge: exposure.Max,
		CustomerPricing:   snapshots.DefaultPricing,
		CustomerPolicy:    snapshots.Policy,
		ModelPricing:      snapshots.ModelPricing,
		CustomerTariff:    snapshots.DefaultTariff,
		ModelTariffs:      snapshots.ModelTariffs,
		PostingOwner:      owner,
	})
	if err != nil {
		return billing.CallRatingResult{}, err
	}
	if err := billing.ValidateCallRatingResultForSettlement(result, call, exposure, owner); err != nil {
		return billing.CallRatingResult{}, err
	}
	return result, nil
}

func (r *JoinRatingResolver) ResolveCallRating(_ context.Context, complete billing.CompleteCall, exposure billing.CallExposure) (billing.CallRatingResult, error) {
	call := complete.Closure
	// Customer rating resolves customer pricing/policy/model cards only. The
	// combined provider-compose method is gone: no operator-rate lookup happens
	// here, so missing provider-cost data can never block customer settlement.
	// Legacy owner-unspecified entry: billing.RateCall infers V2 when V2
	// quantity evidence is present (component or fail-closed, never scalar)
	// and V1 drain otherwise, so this path is safe for historical replay.
	// Production workers must use ResolveCallRatingForOwner with the claim
	// owner; that port additionally enforces the V2 component-valuation
	// boundary before returning.
	snapshots, err := r.catalog.CustomerRatingSnapshots(call, complete.Legs)
	if err != nil {
		return billing.CallRatingResult{}, fmt.Errorf("billingcompose: customer rating snapshots: %w", err)
	}
	return billing.RateCall(billing.CallRatingInput{
		Call:              call,
		Legs:              complete.Legs,
		MaxCustomerCharge: exposure.Max,
		CustomerPricing:   snapshots.DefaultPricing,
		CustomerPolicy:    snapshots.Policy,
		ModelPricing:      snapshots.ModelPricing,
		CustomerTariff:    snapshots.DefaultTariff,
		ModelTariffs:      snapshots.ModelTariffs,
	})
}

// Rate resolves and freezes the accepted tariff before invoking the billing
// domain's post-usage evaluator. The resolver is intentionally separate from
// ResolveCallRating: legacy scalar call settlement remains compatible while
// component E/Q/P valuation uses the immutable tariff path.
func (r *JoinRatingResolver) Rate(ctx context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	if input.Basis == economics.BasisProviderReported {
		return billing.RateProviderReported(ctx, input)
	}
	if r == nil || r.catalog == nil {
		return economics.Valuation{}, fmt.Errorf("billingcompose: tariff catalog is unavailable")
	}
	ref := billing.VersionRef{
		ID: input.Tariff.ID, Version: input.Tariff.Version,
		EffectiveAt: input.Tariff.EffectiveAt, FetchedAt: input.Tariff.FetchedAt,
	}
	tariff, err := r.catalog.ResolveTariff(ctx, ref)
	if err != nil {
		return economics.Valuation{}, fmt.Errorf("billingcompose: resolve tariff: %w", err)
	}
	if input.Basis == economics.BasisCustomerPolicy && input.Scope == "b_leg" {
		return billing.RateCustomerPolicyObservation(ctx, input, tariff)
	}
	return billing.RateWithTariff(ctx, input, tariff)
}

type ProviderCostJoinResolver struct {
	// catalog is retained for historical rate references and OperatorRateRef
	// lineage stamping only (Task 18.2). Live provider-cost resolution never
	// reads operator-rate bodies: token-only evidence stays unreconciled with
	// provider_money_unavailable and V2 owns estimates. Operator migration:
	// keep the catalog for audit/history, do not publish new rates for live
	// fallback.
	catalog  *SnapshotCatalog
	currency string
}

func NewProviderCostResolver(catalog *SnapshotCatalog, currency string) (billing.ProviderCostResolver, error) {
	if catalog == nil {
		return nil, errNilSnapshotCatalog
	}
	if strings.TrimSpace(currency) == "" {
		return nil, fmt.Errorf("billingcompose: provider currency is required")
	}
	return &ProviderCostJoinResolver{catalog: catalog, currency: strings.TrimSpace(currency)}, nil
}

func (r *ProviderCostJoinResolver) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	if r == nil || r.catalog == nil {
		return billing.OperatorCostResult{}, fmt.Errorf("billingcompose: provider-cost catalog is unavailable")
	}
	// Task 18.1 (Migration Strategy step 8): no catalog-rate live fallback.
	// Only provider-reported authoritative V1 money resolves here; token-only
	// evidence stays unreconciled and V2 provider-quantity valuation owns
	// estimates. The catalog is retained for historical rate references.
	return billing.RateProviderCost(leg, nil, r.currency)
}
