package modelregistry_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestRuntime_StartLoadsValidCacheWithoutRemoteFetch(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{load: modelregistry.Snapshot{
		Generation:  "cached-generation",
		RefreshedAt: time.Unix(100, 0).UTC(),
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-cached",
			NativeID:    "gpt-cached",
			BackendID:   "remote-backend",
			Kind:        "openai-responses",
			Source:      modelinventory.SourceRemote,
			LoadedAt:    time.Unix(100, 0).UTC(),
		}},
	}}
	provider := &countingInventoryProvider{err: errors.New("remote must not be called")}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "remote-backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: cache,
		Now:   func() time.Time { return time.Unix(200, 0).UTC() },
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.Calls())
	}
	got, ok := rt.Lookup("openai/gpt-cached")
	if !ok || len(got) != 1 || got[0].BackendID != "remote-backend" {
		t.Fatalf("Lookup cached = %+v, %v", got, ok)
	}
	if cache.saves != 0 {
		t.Fatalf("cache saves = %d, want 0", cache.saves)
	}
}

func TestRuntime_StartIgnoresCacheWithUnconfiguredBackendID(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{load: modelregistry.Snapshot{
		Generation: "stale-generation",
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-stale",
			NativeID:    "gpt-stale",
			BackendID:   "removed-backend",
			Kind:        "openai-responses",
		}},
	}}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "remote-backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	if _, ok := rt.Lookup("openai/gpt-stale"); ok {
		t.Fatal("stale cached backend should not be published")
	}
	if got, ok := rt.Lookup("openai/gpt-remote"); !ok || len(got) != 1 || got[0].BackendID != "remote-backend" {
		t.Fatalf("remote lookup = %+v, %v", got, ok)
	}
}

func TestRuntime_StartColdLoadsRemoteAndSavesCache(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
		DisplayName: "Remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "remote-backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: cache,
		Now:   func() time.Time { return time.Unix(300, 0).UTC() },
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	if cache.saves != 1 {
		t.Fatalf("cache saves = %d, want 1", cache.saves)
	}
	got, ok := rt.Lookup("openai/gpt-remote")
	if !ok || len(got) != 1 || got[0].BackendID != "remote-backend" {
		t.Fatalf("Lookup remote = %+v, %v", got, ok)
	}
}

func TestRuntime_RefreshFailureKeepsPriorSuccessfulRegistry(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-before",
		NativeID:    "gpt-before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
		Now:   func() time.Time { return time.Unix(400, 0).UTC() },
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	provider.SetError(errors.New("upstream unavailable"))
	provider.SetModels(nil)
	rt.RunRefresh(context.Background())

	if got, ok := rt.Lookup("openai/gpt-before"); !ok || len(got) != 1 {
		t.Fatalf("prior lookup = %+v, %v", got, ok)
	}
	if _, ok := rt.Lookup("openai/gpt-after"); ok {
		t.Fatal("unexpected new model after failed refresh")
	}
	if rt.LastRefreshFailure() != modelregistry.RefreshFailureFetch {
		t.Fatalf("LastRefreshFailure = %q", rt.LastRefreshFailure())
	}
}

func TestRuntime_StartInvalidCacheFallsBackToRemote(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{load: modelregistry.Snapshot{
		Generation: "invalid",
		Models: []modelregistry.BackendModel{{
			CanonicalID: "not-canonical",
			NativeID:    "x",
			BackendID:   "cached",
			Kind:        "test",
		}},
	}}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "remote-backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	if got, ok := rt.Lookup("openai/gpt-remote"); !ok || len(got) != 1 {
		t.Fatalf("remote lookup = %+v, %v", got, ok)
	}
	if rt.LastCacheFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastCacheFailure = %q, want none", rt.LastCacheFailure())
	}
	diag := rt.Diagnostics()
	if diag.LastCacheErrorCategory != modelregistry.RefreshFailureNone {
		t.Fatalf("Diagnostics LastCacheErrorCategory = %q, want none", diag.LastCacheErrorCategory)
	}
	if diag.LastRefreshErrorCategory != modelregistry.RefreshFailureNone {
		t.Fatalf("Diagnostics LastRefreshErrorCategory = %q, want none", diag.LastRefreshErrorCategory)
	}
}

func TestRuntime_StartCacheLoadErrorFallsBackToRemoteAndReportsCacheFailure(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{loadErr: errors.New("permission denied")}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "remote-backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	if got, ok := rt.Lookup("openai/gpt-remote"); !ok || len(got) != 1 {
		t.Fatalf("remote lookup = %+v, %v", got, ok)
	}
	if rt.LastCacheFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastCacheFailure = %q, want none", rt.LastCacheFailure())
	}
}

func TestRuntime_SuccessfulRefreshClearsPriorCacheFailure(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{loadErr: errors.New("permission denied")}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "remote-backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt.LastCacheFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastCacheFailure after fallback = %q, want none", rt.LastCacheFailure())
	}

	cache.loadErr = nil
	rt.RunRefresh(context.Background())

	if rt.LastCacheFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastCacheFailure after successful refresh = %q, want none", rt.LastCacheFailure())
	}
	diag := rt.Diagnostics()
	if diag.LastCacheErrorCategory != modelregistry.RefreshFailureNone {
		t.Fatalf("Diagnostics LastCacheErrorCategory = %q, want none", diag.LastCacheErrorCategory)
	}
}

func TestRuntime_CacheSaveFailurePublishesRefreshedRegistry(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{
		loadErr: modelregistry.ErrSnapshotUnavailable,
	}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-before",
		NativeID:    "gpt-before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: cache,
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	cache.saveErr = errors.New("disk full")
	provider.SetModels([]modelinventory.Model{{
		CanonicalID: "openai/gpt-after",
		NativeID:    "gpt-after",
	}})
	rt.RunRefresh(context.Background())

	if got, ok := rt.Lookup("openai/gpt-after"); !ok || len(got) != 1 {
		t.Fatalf("refreshed lookup = %+v, %v", got, ok)
	}
	if rt.LastRefreshFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastRefreshFailure = %q", rt.LastRefreshFailure())
	}
	if rt.LastCacheFailure() != modelregistry.RefreshFailureCache {
		t.Fatalf("LastCacheFailure = %q", rt.LastCacheFailure())
	}
}

func TestRuntime_StartColdPublishesRemoteWhenCacheSaveFails(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{
		loadErr: modelregistry.ErrSnapshotUnavailable,
		saveErr: errors.New("disk full"),
	}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := rt.Lookup("openai/gpt-remote"); !ok || len(got) != 1 {
		t.Fatalf("remote lookup = %+v, %v", got, ok)
	}
	if rt.LastRefreshFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastRefreshFailure = %q", rt.LastRefreshFailure())
	}
	if rt.LastCacheFailure() != modelregistry.RefreshFailureCache {
		t.Fatalf("LastCacheFailure = %q", rt.LastCacheFailure())
	}
}

func TestRuntime_DiagnosticsReportsActiveRegistryAndFailure(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-before",
		NativeID:    "gpt-before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
		Now:   func() time.Time { return time.Unix(700, 0).UTC() },
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.SetError(errors.New("upstream unavailable"))
	rt.RunRefresh(context.Background())

	diag := rt.Diagnostics()
	if !diag.Active {
		t.Fatal("expected active diagnostics")
	}
	if diag.ModelCount != 1 {
		t.Fatalf("ModelCount = %d, want 1", diag.ModelCount)
	}
	if diag.BackendModelCounts["backend"] != 1 {
		t.Fatalf("BackendModelCounts = %+v", diag.BackendModelCounts)
	}
	if diag.LastRefreshErrorCategory != modelregistry.RefreshFailureFetch {
		t.Fatalf("LastRefreshErrorCategory = %q", diag.LastRefreshErrorCategory)
	}
}

func TestRuntime_ConcurrentLookupDuringRefresh(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-initial",
		NativeID:    "gpt-initial",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "backend",
			Kind:      "openai-responses",
			Provider:  provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 200 {
				_, _ = rt.Lookup("openai/gpt-initial")
				_ = rt.All()
			}
		})
	}
	for range 20 {
		rt.RunRefresh(context.Background())
	}
	wg.Wait()
}

type fakeModelRegistryCache struct {
	load    modelregistry.Snapshot
	loadErr error
	saveErr error
	saves   int
	saved   modelregistry.Snapshot
}

func (c *fakeModelRegistryCache) Load(context.Context) (modelregistry.Snapshot, error) {
	if c.loadErr != nil {
		return modelregistry.Snapshot{}, c.loadErr
	}
	return c.load, nil
}

func (c *fakeModelRegistryCache) Save(_ context.Context, snap modelregistry.Snapshot) error {
	if c.saveErr != nil {
		return c.saveErr
	}
	c.saves++
	c.saved = snap
	return nil
}

type countingInventoryProvider struct {
	mu     sync.Mutex
	calls  int
	err    error
	models []modelinventory.Model
}

func (p *countingInventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return modelinventory.Snapshot{}, p.err
	}
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Unix(500, 0).UTC(),
		Models:   p.models,
	}, nil
}

func (p *countingInventoryProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *countingInventoryProvider) SetModels(models []modelinventory.Model) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = models
}

func (p *countingInventoryProvider) SetError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}
