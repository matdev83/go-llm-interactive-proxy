package runtimebundle

import (
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
)

func buildKeepwarmGeneration(cfg *config.Config, nowFn func() time.Time, registry *keepwarm.ManagerRegistry, policy *keepwarm.PolicyStore, accounting billing.ProviderMaintenanceUsageObserver) (*keepwarm.Manager, uint64, error) {
	if cfg == nil {
		return nil, 0, fmt.Errorf("runtimebundle: keep-warm config is nil")
	}
	manager, err := keepwarm.NewManager(cfg.EffectiveKeepwarm(), keepwarm.ClockFunc(nowFn), billingcompose.KeepwarmHooks(accounting))
	if err != nil {
		return nil, 0, fmt.Errorf("runtimebundle: keep-warm manager: %w", err)
	}
	// Reapply process-owned disabled-session policy to the new generation;
	// provider handles and old epochs are never migrated.
	if policy != nil {
		for _, aLegID := range policy.DisabledALegIDs() {
			manager.SetSessionDisabled(aLegID, true)
		}
	}
	// Start only arms the generation-owned loop; workers remain lazy until the
	// first eligible target is admitted.
	manager.Start()
	if registry == nil {
		return manager, 0, nil
	}
	id, err := registry.Register(manager)
	if err != nil {
		_ = manager.Quiesce(nil)
		return nil, 0, fmt.Errorf("runtimebundle: register keep-warm manager: %w", err)
	}
	return manager, id, nil
}
