package main

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

type contextShutdowner interface {
	Shutdown(context.Context) error
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
