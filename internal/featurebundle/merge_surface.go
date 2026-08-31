package featurebundle

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

// FeatureBundleRegistry represents any registry or catalog capable of building FeatureBundles.
type FeatureBundleRegistry interface {
	BuildFeatureBundle(factoryKey string, n yaml.Node) (lipfeature.FeatureBundle, error)
}

// MergedFeatureSurface is the concatenated contribution of all enabled feature plugins in
// registration order.
type MergedFeatureSurface struct {
	Lifecycles []lipplugin.Lifecycle
}

// ErrTerminalDecisionProviderConflict reports an attempted second provider
// contribution to one immutable feature surface.
var ErrTerminalDecisionProviderConflict = lipfeature.ErrTerminalDecisionProviderConflict

// Append concatenates all fields from bundle b into the receiver. This is the single
// merge point: every new FeatureBundle field requires exactly one append line here.
func (m *MergedFeatureSurface) Append(b lipfeature.FeatureBundle) error {
	if m == nil {
		return errors.New("featurebundle: nil merged feature surface")
	}
	if err := b.Validate(); err != nil {
		return fmt.Errorf("featurebundle: invalid bundle %q: %w", "legacy-append", err)
	}
	m.Lifecycles = append(m.Lifecycles, b.Lifecycles...)
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
	gen, err := MergeBundlesGenerated(bundles...)
	if err != nil {
		return MergedFeatureSurface{}, err
	}
	return gen.ToMergedFeatureSurface(), nil
}

// BuildEnabledFeatureBundles builds FeatureBundles from enabled feature registrations in order.
func BuildEnabledFeatureBundles(reg FeatureBundleRegistry, registrations []lipsdk.Registration) ([]lipfeature.FeatureBundle, error) {
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

func buildEnabledFeatureBundles(reg FeatureBundleRegistry, registrations []lipsdk.Registration) ([]lipfeature.FeatureBundle, error) {
	return BuildEnabledFeatureBundles(reg, registrations)
}

func mergeBuiltFeatureBundlesWithHost(
	bundles []lipfeature.FeatureBundle,
	registrations []lipsdk.Registration,
	host HostContributions,
	extraFeatureBundles ...lipfeature.FeatureBundle,
) (GeneratedMergeSurface, error) {
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
			return GeneratedMergeSurface{}, err
		}
		lifecycles = append(lifecycles, b.Lifecycles...)
	}
	if err := ContributeHost(cs, host); err != nil {
		return GeneratedMergeSurface{}, err
	}
	for i, eb := range extraFeatureBundles {
		extraID := fmt.Sprintf("candidate-feature-%d", i)
		if err := ContributeBundle(cs, extraID, eb); err != nil {
			return GeneratedMergeSurface{}, err
		}
		lifecycles = append(lifecycles, eb.Lifecycles...)
	}
	return GeneratedMergeSurface{
		Frozen:     cs.Freeze(),
		Lifecycles: lifecycles,
		set:        cs,
	}, nil
}

// MergeFeatureSurface merges enabled feature plugins into SDK hook slices plus extension surfaces.
// It calls reg.BuildFeatureBundle for each enabled feature plugin and concatenates the results.
// Secrets-guard uniqueness is enforced at the runtimebundle composition root
// ([runtimebundle.BuildHost] / [buildSecretGuardRuntime]), not in this generic merge helper.
func MergeFeatureSurface(reg FeatureBundleRegistry, registrations []lipsdk.Registration) (MergedFeatureSurface, error) {
	bundles, err := buildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return MergedFeatureSurface{}, err
	}
	g, err := mergeBuiltFeatureBundlesWithHost(bundles, registrations, HostContributions{})
	if err != nil {
		return MergedFeatureSurface{}, err
	}
	return g.ToMergedFeatureSurface(), nil
}

// MergeFeatureSurfaces merges enabled feature plugins into both legacy MergedFeatureSurface
// and GeneratedMergeSurface using the same bundle instances.
func MergeFeatureSurfaces(reg FeatureBundleRegistry, registrations []lipsdk.Registration) (MergedFeatureSurface, GeneratedMergeSurface, error) {
	return MergeFeatureSurfacesWithHost(reg, registrations, HostContributions{})
}

// MergeFeatureSurfacesWithHost merges enabled feature plugins, host contributions, and optional candidate feature bundles into
// legacy MergedFeatureSurface and GeneratedMergeSurface. Feature bundles are contributed under SourceFeature (plugins first, candidate extras last),
// and host observer contributions are contributed under SourceHost between initial feature plugins and candidate extras.
func MergeFeatureSurfacesWithHost(reg FeatureBundleRegistry, registrations []lipsdk.Registration, host HostContributions, extraFeatureBundles ...lipfeature.FeatureBundle) (MergedFeatureSurface, GeneratedMergeSurface, error) {
	bundles, err := BuildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return MergedFeatureSurface{}, GeneratedMergeSurface{}, err
	}
	g, err := mergeBuiltFeatureBundlesWithHost(bundles, registrations, host, extraFeatureBundles...)
	if err != nil {
		return MergedFeatureSurface{}, GeneratedMergeSurface{}, err
	}
	return g.ToMergedFeatureSurface(), g, nil
}
