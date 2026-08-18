package billing

import (
	"context"
	"time"
)

// ProviderMaintenanceUsage is provider-authoritative usage from a control-plane
// maintenance call. It is intentionally not a CallLegUsageRecord: maintenance
// must retain its operation identity without masquerading as a foreground B-leg.
type ProviderMaintenanceUsage struct {
	OperationID string
	ALegID      string
	TargetID    string
	BackendID   string
	ModelID     string
	RecordedAt  time.Time
	Evidence    FinalBillingEvidence
}

// ProviderMaintenanceUsageObserver is the existing provider-billable accounting
// injection seam for non-foreground provider calls. Implementations own durable
// delivery/rating policy; keep-warm only emits the immutable evidence.
type ProviderMaintenanceUsageObserver interface {
	ObserveProviderMaintenance(context.Context, ProviderMaintenanceUsage)
}

type ProviderMaintenanceUsageObserverFunc func(context.Context, ProviderMaintenanceUsage)

func (f ProviderMaintenanceUsageObserverFunc) ObserveProviderMaintenance(ctx context.Context, usage ProviderMaintenanceUsage) {
	if f != nil {
		f(ctx, usage)
	}
}
