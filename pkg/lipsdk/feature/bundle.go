package feature

import (
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
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

// SchemaVersionV1 is the initial FeatureBundle wire/compile contract. New optional
// fields may be added in backward-compatible ways; bump only when breaking stable fields.
const SchemaVersionV1 = 1

// FeatureBundle is the typed contribution of one feature plugin instance: version
// metadata, hook chains (same interface types as the core hook bus), immutable plane set,
// and optional lifecycles.
type FeatureBundle struct {
	SchemaVersion int

	// PlaneSet is the immutable composed extension plane snapshot (V1).
	PlaneSet FrozenPlaneSet

	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	SubmitHooks []sdkhooks.SubmitHook
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	RequestPartHooks []sdkhooks.RequestPartHook
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	ResponsePartHooks []sdkhooks.ResponsePartHook
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	ToolReactors []sdkhooks.ToolReactor

	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	SessionOpeners []session.Opener
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	WorkspaceResolvers []workspace.Resolver

	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	ToolCatalogFilters []toolcatalog.Filter
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	ToolCallPolicies []toolpolicy.Policy
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	ToolCallFinalizers []toolcall.Finalizer
	// ToolCallFinalizationMaxArgsBytes is an optional per-bundle assembler buffer
	// cap (bytes). Zero means no contribution. Positive values are contributions;
	// Validate rejects negatives. Mergers take the minimum of positive
	// contributions so the strictest enabled finalizer wins.
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	ToolCallFinalizationMaxArgsBytes int
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	RequestTransforms []request.Transform
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	PreRequestHandlers []prerequest.Handler
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	RouteHintProviders []routehint.Provider

	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	CompletionGates []completion.Gate

	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	AttemptTransforms []request.AttemptTransform

	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	StreamObserverFactories []response.StreamObserverFactory

	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	TrafficObservers []traffic.Observer
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	UsageObservers []usage.Observer
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	RawCaptureSinks []traffic.RawCaptureSink
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	TrafficRedactors []traffic.Redactor

	// CompactionObservers subscribe to typed, fail-open proxy-derived compaction
	// lifecycle observations (optional; schema V1). Observers are non-mutating and
	// never receive prompt/response content.
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	CompactionObservers []compaction.Observer
	// CompactionPreservers are ordered content-bearing preservation callbacks
	// (optional; schema V1). They are distinct from metadata-only observers and
	// run at explicit request/open/final-response boundaries.
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	CompactionPreservers []compaction.Preserver

	// SecretGuards contribute opaque ingress secret-guard evaluators (optional; schema V1).
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	SecretGuards []secretguard.Guard

	// LocalTurnHandlers contribute generic proxy-local turn handlers (optional; schema V1).
	// Each handler is ordered via Handler.Order/ID; nil entries are rejected by Validate.
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	LocalTurnHandlers []localturn.Handler

	// TerminalDecisionProvider contributes one provider-neutral provisional-terminal
	// decision evaluator (optional; schema V1). Generation composition rejects
	// contributions from more than one enabled feature.
	// Deprecated: use PlaneSet via ContributionSet / BundleFromPlanes.
	TerminalDecisionProvider terminaldecision.Provider

	Lifecycles []lipplugin.Lifecycle
}

// BundleFromPlanes constructs a FeatureBundle from a FrozenPlaneSet and optional lifecycles.
// It sets SchemaVersion to SchemaVersionV1, defensively clones the FrozenPlaneSet and lifecycles
// (preserving nil vs explicit empty slice semantics), and performs no validation side-effects.
func BundleFromPlanes(planes FrozenPlaneSet, lifecycles []lipplugin.Lifecycle) FeatureBundle {
	return FeatureBundle{
		SchemaVersion: SchemaVersionV1,
		PlaneSet:      planes.Clone(),
		Lifecycles:    cloneSlice(lifecycles),
	}
}

func (b FeatureBundle) hasLegacyPlaneFields() bool {
	return b.SubmitHooks != nil ||
		b.RequestPartHooks != nil ||
		b.ResponsePartHooks != nil ||
		b.ToolReactors != nil ||
		b.SessionOpeners != nil ||
		b.WorkspaceResolvers != nil ||
		b.ToolCatalogFilters != nil ||
		b.ToolCallPolicies != nil ||
		b.ToolCallFinalizers != nil ||
		b.ToolCallFinalizationMaxArgsBytes != 0 ||
		b.RequestTransforms != nil ||
		b.PreRequestHandlers != nil ||
		b.RouteHintProviders != nil ||
		b.CompletionGates != nil ||
		b.AttemptTransforms != nil ||
		b.StreamObserverFactories != nil ||
		b.TrafficObservers != nil ||
		b.UsageObservers != nil ||
		b.RawCaptureSinks != nil ||
		b.TrafficRedactors != nil ||
		b.CompactionObservers != nil ||
		b.CompactionPreservers != nil ||
		b.SecretGuards != nil ||
		b.LocalTurnHandlers != nil ||
		b.TerminalDecisionProvider != nil
}

func (b FeatureBundle) empty() bool {
	return b.PlaneSet.IsZero() &&
		len(b.SubmitHooks) == 0 &&
		len(b.RequestPartHooks) == 0 &&
		len(b.ResponsePartHooks) == 0 &&
		len(b.ToolReactors) == 0 &&
		len(b.SessionOpeners) == 0 &&
		len(b.WorkspaceResolvers) == 0 &&
		len(b.ToolCatalogFilters) == 0 &&
		len(b.ToolCallPolicies) == 0 &&
		len(b.ToolCallFinalizers) == 0 &&
		b.ToolCallFinalizationMaxArgsBytes == 0 &&
		len(b.RequestTransforms) == 0 &&
		len(b.PreRequestHandlers) == 0 &&
		len(b.RouteHintProviders) == 0 &&
		len(b.CompletionGates) == 0 &&
		len(b.AttemptTransforms) == 0 &&
		len(b.StreamObserverFactories) == 0 &&
		len(b.TrafficObservers) == 0 &&
		len(b.UsageObservers) == 0 &&
		len(b.RawCaptureSinks) == 0 &&
		len(b.TrafficRedactors) == 0 &&
		len(b.CompactionObservers) == 0 &&
		len(b.CompactionPreservers) == 0 &&
		len(b.SecretGuards) == 0 &&
		len(b.LocalTurnHandlers) == 0 &&
		b.TerminalDecisionProvider == nil &&
		len(b.Lifecycles) == 0
}

// Validate checks schema metadata against bundle contents. An empty bundle may use
// SchemaVersion 0 (unset) or SchemaVersionV1; any non-empty bundle must declare SchemaVersionV1.
// Negative ToolCallFinalizationMaxArgsBytes is always invalid.
func (b FeatureBundle) Validate() error {
	if !b.PlaneSet.IsZero() && b.hasLegacyPlaneFields() {
		return errors.New("feature: FeatureBundle: PlaneSet cannot be combined with deprecated named plane fields")
	}
	if b.TerminalDecisionProvider != nil {
		if _, err := terminaldecision.ProviderIdentity(b.TerminalDecisionProvider); err != nil {
			return fmt.Errorf("feature: FeatureBundle: TerminalDecisionProvider: %w", err)
		}
	}
	if b.ToolCallFinalizationMaxArgsBytes < 0 {
		return fmt.Errorf("feature: FeatureBundle: ToolCallFinalizationMaxArgsBytes must be >= 0, got %d", b.ToolCallFinalizationMaxArgsBytes)
	}
	if b.empty() {
		if b.SchemaVersion != 0 && b.SchemaVersion != SchemaVersionV1 {
			return fmt.Errorf("feature: FeatureBundle: invalid schema version %d for empty bundle", b.SchemaVersion)
		}
		return nil
	}
	if b.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("feature: FeatureBundle: schema version want %d got %d", SchemaVersionV1, b.SchemaVersion)
	}
	if !b.PlaneSet.IsZero() {
		if err := b.PlaneSet.Validate(); err != nil {
			return fmt.Errorf("feature: FeatureBundle: PlaneSet: %w", err)
		}
		return nil
	}
	for i, at := range b.AttemptTransforms {
		if isNilValue(at) {
			return fmt.Errorf("feature: FeatureBundle: AttemptTransforms[%d] must not be nil", i)
		}
	}
	for i, f := range b.StreamObserverFactories {
		if f == nil {
			return fmt.Errorf("feature: FeatureBundle: StreamObserverFactories[%d] must not be nil", i)
		}
	}
	for i, p := range b.CompactionPreservers {
		if p == nil {
			return fmt.Errorf("feature: FeatureBundle: CompactionPreservers[%d] must not be nil", i)
		}
	}
	for i, h := range b.LocalTurnHandlers {
		if localturn.IsNilHandler(h) {
			return fmt.Errorf("feature: FeatureBundle: LocalTurnHandlers[%d] must not be nil", i)
		}
		if err := localturn.ValidateHandlerID(h.ID()); err != nil {
			return fmt.Errorf("feature: FeatureBundle: LocalTurnHandlers[%d] invalid id: %w", i, err)
		}
	}
	return nil
}
