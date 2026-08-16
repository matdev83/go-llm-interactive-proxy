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
	}
}

func accountIDFromPrincipal(ctx context.Context, _ lipapi.Call) string {
	view, ok := scope.ScopeFromContext(ctx)
	if !ok || !view.PrincipalID.IsKnown() {
		return ""
	}
	return strings.TrimSpace(view.PrincipalID.String())
}
