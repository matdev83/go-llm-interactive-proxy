package featurebundle

import (
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

func appendLifecycles(dst []lipplugin.Lifecycle, incoming []lipplugin.Lifecycle) []lipplugin.Lifecycle {
	if incoming == nil {
		return dst
	}
	if dst == nil {
		dst = make([]lipplugin.Lifecycle, 0, len(incoming))
	}
	return append(dst, incoming...)
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
		lifecycles = appendLifecycles(lifecycles, b.Lifecycles)
	}
	if err := ContributeHost(cs, host); err != nil {
		return GeneratedMergeSurface{}, err
	}
	for i, eb := range extraFeatureBundles {
		extraID := fmt.Sprintf("candidate-feature-%d", i)
		if err := ContributeBundle(cs, extraID, eb); err != nil {
			return GeneratedMergeSurface{}, err
		}
		lifecycles = appendLifecycles(lifecycles, eb.Lifecycles)
	}
	return GeneratedMergeSurface{
		Frozen:     cs.Freeze(),
		Lifecycles: lifecycles,
		set:        cs,
	}, nil
}

// MergeFeatureSurfacesWithHost merges enabled feature plugins, host contributions, and optional candidate feature bundles into
// a GeneratedMergeSurface. Feature bundles are contributed under SourceFeature (plugins first, candidate extras last),
// and host observer contributions are contributed under SourceHost between initial feature plugins and candidate extras.
func MergeFeatureSurfacesWithHost(reg FeatureBundleRegistry, registrations []lipsdk.Registration, host HostContributions, extraFeatureBundles ...lipfeature.FeatureBundle) (GeneratedMergeSurface, error) {
	bundles, err := BuildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return GeneratedMergeSurface{}, err
	}
	return mergeBuiltFeatureBundlesWithHost(bundles, registrations, host, extraFeatureBundles...)
}
