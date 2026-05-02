package diag

import (
	"context"
	"fmt"
	"log/slog"

	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)

// FeatureRegistry is the minimal surface needed to introspect feature bundles for inventory (R14).
// *pluginreg.Registry implements this without importing pluginreg from diag (avoids import cycles).
type FeatureRegistry interface {
	BuildFeatureBundle(factoryKey string, n yaml.Node) (lipfeature.FeatureBundle, error)
}

// InventoryExtras supplies optional registry context for extension inventory (R14).
type InventoryExtras struct {
	Reg           FeatureRegistry
	Registrations []lipsdk.Registration
}

// InventoryExtensions is the operator-visible extension surface (design §14).
type InventoryExtensions struct {
	LegalPipeline []string                     `json:"legal_pipeline"`
	Stages        []InventoryExtensionStage    `json:"stages"`
	Features      []InventoryFeatureExtensions `json:"features"`
}

// InventoryExtensionStage lists default failure policy per legal stage id.
type InventoryExtensionStage struct {
	ID             string `json:"id"`
	DefaultFailure string `json:"default_failure"`
}

// InventoryFeatureExtensions is per configured feature row plus brownfield hook occupancy.
type InventoryFeatureExtensions struct {
	InstanceID     string                    `json:"instance_id"`
	FactoryKind    string                    `json:"factory_kind"`
	Enabled        bool                      `json:"enabled"`
	BundleError    string                    `json:"bundle_error,omitempty"`
	StageOccupancy []InventoryStageOccupancy `json:"stage_occupancy"`
	Privileges     InventoryPrivileges       `json:"privileges"`
}

// InventoryStageOccupancy maps one legal stage to sorted handler ids for this plugin instance.
//
// Brownfield mapping: submit hooks → submit_request; tool catalog filters → tool_catalog_filter;
// request-wide transforms and request-part hooks → request_wide_shaping (prefixes distinguish roles);
// response-part hooks → stream_event_mutation; tool reactors → tool_event_reaction.
type InventoryStageOccupancy struct {
	StageID    string   `json:"stage_id"`
	HandlerIDs []string `json:"handler_ids"`
	Count      int      `json:"count"`
}

// InventoryPrivileges surfaces privileged contract boundaries (all false until bundles declare them).
type InventoryPrivileges struct {
	RawCapture        bool `json:"raw_capture"`
	AuxiliaryRequests bool `json:"auxiliary_requests"`
	AuthProvider      bool `json:"auth_provider"`
	CompletionGate    bool `json:"completion_gate"`
}

func zeroPrivileges() InventoryPrivileges {
	return InventoryPrivileges{
		RawCapture:        false,
		AuxiliaryRequests: false,
		AuthProvider:      false,
		CompletionGate:    false,
	}
}

func buildInventoryExtensions(ctx context.Context, cfg *config.Config, extras *InventoryExtras) InventoryExtensions {
	if ctx == nil {
		ctx = context.TODO()
	}
	pipeline := extensions.LegalPipelineStageNames()
	stages := make([]InventoryExtensionStage, 0, len(pipeline))
	for _, id := range pipeline {
		stages = append(stages, InventoryExtensionStage{
			ID:             id,
			DefaultFailure: extensions.FailurePolicyLabel(extensions.DefaultFailurePolicyForStage(id)),
		})
	}
	if cfg == nil {
		return InventoryExtensions{
			LegalPipeline: pipeline,
			Stages:        stages,
			Features:      []InventoryFeatureExtensions{},
		}
	}

	var reg FeatureRegistry
	var regs []lipsdk.Registration
	if extras != nil {
		reg = extras.Reg
		if len(extras.Registrations) > 0 {
			regs = extras.Registrations
		}
	}
	if len(regs) == 0 {
		regs = config.RegistrationsFromConfig(cfg)
	}

	featureRows := cfg.Plugins.Features
	feats := make([]InventoryFeatureExtensions, 0, len(featureRows))
	for _, pc := range featureRows {
		entry := InventoryFeatureExtensions{
			InstanceID:     pc.InstanceID(),
			FactoryKind:    pc.FactoryID(),
			Enabled:        pc.Enabled,
			StageOccupancy: []InventoryStageOccupancy{},
			Privileges:     zeroPrivileges(),
		}
		if reg != nil && pc.Enabled {
			if err := ctx.Err(); err != nil {
				entry.BundleError = err.Error()
			} else if r, ok := findFeatureRegistration(regs, pc.InstanceID()); !ok {
				msg := "diag: feature registration row missing for inventory snapshot (extras vs config mismatch)"
				entry.BundleError = msg
				slog.Default().Warn(
					"inventory extensions",
					"bundle_error", msg,
					"instance_id", entry.InstanceID,
					"factory_kind", entry.FactoryKind,
				)
			} else {
				b, err := reg.BuildFeatureBundle(r.RegistryFactoryKey(), r.Config.Node)
				if err != nil {
					entry.BundleError = err.Error()
					slog.Default().Warn(
						"inventory extensions",
						"bundle_error", err.Error(),
						"instance_id", entry.InstanceID,
						"factory_kind", entry.FactoryKind,
					)
				} else if vErr := b.Validate(); vErr != nil {
					entry.BundleError = vErr.Error()
					slog.Default().Warn(
						"inventory extensions",
						"bundle_error", vErr.Error(),
						"instance_id", entry.InstanceID,
						"factory_kind", entry.FactoryKind,
					)
				} else {
					entry.StageOccupancy = stageOccupancyFromBundle(b)
					if len(b.RequestTransforms) > 0 || len(b.ToolCatalogFilters) > 0 || len(b.CompletionGates) > 0 {
						entry.Privileges.AuxiliaryRequests = true
					}
					if len(b.CompletionGates) > 0 {
						entry.Privileges.CompletionGate = true
					}
					if len(b.RawCaptureSinks) > 0 {
						entry.Privileges.RawCapture = true
					}
				}
			}
		}
		feats = append(feats, entry)
	}
	return InventoryExtensions{
		LegalPipeline: pipeline,
		Stages:        stages,
		Features:      feats,
	}
}

func findFeatureRegistration(regs []lipsdk.Registration, instanceID string) (lipsdk.Registration, bool) {
	for _, r := range regs {
		if r.Kind == lipsdk.PluginKindFeature && r.ID == instanceID {
			return r, true
		}
	}
	return lipsdk.Registration{}, false
}

func stageOccupancyFromBundle(b lipfeature.FeatureBundle) []InventoryStageOccupancy {
	ms := hooks.MaterializeSorted(hooks.Config{
		SubmitHooks:       b.SubmitHooks,
		RequestPartHooks:  b.RequestPartHooks,
		ResponsePartHooks: b.ResponsePartHooks,
		ToolReactors:      b.ToolReactors,
	})
	out := make([]InventoryStageOccupancy, 0, 4)
	if n := len(ms.SubmitHooks); n > 0 {
		ids := make([]string, n)
		for i := range ms.SubmitHooks {
			ids[i] = ms.SubmitHooks[i].ID()
		}
		out = append(out, InventoryStageOccupancy{
			StageID:    extensions.StageSubmit,
			HandlerIDs: ids,
			Count:      n,
		})
	}
	if n := len(b.ToolCatalogFilters); n > 0 {
		sorted := toolcatalog.MaterializeSorted(b.ToolCatalogFilters)
		ids := make([]string, 0, n)
		for _, f := range sorted {
			if f == nil {
				continue
			}
			ids = append(ids, "tool_catalog:"+f.ID())
		}
		if len(ids) > 0 {
			out = append(out, InventoryStageOccupancy{
				StageID:    extensions.StageToolCatalog,
				HandlerIDs: ids,
				Count:      len(ids),
			})
		}
	}
	if n := len(b.RequestTransforms); n > 0 {
		sorted := request.MaterializeSorted(b.RequestTransforms)
		ids := make([]string, 0, n)
		for _, tr := range sorted {
			if tr == nil {
				continue
			}
			ids = append(ids, "request_transform:"+tr.ID())
		}
		if len(ids) > 0 {
			out = append(out, InventoryStageOccupancy{
				StageID:    extensions.StageRequestWide,
				HandlerIDs: ids,
				Count:      len(ids),
			})
		}
	}
	if n := len(b.RouteHintProviders); n > 0 {
		sorted := routehint.MaterializeSorted(b.RouteHintProviders)
		ids := make([]string, 0, n)
		for _, p := range sorted {
			if p == nil {
				continue
			}
			ids = append(ids, "route_hint:"+p.ID())
		}
		if len(ids) > 0 {
			out = append(out, InventoryStageOccupancy{
				StageID:    extensions.StageRouteHinting,
				HandlerIDs: ids,
				Count:      len(ids),
			})
		}
	}
	if n := len(ms.RequestPartHooks); n > 0 {
		ids := make([]string, n)
		for i := range ms.RequestPartHooks {
			ids[i] = "request_part:" + ms.RequestPartHooks[i].ID()
		}
		out = append(out, InventoryStageOccupancy{
			StageID:    extensions.StageRequestWide,
			HandlerIDs: ids,
			Count:      n,
		})
	}
	if n := len(ms.ResponsePartHooks); n > 0 {
		ids := make([]string, n)
		for i := range ms.ResponsePartHooks {
			ids[i] = ms.ResponsePartHooks[i].ID()
		}
		out = append(out, InventoryStageOccupancy{
			StageID:    extensions.StageStreamEventMutation,
			HandlerIDs: ids,
			Count:      n,
		})
	}
	toolReactionIDs := make([]string, 0, len(b.ToolCallPolicies)+len(ms.ToolReactors))
	for _, pol := range toolpolicy.MaterializeSorted(b.ToolCallPolicies) {
		if pol == nil {
			continue
		}
		toolReactionIDs = append(toolReactionIDs, "tool_policy:"+pol.ID())
	}
	for i := range ms.ToolReactors {
		toolReactionIDs = append(toolReactionIDs, ms.ToolReactors[i].ID())
	}
	if len(toolReactionIDs) > 0 {
		out = append(out, InventoryStageOccupancy{
			StageID:    extensions.StageToolEventReaction,
			HandlerIDs: toolReactionIDs,
			Count:      len(toolReactionIDs),
		})
	}
	sessionOpenIDs := make([]string, 0, len(b.SessionOpeners)+len(b.WorkspaceResolvers))
	for _, o := range b.SessionOpeners {
		if o == nil {
			continue
		}
		sessionOpenIDs = append(sessionOpenIDs, "opener:"+o.ID())
	}
	for i := range b.WorkspaceResolvers {
		if b.WorkspaceResolvers[i] == nil {
			continue
		}
		sessionOpenIDs = append(sessionOpenIDs, fmt.Sprintf("workspace_resolver:%d", i))
	}
	if len(sessionOpenIDs) > 0 {
		out = append(out, InventoryStageOccupancy{
			StageID:    extensions.StageSessionOpen,
			HandlerIDs: sessionOpenIDs,
			Count:      len(sessionOpenIDs),
		})
	}
	if n := len(b.CompletionGates); n > 0 {
		sorted := completion.MaterializeSorted(b.CompletionGates)
		ids := make([]string, 0, n)
		for _, g := range sorted {
			if g == nil {
				continue
			}
			ids = append(ids, "completion_gate:"+g.ID())
		}
		if len(ids) > 0 {
			out = append(out, InventoryStageOccupancy{
				StageID:    extensions.StageCompletionGating,
				HandlerIDs: ids,
				Count:      len(ids),
			})
		}
	}
	trafficIDs := make([]string, 0, len(b.TrafficObservers)+len(b.UsageObservers)+len(b.RawCaptureSinks)+len(b.TrafficRedactors))
	for i, o := range b.TrafficObservers {
		if o == nil {
			continue
		}
		trafficIDs = append(trafficIDs, fmt.Sprintf("traffic_observer:%d", i))
	}
	for i, o := range b.UsageObservers {
		if o == nil {
			continue
		}
		trafficIDs = append(trafficIDs, fmt.Sprintf("usage_observer:%d", i))
	}
	for i := range b.RawCaptureSinks {
		if b.RawCaptureSinks[i] == nil {
			continue
		}
		trafficIDs = append(trafficIDs, fmt.Sprintf("raw_capture:%d", i))
	}
	for _, r := range b.TrafficRedactors {
		if r == nil {
			continue
		}
		trafficIDs = append(trafficIDs, "traffic_redactor:"+r.ID())
	}
	if len(trafficIDs) > 0 {
		out = append(out, InventoryStageOccupancy{
			StageID:    extensions.StageTrafficObservation,
			HandlerIDs: trafficIDs,
			Count:      len(trafficIDs),
		})
	}
	return out
}
