package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

func hostServeCleanup(t *testing.T, host *runtimebundle.Host) {
	t.Helper()
	t.Cleanup(func() {
		if host != nil {
			_ = host.Close(context.Background())
		}
	})
}

func compileCandidateAfterHost(t *testing.T, host *runtimebundle.Host) *runtimebundle.CandidateRuntime {
	t.Helper()
	if host == nil || host.Process == nil {
		t.Fatal("expected Process after BuildHost")
	}
	cand, err := runtimebundle.CompileCandidate(t.Context(), runtimebundle.GenerationCompileInput{
		Process: host.Process,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	t.Cleanup(func() { _ = cand.Close() })
	return cand
}
