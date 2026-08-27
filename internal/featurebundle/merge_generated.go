package featurebundle

import (
	"errors"
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
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
}

// ToMergedFeatureSurface projects the GeneratedMergeSurface into a legacy MergedFeatureSurface,
// accessing each plane value via lipfeature.Get and the terminal-decision provider identity
// via lipfeature.FrozenIdentity.
func (g GeneratedMergeSurface) ToMergedFeatureSurface() MergedFeatureSurface {
	m := MergedFeatureSurface{
		SessionOpeners:                   lipfeature.Get(g.Frozen, lipfeature.PlaneSessionOpeners),
		WorkspaceResolvers:               lipfeature.Get(g.Frozen, lipfeature.PlaneWorkspaceResolvers),
		ToolCatalogFilters:               lipfeature.Get(g.Frozen, lipfeature.PlaneToolCatalogFilters),
		ToolCallPolicies:                 lipfeature.Get(g.Frozen, lipfeature.PlaneToolCallPolicies),
		ToolCallFinalizers:               lipfeature.Get(g.Frozen, lipfeature.PlaneToolCallFinalizers),
		ToolCallFinalizationMaxArgsBytes: lipfeature.Get(g.Frozen, lipfeature.PlaneToolCallFinalizationMaxArgsBytes),
		RequestTransforms:                lipfeature.Get(g.Frozen, lipfeature.PlaneRequestTransforms),
		PreRequestHandlers:               lipfeature.Get(g.Frozen, lipfeature.PlanePreRequestHandlers),
		RouteHintProviders:               lipfeature.Get(g.Frozen, lipfeature.PlaneRouteHintProviders),
		CompletionGates:                  lipfeature.Get(g.Frozen, lipfeature.PlaneCompletionGates),
		AttemptTransforms:                lipfeature.Get(g.Frozen, lipfeature.PlaneAttemptTransforms),
		StreamObserverFactories:          lipfeature.Get(g.Frozen, lipfeature.PlaneStreamObserverFactories),
		CompactionObservers:              lipfeature.Get(g.Frozen, lipfeature.PlaneCompactionObservers),
		CompactionPreservers:             lipfeature.Get(g.Frozen, lipfeature.PlaneCompactionPreservers),
		SecretGuards:                     lipfeature.Get(g.Frozen, lipfeature.PlaneSecretGuards),
		LocalTurnHandlers:                lipfeature.Get(g.Frozen, lipfeature.PlaneLocalTurnHandlers),
		TerminalDecisionProvider:         lipfeature.Get(g.Frozen, lipfeature.PlaneTerminalDecisionProvider),
		Lifecycles:                       g.Lifecycles,
	}
	if id, hasID := lipfeature.FrozenIdentity(g.Frozen, lipfeature.PlaneTerminalDecisionProvider); hasID {
		m.terminalDecisionProviderID = id
	}
	return m
}

// ContributeBundle contributes all non-empty / non-nil planes from FeatureBundle b into
// the given ContributionSet using the provided pluginID.
// MergeBundlesGenerated provides fail-before-mutate at the candidate level by discarding local
// ContributionSet on error; ContributeBundle itself is incremental and caller must discard
// candidate on error.
func ContributeBundle(cs *lipfeature.ContributionSet, pluginID string, b lipfeature.FeatureBundle) error {
	if cs == nil {
		return errors.New("featurebundle: nil ContributionSet")
	}
	if pluginID == "" {
		pluginID = "feature"
	}
	if len(b.SubmitHooks) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneSubmitHooks, pluginID, b.SubmitHooks); err != nil {
			return err
		}
	}
	if len(b.RequestPartHooks) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, pluginID, b.RequestPartHooks); err != nil {
			return err
		}
	}
	if len(b.ResponsePartHooks) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneResponsePartHooks, pluginID, b.ResponsePartHooks); err != nil {
			return err
		}
	}
	if len(b.ToolReactors) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneToolReactors, pluginID, b.ToolReactors); err != nil {
			return err
		}
	}
	if len(b.SessionOpeners) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneSessionOpeners, pluginID, b.SessionOpeners); err != nil {
			return err
		}
	}
	if len(b.WorkspaceResolvers) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneWorkspaceResolvers, pluginID, b.WorkspaceResolvers); err != nil {
			return err
		}
	}
	if len(b.ToolCatalogFilters) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCatalogFilters, pluginID, b.ToolCatalogFilters); err != nil {
			return err
		}
	}
	if len(b.ToolCallPolicies) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallPolicies, pluginID, b.ToolCallPolicies); err != nil {
			return err
		}
	}
	if len(b.ToolCallFinalizers) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizers, pluginID, b.ToolCallFinalizers); err != nil {
			return err
		}
	}
	if b.ToolCallFinalizationMaxArgsBytes > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, pluginID, b.ToolCallFinalizationMaxArgsBytes); err != nil {
			return err
		}
	}
	if len(b.RequestTransforms) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestTransforms, pluginID, b.RequestTransforms); err != nil {
			return err
		}
	}
	if len(b.PreRequestHandlers) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlanePreRequestHandlers, pluginID, b.PreRequestHandlers); err != nil {
			return err
		}
	}
	if len(b.RouteHintProviders) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneRouteHintProviders, pluginID, b.RouteHintProviders); err != nil {
			return err
		}
	}
	if len(b.CompletionGates) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneCompletionGates, pluginID, b.CompletionGates); err != nil {
			return err
		}
	}
	if len(b.AttemptTransforms) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, pluginID, b.AttemptTransforms); err != nil {
			return err
		}
	}
	if len(b.StreamObserverFactories) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, pluginID, b.StreamObserverFactories); err != nil {
			return err
		}
	}
	if len(b.TrafficObservers) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneTrafficObservers, pluginID, b.TrafficObservers); err != nil {
			return err
		}
	}
	if len(b.UsageObservers) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneUsageObservers, pluginID, b.UsageObservers); err != nil {
			return err
		}
	}
	if len(b.RawCaptureSinks) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneRawCaptureSinks, pluginID, b.RawCaptureSinks); err != nil {
			return err
		}
	}
	if len(b.TrafficRedactors) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneTrafficRedactors, pluginID, b.TrafficRedactors); err != nil {
			return err
		}
	}
	if len(b.CompactionObservers) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneCompactionObservers, pluginID, b.CompactionObservers); err != nil {
			return err
		}
	}
	if len(b.CompactionPreservers) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneCompactionPreservers, pluginID, b.CompactionPreservers); err != nil {
			return err
		}
	}
	if len(b.SecretGuards) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneSecretGuards, pluginID, b.SecretGuards); err != nil {
			return err
		}
	}
	if len(b.LocalTurnHandlers) > 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneLocalTurnHandlers, pluginID, b.LocalTurnHandlers); err != nil {
			return err
		}
	}
	if b.TerminalDecisionProvider != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, pluginID, b.TerminalDecisionProvider); err != nil {
			return err
		}
	}
	return nil
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
		if b.TerminalDecisionProvider != nil {
			if id, err := terminaldecision.ProviderIdentity(b.TerminalDecisionProvider); err == nil && id != "" {
				pluginID = id
			}
		}
		if err := ContributeBundle(cs, pluginID, b); err != nil {
			return GeneratedMergeSurface{}, err
		}
		lifecycles = append(lifecycles, b.Lifecycles...)
	}
	return GeneratedMergeSurface{
		Frozen:     cs.Freeze(),
		Lifecycles: lifecycles,
	}, nil
}

// MergeBundlesViaGenerated merges one or more FeatureBundles using the generated plane adapters
// and projects the result into a MergedFeatureSurface.
func MergeBundlesViaGenerated(bundles ...lipfeature.FeatureBundle) (MergedFeatureSurface, error) {
	gen, err := MergeBundlesGenerated(bundles...)
	if err != nil {
		return MergedFeatureSurface{}, err
	}
	return gen.ToMergedFeatureSurface(), nil
}

// MergeFeatureSurfaceGenerated merges enabled feature plugins into a GeneratedMergeSurface.
// It builds bundles from enabled feature registrations in order and merges them through
// generated plane adapters.
func MergeFeatureSurfaceGenerated(reg *pluginreg.Registry, registrations []lipsdk.Registration) (GeneratedMergeSurface, error) {
	bundles, err := buildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return GeneratedMergeSurface{}, err
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
			return GeneratedMergeSurface{}, err
		}
		lifecycles = append(lifecycles, b.Lifecycles...)
	}
	return GeneratedMergeSurface{
		Frozen:     cs.Freeze(),
		Lifecycles: lifecycles,
	}, nil
}

// MergeFeatureSurfaceViaGenerated merges enabled feature plugins using generated adapters
// and projects the result into a MergedFeatureSurface.
func MergeFeatureSurfaceViaGenerated(reg *pluginreg.Registry, registrations []lipsdk.Registration) (MergedFeatureSurface, error) {
	gen, err := MergeFeatureSurfaceGenerated(reg, registrations)
	if err != nil {
		return MergedFeatureSurface{}, err
	}
	return gen.ToMergedFeatureSurface(), nil
}
