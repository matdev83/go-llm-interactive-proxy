package billingcompose

import (
	"context"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
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

func ComposeKeepwarmAccounting(store billing.AuthoritativeBilling, injected billing.ProviderMaintenanceUsageObserver) (billing.ProviderMaintenanceUsageObserver, error) {
	if injected != nil {
		return injected, nil
	}
	maintenanceStore, ok := store.(billing.ProviderMaintenanceUsageStore)
	if !ok {
		return nil, fmt.Errorf("store must implement durable provider maintenance usage")
	}
	return NewDurableMaintenanceObserver(maintenanceStore), nil
}

// KeepwarmHooks keeps maintenance accounting in the billing composition layer,
// separate from foreground call-leg usage.
func KeepwarmHooks(observer billing.ProviderMaintenanceUsageObserver) keepwarm.Hooks {
	if observer == nil {
		return keepwarm.Hooks{}
	}
	return keepwarm.Hooks{Accounting: func(ctx context.Context, record keepwarm.RenewalRecord) error {
		if record.Accounting == nil {
			return nil
		}
		return observer.ObserveProviderMaintenance(ctx, billing.ProviderMaintenanceUsage{
			OperationID: record.OperationID,
			ALegID:      record.ALegID,
			TargetID:    string(record.TargetID),
			BackendID:   record.BackendID,
			ModelID:     record.ModelID,
			RecordedAt:  time.Now().UTC(),
			Evidence:    maintenanceEvidence(*record.Accounting, record.OperationID),
		})
	}}
}

func maintenanceEvidence(e promptcache.AccountingEvidence, dedupe string) billing.FinalBillingEvidence {
	return billing.FinalBillingEvidence{
		InputTokens:      maintenanceQuantity(e.InputTokens),
		OutputTokens:     maintenanceQuantity(e.OutputTokens),
		CacheReadTokens:  maintenanceQuantity(e.CacheReadTokens),
		CacheWriteTokens: maintenanceQuantity(e.CacheWriteTokens),
		ReasoningTokens:  maintenanceQuantity(e.ReasoningTokens),
		TotalTokens:      maintenanceQuantity(e.TotalTokens),
		Cost:             billing.MoneyEvidence{},
		Source:           billing.EvidenceSourceProviderReported,
		Authority:        billing.EvidenceAuthorityAuthoritative,
		DedupeKey:        dedupe,
	}
}

func maintenanceQuantity(value *int64) billing.Quantity {
	if value == nil {
		return billing.Quantity{}
	}
	return billing.Quantity{Value: *value, Present: true}
}
