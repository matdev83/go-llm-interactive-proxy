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
