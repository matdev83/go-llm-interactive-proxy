package runtimebundle

import (
	adminkeepwarm "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/keepwarm"
)

func keepwarmAdminProjection(process candidateProcessRefs) (adminkeepwarm.Options, bool) {
	if process.standardFeatures == nil {
		return adminkeepwarm.Options{}, false
	}
	svc := process.standardFeatures.KeepwarmAdminService()
	if svc == nil {
		return adminkeepwarm.Options{}, false
	}
	return adminkeepwarm.Options{
		Enabled:       true,
		Service:       svc,
		ResolveALegID: adminkeepwarm.PathALegID,
	}, true
}
