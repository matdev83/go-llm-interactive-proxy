package runtimebundle

import (
	"context"
	"fmt"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// hostBuildInput configures the one-snapshot HostBuilder transaction (Task 5.2).
type hostBuildInput struct {
	ConfigPath              string
	Mandatory               []lipsdk.Requirement
	LogWriter               io.Writer
	StreamRecoveryOverrides config.StreamRecoveryOverrides
	HandlerComposer         HandlerComposer
	Production              ProductionOptions
	MultiUser               *bool
}

type hostBuildJournal struct {
	Acquired []string
	Cleaned  []string
	Loads    int
}

type hostBuildOutcome struct {
	Host     *ReloadHost
	Journal  hostBuildJournal
	Complete bool
}

// buildHost is the per-invocation HostBuilder body (call-scoped loader; no globals).
// Task 5.2 replaces this stub with the real one-snapshot transaction.
func buildHost(ctx context.Context, in hostBuildInput, loadEffective bootstrapEffectiveLoader) (hostBuildOutcome, error) {
	if ctx == nil {
		return hostBuildOutcome{}, fmt.Errorf("runtimebundle: nil context")
	}
	if loadEffective == nil {
		return hostBuildOutcome{}, fmt.Errorf("runtimebundle: nil effective loader")
	}
	_ = in
	return hostBuildOutcome{}, fmt.Errorf("runtimebundle: BuildHost not implemented")
}

func hostIsComplete(out hostBuildOutcome) bool {
	h := out.Host
	return out.Complete && h != nil && h.Coordinator != nil && h.Manager != nil && h.Process != nil && h.Effective != nil && h.Executor != nil
}
