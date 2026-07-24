package runtimebundle

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
)

// Test-facing HostBuilder outcome types (Task 5.1/5.2 RED matrices).

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

func hostIsComplete(out hostBuildOutcome) bool {
	h := out.Host
	return out.Complete && h != nil && h.Coordinator != nil && h.Manager != nil && h.Process != nil && h.Effective != nil && h.Executor != nil
}

func buildHostOutcome(ctx context.Context, in hostBuildInput, loadEffective bootstrapEffectiveLoader) (hostBuildOutcome, error) {
	loads := 0
	wrapped := loadEffective
	if loadEffective != nil {
		inner := loadEffective
		wrapped = func(ctx context.Context, path string, cli config.StreamRecoveryOverrides) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, config.StreamRecoveryOverrides, error) {
			loads++
			return inner(ctx, path, cli)
		}
	}
	host, err := buildHost(ctx, in, wrapped, nil)
	if err != nil {
		return hostBuildOutcome{Journal: hostBuildJournal{Loads: loads}}, err
	}
	return hostBuildOutcome{
		Host:     host,
		Journal:  hostBuildJournal{Loads: loads},
		Complete: true,
	}, nil
}
