package pluginreg

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/partsnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refparts"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refsubmit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolreactornoop"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"gopkg.in/yaml.v3"
)

func featureSubmitNoop(n yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error) {
	cfg, err := submitnoop.DecodeHookConfig(n)
	if err != nil {
		return hooks.Config{}, nil, err
	}
	var lifes []lipplugin.Lifecycle
	if cfg.LifecycleProbe {
		lifes = append(lifes, &submitnoop.LifecycleProbe{})
	}
	return hooks.Config{SubmitHooks: []sdk.SubmitHook{submitnoop.NewSubmitHookWithConfig(cfg)}}, lifes, nil
}

func featurePartsNoop(n yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error) {
	if err := requireEmptyFeatureYAML(partsnoop.ID, n); err != nil {
		return hooks.Config{}, nil, err
	}
	return hooks.Config{
		RequestPartHooks:  []sdk.RequestPartHook{partsnoop.NewRequestPartHook()},
		ResponsePartHooks: []sdk.ResponsePartHook{partsnoop.NewResponsePartHook()},
	}, nil, nil
}

func featureToolReactorNoop(n yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error) {
	if err := requireEmptyFeatureYAML(toolreactornoop.ID, n); err != nil {
		return hooks.Config{}, nil, err
	}
	return hooks.Config{ToolReactors: []sdk.ToolReactor{toolreactornoop.NewToolReactor()}}, nil, nil
}

func featureRefSubmit(n yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error) {
	cfg, err := refsubmit.DecodeConfig(n)
	if err != nil {
		return hooks.Config{}, nil, err
	}
	return hooks.Config{SubmitHooks: []sdk.SubmitHook{refsubmit.NewHook(cfg)}}, nil, nil
}

func featureRefParts(n yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error) {
	cfg, err := refparts.DecodeConfig(n)
	if err != nil {
		return hooks.Config{}, nil, err
	}
	return hooks.Config{
		RequestPartHooks:  []sdk.RequestPartHook{refparts.NewRequestHook(cfg)},
		ResponsePartHooks: []sdk.ResponsePartHook{refparts.NewResponseHook(cfg)},
	}, nil, nil
}

func featureRefTool(n yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error) {
	cfg, err := reftool.DecodeConfig(n)
	if err != nil {
		return hooks.Config{}, nil, err
	}
	return hooks.Config{ToolReactors: []sdk.ToolReactor{reftool.NewReactor(cfg)}}, nil, nil
}
