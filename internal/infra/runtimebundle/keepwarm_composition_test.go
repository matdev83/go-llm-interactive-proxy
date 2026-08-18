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
	if _, ok := production.KeepwarmAccounting.(*billingcompose.DurableMaintenanceObserver); !ok {
		t.Fatalf("KeepwarmAccounting = %T, want durable observer", production.KeepwarmAccounting)
	}
}

func TestComposeBillingPreservesKeepwarmAccountingObserver(t *testing.T) {
	t.Parallel()

	input, _, _, _ := validComposeInput(t)
	observer := &composedMaintenanceObserver{}
	input.KeepwarmAccounting = observer

	production, err := runtimebundle.ComposeBilling(input)
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	if production.KeepwarmAccounting != observer {
		t.Fatalf("KeepwarmAccounting = %T, want the injected observer instance", production.KeepwarmAccounting)
	}
}
