package standardplugins

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"gopkg.in/yaml.v3"
)

// ReasoningOutputPreservationFeatureID is the standard factory/instance id for
// reasoning-output-preservation. Any features config row matching this ID (instance)
// or FactoryKind suppresses default standard-distribution injection. A present row
// without enabled: true is an explicit disabled opt-out under plain-bool PluginConfig.Enabled.
const ReasoningOutputPreservationFeatureID = reasoningpreservation.ID

// ReasoningOutputPreservationInjectOpts controls standard-distribution default injection.
type ReasoningOutputPreservationInjectOpts struct {
	StandardDistribution bool
}

// EnsureReasoningOutputPreservationInConfig injects one enabled standard feature row when
// absent on the standard distribution path. Explicit matching rows suppress injection.
func EnsureReasoningOutputPreservationInConfig(cfg *config.Config, opts ReasoningOutputPreservationInjectOpts) error {
	if cfg == nil || !opts.StandardDistribution {
		return nil
	}
	for _, p := range cfg.Plugins.Features {
		if p.FactoryID() == ReasoningOutputPreservationFeatureID || p.InstanceID() == ReasoningOutputPreservationFeatureID {
			return nil
		}
	}
	node, err := standardReasoningPreservationConfigNode()
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

func standardReasoningPreservationConfigNode() (yaml.Node, error) {
	raw := map[string]any{
		"action":              reasoningpreservation.ActionRestore,
		"use_builtin_catalog": true,
		"on_ambiguous":        reasoningpreservation.PolicyLogSkip,
		"on_unrepresentable":  reasoningpreservation.PolicyReject,
		"on_state_error":      reasoningpreservation.PolicyReject,
		"state": map[string]any{
			"ttl":                          "24h",
			"max_turns_per_session":        16,
			"max_reasoning_bytes_per_turn": 65536,
			"max_session_bytes":            262144,
		},
	}
	b, err := yaml.Marshal(raw)
	if err != nil {
		return yaml.Node{}, fmt.Errorf("reasoning-output-preservation defaults: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return yaml.Node{}, fmt.Errorf("reasoning-output-preservation defaults: %w", err)
	}
	node := doc
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return yaml.Node{}, fmt.Errorf("reasoning-output-preservation defaults: empty document")
		}
		node = *doc.Content[0]
	}
	if _, err := reasoningpreservation.DecodeConfig(node); err != nil {
		return yaml.Node{}, fmt.Errorf("reasoning-output-preservation defaults: %w", err)
	}
	return node, nil
}
