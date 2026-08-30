package contract_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
)

type concurrencyTrackingFactory struct {
	allowParallel bool
	active        atomic.Int32
	maxConcurrent atomic.Int32
}

func (f *concurrencyTrackingFactory) ParallelContract() bool {
	return f.allowParallel
}

func (f *concurrencyTrackingFactory) Build(t *testing.T) app.StateStore {
	t.Helper()
	cur := f.active.Add(1)
	for {
		old := f.maxConcurrent.Load()
		if cur <= old || f.maxConcurrent.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(15 * time.Millisecond)
	defer f.active.Add(-1)

	return authoritystore.NewMemory(authoritystore.Config{
		StoreID:   "tracking-store",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	})
}

func TestRunSuite_OptOutParallelism_NeverConcurrentBuild(t *testing.T) {
	t.Parallel()
	factory := &concurrencyTrackingFactory{allowParallel: false}
	t.Cleanup(func() {
		if max := factory.maxConcurrent.Load(); max != 1 {
			t.Fatalf("max concurrent Build calls = %d, want 1 (strictly serial builds)", max)
		}
	})
	contract.RunSuite(t, factory)
}
