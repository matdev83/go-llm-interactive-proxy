package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
)

// attachAuthorityCoordinators fills Phase 6 request/attempt coordinators when
// usage authority is enabled. ConcurrencyProvider stays nil until Phase 8.
func attachAuthorityCoordinators(rt *runtime.AccountingRuntime) {
	if rt == nil || rt.UsageAuthority == nil {
		return
	}
	req, att := runtime.BuildAuthorityCoordinators(rt.UsageAuthority)
	rt.RequestCoordinator = req
	rt.AttemptCoordinator = att
}
