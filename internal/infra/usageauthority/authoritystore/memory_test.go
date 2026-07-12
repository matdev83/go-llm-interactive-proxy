package authoritystore_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
)

type memoryFactory struct {
	backing domain.BackingCapability
}

func (f memoryFactory) Build(t *testing.T) app.StateStore {
	t.Helper()
	store := authoritystore.NewMemory(authoritystore.Config{
		StoreID:   "memory-test",
		Backing:   f.backing,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	})
	return store
}

func TestMemoryStore_Contract(t *testing.T) {
	t.Parallel()
	contract.RunSuite(t, memoryFactory{backing: domain.BackingCapabilityAtomic})
}

func TestMemoryStore_ReadinessStates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		backing domain.BackingCapability
		state   domain.AuthorityState
	}{
		{name: "disabled", backing: domain.BackingCapabilityDisabled, state: domain.AuthorityStateDisabled},
		{name: "ready", backing: domain.BackingCapabilityAtomic, state: domain.AuthorityStateReady},
		{name: "degraded", backing: domain.BackingCapabilityDegraded, state: domain.AuthorityStateDegraded},
		{name: "unavailable", backing: domain.BackingCapabilityUnavailable, state: domain.AuthorityStateUnavailable},
		{name: "advisory_only", backing: domain.BackingCapabilityAdvisoryOnly, state: domain.AuthorityStateAdvisoryOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := authoritystore.NewMemory(authoritystore.Config{
				StoreID:   "memory-" + tc.name,
				Backing:   tc.backing,
				LimitRows: contract.SeededLimitRows(),
			})
			got, err := store.CheckReadiness(context.Background())
			if err != nil {
				t.Fatalf("CheckReadiness() error = %v", err)
			}
			if got.State != tc.state {
				t.Fatalf("CheckReadiness() state = %v, want %v", got.State, tc.state)
			}
		})
	}
}
