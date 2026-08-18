package runtimebundle

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingspool"
)

type processBillingLifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
}

func startProcessBillingWorker(owner *processResourceOwner, worker processBillingLifecycle) error {
	owner.Own(func() error { return worker.Stop(context.Background()) })
	return worker.Start(context.Background())
}

func configureProcessBilling(owner *processResourceOwner, parent context.Context, cfg *config.Config, opts *BuildOptions) error {
	effective, err := buildProcessBillingRuntime(owner, parent, cfg.Accounting.Billing.ReportsPath, cfg.Accounting.Billing.SpoolPath, opts.Production)
	if err != nil {
		return err
	}
	opts.Production = effective
	return nil
}
func buildProcessBillingRuntime(owner *processResourceOwner, openCtx context.Context, cfgReportsPath, cfgSpoolPath string, prod ProductionOptions) (ProductionOptions, error) {
	spoolOwned := false
	if path := strings.TrimSpace(cfgReportsPath); path != "" && strings.TrimSpace(prod.BillingReportsPath) == "" {
		prod.BillingReportsPath = path
	}
	if !billingCompositionConfigured(prod) {
		return prod, nil
	}
	if prod.BillingTerminalUsageSink == nil && strings.TrimSpace(cfgSpoolPath) != "" {
		if err := billingspool.ValidateStablePath(cfgSpoolPath); err != nil {
			return ProductionOptions{}, err
		}
		central, ok := prod.BillingStore.(billing.TerminalUsageSink)
		if !ok {
			return ProductionOptions{}, fmt.Errorf("%w: terminal spool central sink", ErrAuthoritativeBillingRequired)
		}
		spool, err := billingspool.Open(openCtx, billingspool.Config{Path: strings.TrimSpace(cfgSpoolPath)}, central)
		if err != nil {
			return ProductionOptions{}, fmt.Errorf("runtimebundle: open billing terminal spool: %w", err)
		}
		prod.BillingTerminalUsageSink = spool
		owner.Own(spool.Close)
		spoolOwned = true
	}
	if err := requireCompleteBillingComposition(prod); err != nil {
		return ProductionOptions{}, err
	}
	prod.BillingReports = prod.BillingStore
	if closer, ok := prod.BillingTerminalUsageSink.(interface{ Close() error }); ok && !spoolOwned {
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

	providerWork, providerWorkOK := prod.BillingStore.(billing.ProviderCostWorkReader)
	providerStore, providerStoreOK := prod.BillingStore.(billing.ProviderCostStore)
	if !providerWorkOK || !providerStoreOK || prod.BillingProviderCostResolver == nil {
		return ProductionOptions{}, ErrAuthoritativeBillingRequired
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
