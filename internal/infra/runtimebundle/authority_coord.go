package runtimebundle

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

// attachAuthorityCoordinators fills request/attempt coordinators when usage
// authority and/or concurrency lease authority is enabled, and merges production
// public provider overrides (requirements 12.1, 12.4).
func attachAuthorityCoordinators(rt *runtime.AccountingRuntime, prod ProductionOptions) {
	if rt == nil {
		return
	}
	if prod.ConcurrencyProvider != nil {
		rt.ConcurrencyProvider = prod.ConcurrencyProvider
	}
	if rt.UsageAuthority == nil && rt.ConcurrencyProvider == nil && !prod.HasAuthorityOverrides() {
		return
	}
	req, att := runtime.BuildAuthorityCoordinators(rt.UsageAuthority, rt.ConcurrencyProvider)
	if req == nil && (len(prod.RequestProviders) > 0 || rt.ConcurrencyProvider != nil) {
		req = &authoritycoord.RequestCoordinator{Concurrency: rt.ConcurrencyProvider}
	}
	if att == nil && len(prod.AttemptProviders) > 0 {
		att = &authoritycoord.AttemptCoordinator{}
	}
	for i, p := range prod.RequestProviders {
		if p == nil {
			continue
		}
		if req == nil {
			req = &authoritycoord.RequestCoordinator{Concurrency: rt.ConcurrencyProvider}
		}
		req.Slots = append(req.Slots, authoritycoord.RequestSlot{
			ID:       fmt.Sprintf("production-request-%d", i),
			Class:    authoritycoord.PriorityQuotaBudgetRate,
			Provider: p,
			Strength: authority.StrengthRequired,
		})
	}
	for i, p := range prod.AttemptProviders {
		if p == nil {
			continue
		}
		if att == nil {
			att = &authoritycoord.AttemptCoordinator{}
		}
		att.Slots = append(att.Slots, authoritycoord.AttemptSlot{
			ID:       fmt.Sprintf("production-attempt-%d", i),
			Class:    authoritycoord.AttemptPriorityHardSpend,
			Provider: p,
			Strength: authority.StrengthRequired,
		})
	}
	if req != nil && prod.ConcurrencyProvider != nil {
		req.Concurrency = prod.ConcurrencyProvider
	}
	rt.RequestCoordinator = req
	rt.AttemptCoordinator = att
}
