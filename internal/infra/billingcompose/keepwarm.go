package billingcompose

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// DurableMaintenanceObserver delivers maintenance usage to the authoritative
// provider-billable store. The store owns idempotency and durable persistence.
type DurableMaintenanceObserver struct {
	store billing.ProviderMaintenanceUsageStore
}

var _ billing.ProviderMaintenanceUsageObserver = (*DurableMaintenanceObserver)(nil)

func NewDurableMaintenanceObserver(store billing.ProviderMaintenanceUsageStore) *DurableMaintenanceObserver {
	return &DurableMaintenanceObserver{store: store}
}

func (o *DurableMaintenanceObserver) ObserveProviderMaintenance(ctx context.Context, usage billing.ProviderMaintenanceUsage) error {
	if o == nil || o.store == nil {
		return billing.ErrBillingStoreUnavailable
	}
	return o.store.AppendProviderMaintenance(ctx, usage)
}

func ComposeMaintenanceAccounting(store billing.AuthoritativeBilling, injected billing.ProviderMaintenanceUsageObserver) (billing.ProviderMaintenanceUsageObserver, error) {
	if injected != nil {
		return injected, nil
	}
	maintenanceStore, ok := store.(billing.ProviderMaintenanceUsageStore)
	if !ok {
		return nil, fmt.Errorf("store must implement durable provider maintenance usage")
	}
	return NewDurableMaintenanceObserver(maintenanceStore), nil
}
