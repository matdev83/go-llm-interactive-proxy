package feature

import (
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
// metadata, hook chains (same interface types as the core hook bus), and optional
// lifecycles. Future extension points attach as new fields with their own slices.
type FeatureBundle struct {
	SchemaVersion int

	SubmitHooks       []sdkhooks.SubmitHook
	RequestPartHooks  []sdkhooks.RequestPartHook
	ResponsePartHooks []sdkhooks.ResponsePartHook
	ToolReactors      []sdkhooks.ToolReactor

	SessionOpeners     []session.Opener
	WorkspaceResolvers []workspace.Resolver

	ToolCatalogFilters []toolcatalog.Filter
	ToolCallPolicies   []toolpolicy.Policy
	ToolCallFinalizers []toolcall.Finalizer
	// ToolCallFinalizationMaxArgsBytes is an optional per-bundle assembler buffer
	// cap (bytes). Zero means no contribution. Positive values are contributions;
	// Validate rejects negatives. Mergers take the minimum of positive
	// contributions so the strictest enabled finalizer wins.
	ToolCallFinalizationMaxArgsBytes int
	RequestTransforms                []request.Transform
	PreRequestHandlers               []prerequest.Handler
	RouteHintProviders               []routehint.Provider

	CompletionGates []completion.Gate

	AttemptTransforms []request.AttemptTransform

	StreamObserverFactories []response.StreamObserverFactory

	TrafficObservers []traffic.Observer
	UsageObservers   []usage.Observer
	RawCaptureSinks  []traffic.RawCaptureSink
	TrafficRedactors []traffic.Redactor

	// CompactionObservers subscribe to typed, fail-open proxy-derived compaction
	// lifecycle observations (optional; schema V1). Observers are non-mutating and
	// never receive prompt/response content.
	CompactionObservers []compaction.Observer
	// CompactionPreservers are ordered content-bearing preservation callbacks
	// (optional; schema V1). They are distinct from metadata-only observers and
	// run at explicit request/open/final-response boundaries.
	CompactionPreservers []compaction.Preserver

	// SecretGuards contribute opaque ingress secret-guard evaluators (optional; schema V1).
	SecretGuards []secretguard.Guard

	// LocalTurnHandlers contribute generic proxy-local turn handlers (optional; schema V1).
	// Each handler is ordered via Handler.Order/ID; nil entries are rejected by Validate.
	LocalTurnHandlers []localturn.Handler

	Lifecycles []lipplugin.Lifecycle
}

func (b FeatureBundle) empty() bool {
	return len(b.SubmitHooks) == 0 &&
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
		len(b.Lifecycles) == 0
}

// Validate checks schema metadata against bundle contents. An empty bundle may use
// SchemaVersion 0 (unset) or SchemaVersionV1; any non-empty bundle must declare SchemaVersionV1.
// Negative ToolCallFinalizationMaxArgsBytes is always invalid.
func (b FeatureBundle) Validate() error {
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
	for i, at := range b.AttemptTransforms {
		if at == nil {
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
	}
	return nil
}
