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
// Legacy BootstrapResult path retained for inspect/check-config callers until Tasks 5.3-5.5.
func serveStartupRollback(
	ctx context.Context,
	res *runtimebundle.BootstrapResult,
	host beginShutdowner,
	mgmt contextShutdowner,
) error {
	if host != nil {
		host.BeginShutdown()
	}
	var mgr *runtimehost.Manager
	var ps *runtimebundle.ProcessServices
	if res != nil {
		mgr = res.GenerationManager
		ps = res.ProcessServices
	}
	return serveRollbackCore(ctx, mgr, ps, mgmt)
}

// closeServeHostAfterBuild is the post-BuildHost startup failure path (req 4.8):
// close any separate management resource, then invoke Host.Close once. Callers
// must not reconstruct Manager/Process ownership or invoke ShutdownTracing here.
func closeServeHostAfterBuild(ctx context.Context, host *runtimebundle.Host, mgmt contextShutdowner) error {
	var out error
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bootstrapTracingShutdownTimeout)
	defer cancel()
	if mgmt != nil {
		if err := mgmt.Shutdown(cleanupCtx); err != nil {
			out = errors.Join(out, err)
		}
	}
	if host != nil {
		if err := host.Close(cleanupCtx); err != nil {
			out = errors.Join(out, err)
		}
	}
	return out
}

// serveRollbackCore is the shared ownership-safe teardown body for legacy
// BootstrapResult callers (serveStartupRollback). Order: retire generations,
// close management if started, close process services only when no generation
// remains. Tracing stays last via the caller's deferBootstrapTracingShutdown.
func serveRollbackCore(
	ctx context.Context,
	mgr *runtimehost.Manager,
	ps *runtimebundle.ProcessServices,
	mgmt contextShutdowner,
) error {
	var out error
	cleanupCtx := context.WithoutCancel(ctx)
	if mgr != nil {
		if err := retireServeGenerations(cleanupCtx, mgr); err != nil {
			out = errors.Join(out, err)
		}
	}
	if mgmt != nil {
		if err := mgmt.Shutdown(cleanupCtx); err != nil {
			out = errors.Join(out, err)
		}
	}
	if ps != nil {
		if mgr == nil || !mgr.HasOpenGenerations() {
			if err := closeServeProcessServices(ps); err != nil {
				out = errors.Join(out, err)
			}
		}
	}
	return out
}
