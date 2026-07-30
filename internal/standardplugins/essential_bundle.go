package standardplugins

import (
	"net/http"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/alibabatokenplanintl"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"gopkg.in/yaml.v3"
)

// EssentialBackendKinds is the final allowlist for the built-in essential backend table.
// Non-essential kinds are provided only by discovered external connector artifacts.
var EssentialBackendKinds = []string{
	openairesponses.ID,
	openailegacy.ID,
	anthropic.ID,
	alibabatokenplanintl.ID,
	gemini.ID,
	bedrock.ID,
	CustomOpenAIResponsesCompatibleID,
	CustomOpenAILegacyCompatibleID,
	CustomAnthropicCompatibleID,
}

// IsEssentialBackendKind reports whether id is in the final essential allowlist.
func IsEssentialBackendKind(id string) bool {
	return slices.Contains(EssentialBackendKinds, id)
}

// EssentialBackendBundle returns only the five essential families plus approved
// dependency-free compatible modes.
func EssentialBackendBundle(keys UpstreamAPIKeys) Bundle {
	return Bundle{Backends: []BackendRegistration{
		{ID: openairesponses.ID, Factory: func(n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return backendOpenAIResponses(n, upstream, keys, deps.Identity)
		}, Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}},
		{ID: openailegacy.ID, Factory: func(n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return backendOpenAILegacy(n, upstream, keys, deps.Identity)
		}, Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}},
		{ID: anthropic.ID, Factory: func(n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return backendAnthropic(n, upstream, keys, deps.Identity)
		}, Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}},
		{ID: alibabatokenplanintl.ID, Factory: func(n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return backendAlibabaTokenPlanIntl(n, upstream, keys, deps.Identity)
		}, Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}},
		{ID: gemini.ID, Factory: func(n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return backendGemini(n, upstream, keys, deps.Identity)
		}, Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}},
		{ID: bedrock.ID, Factory: func(n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return backendBedrock(n, upstream, deps.Identity)
		}, Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialWorkload}},
		{ID: CustomOpenAILegacyCompatibleID, LifecycleFactory: openaicompat.LifecycleOpenAILegacyCompatible, Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}, Source: pluginreg.BackendSourceBuiltinCompatible},
		{ID: CustomOpenAIResponsesCompatibleID, LifecycleFactory: openaicompat.LifecycleOpenAIResponsesCompatible, Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}, Source: pluginreg.BackendSourceBuiltinCompatible},
		{ID: CustomAnthropicCompatibleID, LifecycleFactory: anthropic.LifecycleAnthropicCompatible, Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}, Source: pluginreg.BackendSourceBuiltinCompatible},
	}}
}
