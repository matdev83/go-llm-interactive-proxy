package main

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

const bootstrapTracingShutdownTimeout = 12 * time.Second

// deferBootstrapTracingShutdown runs bounded OpenTelemetry shutdown after a successful
// [runtimebundle.BuildBootstrap]. logCtx is used only for structured logging (WarnContext);
// shutdown itself uses a fresh timeout context so it can complete after the caller context
// is cancelled (e.g. SIGINT during serve).
func deferBootstrapTracingShutdown(logCtx context.Context, res *runtimebundle.BootstrapResult) {
	if res == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), bootstrapTracingShutdownTimeout)
	defer cancel()
	if res.ShutdownTracing == nil {
		return
	}
	if err := res.ShutdownTracing(shutdownCtx); err != nil && res.Logger != nil {
		res.Logger.WarnContext(logCtx, "lipstd: tracing shutdown", "error", err)
	}
}

// deferHostTracingShutdown runs bounded tracing shutdown for the normal serve
// return path after RunWithGenerationHost has already retired manager/process.
// Pre-listen failures must use closeServeHostAfterBuild (Host.Close) instead and
// skip this defer so tracing shuts down exactly once.
func deferHostTracingShutdown(logCtx context.Context, host *runtimebundle.Host) {
	if host == nil || host.ShutdownTracing == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), bootstrapTracingShutdownTimeout)
	defer cancel()
	fn := host.ShutdownTracing
	host.ShutdownTracing = nil
	if err := fn(shutdownCtx); err != nil && host.Logger != nil {
		host.Logger.WarnContext(logCtx, "lipstd: tracing shutdown", "error", err)
	}
}
