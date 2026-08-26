package leasestore_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/domain"
)

// TestDBParity_SQLite is the canonical parity entry point for concurrency-authority on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()
	runLeaseParitySuite(t, func(t *testing.T) *leaseStoreFactory {
		t.Helper()
		path := filepath.Join(t.TempDir(), "parity.db")
		store := newSQLiteStore(t, path, "parity-sqlite")
		return &leaseStoreFactory{store: store}
	})
}

type leaseStoreFactory struct {
	store interface {
		CheckReadiness(ctx context.Context) (domain.Readiness, error)
	}
}

func runLeaseParitySuite(t *testing.T, newStore func(t *testing.T) *leaseStoreFactory) {
	t.Helper()
	t.Run("CheckReadiness", func(t *testing.T) {
		t.Parallel()
		f := newStore(t)
		ready, err := f.store.CheckReadiness(context.Background())
		if err != nil {
			t.Fatalf("CheckReadiness: %v", err)
		}
		if ready.State != domain.ReadinessStateReady && ready.State != domain.ReadinessStateDegraded {
			t.Fatalf("unexpected readiness state %v", ready.State)
		}
	})
}
