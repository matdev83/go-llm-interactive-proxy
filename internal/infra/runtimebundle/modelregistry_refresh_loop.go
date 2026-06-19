package runtimebundle

import (
	"context"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
)

func runModelRegistryRefreshLoop(
	ctx context.Context,
	rt *modelregistry.Runtime,
	interval time.Duration,
	wg *sync.WaitGroup,
) {
	if ctx == nil || rt == nil || interval <= 0 || wg == nil {
		return
	}
	wg.Go(func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rt.RunRefresh(ctx)
			}
		}
	})
}
