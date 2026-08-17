package billingcompose

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

type JoinRatingResolver struct {
	catalog *SnapshotCatalog
}

var (
	_ billing.CallRatingResolver   = (*JoinRatingResolver)(nil)
	_ billing.ProviderCostResolver = (*ProviderCostJoinResolver)(nil)
)

func NewCallRatingResolver(catalog *SnapshotCatalog) (billing.CallRatingResolver, error) {
	if catalog == nil {
		return nil, errNilSnapshotCatalog
	}
	return &JoinRatingResolver{catalog: catalog}, nil
}

func (r *JoinRatingResolver) ResolveCallRating(_ context.Context, complete billing.CompleteCall, exposure billing.CallExposure) (billing.CallRatingResult, error) {
	call := complete.Closure
	// Customer rating resolves customer pricing/policy/model cards only. The
	// combined provider-compose method is gone: no operator-rate lookup happens
	// here, so missing provider-cost data can never block customer settlement.
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
	})
}

type ProviderCostJoinResolver struct {
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
	got, err := billing.RateProviderCost(leg, nil, r.currency)
	if err == nil || !errors.Is(err, billing.ErrUnreconciledCost) {
		return got, err
	}
	rate, rateErr := r.catalog.OperatorRate(leg.OperatorRateRef)
	if rateErr != nil {
		return got, err
	}
	return billing.RateProviderCost(leg, billing.OperatorRateSet{rate}, r.currency)
}
