package featurebundle

import (
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// MergedFeatureSurface is the concatenated contribution of all enabled feature plugins in
// registration order (session openers and workspace resolvers preserve bundle order within each plugin).
type MergedFeatureSurface struct {
	SubmitHooks                      []sdk.SubmitHook
	RequestPartHooks                 []sdk.RequestPartHook
	ResponsePartHooks                []sdk.ResponsePartHook
	ToolReactors                     []sdk.ToolReactor
	ToolReactorErrorPolicy           sdk.ToolReactorErrorPolicy
	Lifecycles                       []lipplugin.Lifecycle
	SessionOpeners                   []session.Opener
	WorkspaceResolvers               []workspace.Resolver
	ToolCatalogFilters               []toolcatalog.Filter
	ToolCallPolicies                 []toolpolicy.Policy
	ToolCallFinalizers               []toolcall.Finalizer
	ToolCallFinalizationMaxArgsBytes int
	RequestTransforms                []request.Transform
	PreRequestHandlers               []prerequest.Handler
	RouteHintProviders               []routehint.Provider
	CompletionGates                  []completion.Gate
	AttemptTransforms                []request.AttemptTransform
	StreamObserverFactories          []response.StreamObserverFactory
	TrafficObservers                 []traffic.Observer
	UsageObservers                   []usage.Observer
	RawCaptureSinks                  []traffic.RawCaptureSink
	TrafficRedactors                 []traffic.Redactor
	CompactionObservers              []compaction.Observer
	CompactionPreservers             []compaction.Preserver
	SecretGuards                     []secretguard.Guard
	LocalTurnHandlers                []localturn.Handler
	TerminalDecisionProvider         terminaldecision.Provider
	terminalDecisionProviderID       string
}

// ErrTerminalDecisionProviderConflict reports an attempted second provider
// contribution to one immutable feature surface.
var ErrTerminalDecisionProviderConflict = errors.New("featurebundle: terminal-decision provider conflict")

// Append concatenates all fields from bundle b into the receiver. This is the single
// merge point: every new FeatureBundle field requires exactly one append line here.
// Exclusive-provider validation runs before any receiver field is changed.
func (m *MergedFeatureSurface) Append(b lipfeature.FeatureBundle) error {
	if m == nil {
		return errors.New("featurebundle: nil merged feature surface")
	}
	var providerID string
	if m.TerminalDecisionProvider != nil {
		providerID = m.terminalDecisionProviderID
		if providerID == "" {
			var err error
			providerID, err = terminaldecision.ProviderIdentity(m.TerminalDecisionProvider)
			if err != nil {
				return fmt.Errorf("featurebundle: merged terminal-decision provider: %w", err)
			}
		} else if err := terminaldecision.ValidateProviderID(providerID); err != nil {
			return fmt.Errorf("featurebundle: merged terminal-decision provider: %w", err)
		}
	}
	if b.TerminalDecisionProvider != nil {
		incomingID, err := terminaldecision.ProviderIdentity(b.TerminalDecisionProvider)
		if err != nil {
			return fmt.Errorf("featurebundle: contributed terminal-decision provider: %w", err)
		}
		if m.TerminalDecisionProvider != nil {
			return fmt.Errorf("%w: %q and %q", ErrTerminalDecisionProviderConflict, providerID, incomingID)
		}
		providerID = incomingID
	}
	m.SubmitHooks = append(m.SubmitHooks, b.SubmitHooks...)
	m.RequestPartHooks = append(m.RequestPartHooks, b.RequestPartHooks...)
	m.ResponsePartHooks = append(m.ResponsePartHooks, b.ResponsePartHooks...)
	m.ToolReactors = append(m.ToolReactors, b.ToolReactors...)
	m.Lifecycles = append(m.Lifecycles, b.Lifecycles...)
	m.SessionOpeners = append(m.SessionOpeners, b.SessionOpeners...)
	m.WorkspaceResolvers = append(m.WorkspaceResolvers, b.WorkspaceResolvers...)
	m.ToolCatalogFilters = append(m.ToolCatalogFilters, b.ToolCatalogFilters...)
	m.ToolCallPolicies = append(m.ToolCallPolicies, b.ToolCallPolicies...)
	m.ToolCallFinalizers = append(m.ToolCallFinalizers, b.ToolCallFinalizers...)
	// Non-positive values are not merge contributions (zero = unset; negatives are
	// rejected by FeatureBundle.Validate before a valid bundle is merged).
	if b.ToolCallFinalizationMaxArgsBytes > 0 {
		if m.ToolCallFinalizationMaxArgsBytes <= 0 || b.ToolCallFinalizationMaxArgsBytes < m.ToolCallFinalizationMaxArgsBytes {
			m.ToolCallFinalizationMaxArgsBytes = b.ToolCallFinalizationMaxArgsBytes
		}
	}
	m.RequestTransforms = append(m.RequestTransforms, b.RequestTransforms...)
	m.PreRequestHandlers = append(m.PreRequestHandlers, b.PreRequestHandlers...)
	m.RouteHintProviders = append(m.RouteHintProviders, b.RouteHintProviders...)
	m.CompletionGates = append(m.CompletionGates, b.CompletionGates...)
	m.AttemptTransforms = append(m.AttemptTransforms, b.AttemptTransforms...)
	m.StreamObserverFactories = append(m.StreamObserverFactories, b.StreamObserverFactories...)
	m.TrafficObservers = append(m.TrafficObservers, b.TrafficObservers...)
	m.UsageObservers = append(m.UsageObservers, b.UsageObservers...)
	m.RawCaptureSinks = append(m.RawCaptureSinks, b.RawCaptureSinks...)
	m.TrafficRedactors = append(m.TrafficRedactors, b.TrafficRedactors...)
	m.CompactionObservers = append(m.CompactionObservers, b.CompactionObservers...)
	m.CompactionPreservers = append(m.CompactionPreservers, b.CompactionPreservers...)
	m.SecretGuards = append(m.SecretGuards, b.SecretGuards...)
	m.LocalTurnHandlers = append(m.LocalTurnHandlers, b.LocalTurnHandlers...)
	if b.TerminalDecisionProvider != nil {
		m.TerminalDecisionProvider = b.TerminalDecisionProvider
		m.terminalDecisionProviderID = providerID
	}
	return nil
}

// MergeBundles concatenates one or more FeatureBundles into a single MergedFeatureSurface,
// preserving bundle order across all slice fields.
func MergeBundles(bundles ...lipfeature.FeatureBundle) MergedFeatureSurface {
	out, err := MergeBundlesChecked(bundles...)
	if err != nil {
		panic(err)
	}
	return out
}

// MergeBundlesChecked merges bundles and rejects invalid or conflicting
// terminal-decision provider contributions before returning a candidate.
func MergeBundlesChecked(bundles ...lipfeature.FeatureBundle) (MergedFeatureSurface, error) {
	var out MergedFeatureSurface
	for _, b := range bundles {
		if err := out.Append(b); err != nil {
			return MergedFeatureSurface{}, err
		}
	}
	return out, nil
}

// BuildEnabledFeatureBundles builds FeatureBundles from enabled feature registrations in order.
func BuildEnabledFeatureBundles(reg *pluginreg.Registry, registrations []lipsdk.Registration) ([]lipfeature.FeatureBundle, error) {
	nFeat := 0
	for _, regEntry := range registrations {
		if regEntry.Kind == lipsdk.PluginKindFeature && regEntry.Enabled {
			nFeat++
		}
	}
	bundles := make([]lipfeature.FeatureBundle, 0, nFeat)
	for _, regEntry := range registrations {
		if regEntry.Kind != lipsdk.PluginKindFeature || !regEntry.Enabled {
			continue
		}
		factoryKey := regEntry.RegistryFactoryKey()
		b, err := reg.BuildFeatureBundle(factoryKey, regEntry.Config.Node)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, b)
	}
	return bundles, nil
}

func buildEnabledFeatureBundles(reg *pluginreg.Registry, registrations []lipsdk.Registration) ([]lipfeature.FeatureBundle, error) {
	return BuildEnabledFeatureBundles(reg, registrations)
}

// MergeFeatureSurface merges enabled feature plugins into SDK hook slices plus extension surfaces.
// It calls reg.BuildFeatureBundle for each enabled feature plugin and concatenates the results.
// Secrets-guard uniqueness is enforced at the runtimebundle composition root
// ([runtimebundle.BuildHost] / [buildSecretGuardRuntime]), not in this generic merge helper.
func MergeFeatureSurface(reg *pluginreg.Registry, registrations []lipsdk.Registration) (MergedFeatureSurface, error) {
	bundles, err := buildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return MergedFeatureSurface{}, err
	}
	return MergeBundlesChecked(bundles...)
}

// MergeFeatureSurfaces merges enabled feature plugins into both legacy MergedFeatureSurface
// and GeneratedMergeSurface using the same bundle instances.
func MergeFeatureSurfaces(reg *pluginreg.Registry, registrations []lipsdk.Registration) (MergedFeatureSurface, GeneratedMergeSurface, error) {
	bundles, err := BuildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return MergedFeatureSurface{}, GeneratedMergeSurface{}, err
	}
	m, err := MergeBundlesChecked(bundles...)
	if err != nil {
		return MergedFeatureSurface{}, GeneratedMergeSurface{}, err
	}
	g, err := MergeBundlesGenerated(bundles...)
	if err != nil {
		return MergedFeatureSurface{}, GeneratedMergeSurface{}, err
	}
	return m, g, nil
}
