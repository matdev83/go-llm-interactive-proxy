package billingcompose

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// SnapshotRefFuncs supplies catalog (or test stub) snapshot identity resolvers
// stamped onto BillingIdentity. Identity mapping never creates an account.
type SnapshotRefFuncs struct {
	CustomerPricingRef func(context.Context, lipapi.Call) billing.VersionRef
	ChargePolicyRef    func(context.Context, lipapi.Call) billing.VersionRef
	OperatorRateRef    func(context.Context, string, string) billing.VersionRef
}

// PrincipalSessionIdentity returns the stock BillingIdentity: account from the
// authenticated principal, authorization from proxy-owned session plus A-leg.
func PrincipalSessionIdentity(refs SnapshotRefFuncs) runtime.BillingIdentity {
	return runtime.BillingIdentity{
		AccountID:          accountIDFromPrincipal,
		AuthorizationID:    authorizationIDFromSession,
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

func authorizationIDFromSession(_ context.Context, call lipapi.Call, aLegID string) string {
	sessionID := strings.TrimSpace(call.Session.AuthoritativeSessionID)
	aLeg := strings.TrimSpace(aLegID)
	if sessionID == "" || aLeg == "" {
		return ""
	}
	return sessionID + ":" + aLeg
}
