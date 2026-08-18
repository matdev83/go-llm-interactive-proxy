package billingcompose

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

// KeepwarmHooks keeps maintenance accounting in the billing composition layer,
// separate from foreground call-leg usage.
func KeepwarmHooks(observer billing.ProviderMaintenanceUsageObserver) keepwarm.Hooks {
	if observer == nil {
		return keepwarm.Hooks{}
	}
	return keepwarm.Hooks{Accounting: func(record keepwarm.RenewalRecord) {
		if record.Accounting == nil {
			return
		}
		observer.ObserveProviderMaintenance(context.Background(), billing.ProviderMaintenanceUsage{
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
