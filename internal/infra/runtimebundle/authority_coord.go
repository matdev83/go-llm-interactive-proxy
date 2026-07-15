package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
)

// attachAuthorityCoordinators fills request/attempt coordinators when usage
// authority and/or concurrency lease authority is enabled.
func attachAuthorityCoordinators(rt *runtime.AccountingRuntime) {
	if rt == nil {
		return
	}
	if rt.UsageAuthority == nil && rt.ConcurrencyProvider == nil {
		return
	}
	req, att := runtime.BuildAuthorityCoordinators(rt.UsageAuthority, rt.ConcurrencyProvider)
	rt.RequestCoordinator = req
	rt.AttemptCoordinator = att
}
