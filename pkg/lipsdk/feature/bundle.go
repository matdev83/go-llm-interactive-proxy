package feature

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
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
	RequestTransforms  []request.Transform
	PreRequestHandlers []prerequest.Handler
	RouteHintProviders []routehint.Provider

	CompletionGates []completion.Gate

	TrafficObservers []traffic.Observer
	UsageObservers   []usage.Observer
	RawCaptureSinks  []traffic.RawCaptureSink
	TrafficRedactors []traffic.Redactor

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
		len(b.RequestTransforms) == 0 &&
		len(b.PreRequestHandlers) == 0 &&
		len(b.RouteHintProviders) == 0 &&
		len(b.CompletionGates) == 0 &&
		len(b.TrafficObservers) == 0 &&
		len(b.UsageObservers) == 0 &&
		len(b.RawCaptureSinks) == 0 &&
		len(b.TrafficRedactors) == 0 &&
		len(b.Lifecycles) == 0
}

// Validate checks schema metadata against bundle contents. An empty bundle may use
// SchemaVersion 0 (unset) or SchemaVersionV1; any non-empty bundle must declare SchemaVersionV1.
func (b FeatureBundle) Validate() error {
	if b.empty() {
		if b.SchemaVersion != 0 && b.SchemaVersion != SchemaVersionV1 {
			return fmt.Errorf("feature: FeatureBundle: invalid schema version %d for empty bundle", b.SchemaVersion)
		}
		return nil
	}
	if b.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("feature: FeatureBundle: schema version want %d got %d", SchemaVersionV1, b.SchemaVersion)
	}
	return nil
}
