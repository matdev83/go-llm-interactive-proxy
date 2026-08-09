package standardplugins

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"gopkg.in/yaml.v3"
)

// ReasoningOutputPreservationFeatureID is the standard factory/instance id for
// reasoning-output-preservation. A matching disabled row is an explicit opt-out.
const ReasoningOutputPreservationFeatureID = reasoningpreservation.ID

// ReasoningOutputPreservationInjectOpts controls standard-distribution default injection.
type ReasoningOutputPreservationInjectOpts struct {
	StandardDistribution bool
}

const openAICodexFactory = "openai-codex"

// EnsureReasoningOutputPreservationInConfig discovers eligible direct Codex
// instances, then delegates feature-schema mutation to the feature owner.
func EnsureReasoningOutputPreservationInConfig(cfg *config.Config, opts ReasoningOutputPreservationInjectOpts) error {
	if cfg == nil || !opts.StandardDistribution {
		return nil
	}
	featureIndex := -1
	for i, row := range cfg.Plugins.Features {
		if row.FactoryID() != ReasoningOutputPreservationFeatureID && row.InstanceID() != ReasoningOutputPreservationFeatureID {
			continue
		}
		if !row.Enabled {
			return nil
		}
		if featureIndex < 0 {
			featureIndex = i
		}
	}
	backends, err := codexCompanionBackends(cfg.Plugins.Backends)
	if err != nil {
		return err
	}
	if len(backends) == 0 {
		return nil
	}

	if featureIndex < 0 {
		node, err := reasoningpreservation.NewCodexCompanionConfig(backends)
		if err != nil {
			return err
		}
		cfg.Plugins.Features = append(cfg.Plugins.Features, config.PluginConfig{
			ID:      ReasoningOutputPreservationFeatureID,
			Enabled: true,
			Config:  node,
		})
		return nil
	}

	row := &cfg.Plugins.Features[featureIndex]
	row.Config, err = reasoningpreservation.EnsureCodexCompanionRules(row.Config, backends)
	if err != nil {
		return fmt.Errorf("reasoning-output-preservation config: %w", err)
	}
	return nil
}

type nativeContextWire struct {
	Enabled *bool `yaml:"enabled"`
}

type codexBackendWire struct {
	NativeContext *nativeContextWire `yaml:"native_context"`
}

func codexCompanionBackends(rows []config.PluginConfig) ([]string, error) {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled || strings.TrimSpace(row.FactoryID()) != openAICodexFactory {
			continue
		}
		nativeEnabled, err := nativeContextEnabled(row.Config)
		if err != nil {
			return nil, fmt.Errorf("backend %q native_context: %w", row.InstanceID(), err)
		}
		if !nativeEnabled || row.InstanceID() == "" {
			continue
		}
		out = append(out, row.InstanceID())
	}
	return out, nil
}

func nativeContextEnabled(n yaml.Node) (bool, error) {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return true, nil
		}
		root = *root.Content[0]
	}
	if root.Kind == 0 {
		return true, nil
	}
	var wire codexBackendWire
	if err := root.Decode(&wire); err != nil {
		return false, err
	}
	if wire.NativeContext == nil || wire.NativeContext.Enabled == nil {
		return true, nil
	}
	return *wire.NativeContext.Enabled, nil
}
