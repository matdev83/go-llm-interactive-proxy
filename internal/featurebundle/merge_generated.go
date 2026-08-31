package featurebundle

import (
	"errors"
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
)

// HostContributions holds host-provided contributions (process options) that can be merged
// alongside feature plugin contributions. Host contributions are merged under SourceHost
// and are strictly limited to planes that permit host contributions (TrafficObservers, UsageObservers).
type HostContributions struct {
	TrafficObservers []traffic.Observer
	UsageObservers   []usage.Observer
}

// ContributeHost contributes host-level observer contributions into the given ContributionSet
// with SourceHost semantics.
func ContributeHost(cs *lipfeature.ContributionSet, host HostContributions) error {
	if cs == nil {
		return errors.New("featurebundle: nil ContributionSet")
	}
	if len(host.TrafficObservers) > 0 {
		if err := lipfeature.ContributeSource(cs, lipfeature.PlaneTrafficObservers, lipfeature.SourceHost, "host", slices.Clone(host.TrafficObservers)); err != nil {
			return err
		}
	}
	if len(host.UsageObservers) > 0 {
		if err := lipfeature.ContributeSource(cs, lipfeature.PlaneUsageObservers, lipfeature.SourceHost, "host", slices.Clone(host.UsageObservers)); err != nil {
			return err
		}
	}
	return nil
}

// GeneratedMergeSurface represents the composed result of feature contributions merged through
// generated typed plane adapters. It holds an immutable FrozenPlaneSet containing all declared
// plane values, and a separate Lifecycles slice side-channel (lifecycles are not an extension plane).
type GeneratedMergeSurface struct {
	Frozen     lipfeature.FrozenPlaneSet
	Lifecycles []lipplugin.Lifecycle
	set        *lipfeature.ContributionSet
}

func (g GeneratedMergeSurface) workingSet() *lipfeature.ContributionSet {
	if g.set != nil {
		return g.set.Clone()
	}
	return g.Frozen.ToContributions()
}

// BindStreamObserverFactories replaces stream observer factories contributed by contributorID
// in this GeneratedMergeSurface under SourceGenerationBinder semantics (CombReplaceByIdentity).
// If validation or combination fails, the candidate is left unmodified and an error is returned.
func (g GeneratedMergeSurface) BindStreamObserverFactories(contributorID string, factories []response.StreamObserverFactory) (GeneratedMergeSurface, error) {
	working := g.workingSet()
	if err := working.BindStreamObserverFactories(contributorID, factories); err != nil {
		return GeneratedMergeSurface{}, err
	}
	return GeneratedMergeSurface{
		Frozen:     working.Freeze(),
		Lifecycles: slices.Clone(g.Lifecycles),
		set:        working,
	}, nil
}

// BindAttemptTransforms replaces attempt transforms contributed by contributorID
// in this GeneratedMergeSurface under SourceGenerationBinder semantics (CombReplaceByIdentity).
// If validation or combination fails, the candidate is left unmodified and an error is returned.
func (g GeneratedMergeSurface) BindAttemptTransforms(contributorID string, transforms []request.AttemptTransform) (GeneratedMergeSurface, error) {
	working := g.workingSet()
	if err := working.BindAttemptTransforms(contributorID, transforms); err != nil {
		return GeneratedMergeSurface{}, err
	}
	return GeneratedMergeSurface{
		Frozen:     working.Freeze(),
		Lifecycles: slices.Clone(g.Lifecycles),
		set:        working,
	}, nil
}

// BindCompactionPreservers replaces compaction preservers contributed by contributorID
// in this GeneratedMergeSurface under SourceGenerationBinder semantics (CombReplaceByIdentity).
// If validation or combination fails, the candidate is left unmodified and an error is returned.
func (g GeneratedMergeSurface) BindCompactionPreservers(contributorID string, preservers []compaction.Preserver) (GeneratedMergeSurface, error) {
	working := g.workingSet()
	if err := working.BindCompactionPreservers(contributorID, preservers); err != nil {
		return GeneratedMergeSurface{}, err
	}
	return GeneratedMergeSurface{
		Frozen:     working.Freeze(),
		Lifecycles: slices.Clone(g.Lifecycles),
		set:        working,
	}, nil
}

// MergeCandidatePlanes merges candidate feature planes under SourceFeature into the surface.
// If candidate planes are zero/empty, the surface is returned unmodified.
// If validation or combination fails, the surface is left unmodified and an error is returned.
func (g GeneratedMergeSurface) MergeCandidatePlanes(cand lipfeature.FrozenPlaneSet) (GeneratedMergeSurface, error) {
	if cand.IsZero() {
		return g, nil
	}
	working := g.workingSet()
	if err := working.ContributeCandidate(cand); err != nil {
		return GeneratedMergeSurface{}, err
	}
	return GeneratedMergeSurface{
		Frozen:     working.Freeze(),
		Lifecycles: slices.Clone(g.Lifecycles),
		set:        working,
	}, nil
}

// ContributeBundle contributes all planes from FeatureBundle b into the given ContributionSet
// using the provided pluginID via b.PlaneSet.ReplayTo. It validates the bundle schema before replay.
// If b.PlaneSet is zero, it is a no-op after schema validation.
func ContributeBundle(cs *lipfeature.ContributionSet, pluginID string, b lipfeature.FeatureBundle) error {
	if cs == nil {
		return errors.New("featurebundle: nil ContributionSet")
	}
	if pluginID == "" {
		pluginID = "feature"
	}
	if err := b.Validate(); err != nil {
		return fmt.Errorf("featurebundle: invalid bundle %q: %w", pluginID, err)
	}
	return b.PlaneSet.ReplayTo(cs, pluginID)
}

// FreezeBundle converts a single FeatureBundle into a FrozenPlaneSet with pluginID.
func FreezeBundle(b lipfeature.FeatureBundle, pluginID string) (lipfeature.FrozenPlaneSet, error) {
	cs := lipfeature.NewContributionSet()
	if pluginID == "" {
		if id, ok := lipfeature.FrozenIdentity(b.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); ok && id != "" {
			pluginID = id
		}
		if pluginID == "" {
			pluginID = "feature"
		}
	}
	if err := ContributeBundle(cs, pluginID, b); err != nil {
		return lipfeature.FrozenPlaneSet{}, err
	}
	return cs.Freeze(), nil
}

// MergeBundlesGenerated merges one or more FeatureBundles through generated plane adapters
// in registration order, returning a GeneratedMergeSurface containing the frozen plane set and
// separate lifecycles. If any contribution fails validation or conflicts, the candidate is
// discarded and an empty GeneratedMergeSurface and error are returned.
func MergeBundlesGenerated(bundles ...lipfeature.FeatureBundle) (GeneratedMergeSurface, error) {
	cs := lipfeature.NewContributionSet()
	var lifecycles []lipplugin.Lifecycle
	for i, b := range bundles {
		pluginID := fmt.Sprintf("bundle-%d", i)
		if id, ok := lipfeature.FrozenIdentity(b.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); ok && id != "" {
			pluginID = id
		}
		if err := ContributeBundle(cs, pluginID, b); err != nil {
			return GeneratedMergeSurface{}, err
		}
		lifecycles = appendLifecycles(lifecycles, b.Lifecycles)
	}
	return GeneratedMergeSurface{
		Frozen:     cs.Freeze(),
		Lifecycles: lifecycles,
		set:        cs,
	}, nil
}

// MergeFeatureSurfaceGenerated merges enabled feature plugins into a GeneratedMergeSurface.
// It builds bundles from enabled feature registrations in order and merges them through
// generated plane adapters.
func MergeFeatureSurfaceGenerated(reg FeatureBundleRegistry, registrations []lipsdk.Registration) (GeneratedMergeSurface, error) {
	bundles, err := buildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return GeneratedMergeSurface{}, err
	}
	return mergeBuiltFeatureBundlesWithHost(bundles, registrations, HostContributions{})
}
