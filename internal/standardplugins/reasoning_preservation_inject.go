package standardplugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"gopkg.in/yaml.v3"
)

// ReasoningOutputPreservationFeatureID is the standard factory/instance id for
// reasoning-output-preservation. A matching disabled row is an explicit opt-out.
const ReasoningOutputPreservationFeatureID = reasoningpreservation.ID

// ReasoningOutputPreservationInjectOpts controls standard-distribution default injection.
type ReasoningOutputPreservationInjectOpts struct {
	StandardDistribution bool
}

const (
	openAICodexFactory       = "openai-codex"
	codexCompanionRulePrefix = "codex-native-context-"
	ContinuityMarkerKey      = "lip.internal.openai_codex.reasoning_continuity.v1"
	ContinuityMarkerValue    = `{"eligible":true,"dialect":"openai.responses.reasoning_item.v1"}`
)

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
		node, err := reasoningpreservation.NewCompanionConfig(backends, codexCompanionRulePrefix)
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
	row.Config, err = reasoningpreservation.EnsureCompanionRules(row.Config, backends, codexCompanionRulePrefix)
	if err != nil {
		return fmt.Errorf("reasoning-output-preservation config: %w", err)
	}
	return nil
}

func codexCompanionPolicy() reasoningpreservation.CompanionPolicy {
	return reasoningpreservation.CompanionPolicy{
		BeforeMatch: func(call *lipapi.Call, _ request.AttemptMeta) {
			if call != nil && call.Extensions != nil {
				delete(call.Extensions, ContinuityMarkerKey)
			}
		},
		AfterRestore: func(_ context.Context, call *lipapi.Call, meta request.AttemptMeta, match reasoningpreservation.MatchResult, res reasoningpreservation.RestoreResult) {
			if call == nil || match.Kind == reasoningpreservation.MatchNone || !safeCodexOutcome(res.Outcomes) || !codexIdentity(meta, call) {
				return
			}
			if call.Extensions == nil {
				call.Extensions = make(map[string]json.RawMessage)
			}
			call.Extensions[ContinuityMarkerKey] = json.RawMessage(ContinuityMarkerValue)
		},
	}
}

func codexIdentity(meta request.AttemptMeta, call *lipapi.Call) bool {
	if strings.TrimSpace(meta.BackendID) == "" || !strings.Contains(strings.ToLower(meta.BackendID), "codex") {
		return false
	}
	if !strings.Contains(strings.ToLower(meta.Model), "codex") {
		return false
	}
	for _, d := range meta.ReplaySupport.Dialects {
		if lipapi.NormalizeReasoningDialect(d) == lipapi.ReasoningDialectOpenAIResponsesItemV1 {
			return true
		}
	}
	return false
}

func safeCodexOutcome(outcomes []reasoningpreservation.SafeOutcome) bool {
	for _, o := range outcomes {
		switch o {
		case reasoningpreservation.OutcomePreserved, reasoningpreservation.OutcomeRestored, reasoningpreservation.OutcomeMissing, reasoningpreservation.OutcomeUnmatched:
		default:
			return false
		}
	}
	return true
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
