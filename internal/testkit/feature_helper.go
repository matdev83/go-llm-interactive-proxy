package testkit

import (
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
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
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// TestFeatureBundle is a test fixture struct for constructing FrozenPlaneSets.
type TestFeatureBundle struct {
	ContributorID                    string
	CompletionGates                  []completion.Gate
	RequestTransforms                []request.Transform
	PreRequestHandlers               []prerequest.Handler
	RouteHintProviders               []routehint.Provider
	AttemptTransforms                []request.AttemptTransform
	SessionOpeners                   []session.Opener
	WorkspaceResolvers               []lipworkspace.Resolver
	ToolCatalogFilters               []toolcatalog.Filter
	ToolCallPolicies                 []toolpolicy.Policy
	ToolCallFinalizers               []toolcall.Finalizer
	ToolCallFinalizationMaxArgsBytes int
	CompactionObservers              []compaction.Observer
	CompactionPreservers             []compaction.Preserver
	TrafficObservers                 []traffic.Observer
	UsageObservers                   []usage.Observer
	RawCaptureSinks                  []traffic.RawCaptureSink
	TrafficRedactors                 []traffic.Redactor
	StreamObserverFactories          []response.StreamObserverFactory
	LocalTurnHandlers                []localturn.Handler
	TerminalDecisionProvider         terminaldecision.Provider
	SecretGuards                     []secretguard.Guard
}

// FreezeTestBundleChecked converts a TestFeatureBundle into a FrozenPlaneSet,
// returning an attributed error if any contribution fails.
func FreezeTestBundleChecked(b TestFeatureBundle) (lipfeature.FrozenPlaneSet, error) {
	contributorID := b.ContributorID
	if contributorID == "" {
		contributorID = "test"
	}
	cs := lipfeature.NewContributionSet()
	if b.CompletionGates != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneCompletionGates, contributorID, b.CompletionGates); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneCompletionGates.ID, err)
		}
	}
	if b.RequestTransforms != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestTransforms, contributorID, b.RequestTransforms); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneRequestTransforms.ID, err)
		}
	}
	if b.PreRequestHandlers != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlanePreRequestHandlers, contributorID, b.PreRequestHandlers); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlanePreRequestHandlers.ID, err)
		}
	}
	if b.RouteHintProviders != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneRouteHintProviders, contributorID, b.RouteHintProviders); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneRouteHintProviders.ID, err)
		}
	}
	if b.AttemptTransforms != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, contributorID, b.AttemptTransforms); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneAttemptTransforms.ID, err)
		}
	}
	if b.SessionOpeners != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneSessionOpeners, contributorID, b.SessionOpeners); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneSessionOpeners.ID, err)
		}
	}
	if b.WorkspaceResolvers != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneWorkspaceResolvers, contributorID, b.WorkspaceResolvers); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneWorkspaceResolvers.ID, err)
		}
	}
	if b.ToolCatalogFilters != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCatalogFilters, contributorID, b.ToolCatalogFilters); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneToolCatalogFilters.ID, err)
		}
	}
	if b.ToolCallPolicies != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallPolicies, contributorID, b.ToolCallPolicies); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneToolCallPolicies.ID, err)
		}
	}
	if b.ToolCallFinalizers != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizers, contributorID, b.ToolCallFinalizers); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneToolCallFinalizers.ID, err)
		}
	}
	if b.ToolCallFinalizationMaxArgsBytes != 0 {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizationMaxArgsBytes, contributorID, b.ToolCallFinalizationMaxArgsBytes); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneToolCallFinalizationMaxArgsBytes.ID, err)
		}
	}
	if b.CompactionObservers != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneCompactionObservers, contributorID, b.CompactionObservers); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneCompactionObservers.ID, err)
		}
	}
	if b.CompactionPreservers != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneCompactionPreservers, contributorID, b.CompactionPreservers); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneCompactionPreservers.ID, err)
		}
	}
	if b.TrafficObservers != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneTrafficObservers, contributorID, b.TrafficObservers); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneTrafficObservers.ID, err)
		}
	}
	if b.UsageObservers != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneUsageObservers, contributorID, b.UsageObservers); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneUsageObservers.ID, err)
		}
	}
	if b.RawCaptureSinks != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneRawCaptureSinks, contributorID, b.RawCaptureSinks); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneRawCaptureSinks.ID, err)
		}
	}
	if b.TrafficRedactors != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneTrafficRedactors, contributorID, b.TrafficRedactors); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneTrafficRedactors.ID, err)
		}
	}
	if b.StreamObserverFactories != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneStreamObserverFactories, contributorID, b.StreamObserverFactories); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneStreamObserverFactories.ID, err)
		}
	}
	if b.LocalTurnHandlers != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneLocalTurnHandlers, contributorID, b.LocalTurnHandlers); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneLocalTurnHandlers.ID, err)
		}
	}
	if b.TerminalDecisionProvider != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneTerminalDecisionProvider, contributorID, b.TerminalDecisionProvider); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneTerminalDecisionProvider.ID, err)
		}
	}
	if b.SecretGuards != nil {
		if err := lipfeature.Contribute(cs, lipfeature.PlaneSecretGuards, contributorID, b.SecretGuards); err != nil {
			return lipfeature.FrozenPlaneSet{}, fmt.Errorf("testkit: contribute %s: %w", lipfeature.PlaneSecretGuards.ID, err)
		}
	}
	return cs.Freeze(), nil
}

// FreezeTestBundle converts a TestFeatureBundle into a FrozenPlaneSet,
// panicking if any contribution fails.
func FreezeTestBundle(b TestFeatureBundle) lipfeature.FrozenPlaneSet {
	frozen, err := FreezeTestBundleChecked(b)
	if err != nil {
		panic(fmt.Errorf("testkit: freeze test bundle: %w", err))
	}
	return frozen
}

// FeatureBundle constructs a FeatureBundle by contributing into a new ContributionSet,
// freezing it, and wrapping it via lipfeature.BundleFromPlanes.
// contributorID provides the bundle/error-context identifier for test failure reporting and does
// not override contributor IDs passed to lipfeature.Contribute inside the callback.
// If contribute is nil, an empty PlaneSet is used.
// If contribute fails, the test fails via t.Fatalf (or panics if t is nil).
func FeatureBundle( //nolint:thelper // tb is optional to support testing panic fallback on nil
	tb testing.TB,
	contributorID string,
	contribute func(*lipfeature.ContributionSet) error,
	lifecycles []lipplugin.Lifecycle,
) lipfeature.FeatureBundle {
	if tb != nil {
		tb.Helper()
	}
	if contributorID == "" {
		contributorID = "test-feature"
	}
	cs := lipfeature.NewContributionSet()
	if contribute != nil {
		if err := contribute(cs); err != nil {
			if tb != nil {
				tb.Fatalf("testkit.FeatureBundle (%s): contribute: %v", contributorID, err)
			}
			panic(fmt.Errorf("testkit.FeatureBundle (%s): contribute: %w", contributorID, err))
		}
	}
	frozen := cs.Freeze()
	return lipfeature.BundleFromPlanes(frozen, lifecycles)
}

// FreezeBundle merges one or more FeatureBundles through generated plane adapters
// and returns the resulting FrozenPlaneSet. Panics if merging fails, making it ideal
// for concise test setup.
func FreezeBundle(bundles ...lipfeature.FeatureBundle) lipfeature.FrozenPlaneSet {
	cs := lipfeature.NewContributionSet()
	for i, b := range bundles {
		id := fmt.Sprintf("bundle-%d", i)
		if provId, ok := lipfeature.FrozenIdentity(b.PlaneSet, lipfeature.PlaneTerminalDecisionProvider); ok && provId != "" {
			id = provId
		}
		if err := b.PlaneSet.ReplayTo(cs, id); err != nil {
			panic(err)
		}
	}
	return cs.Freeze()
}
