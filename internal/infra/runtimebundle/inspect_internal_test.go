package runtimebundle

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func dogfoodInspectPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")
}

func countingLoader(inner bootstrapEffectiveLoader, n *atomic.Int64) bootstrapEffectiveLoader {
	return func(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, config.StreamRecoveryOverrides, error) {
		n.Add(1)
		return inner(ctx, path, cliOverrides)
	}
}

func TestInspectRoutes_OneStrictEffectiveLoad(t *testing.T) {
	t.Parallel()
	var loads atomic.Int64
	snap, err := inspectRoutes(t.Context(), InspectInput{
		ConfigPath: dogfoodInspectPath(t),
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	}, countingLoader(LoadBootstrapEffectiveWithSource, &loads))
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loads=%d want 1", loads.Load())
	}
	if snap.EffectiveDefaultRoute == "" {
		t.Fatal("expected effective default route")
	}
}

func TestInspectInventory_OneStrictEffectiveLoad(t *testing.T) {
	t.Parallel()
	var loads atomic.Int64
	snap, err := inspectInventory(t.Context(), InspectInput{
		ConfigPath: dogfoodInspectPath(t),
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	}, countingLoader(LoadBootstrapEffectiveWithSource, &loads))
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loads=%d want 1", loads.Load())
	}
	if len(snap.Frontends) == 0 {
		t.Fatal("expected frontend rows")
	}
}

func TestInspect_ConcurrentRepeatedNoSharedMutableLeak(t *testing.T) {
	t.Parallel()
	path := dogfoodInspectPath(t)
	in := InspectInput{ConfigPath: path, Mandatory: lipsdk.StandardDistributionRequirements()}
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers*2)
	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			var loads atomic.Int64
			_, err := inspectRoutes(context.Background(), in, countingLoader(LoadBootstrapEffectiveWithSource, &loads))
			if err != nil {
				errs <- err
				return
			}
			if loads.Load() != 1 {
				errs <- fmt.Errorf("routes loads=%d want 1", loads.Load())
			}
		}()
		go func() {
			defer wg.Done()
			var loads atomic.Int64
			_, err := inspectInventory(context.Background(), in, countingLoader(LoadBootstrapEffectiveWithSource, &loads))
			if err != nil {
				errs <- err
				return
			}
			if loads.Load() != 1 {
				errs <- fmt.Errorf("inventory loads=%d want 1", loads.Load())
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestInspect_NilContextAndEmptyPath(t *testing.T) {
	t.Parallel()
	_, err := InspectRoutes(nil, InspectInput{ConfigPath: dogfoodInspectPath(t)}) //nolint:staticcheck
	if err == nil {
		t.Fatal("expected nil context error")
	}
	_, err = InspectInventory(t.Context(), InspectInput{})
	if err == nil {
		t.Fatal("expected empty path error")
	}
}
