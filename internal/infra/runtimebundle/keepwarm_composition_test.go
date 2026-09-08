package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

type composedMaintenanceObserver struct{}

func (*composedMaintenanceObserver) ObserveProviderMaintenance(context.Context, billing.ProviderMaintenanceUsage) error {
	return nil
}

func TestComposeBillingBuildsDurableKeepwarmAccountingObserver(t *testing.T) {
	t.Parallel()

	input, _, _, _ := validComposeInput(t)
	production, err := runtimebundle.ComposeBilling(input)
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	if _, ok := production.MaintenanceAccounting.(*billingcompose.DurableMaintenanceObserver); !ok {
		t.Fatalf("MaintenanceAccounting = %T, want durable observer", production.MaintenanceAccounting)
	}
}

func TestComposeBillingPreservesKeepwarmAccountingObserver(t *testing.T) {
	t.Parallel()

	input, _, _, _ := validComposeInput(t)
	observer := &composedMaintenanceObserver{}
	input.MaintenanceAccounting = observer

	production, err := runtimebundle.ComposeBilling(input)
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	if production.MaintenanceAccounting != observer {
		t.Fatalf("MaintenanceAccounting = %T, want the injected observer instance", production.MaintenanceAccounting)
	}
}
