package main

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

type beginShutdowner interface {
	BeginShutdown()
}

type contextShutdowner interface {
	Shutdown(context.Context) error
}

// closeServeProcessServices closes process services during pre-serve rollback.
// Overridable in tests for exact-close-order assertions.
var closeServeProcessServices = func(ps *runtimebundle.ProcessServices) error {
	if ps == nil {
		return nil
	}
	return ps.Close()
}

// retireServeGenerations retires generation resources during pre-serve rollback.
// Overridable in tests for exact-close-order assertions.
var retireServeGenerations = func(ctx context.Context, m *runtimehost.Manager) error {
	if m == nil {
		return nil
	}
	return m.ShutdownDetached(ctx, runtimehost.NewLifecycleWorker())
}

// serveStartupRollback performs ownership-safe teardown when AttachReloadHost or
// management startup fails after bootstrap and before listen. Order:
//  1. coordinator BeginShutdown (when available)
//  2. retire generations
//  3. close management if started
//  4. close process services only when no generation remains
//
// Tracing stays last via the caller's deferBootstrapTracingShutdown.
// Cleanup errors are joined; callers must join them with the primary startup error.
func serveStartupRollback(
	ctx context.Context,
	res *runtimebundle.BootstrapResult,
	host beginShutdowner,
	mgmt contextShutdowner,
) error {
	var out error
	if host != nil {
		host.BeginShutdown()
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if res != nil && res.GenerationManager != nil {
		if err := retireServeGenerations(cleanupCtx, res.GenerationManager); err != nil {
			out = errors.Join(out, err)
		}
	}
	if mgmt != nil {
		if err := mgmt.Shutdown(cleanupCtx); err != nil {
			out = errors.Join(out, err)
		}
	}
	if res != nil && res.ProcessServices != nil {
		if res.GenerationManager == nil || !res.GenerationManager.HasOpenGenerations() {
			if err := closeServeProcessServices(res.ProcessServices); err != nil {
				out = errors.Join(out, err)
			}
		}
	}
	return out
}
