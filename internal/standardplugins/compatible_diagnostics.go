package standardplugins

import (
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/endpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tokenizers"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

const compatibleOriginBuiltIn = "built_in_compatible"

// CompatibleBackendKinds returns the three stable built-in compatible factory ids.
func CompatibleBackendKinds() []string {
	return []string{
		CustomOpenAILegacyCompatibleID,
		CustomOpenAIResponsesCompatibleID,
		CustomAnthropicCompatibleID,
	}
}

// ProjectCompatibleBackendRows builds secret-safe diagnostics rows from config only.
// No provider requests, plugin activation, or credential value resolution occur.
func ProjectCompatibleBackendRows(cfg *config.Config) []diag.CompatibleBackendRow {
	if cfg == nil {
		return nil
	}
	out := make([]diag.CompatibleBackendRow, 0)
	for _, row := range cfg.Plugins.Backends {
		kind := row.FactoryID()
		if !IsCustomCompatibleBackendKind(kind) {
			continue
		}
		entry := diag.CompatibleBackendRow{
			Origin:      compatibleOriginBuiltIn,
			InstanceID:  row.InstanceID(),
			FactoryKind: kind,
			Enabled:     row.Enabled,
		}
		decoded, err := config.DecodeCompatibleModeConfig(row.InstanceID(), kind, row.Config)
		if err != nil {
			entry.ConfigError = err.Error()
			out = append(out, entry)
			continue
		}
		if ep, err := endpoint.ParseBaseURL(decoded.BaseURL); err != nil {
			entry.ConfigError = err.Error()
			out = append(out, entry)
			continue
		} else {
			entry.EndpointIdentity = ep.BaseURL()
		}
		entry.Prefix = strings.TrimSpace(decoded.BackendPrefix)
		entry.AuthConfigured = strings.TrimSpace(decoded.APIKeyEnvVarRoot) != ""
		if _, id, err := tokenizers.ResolveCompatibleID(decoded.TokenizerID); err != nil {
			entry.ConfigError = err.Error()
			out = append(out, entry)
			continue
		} else if id != "" {
			entry.TokenizerID = id
		}
		entry.ConcurrencyPolicy = compatibleConcurrencyPolicy(decoded.MaxConcurrentRequests)
		entry.InventoryState = compatibleInventoryState(decoded.Models)
		out = append(out, entry)
	}
	return out
}

func compatibleConcurrencyPolicy(max int) string {
	if max <= 0 {
		return "default"
	}
	return "limit:" + strconv.Itoa(max)
}

func compatibleInventoryState(models config.CompatibleModeModelsConfig) string {
	switch strings.TrimSpace(strings.ToLower(models.Source)) {
	case "inline":
		if len(models.Items) > 0 {
			return "static_inline"
		}
		return "static_inline_empty"
	case "file":
		if strings.TrimSpace(models.Path) != "" {
			return "static_file"
		}
		return "static_file_unconfigured"
	default:
		return "remote"
	}
}

// CollectBuiltinCompatibleKinds returns registered factory ids with built-in-compatible provenance.
func CollectBuiltinCompatibleKinds(reg *pluginreg.Registry) []string {
	if reg == nil {
		return append([]string(nil), CompatibleBackendKinds()...)
	}
	out := make([]string, 0, len(CompatibleBackendKinds()))
	for _, kind := range reg.BackendFactoryIDs() {
		source, ok := reg.BackendRegistrationSource(kind)
		if ok && source == pluginreg.BackendSourceBuiltinCompatible {
			out = append(out, kind)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), CompatibleBackendKinds()...)
	}
	return out
}
