package billingcompose

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type SnapshotRefFuncs struct {
	CustomerPricingRef func(context.Context, lipapi.Call) billing.VersionRef
	ChargePolicyRef    func(context.Context, lipapi.Call) billing.VersionRef
	OperatorRateRef    func(context.Context, string, string) billing.VersionRef
}

func PrincipalSessionIdentity(refs SnapshotRefFuncs) runtime.BillingIdentity {
	return runtime.BillingIdentity{
		AccountID:          accountIDFromPrincipal,
		CustomerPricingRef: refs.CustomerPricingRef,
		ChargePolicyRef:    refs.ChargePolicyRef,
		OperatorRateRef:    refs.OperatorRateRef,
		WireBounded:        true,
		WireAccountID: func(ctx context.Context, sc scope.PrincipalScopeView) string {
			if sc.PrincipalID.IsKnown() {
				return strings.TrimSpace(sc.PrincipalID.String())
			}
			return accountIDFromPrincipal(ctx, lipapi.Call{})
		},
		WireCustomerPricingRef: func(ctx context.Context) billing.VersionRef {
			if refs.CustomerPricingRef != nil {
				return refs.CustomerPricingRef(ctx, lipapi.Call{})
			}
			return billing.VersionRef{}
		},
		WireChargePolicyRef: func(ctx context.Context) billing.VersionRef {
			if refs.ChargePolicyRef != nil {
				return refs.ChargePolicyRef(ctx, lipapi.Call{})
			}
			return billing.VersionRef{}
		},
	}
}

func accountIDFromPrincipal(ctx context.Context, _ lipapi.Call) string {
	view, ok := scope.ScopeFromContext(ctx)
	if !ok || !view.PrincipalID.IsKnown() {
		return ""
	}
	return strings.TrimSpace(view.PrincipalID.String())
}
