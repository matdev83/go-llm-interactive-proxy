package runtimebundle

import (
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	adminkeepwarm "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/keepwarm"
)

func keepwarmAdminProjection(process candidateProcessRefs) (adminkeepwarm.Options, bool) {
	if process.keepwarmPolicy == nil || process.keepwarmRegistry == nil {
		return adminkeepwarm.Options{}, false
	}
	return adminkeepwarm.Options{
		Enabled:       true,
		Service:       keepwarm.NewPolicyService(process.keepwarmPolicy, process.keepwarmRegistry, keepwarm.ClockFunc(func() time.Time { return time.Now().UTC() })),
		ResolveALegID: adminkeepwarm.PathALegID,
	}, true
}
