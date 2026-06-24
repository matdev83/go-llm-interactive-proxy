package pluginreg

import (
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/llamacpp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/lmstudio"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/localstub"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/nvidia"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/ollama"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/vllm"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/partsnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/prerequestpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refautoappend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refparts"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refsubmit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftoolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftraffictranscript"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refverifier"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refworkspaceguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolreactornoop"
	frontanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	frontgemini "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func installFrontends(reg *Registry) error {
	for _, e := range StandardBundle().Frontends {
		if err := reg.RegisterFrontend(e.ID, e.Mount); err != nil {
			return err
		}
	}
	return nil
}

// standardFrontendAuthErrorRenderers is the extension point for optional per-wire-frontend renderers
// (auth wire ids per stdhttp/auth DefaultFrontendIDFromRequest). Entries with nil Renderer are skipped.
func installStandardFrontendAuthErrorRenderers(reg *Registry) error {
	for _, e := range StandardBundle().AuthErrorRenderers {
		if e.Renderer == nil {
			continue
		}
		if err := reg.RegisterAuthErrorRenderer(e.WireID, e.Renderer); err != nil {
			return err
		}
	}
	return nil
}

func installFeatures(reg *Registry) error {
	for _, e := range StandardBundle().Features {
		if err := reg.RegisterFeature(e.ID, e.Factory); err != nil {
			return err
		}
	}
	return nil
}

// InstallStandardBundleOn registers all standard bundled factories on reg (tests, alternate bundles).
// keys supplies default API key material when plugin YAML omits api_key (typically from
// [ResolveUpstreamAPIKeysFromEnv] at process startup); tests may pass a zero value.
func InstallStandardBundleOn(reg *Registry, keys UpstreamAPIKeys) error {
	if err := InstallBundleOn(reg, StandardBackendBundle(keys)); err != nil {
		return err
	}
	if err := installFrontends(reg); err != nil {
		return err
	}
	if err := installStandardFrontendAuthErrorRenderers(reg); err != nil {
		return err
	}
	return installFeatures(reg)
}

// InstallStandardBackendsOn registers only bundled backend factories on reg (minimal partial bundles).
func InstallStandardBackendsOn(reg *Registry, keys UpstreamAPIKeys) error {
	return InstallBundleOn(reg, StandardBackendBundle(keys))
}

// FrontendRegistration is one explicit frontend contribution to a bundle.
type FrontendRegistration struct {
	ID    string
	Mount FrontendMount
}

// BackendRegistration is one explicit backend contribution to a bundle.
type BackendRegistration struct {
	ID      string
	Factory BackendFactory
	Profile BackendSecurityProfile
}

// FeatureRegistration is one explicit feature contribution to a bundle.
type FeatureRegistration struct {
	ID      string
	Factory FeatureFactory
}

// AuthErrorRendererRegistration binds optional transport-auth error rendering to a wire frontend id.
type AuthErrorRendererRegistration struct {
	WireID   string
	Renderer lipsdk.AuthErrorRenderer
}

// Bundle is the standard distribution composition input. It is a value, not process-global registry state.
type Bundle struct {
	Frontends          []FrontendRegistration
	Backends           []BackendRegistration
	Features           []FeatureRegistration
	AuthErrorRenderers []AuthErrorRendererRegistration
}

// StandardBundle returns the concrete standard distribution table. The standard distribution may import
// bundled plugins here; core and SDK packages must continue to depend only on canonical/SDK contracts.
func StandardBundle() Bundle {
	return Bundle{
		Frontends: []FrontendRegistration{
			{ID: frontopenairesponses.ID, Mount: mountOpenAIResponses},
			{ID: frontopenailegacy.ID, Mount: mountOpenAILegacy},
			{ID: frontanthropic.ID, Mount: mountAnthropic},
			{ID: frontgemini.ID, Mount: mountGemini},
		},
		Features: []FeatureRegistration{
			{ID: submitnoop.ID, Factory: FeatureFactoryFromHooks(featureSubmitNoop)},
			{ID: partsnoop.ID, Factory: FeatureFactoryFromHooks(featurePartsNoop)},
			{ID: toolreactornoop.ID, Factory: FeatureFactoryFromHooks(featureToolReactorNoop)},
			{ID: refsubmit.ID, Factory: FeatureFactoryFromHooks(featureRefSubmit)},
			{ID: refparts.ID, Factory: FeatureFactoryFromHooks(featureRefParts)},
			{ID: reftool.ID, Factory: FeatureFactoryFromHooks(featureRefTool)},
			{ID: refautoappend.ID, Factory: featureRefAutoappend},
			{ID: reftoolpolicy.ID, Factory: featureRefToolPolicy},
			{ID: refworkspaceguard.ID, Factory: featureRefWorkspaceGuard},
			{ID: reftraffictranscript.ID, Factory: featureRefTrafficTranscript},
			{ID: refverifier.ID, Factory: featureRefVerifier},
			{ID: prerequestpolicy.ID, Factory: featurePreRequestPolicy},
		},
	}
}

// StandardBackendBundle returns the standard backend table with environment/default keys already bound.
func StandardBackendBundle(keys UpstreamAPIKeys) Bundle {
	return Bundle{Backends: []BackendRegistration{
		{ID: openairesponses.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendOpenAIResponses(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialStatic}},
		{ID: openailegacy.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendOpenAILegacy(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialStatic}},
		{ID: anthropic.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendAnthropic(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialStatic}},
		{ID: gemini.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendGemini(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialStatic}},
		{ID: bedrock.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendBedrock(n, upstream)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialWorkload}},
		{ID: acp.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendACP(n, upstream)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialStatic}},
		{ID: openrouter.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendOpenRouter(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialStatic}},
		{ID: nvidia.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendNvidia(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialStatic}},
		{ID: ollama.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendOllama(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialNone}},
		{ID: ollama.CloudID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendOllamaCloud(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialNone}},
		{ID: llamacpp.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendLlamacpp(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialNone}},
		{ID: lmstudio.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendLmstudio(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialNone}},
		{ID: vllm.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendVllm(n, upstream, keys)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialNone}},
		{ID: localstub.ID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendLocalStub(n, upstream)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialNone}},
		{ID: CustomOpenAILegacyCompatibleID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendCustomOpenAILegacyCompatible(n, upstream)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialStatic}},
		{ID: CustomOpenAIResponsesCompatibleID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendCustomOpenAIResponsesCompatible(n, upstream)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialStatic}},
		{ID: CustomAnthropicCompatibleID, Factory: func(n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
			return backendCustomAnthropicCompatible(n, upstream)
		}, Profile: BackendSecurityProfile{CredentialMode: CredentialStatic}},
	}}
}

// InstallBundleOn registers b on reg. Tests and alternate composition roots can pass a custom bundle
// without mutating package-level globals.
func InstallBundleOn(reg *Registry, b Bundle) error {
	if reg == nil {
		return fmt.Errorf("pluginreg: InstallBundleOn: nil registry")
	}
	for _, e := range b.Backends {
		if err := reg.RegisterBackendWithProfile(e.ID, e.Factory, e.Profile); err != nil {
			return fmt.Errorf("pluginreg: InstallBundleOn: register backend %q: %w", e.ID, err)
		}
	}
	for _, e := range b.Frontends {
		if err := reg.RegisterFrontend(e.ID, e.Mount); err != nil {
			return fmt.Errorf("pluginreg: InstallBundleOn: register frontend %q: %w", e.ID, err)
		}
	}
	for _, e := range b.AuthErrorRenderers {
		if err := reg.RegisterAuthErrorRenderer(e.WireID, e.Renderer); err != nil {
			return fmt.Errorf("pluginreg: InstallBundleOn: register auth error renderer %q: %w", e.WireID, err)
		}
	}
	for _, e := range b.Features {
		if err := reg.RegisterFeature(e.ID, e.Factory); err != nil {
			return fmt.Errorf("pluginreg: InstallBundleOn: register feature %q: %w", e.ID, err)
		}
	}
	return nil
}
