package billingcompose_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
)

type mockMaintenanceStore struct {
	billing.AuthoritativeBilling
	appended []billing.ProviderMaintenanceUsage
}

func (m *mockMaintenanceStore) AppendProviderMaintenance(ctx context.Context, usage billing.ProviderMaintenanceUsage) error {
	m.appended = append(m.appended, usage)
	return nil
}

type nonMaintenanceStore struct {
	billing.AuthoritativeBilling
}

func TestDurableMaintenanceObserver_AppendsUsage(t *testing.T) {
	t.Parallel()

	store := &mockMaintenanceStore{}
	obs := billingcompose.NewDurableMaintenanceObserver(store)

	usage := billing.ProviderMaintenanceUsage{
		OperationID: "op-1",
		ALegID:      "aleg-1",
		TargetID:    "target-1",
		BackendID:   "backend-1",
		ModelID:     "model-1",
		RecordedAt:  time.Now().UTC(),
	}

	if err := obs.ObserveProviderMaintenance(context.Background(), usage); err != nil {
		t.Fatalf("ObserveProviderMaintenance failed: %v", err)
	}

	if len(store.appended) != 1 {
		t.Fatalf("expected 1 appended usage, got %d", len(store.appended))
	}
	if store.appended[0].OperationID != "op-1" {
		t.Fatalf("expected OperationID op-1, got %s", store.appended[0].OperationID)
	}
}

func TestDurableMaintenanceObserver_NilStoreReturnsError(t *testing.T) {
	t.Parallel()

	obs := billingcompose.NewDurableMaintenanceObserver(nil)
	if err := obs.ObserveProviderMaintenance(context.Background(), billing.ProviderMaintenanceUsage{}); err == nil {
		t.Fatal("expected error on nil store, got nil")
	}

	var nilObs *billingcompose.DurableMaintenanceObserver
	if err := nilObs.ObserveProviderMaintenance(context.Background(), billing.ProviderMaintenanceUsage{}); err == nil {
		t.Fatal("expected error on nil observer, got nil")
	}
}

func TestComposeMaintenanceAccounting_InjectedPreserved(t *testing.T) {
	t.Parallel()

	injected := billing.ProviderMaintenanceUsageObserverFunc(func(context.Context, billing.ProviderMaintenanceUsage) error {
		return nil
	})

	got, err := billingcompose.ComposeMaintenanceAccounting(nil, injected)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil observer")
	}
}

func TestComposeMaintenanceAccounting_CreatesDurableObserver(t *testing.T) {
	t.Parallel()

	store := &mockMaintenanceStore{}
	got, err := billingcompose.ComposeMaintenanceAccounting(store, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := got.(*billingcompose.DurableMaintenanceObserver); !ok {
		t.Fatalf("expected *DurableMaintenanceObserver, got %T", got)
	}
}

func TestComposeMaintenanceAccounting_RejectsNonMaintenanceStore(t *testing.T) {
	t.Parallel()

	store := &nonMaintenanceStore{}
	_, err := billingcompose.ComposeMaintenanceAccounting(store, nil)
	if err == nil {
		t.Fatal("expected error for non-maintenance store, got nil")
	}
}
