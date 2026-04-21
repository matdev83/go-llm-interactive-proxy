package main

import (
	"fmt"

	corehooks "github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/partsnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolreactornoop"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// featureHooksFromRegistrations maps enabled feature plugin registrations to hook chains.
// Unknown enabled feature IDs are rejected so typos fail at startup.
func featureHooksFromRegistrations(reg []lipsdk.Registration) (corehooks.Config, error) {
	var cfg corehooks.Config
	for _, r := range reg {
		if r.Kind != lipsdk.PluginKindFeature || !r.Enabled {
			continue
		}
		switch r.ID {
		case submitnoop.ID:
			cfg.SubmitHooks = append(cfg.SubmitHooks, submitnoop.NewSubmitHook())
		case partsnoop.ID:
			cfg.RequestPartHooks = append(cfg.RequestPartHooks, partsnoop.NewRequestPartHook())
			cfg.ResponsePartHooks = append(cfg.ResponsePartHooks, partsnoop.NewResponsePartHook())
		case toolreactornoop.ID:
			cfg.ToolReactors = append(cfg.ToolReactors, toolreactornoop.NewToolReactor())
		default:
			return corehooks.Config{}, fmt.Errorf("lipstd: unknown enabled feature plugin %q", r.ID)
		}
	}
	return cfg, nil
}
