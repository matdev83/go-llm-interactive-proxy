package runtimebundle

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

type processBillingLifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
}

func startProcessBillingWorker(owner *processResourceOwner, worker processBillingLifecycle) error {
	owner.Own(func() error { return worker.Stop(context.Background()) })
	return worker.Start(context.Background())
}

func configureProcessBilling(owner *processResourceOwner, cfg *config.Config, opts *BuildOptions) error {
	effective, err := buildProcessBillingRuntime(owner, cfg.Accounting.Billing.ReportsPath, opts.Production)
	if err == nil {
		opts.Production = effective
	}
	return err
}

func buildProcessBillingRuntime(owner *processResourceOwner, cfgReportsPath string, prod ProductionOptions) (ProductionOptions, error) {
	if path := strings.TrimSpace(cfgReportsPath); path != "" && strings.TrimSpace(prod.BillingReportsPath) == "" {
		prod.BillingReportsPath = path
	}
	if !billingCompositionConfigured(prod) {
		return prod, nil
	}
	if err := requireCompleteBillingComposition(prod); err != nil {
		return ProductionOptions{}, err
	}
	if err := validateProviderCostRevisionRuntime(prod); err != nil {
		return ProductionOptions{}, err
	}
	prod.BillingReports = prod.BillingStore
	if closer, ok := prod.BillingTerminalUsageSink.(interface{ Close() error }); ok {
		owner.Own(closer.Close)
	}
	if lifecycle, ok := prod.BillingTerminalUsageSink.(processBillingLifecycle); ok {
		if err := startProcessBillingWorker(owner, lifecycle); err != nil {
			return ProductionOptions{}, fmt.Errorf("runtimebundle: start billing terminal spool: %w", err)
		}
	}
	callUsage, usageOK := prod.BillingStore.(billing.CallUsageStore)
	callSettlement, settlementOK := prod.BillingStore.(billing.CallSettlementStore)
	if !usageOK || !settlementOK || prod.BillingCallRatingResolver == nil {
		return ProductionOptions{}, ErrAuthoritativeBillingRequired
	}
	callWorker, err := billing.NewCallPostUsageWorker(callUsage, callSettlement, prod.BillingCallRatingResolver, prod.BillingPostTurnBatchSize)
	if err != nil {
		return ProductionOptions{}, fmt.Errorf("runtimebundle: complete-call billing worker: %w", err)
	}
	if err := startProcessBillingWorker(owner, callWorker); err != nil {
		return ProductionOptions{}, fmt.Errorf("runtimebundle: start complete-call billing worker: %w", err)
	}
	if err := startEconomicRevisionWorkers(owner, prod); err != nil {
		return ProductionOptions{}, err
	}

	providerWork, providerWorkOK := prod.BillingStore.(billing.ProviderCostWorkReader)
	providerStore, providerStoreOK := prod.BillingStore.(billing.ProviderCostStore)
	if !providerWorkOK || !providerStoreOK || prod.BillingProviderCostResolver == nil {
		return prod, nil // Supplier costing is optional; retail settlement is independent.
	}
	providerWorker, err := billing.NewCallProviderCostWorker(providerWork, providerStore, prod.BillingProviderCostResolver, prod.BillingPostTurnBatchSize)
	if err != nil {
		return ProductionOptions{}, fmt.Errorf("runtimebundle: provider-cost worker: %w", err)
	}
	if err := startProcessBillingWorker(owner, providerWorker); err != nil {
		return ProductionOptions{}, fmt.Errorf("runtimebundle: start provider-cost worker: %w", err)
	}
	return prod, nil
}

func startEconomicRevisionWorkers(owner *processResourceOwner, prod ProductionOptions) error {
	if prod.BillingEconomicRevisionRater == nil {
		return nil
	}
	economicWork, workOK := prod.BillingStore.(billing.EconomicRevisionWorkReader)
	economicResults, resultsOK := prod.BillingStore.(billing.EconomicRevisionResultStore)
	if !workOK || !resultsOK {
		return ErrAuthoritativeBillingRequired
	}
	providerCost, providerCostOK := prod.BillingStore.(billing.ProviderCostRevisionStore)
	if providerCostOK {
		if _, cutoverOK := prod.BillingStore.(billing.ProviderCostWorkCutoverStore); !cutoverOK {
			return fmt.Errorf("%w: %w", ErrAuthoritativeBillingRequired, ErrProviderCostCutoverRequired)
		}
	}
	for _, queue := range []billing.EconomicQueue{billing.EconomicQueueCustomer, billing.EconomicQueueProvider} {
		var economicWorker *billing.EconomicRevisionWorker
		var workerErr error
		if queue == billing.EconomicQueueProvider && providerCostOK {
			economicWorker, workerErr = billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCost(
				economicWork, economicResults, prod.BillingEconomicRevisionRater,
				prod.BillingEconomicRevisionReconciler, providerCost, queue, prod.BillingPostTurnBatchSize,
			)
		} else {
			economicWorker, workerErr = billing.NewEconomicRevisionWorkerWithReconciler(
				economicWork, economicResults, prod.BillingEconomicRevisionRater,
				prod.BillingEconomicRevisionReconciler, queue, prod.BillingPostTurnBatchSize,
			)
		}
		if workerErr != nil {
			return fmt.Errorf("runtimebundle: economic revision worker: %w", workerErr)
		}
		if workerErr := startProcessBillingWorker(owner, economicWorker); workerErr != nil {
			return fmt.Errorf("runtimebundle: start %s economic revision worker: %w", queue, workerErr)
		}
	}
	return nil
}

// validateProviderCostRevisionRuntime rejects a monetary revision writer
// unless the same durable store exposes the cross-path execution/cutover
// fence. Pure economic valuation is intentionally unaffected when no provider
// revision store is present, and legacy-only costing remains compatible.
func validateProviderCostRevisionRuntime(prod ProductionOptions) error {
	if prod.BillingEconomicRevisionRater == nil || prod.BillingStore == nil {
		return nil
	}
	if _, revisionOK := prod.BillingStore.(billing.ProviderCostRevisionStore); !revisionOK {
		return nil
	}
	if _, cutoverOK := prod.BillingStore.(billing.ProviderCostWorkCutoverStore); !cutoverOK {
		return fmt.Errorf("%w: %w", ErrAuthoritativeBillingRequired, ErrProviderCostCutoverRequired)
	}
	return nil
}
