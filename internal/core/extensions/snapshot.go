package extensions

import (
	"context"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type snapCtxKey struct{}

// RequestRuntimeSnapshot is a per-build binding of hook chains and service facades published
// onto each request context (design §15B, task 4.2). Many request goroutines may read the same
// pointer without synchronization; callers must treat it as frozen after construction: do not
// replace fields, mutate the embedded [*hooks.Bus], or swap facade implementations. Config reload
// or rebinding must publish a new snapshot (new [RequestRuntimeSnapshot] value and new executor
// wiring from [github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle.Build]).
type RequestRuntimeSnapshot struct {
	hookBus            *hooks.Bus
	state              state.Store
	aux                auxiliary.Client
	obs                traffic.Observer
	usageObs           usage.Observer
	raw                traffic.RawCaptureSink
	ws                 workspace.Resolver
	sessionOpeners     []session.Opener
	toolCatalogFilters []toolcatalog.Filter
	toolCallPolicies   []toolpolicy.Policy
	requestTransforms  []request.Transform
	routeHintProviders []routehint.Provider
	completionGates    []completion.Gate
	trafficRedactors   []traffic.Redactor
	gen                int64
}

// SnapshotOptions configures optional facades; zero value uses disabled placeholders.
type SnapshotOptions struct {
	State              state.Store
	Aux                auxiliary.Client
	TrafficObserver    traffic.Observer
	UsageObserver      usage.Observer
	RawCapture         traffic.RawCaptureSink
	Workspace          workspace.Resolver
	SessionOpeners     []session.Opener
	ToolCatalogFilters []toolcatalog.Filter
	ToolCallPolicies   []toolpolicy.Policy
	RequestTransforms  []request.Transform
	RouteHintProviders []routehint.Provider
	CompletionGates    []completion.Gate
	TrafficRedactors   []traffic.Redactor
	Generation         int64
}

// NewRequestRuntimeSnapshot captures bus and facades for the lifetime of the returned value.
// bus must be non-nil (or replaced with [hooks.New] empty bus). The same [*hooks.Bus] must not
// be mutated after this call if the snapshot is shared across concurrent requests.
func NewRequestRuntimeSnapshot(bus *hooks.Bus, opts SnapshotOptions) *RequestRuntimeSnapshot {
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	st := opts.State
	if st == nil {
		st = state.DisabledStore{}
	}
	ax := opts.Aux
	if ax == nil {
		ax = auxiliary.DisabledClient{}
	}
	ob := opts.TrafficObserver
	if ob == nil {
		ob = traffic.NoopObserver{}
	}
	uob := opts.UsageObserver
	if uob == nil {
		uob = usage.NoopObserver{}
	}
	raw := opts.RawCapture
	if raw == nil {
		raw = traffic.DisabledRawCapture{}
	}
	ws := opts.Workspace
	if ws == nil {
		ws = workspace.DisabledResolver{}
	}
	openers := slices.Clone(opts.SessionOpeners)
	catalog := slices.Clone(opts.ToolCatalogFilters)
	// Frozen execution order for the request lifetime (same contract as [toolpolicy.MaterializeSorted]).
	policies := toolpolicy.MaterializeSorted(opts.ToolCallPolicies)
	transforms := slices.Clone(opts.RequestTransforms)
	routeHints := slices.Clone(opts.RouteHintProviders)
	compGates := slices.Clone(opts.CompletionGates)
	reds := traffic.MaterializeSortedRedactors(opts.TrafficRedactors)
	return &RequestRuntimeSnapshot{
		hookBus:            bus,
		state:              st,
		aux:                ax,
		obs:                ob,
		usageObs:           uob,
		raw:                raw,
		ws:                 ws,
		sessionOpeners:     openers,
		toolCatalogFilters: catalog,
		toolCallPolicies:   policies,
		requestTransforms:  transforms,
		routeHintProviders: routeHints,
		completionGates:    compGates,
		trafficRedactors:   reds,
		gen:                opts.Generation,
	}
}

// HookBus returns the hook bus bound at snapshot construction (brownfield compatibility).
func (s *RequestRuntimeSnapshot) HookBus() *hooks.Bus {
	if s == nil {
		return nil
	}
	return s.hookBus
}

// State returns the plugin state facade for this snapshot.
func (s *RequestRuntimeSnapshot) State() state.Store {
	if s == nil {
		return nil
	}
	return s.state
}

// Aux returns the auxiliary request client for this snapshot.
func (s *RequestRuntimeSnapshot) Aux() auxiliary.Client {
	if s == nil {
		return nil
	}
	return s.aux
}

// TrafficObserver returns the structured traffic observer for this snapshot.
func (s *RequestRuntimeSnapshot) TrafficObserver() traffic.Observer {
	if s == nil {
		return nil
	}
	return s.obs
}

// UsageObserver returns the usage observer for this snapshot.
func (s *RequestRuntimeSnapshot) UsageObserver() usage.Observer {
	if s == nil {
		return nil
	}
	return s.usageObs
}

// RawCapture returns the privileged raw capture sink for this snapshot.
func (s *RequestRuntimeSnapshot) RawCapture() traffic.RawCaptureSink {
	if s == nil {
		return nil
	}
	return s.raw
}

// Workspace returns the workspace resolver for this snapshot.
func (s *RequestRuntimeSnapshot) Workspace() workspace.Resolver {
	if s == nil {
		return nil
	}
	return s.ws
}

// SessionOpeners returns a defensive copy of the frozen session-open stage handlers (may be empty).
// Mutating the returned slice does not affect the snapshot.
func (s *RequestRuntimeSnapshot) SessionOpeners() []session.Opener {
	if s == nil {
		return nil
	}
	return slices.Clone(s.sessionOpeners)
}

// ToolCatalogFilters returns a defensive copy of frozen catalog filters (may be empty).
func (s *RequestRuntimeSnapshot) ToolCatalogFilters() []toolcatalog.Filter {
	if s == nil {
		return nil
	}
	return slices.Clone(s.toolCatalogFilters)
}

// ToolCallPolicies returns a defensive copy of frozen tool-call policies (may be empty).
// Mutating the returned slice does not affect the snapshot.
func (s *RequestRuntimeSnapshot) ToolCallPolicies() []toolpolicy.Policy {
	if s == nil {
		return nil
	}
	return slices.Clone(s.toolCallPolicies)
}

// ToolCallPoliciesExecution returns the frozen tool-call policy slice in execution order
// (the same ordering as [toolpolicy.MaterializeSorted]). The returned slice must not be
// mutated; it is the snapshot's internal backing store. Prefer [RequestRuntimeSnapshot.ToolCallPolicies]
// for a defensive copy; this accessor exists for the runtime executor hot path.
func (s *RequestRuntimeSnapshot) ToolCallPoliciesExecution() []toolpolicy.Policy {
	if s == nil {
		return nil
	}
	return s.toolCallPolicies
}

// RequestTransforms returns a defensive copy of frozen request-wide transforms (may be empty).
func (s *RequestRuntimeSnapshot) RequestTransforms() []request.Transform {
	if s == nil {
		return nil
	}
	return slices.Clone(s.requestTransforms)
}

// RouteHintProviders returns a defensive copy of frozen route hint providers (may be empty).
func (s *RequestRuntimeSnapshot) RouteHintProviders() []routehint.Provider {
	if s == nil {
		return nil
	}
	return slices.Clone(s.routeHintProviders)
}

// CompletionGates returns a defensive copy of frozen completion gates (may be empty).
func (s *RequestRuntimeSnapshot) CompletionGates() []completion.Gate {
	if s == nil {
		return nil
	}
	return slices.Clone(s.completionGates)
}

// TrafficRedactors returns a defensive copy of frozen redactors for the traffic pipeline (may be empty).
func (s *RequestRuntimeSnapshot) TrafficRedactors() []traffic.Redactor {
	if s == nil {
		return nil
	}
	return slices.Clone(s.trafficRedactors)
}

// Generation is an opaque build stamp (e.g. config reload generation in a future spec).
func (s *RequestRuntimeSnapshot) Generation() int64 {
	if s == nil {
		return 0
	}
	return s.gen
}

// WithRequestRuntimeSnapshot attaches snap to ctx for the remainder of the request lifetime.
// snap must remain valid and unchanged for the lifetime of ctx (see [RequestRuntimeSnapshot]).
func WithRequestRuntimeSnapshot(ctx context.Context, snap *RequestRuntimeSnapshot) context.Context {
	if ctx == nil || snap == nil {
		return ctx
	}
	return context.WithValue(ctx, snapCtxKey{}, snap)
}

// RequestRuntimeSnapshotFromContext returns the snapshot from [WithRequestRuntimeSnapshot], if any.
func RequestRuntimeSnapshotFromContext(ctx context.Context) *RequestRuntimeSnapshot {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(snapCtxKey{})
	s, ok := raw.(*RequestRuntimeSnapshot)
	if !ok {
		return nil
	}
	return s
}
