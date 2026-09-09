package standardplugins

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/compatmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openresponsescompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/providerprofiles"
	"gopkg.in/yaml.v3"
)

var (
	testCatSeq   atomic.Uint64
	testCatalogs sync.Map // handle (string) -> *providerprofiles.Catalog
)

const (
	profileAnchorPrefix = "lip_profile_"
	profileTagPrefix    = "lip:provider-profile:"
)

func formatProfileAnchor(profileID, catHandle string) string {
	if catHandle != "" {
		return profileAnchorPrefix + profileID + "__" + catHandle
	}
	return profileAnchorPrefix + profileID
}

func formatProfileTag(profileID, catHandle string) string {
	if catHandle != "" {
		return profileTagPrefix + profileID + ":" + catHandle
	}
	return profileTagPrefix + profileID
}

func extractProfileReference(n yaml.Node) (profileID, catHandle string, ok bool) {
	// 1. Check node anchor first (durable across comment stripping and round-trips)
	anchor := n.Anchor
	if anchor == "" && len(n.Content) > 0 {
		anchor = n.Content[0].Anchor
	}
	if rest, ok := strings.CutPrefix(anchor, profileAnchorPrefix); ok {
		if before, after, found := strings.Cut(rest, "__"); found {
			return before, after, true
		}
		return rest, "", true
	}

	// 2. Check head comment (backward-compatible tag)
	tag := n.HeadComment
	if tag == "" && len(n.Content) > 0 {
		tag = n.Content[0].HeadComment
	}
	if rest, ok := strings.CutPrefix(tag, profileTagPrefix); ok {
		if before, after, found := strings.Cut(rest, ":"); found {
			return before, after, true
		}
		return rest, "", true
	}

	return "", "", false
}

func resolveProviderProfile(n yaml.Node) (providerprofiles.CompiledProfile, bool) {
	profileID, catHandle, ok := extractProfileReference(n)
	if !ok || profileID == "" {
		return providerprofiles.CompiledProfile{}, false
	}

	if catHandle != "" {
		catVal, found := testCatalogs.Load(catHandle)
		if !found {
			return providerprofiles.CompiledProfile{}, false
		}
		cat, ok := catVal.(*providerprofiles.Catalog)
		if !ok {
			return providerprofiles.CompiledProfile{}, false
		}
		for _, p := range cat.Profiles() {
			if p.ID == profileID {
				compiled, err := providerprofiles.CompileProfile(p)
				if err != nil {
					return providerprofiles.CompiledProfile{}, false
				}
				return compiled, true
			}
		}
		return providerprofiles.CompiledProfile{}, false
	}

	prof, err := providerprofiles.EmbeddedProfile(profileID)
	if err != nil {
		return providerprofiles.CompiledProfile{}, false
	}
	compiled, err := providerprofiles.CompileProfile(prof)
	if err != nil {
		return providerprofiles.CompiledProfile{}, false
	}
	return compiled, true
}

// ProviderProfileKind is the declarative row kind used by the profile
// contribution. Keeping the identity in the composition source avoids a
// second provider-profile registry.
const ProviderProfileKind = "provider-profile"

// ExpandProviderProfileRows resolves profile references into the existing
// compatible-family factory rows. Only rows explicitly using ProviderProfileKind
// are changed; arbitrary custom-compatible rows retain their exact config.
// The operation is deterministic and performs no provider activation.
func ExpandProviderProfileRows(cfg *config.Config) (*config.Config, error) {
	catalog, err := ProviderProfileCatalog()
	if err != nil {
		return nil, err
	}
	return ExpandProviderProfileRowsWithCatalog(cfg, catalog)
}

// ExpandProviderProfileRowsWithCatalog resolves profile references against the provided catalog.
func ExpandProviderProfileRowsWithCatalog(cfg *config.Config, catalog *providerprofiles.Catalog) (*config.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("provider profiles: nil config")
	}
	if catalog == nil {
		return nil, fmt.Errorf("provider profiles: nil catalog")
	}
	byID := make(map[string]providerprofiles.Profile, len(catalog.Profiles()))
	for _, profile := range catalog.Profiles() {
		byID[profile.ID] = profile
	}

	embeddedCat, _ := providerprofiles.EmbeddedCatalog()
	var catHandle string
	if catalog != embeddedCat {
		catHandle = fmt.Sprintf("cat_%d", testCatSeq.Add(1))
		testCatalogs.Store(catHandle, catalog)
	}

	rows := make([]config.PluginConfig, len(cfg.Plugins.Backends))
	copy(rows, cfg.Plugins.Backends)
	for i := range rows {
		row := &rows[i]
		if row.FactoryID() != ProviderProfileKind {
			if IsCustomCompatibleBackendKind(row.FactoryID()) {
				if _, _, ok := extractProfileReference(row.Config); ok {
					return nil, fmt.Errorf("provider profile row %q: custom-compatible config reserves anchor prefix %q and comment prefix %q for expanded provider profiles; remove the marker to keep the row independent of profiles", row.InstanceID(), profileAnchorPrefix, profileTagPrefix)
				}
			}
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
		var node yaml.Node
		if compiled.Binding.Family == providerprofiles.FamilyOpenResponses {
			node, err = openResponsesProfileConfigNode(profile)
		} else {
			node, err = ProfileConfigNode(profile)
		}
		if err != nil {
			return nil, err
		}

		anchor := formatProfileAnchor(profileID, catHandle)
		tag := formatProfileTag(profileID, catHandle)

		node.Anchor = anchor
		node.HeadComment = tag
		if len(node.Content) > 0 {
			node.Content[0].Anchor = anchor
			node.Content[0].HeadComment = tag
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

type profileFamilyBuilder func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client) (execbackend.Backend, error)

var profileFamilyBuilders = map[providerprofiles.Family]profileFamilyBuilder{
	providerprofiles.FamilyOpenAIChat: func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
		be, err := openaicompat.BuildCompatibleWithHeaders(instanceID, profile.Binding.FactoryKind, node, upstream, openaicompat.FlavorChat, openaicompat.CompatibleTransportCaps(openaicompat.FlavorChat), profileHeaders(profile.Profile.Headers))
		return applyProfileCapabilities(be, err, profile)
	},
	providerprofiles.FamilyOpenAIResponses: func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
		be, err := openaicompat.BuildCompatibleWithHeaders(instanceID, profile.Binding.FactoryKind, node, upstream, openaicompat.FlavorResponses, openaicompat.CompatibleTransportCaps(openaicompat.FlavorResponses), profileHeaders(profile.Profile.Headers))
		return applyProfileCapabilities(be, err, profile)
	},
	providerprofiles.FamilyAnthropic: func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
		modelsPath := ""
		if slices.Contains(profile.Profile.Quirks, providerprofiles.QuirkAnthropicV1Models) {
			modelsPath = profile.Profile.Models.Path
		}
		be, err := anthropic.BuildCompatibleWithModelsPath(instanceID, profile.Binding.FactoryKind, node, upstream, modelsPath)
		return applyProfileCapabilities(be, err, profile)
	},
	providerprofiles.FamilyOpenResponses: func(profile providerprofiles.CompiledProfile, instanceID string, node yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
		be, err := openresponsescompat.Build(instanceID, node, upstream)
		return applyProfileCapabilities(be, err, profile)
	},
}

// BuildProviderProfileBackend applies one compiled profile through the existing
// family adapter strategy.
func BuildProviderProfileBackend(
	profile providerprofiles.CompiledProfile,
	instanceID string,
	upstream *http.Client,
	deps pluginreg.BackendFactoryDeps,
) (execbackend.Backend, error) {
	return buildProviderProfileBackendWithNode(profile, instanceID, yaml.Node{}, upstream, deps)
}

func buildProviderProfileBackendWithNode(
	profile providerprofiles.CompiledProfile,
	instanceID string,
	node yaml.Node,
	upstream *http.Client,
	deps pluginreg.BackendFactoryDeps,
) (execbackend.Backend, error) {
	if instanceID == "" {
		return execbackend.Backend{}, fmt.Errorf("provider profile %q: empty instance id", profile.Profile.ID)
	}
	if upstream == nil {
		return execbackend.Backend{}, fmt.Errorf("provider profile %q: nil upstream client", profile.Profile.ID)
	}
	if node.Kind == 0 {
		var err error
		if profile.Binding.Family == providerprofiles.FamilyOpenResponses {
			node, err = openResponsesProfileConfigNode(profile.Profile)
		} else {
			node, err = ProfileConfigNode(profile.Profile)
		}
		if err != nil {
			return execbackend.Backend{}, err
		}
	}
	builder, ok := profileFamilyBuilders[profile.Binding.Family]
	if !ok {
		return execbackend.Backend{}, fmt.Errorf("provider profile %q: family %q has no compatible compiler", profile.Profile.ID, profile.Binding.Family)
	}
	return builder(profile, instanceID, node, upstream)
}

func wrapCompatibleLifecycle(family providerprofiles.Family, base pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
	return func(instanceID string, n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
		profileID, _, hasMarker := extractProfileReference(n)
		if !hasMarker || profileID == "" {
			return base(instanceID, n, upstream, deps)
		}
		profile, ok := resolveProviderProfile(n)
		if !ok {
			return pluginreg.BackendBuildResult{}, fmt.Errorf("custom-compatible backend %q: unresolvable provider-profile marker for %q; remove the %q anchor / %q comment to keep the row independent of profiles", instanceID, profileID, profileAnchorPrefix, profileTagPrefix)
		}
		if profile.Binding.Family != family {
			return pluginreg.BackendBuildResult{}, fmt.Errorf("custom-compatible backend %q: provider profile %q belongs to family %q, cannot build via %q lifecycle; remove the marker or move the row to the matching custom-compatible kind", instanceID, profile.Profile.ID, profile.Binding.Family, family)
		}
		be, err := buildProviderProfileBackendWithNode(profile, instanceID, n, upstream, deps)
		if err != nil {
			return pluginreg.BackendBuildResult{}, err
		}
		return pluginreg.BackendBuildResult{Backend: be}, nil
	}
}

func applyProfileCapabilities(be execbackend.Backend, err error, profile providerprofiles.CompiledProfile) (execbackend.Backend, error) {
	if err != nil {
		return execbackend.Backend{}, err
	}
	return compatmode.ApplyCapabilityCeiling(be, profile.Capabilities), nil
}

func buildOpenResponsesProfile(profile providerprofiles.Profile, instanceID string, upstream *http.Client) (execbackend.Backend, error) {
	node, err := openResponsesProfileConfigNode(profile)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return openresponsescompat.Build(instanceID, node, upstream)
}

func openResponsesProfileConfigNode(profile providerprofiles.Profile) (yaml.Node, error) {
	compiled, err := providerprofiles.CompileProfile(profile)
	if err != nil {
		return yaml.Node{}, err
	}
	caps := make([]string, 0, len(compiled.Capabilities))
	for capability := range compiled.Capabilities {
		caps = append(caps, string(capability))
	}
	slices.Sort(caps)
	value := map[string]any{
		"backend_prefix":       profile.ID,
		"profile":              openresponsescompat.DefaultProfile,
		"base_url":             profile.Endpoint.BaseURL,
		"api_key_env_var_root": profile.Auth.EnvVar,
		"capabilities":         caps,
	}
	if models := staticModelsConfigMap(profile.Models.Static); models != nil {
		value["models"] = models
	}
	if len(profile.Dialects.Item)+len(profile.Dialects.Reasoning)+len(profile.Dialects.Compaction)+len(profile.Dialects.Extensions) > 0 {
		dialects := map[string]any{}
		if len(profile.Dialects.Item) > 0 {
			items := make([]map[string]string, 0, len(profile.Dialects.Item))
			for _, d := range profile.Dialects.Item {
				m := map[string]string{"dialect": d.Dialect}
				if d.Implementor != "" {
					m["implementor"] = d.Implementor
				}
				items = append(items, m)
			}
			dialects["item"] = items
		}
		if len(profile.Dialects.Reasoning) > 0 {
			reasonings := make([]map[string]string, 0, len(profile.Dialects.Reasoning))
			for _, d := range profile.Dialects.Reasoning {
				m := map[string]string{"dialect": d.Dialect}
				if d.Implementor != "" {
					m["implementor"] = d.Implementor
				}
				reasonings = append(reasonings, m)
			}
			dialects["reasoning"] = reasonings
		}
		if len(profile.Dialects.Compaction) > 0 {
			compactions := make([]map[string]string, 0, len(profile.Dialects.Compaction))
			for _, d := range profile.Dialects.Compaction {
				m := map[string]string{"dialect": d.Dialect}
				if d.Implementor != "" {
					m["implementor"] = d.Implementor
				}
				compactions = append(compactions, m)
			}
			dialects["compaction"] = compactions
		}
		if len(profile.Dialects.Extensions) > 0 {
			exts := make([]map[string]string, 0, len(profile.Dialects.Extensions))
			for _, e := range profile.Dialects.Extensions {
				m := map[string]string{"namespace": e.Namespace, "type": e.Type}
				if e.Implementor != "" {
					m["implementor"] = e.Implementor
				}
				exts = append(exts, m)
			}
			dialects["extensions"] = exts
		}
		value["dialects"] = dialects
	}
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return yaml.Node{}, fmt.Errorf("provider profile %q: encode OpenResponses config: %w", profile.ID, err)
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
	if models := staticModelsConfigMap(profile.Models.Static); models != nil {
		value["models"] = models
	}
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return yaml.Node{}, fmt.Errorf("provider profile %q: encode config: %w", profile.ID, err)
	}
	return node, nil
}

func staticModelsConfigMap(static []providerprofiles.Model) map[string]any {
	if len(static) == 0 {
		return nil
	}
	items := make([]map[string]string, 0, len(static))
	for _, model := range static {
		items = append(items, map[string]string{
			"canonical_id": model.CanonicalID,
			"native_id":    model.NativeID,
			"display_name": model.DisplayName,
		})
	}
	return map[string]any{
		"source": "inline",
		"items":  items,
	}
}
