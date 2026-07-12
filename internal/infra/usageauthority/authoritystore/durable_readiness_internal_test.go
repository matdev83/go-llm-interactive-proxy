package authoritystore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
)

func TestDurableStore_ReadinessRecoversAfterTransientPingFailure(t *testing.T) {
	t.Parallel()

	bunDB := openSeedRaceDB(t, filepath.Join(t.TempDir(), "authority-recover.db"))
	store, err := NewDurable(context.Background(), bunDB, Config{
		StoreID: "sqlite-readiness-recover",
		Backing: domain.BackingCapabilityAtomic,
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	store.mu.Lock()
	store.c.state = domain.StatusFromBacking(domain.BackingCapabilityUnavailable)
	store.mu.Unlock()

	status, readErr := store.CheckReadiness(context.Background())
	if readErr != nil {
		t.Fatalf("CheckReadiness after recovery: %v", readErr)
	}
	if status.State != domain.AuthorityStateReady {
		t.Fatalf("CheckReadiness state = %v, want ready after successful ping", status.State)
	}
}

// TestDurableStore_ReadinessPreservesAdvisoryOnlyAfterRecovery covers the
// fail_open posture: production may configure Backing=atomic with
// Readiness=advisory_only. A successful ping must restore the configured
// advisory posture, not promote the store to ready via StatusFromBacking.
func TestDurableStore_ReadinessPreservesAdvisoryOnlyAfterRecovery(t *testing.T) {
	t.Parallel()

	bunDB := openSeedRaceDB(t, filepath.Join(t.TempDir(), "authority-advisory-recover.db"))
	store, err := NewDurable(context.Background(), bunDB, Config{
		StoreID:   "sqlite-readiness-advisory",
		Backing:   domain.BackingCapabilityAtomic,
		Readiness: domain.StatusFromBacking(domain.BackingCapabilityAdvisoryOnly),
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	store.mu.Lock()
	store.c.state = domain.StatusFromBacking(domain.BackingCapabilityUnavailable)
	store.mu.Unlock()

	status, readErr := store.CheckReadiness(context.Background())
	if readErr != nil {
		t.Fatalf("CheckReadiness after recovery: %v", readErr)
	}
	if status.State != domain.AuthorityStateAdvisoryOnly {
		t.Fatalf("CheckReadiness state = %v, want advisory_only (configured posture must not promote to ready)", status.State)
	}
}
