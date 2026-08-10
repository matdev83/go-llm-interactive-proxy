package standardplugins

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/compatibleutil"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openresponsescompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/providerprofiles"
	"gopkg.in/yaml.v3"
)

const ProviderProfileKind = "provider-profile"

// ExpandProviderProfileRows resolves profile references into the existing
// compatible-family factory rows. Only rows explicitly using ProviderProfileKind
// are changed; arbitrary custom-compatible rows retain their exact config.
// The operation is deterministic and performs no provider activation.
func ExpandProviderProfileRows(cfg *config.Config) (*config.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("provider profiles: nil config")
	}
	catalog, err := ProviderProfileCatalog()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]providerprofiles.Profile, len(catalog.Profiles()))
	for _, profile := range catalog.Profiles() {
		byID[profile.ID] = profile
	}
	rows := make([]config.PluginConfig, len(cfg.Plugins.Backends))
	copy(rows, cfg.Plugins.Backends)
	for i := range rows {
		row := &rows[i]
		if row.FactoryID() != ProviderProfileKind {
			continue
		}
		profileID, err := profileReference(row.Config)
		if err != nil {
			return nil, fmt.Errorf("provider profile row %q: %w", row.InstanceID(), err)
		}
		profile, ok := byID[profileID]
		if !ok {
			return nil, fmt.Errorf("provider profile row %q: unknown profile %q", row.InstanceID(), profileID)
		}
		compiled, err := providerprofiles.CompileProfile(profile)
		if err != nil {
			return nil, fmt.Errorf("provider profile %q: %w", profileID, err)
		}
		node, err := ProfileConfigNode(profile)
		if err != nil {
			return nil, err
		}
		row.Kind = compiled.Binding.FactoryKind
		row.Config = node
	}
	clone := *cfg
	clone.Plugins = cfg.Plugins
	clone.Plugins.Backends = rows
	return &clone, nil
}

func profileReference(node yaml.Node) (string, error) {
	root := node
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) != 1 {
			return "", fmt.Errorf("profile config must be a mapping")
		}
		root = *root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("profile config must contain profile")
	}
	var ref struct {
		Profile   string `yaml:"profile"`
		ProfileID string `yaml:"profile_id"`
	}
	if err := root.Decode(&ref); err != nil {
		return "", fmt.Errorf("decode profile reference: %w", err)
	}
	id := strings.TrimSpace(ref.Profile)
	if id == "" {
		id = strings.TrimSpace(ref.ProfileID)
	}
	if id == "" {
		return "", fmt.Errorf("profile reference is required")
	}
	return id, nil
}

// CompileProviderProfile validates and binds one profile to an existing family.
// It is pure data compilation: no credentials, network, process, or goroutine
// is involved. The returned binding is an immutable generation input.
func CompileProviderProfile(profile providerprofiles.Profile) (providerprofiles.CompiledProfile, error) {
	return providerprofiles.CompileProfile(profile)
}

// BuildProviderProfileBackend applies one compiled profile through the existing
// family adapter. This is one compiler path, not a per-provider registration.
func BuildProviderProfileBackend(
	profile providerprofiles.CompiledProfile,
	instanceID string,
	upstream *http.Client,
	deps pluginreg.BackendFactoryDeps,
) (execbackend.Backend, error) {
	if instanceID == "" {
		return execbackend.Backend{}, fmt.Errorf("provider profile %q: empty instance id", profile.Profile.ID)
	}
	if upstream == nil {
		return execbackend.Backend{}, fmt.Errorf("provider profile %q: nil upstream client", profile.Profile.ID)
	}
	node, err := ProfileConfigNode(profile.Profile)
	if err != nil {
		return execbackend.Backend{}, err
	}
	switch profile.Binding.Family {
	case providerprofiles.FamilyOpenAIChat:
		be, err := openaicompat.BuildCompatibleWithHeaders(instanceID, profile.Binding.FactoryKind, node, upstream, openaicompat.FlavorChat, openaicompat.CompatibleTransportCaps(openaicompat.FlavorChat), profileHeaders(profile.Profile.Headers))
		return applyProfileCapabilities(be, err, profile)
	case providerprofiles.FamilyOpenAIResponses:
		be, err := openaicompat.BuildCompatibleWithHeaders(instanceID, profile.Binding.FactoryKind, node, upstream, openaicompat.FlavorResponses, openaicompat.CompatibleTransportCaps(openaicompat.FlavorResponses), profileHeaders(profile.Profile.Headers))
		return applyProfileCapabilities(be, err, profile)
	case providerprofiles.FamilyAnthropic:
		be, err := anthropic.BuildCompatible(instanceID, profile.Binding.FactoryKind, node, upstream)
		return applyProfileCapabilities(be, err, profile)
	case providerprofiles.FamilyOpenResponses:
		return buildOpenResponsesProfile(profile.Profile, instanceID, upstream)
	default:
		return execbackend.Backend{}, fmt.Errorf("provider profile %q: family %q has no compatible compiler", profile.Profile.ID, profile.Binding.Family)
	}
}

func applyProfileCapabilities(be execbackend.Backend, err error, profile providerprofiles.CompiledProfile) (execbackend.Backend, error) {
	if err != nil {
		return execbackend.Backend{}, err
	}
	return compatibleutil.ApplyCapabilityCeiling(be, profile.Capabilities), nil
}

func buildOpenResponsesProfile(profile providerprofiles.Profile, instanceID string, upstream *http.Client) (execbackend.Backend, error) {
	node, err := openResponsesProfileConfigNode(profile)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return openresponsescompat.Build(instanceID, node, upstream)
}

func openResponsesProfileConfigNode(profile providerprofiles.Profile) (yaml.Node, error) {
	compiled, err := providerprofiles.Compile(profile)
	if err != nil {
		return yaml.Node{}, err
	}
	caps := make([]string, 0, len(compiled.Capabilities))
	for cap := range compiled.Capabilities {
		caps = append(caps, string(cap))
	}
	slices.Sort(caps)
	value := map[string]any{
		"backend_prefix":       profile.ID,
		"profile":              openresponsescompat.DefaultProfile,
		"base_url":             profile.Endpoint.BaseURL,
		"api_key_env_var_root": profile.Auth.EnvVar,
		"capabilities":         caps,
	}
	if len(profile.Models.Static) > 0 {
		models := map[string]any{"source": "inline"}
		items := make([]map[string]string, 0, len(profile.Models.Static))
		for _, model := range profile.Models.Static {
			items = append(items, map[string]string{"canonical_id": model.CanonicalID, "native_id": model.NativeID, "display_name": model.DisplayName})
		}
		models["items"] = items
		value["models"] = models
	}
	if len(profile.Dialects.Item)+len(profile.Dialects.Reasoning)+len(profile.Dialects.Compaction)+len(profile.Dialects.Extensions) > 0 {
		dialects := map[string]any{}
		if len(profile.Dialects.Item) > 0 {
			dialects["item"] = profile.Dialects.Item
		}
		if len(profile.Dialects.Reasoning) > 0 {
			dialects["reasoning"] = profile.Dialects.Reasoning
		}
		if len(profile.Dialects.Compaction) > 0 {
			dialects["compaction"] = profile.Dialects.Compaction
		}
		if len(profile.Dialects.Extensions) > 0 {
			dialects["extensions"] = profile.Dialects.Extensions
		}
		value["dialects"] = dialects
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return yaml.Node{}, fmt.Errorf("provider profile %q: encode OpenResponses config: %w", profile.ID, err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return yaml.Node{}, fmt.Errorf("provider profile %q: decode OpenResponses config: %w", profile.ID, err)
	}
	return node, nil
}

func profileHeaders(headers []providerprofiles.SafeHeader) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for _, header := range headers {
		out[header.Name] = header.Value
	}
	return out
}

func ProfileConfigNode(profile providerprofiles.Profile) (yaml.Node, error) {
	value := map[string]any{
		"backend_prefix":       profile.ID,
		"base_url":             profile.Endpoint.BaseURL,
		"api_key_env_var_root": profile.Auth.EnvVar,
		"tokenizer":            profile.Tokenizer.TokenizerID,
	}
	if len(profile.Models.Static) > 0 {
		models := map[string]any{"source": "inline"}
		items := make([]map[string]string, 0, len(profile.Models.Static))
		for _, model := range profile.Models.Static {
			items = append(items, map[string]string{"canonical_id": model.CanonicalID, "native_id": model.NativeID, "display_name": model.DisplayName})
		}
		models["items"] = items
		value["models"] = models
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return yaml.Node{}, fmt.Errorf("provider profile %q: encode config: %w", profile.ID, err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return yaml.Node{}, fmt.Errorf("provider profile %q: decode config: %w", profile.ID, err)
	}
	return node, nil
}
