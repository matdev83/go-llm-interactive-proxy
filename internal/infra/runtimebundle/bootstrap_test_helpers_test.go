package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
)

func bootstrapServeCleanup(t *testing.T, res runtimebundle.BootstrapResult) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if res.GenerationManager != nil {
			_ = res.GenerationManager.ShutdownDetached(ctx, runtimehost.NewLifecycleWorker())
		}
		if res.ProcessServices != nil {
			_ = res.ProcessServices.Close()
		}
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(ctx)
		}
	})
}

func compileCandidateAfterBootstrap(t *testing.T, res runtimebundle.BootstrapResult) *runtimebundle.CandidateRuntime {
	t.Helper()
	if res.ProcessServices == nil {
		t.Fatal("expected ProcessServices after BootstrapServe")
	}
	cand, err := runtimebundle.CompileCandidate(t.Context(), runtimebundle.GenerationCompileInput{
		Process: res.ProcessServices,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	t.Cleanup(func() { _ = cand.Close() })
	return cand
}
