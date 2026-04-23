package refworkspaceguard

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

const defaultOrder = 45

// State namespace and key for session-scoped unlock (second request sees gated tool).
const stateNS = "ref_workspace_guard"

// StateKeyUnlocked is set by the request transform after each request.
const StateKeyUnlocked = "unlocked"

// GatedToolName is removed from the catalog until state marks the session unlocked.
const GatedToolName = "ref_ws_gated_tool"

// LabelDenyHeat enables the tool reactor heat prefix rule when set to "1" in the workspace view.
const LabelDenyHeat = "ref_deny_heat"

// HeatToolPrefix is blocked in the tool stream when LabelDenyHeat is active.
const HeatToolPrefix = "ref_heat_"

func bindPluginState(store lipstate.Store) lipstate.Store {
	if store == nil {
		return nil
	}
	return lipstate.BindPlugin(store, ID)
}

type staticResolver struct {
	view workspace.WorkspaceView
}

// NewStaticResolver returns a fixed workspace view from config.
func NewStaticResolver(cfg Config) workspace.Resolver {
	return staticResolver{view: viewFromConfig(cfg)}
}

func viewFromConfig(cfg Config) workspace.WorkspaceView {
	labels := cfg.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	// Defensive copy
	lc := make(map[string]string, len(labels))
	maps.Copy(lc, labels)
	return workspace.WorkspaceView{
		ProjectRoot: cfg.ProjectRoot,
		DirtyTree:   cfg.DirtyTree,
		Markers:     append([]string(nil), cfg.Markers...),
		Labels:      lc,
	}
}

func (s staticResolver) Resolve(context.Context) (workspace.WorkspaceView, error) {
	return s.view, nil
}

type sessionUnlockRtx struct {
	order int
}

var _ request.Transform = sessionUnlockRtx{}

// NewSessionUnlockTransform records session progress so a later request can expose gated tools.
func NewSessionUnlockTransform(cfg Config) request.Transform {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return sessionUnlockRtx{order: o}
}

func (r sessionUnlockRtx) ID() string                   { return ID + "-unlock" }
func (r sessionUnlockRtx) Order() int                   { return r.order }
func (r sessionUnlockRtx) FailureMode() sdk.FailureMode { return sdk.FailOpen }

func (r sessionUnlockRtx) Handle(ctx context.Context, _ *lipapi.Call, _ request.RequestMeta, svc request.Services) error {
	store := bindPluginState(svc.State)
	if store == nil {
		return nil
	}
	// Unlocks catalog filter on subsequent requests in the same session (proof of state).
	return store.Put(ctx, lipstate.ScopeSession, stateNS, StateKeyUnlocked, true, time.Hour)
}

type catalogGate struct {
	order int
}

var _ toolcatalog.Filter = catalogGate{}

// NewCatalogFilter removes GatedToolName until the session unlock transform has run.
func NewCatalogFilter(cfg Config) toolcatalog.Filter {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return catalogGate{order: o}
}

func (f catalogGate) ID() string                   { return ID + "-catalog" }
func (f catalogGate) Order() int                   { return f.order }
func (f catalogGate) FailureMode() sdk.FailureMode { return sdk.FailOpen }

func (f catalogGate) Handle(ctx context.Context, call *lipapi.Call, _ toolcatalog.CatalogMeta, svc toolcatalog.Services) error {
	store := bindPluginState(svc.State)
	if call == nil || store == nil {
		return nil
	}
	var unlocked bool
	found, err := store.Get(ctx, lipstate.ScopeSession, stateNS, StateKeyUnlocked, &unlocked)
	if err != nil {
		return err
	}
	if found && unlocked {
		return nil
	}
	out := call.Tools[:0]
	for _, t := range call.Tools {
		if t.Name == GatedToolName {
			continue
		}
		out = append(out, t)
	}
	call.Tools = out
	return nil
}

type heatGuard struct {
	order int
}

var _ sdk.ToolReactor = heatGuard{}

// NewHeatReactor enforces the workspace label policy on tool stream events.
func NewHeatReactor(cfg Config) sdk.ToolReactor {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return heatGuard{order: o}
}

func (g heatGuard) ID() string { return ID + "-heat" }
func (g heatGuard) Order() int { return g.order }

func (g heatGuard) HandleToolEvent(_ context.Context, te lipapi.ToolEvent, meta sdk.ToolMeta) (sdk.ToolDecision, lipapi.ToolEvent, error) {
	if meta.Workspace.Labels[LabelDenyHeat] == "1" && strings.HasPrefix(te.ToolName, HeatToolPrefix) {
		return sdk.ToolSwallow, lipapi.ToolEvent{}, nil
	}
	return sdk.ToolPass, lipapi.ToolEvent{}, nil
}
