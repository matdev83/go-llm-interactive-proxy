package featurebundle

import (
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

// MergedFeatureSurface is the concatenated contribution of all enabled feature plugins in
// registration order.
type MergedFeatureSurface struct {
	Lifecycles                 []lipplugin.Lifecycle
	CompactionObservers        []compaction.Observer
	CompactionPreservers       []compaction.Preserver
	SecretGuards               []secretguard.Guard
	LocalTurnHandlers          []localturn.Handler
	TerminalDecisionProvider   terminaldecision.Provider
	terminalDecisionProviderID string
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
	m.Lifecycles = append(m.Lifecycles, b.Lifecycles...)
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
	return MergeFeatureSurfacesWithHost(reg, registrations, HostContributions{})
}

// MergeFeatureSurfacesWithHost merges enabled feature plugins, host contributions, and optional candidate feature bundles into
// legacy MergedFeatureSurface and GeneratedMergeSurface. Feature bundles are contributed under SourceFeature (plugins first, candidate extras last),
// and host observer contributions are contributed under SourceHost between initial feature plugins and candidate extras.
func MergeFeatureSurfacesWithHost(reg *pluginreg.Registry, registrations []lipsdk.Registration, host HostContributions, extraFeatureBundles ...lipfeature.FeatureBundle) (MergedFeatureSurface, GeneratedMergeSurface, error) {
	bundles, err := BuildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return MergedFeatureSurface{}, GeneratedMergeSurface{}, err
	}
	allFeatureBundles := append(bundles, extraFeatureBundles...)
	m, err := MergeBundlesChecked(allFeatureBundles...)
	if err != nil {
		return MergedFeatureSurface{}, GeneratedMergeSurface{}, err
	}
	cs := lipfeature.NewContributionSet()
	var lifecycles []lipplugin.Lifecycle
	bIdx := 0
	for _, regEntry := range registrations {
		if regEntry.Kind != lipsdk.PluginKindFeature || !regEntry.Enabled {
			continue
		}
		b := bundles[bIdx]
		bIdx++
		pluginID := regEntry.ID
		if pluginID == "" {
			pluginID = regEntry.RegistryFactoryKey()
		}
		if pluginID == "" {
			pluginID = fmt.Sprintf("feature-%d", bIdx)
		}
		if err := ContributeBundle(cs, pluginID, b); err != nil {
			return MergedFeatureSurface{}, GeneratedMergeSurface{}, err
		}
		lifecycles = append(lifecycles, b.Lifecycles...)
	}
	if err := ContributeHost(cs, host); err != nil {
		return MergedFeatureSurface{}, GeneratedMergeSurface{}, err
	}
	for i, eb := range extraFeatureBundles {
		extraID := fmt.Sprintf("candidate-feature-%d", i)
		if eb.TerminalDecisionProvider != nil {
			if id, err := terminaldecision.ProviderIdentity(eb.TerminalDecisionProvider); err == nil && id != "" {
				extraID = id
			}
		}
		if err := ContributeBundle(cs, extraID, eb); err != nil {
			return MergedFeatureSurface{}, GeneratedMergeSurface{}, err
		}
		lifecycles = append(lifecycles, eb.Lifecycles...)
	}
	g := GeneratedMergeSurface{
		Frozen:     cs.Freeze(),
		Lifecycles: lifecycles,
		set:        cs,
	}
	return m, g, nil
}
