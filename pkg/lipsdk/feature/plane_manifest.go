package feature

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
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

type hookParticipant interface {
	ID() string
	Order() int
}

func sortHooks[T hookParticipant](h []T) []T {
	if len(h) == 0 {
		return nil
	}
	nonNil := make([]T, 0, len(h))
	for _, item := range h {
		var anyVal any = item
		if anyVal != nil {
			nonNil = append(nonNil, item)
		}
	}
	if len(nonNil) == 0 {
		return nil
	}
	idx := make([]int, len(nonNil))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := nonNil[hi], nonNil[hj]
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.ID(), b.ID()); c != 0 {
			return c
		}
		return cmp.Compare(hi, hj)
	})
	out := make([]T, len(nonNil))
	for k, ii := range idx {
		out[k] = nonNil[ii]
	}
	return out
}

// PlaneSubmitHooks declares the SubmitHooks extension plane.
var PlaneSubmitHooks = Plane[[]hooks.SubmitHook]{
	ID:           "submit_hooks",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []hooks.SubmitHook) ([]hooks.SubmitHook, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]hooks.SubmitHook]{
		StageID: StageIDSubmit,
		Materialize: func(v []hooks.SubmitHook) []DiagnosticOccupant {
			sorted := sortHooks(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, h := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: h.ID()})
			}
			return occupants
		},
	},
}

// PlaneRequestPartHooks declares the RequestPartHooks extension plane.
var PlaneRequestPartHooks = Plane[[]hooks.RequestPartHook]{
	ID:           "request_part_hooks",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []hooks.RequestPartHook) ([]hooks.RequestPartHook, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]hooks.RequestPartHook]{
		StageID: StageIDRequestWide,
		Materialize: func(v []hooks.RequestPartHook) []DiagnosticOccupant {
			sorted := sortHooks(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, h := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: "request_part:" + h.ID()})
			}
			return occupants
		},
	},
}

// PlaneResponsePartHooks declares the ResponsePartHooks extension plane.
var PlaneResponsePartHooks = Plane[[]hooks.ResponsePartHook]{
	ID:           "response_part_hooks",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []hooks.ResponsePartHook) ([]hooks.ResponsePartHook, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]hooks.ResponsePartHook]{
		StageID: StageIDStreamEventMutation,
		Materialize: func(v []hooks.ResponsePartHook) []DiagnosticOccupant {
			sorted := sortHooks(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, h := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: h.ID()})
			}
			return occupants
		},
	},
}

// PlaneToolReactors declares the ToolReactors extension plane.
var PlaneToolReactors = Plane[[]hooks.ToolReactor]{
	ID:           "tool_reactors",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []hooks.ToolReactor) ([]hooks.ToolReactor, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]hooks.ToolReactor]{
		StageID:       StageIDToolEventReaction,
		CoalesceGroup: "tool_reaction",
		Materialize: func(v []hooks.ToolReactor) []DiagnosticOccupant {
			sorted := sortHooks(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, r := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: r.ID()})
			}
			return occupants
		},
	},
}

// PlaneSessionOpeners declares the SessionOpeners extension plane.
var PlaneSessionOpeners = Plane[[]session.Opener]{
	ID:           "session_openers",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []session.Opener) ([]session.Opener, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]session.Opener]{
		StageID:       StageIDSessionOpen,
		CoalesceGroup: "session_open",
		Materialize: func(v []session.Opener) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for _, o := range v {
				if o == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "opener:" + o.ID()})
			}
			return occupants
		},
	},
}

// PlaneWorkspaceResolvers declares the WorkspaceResolvers extension plane.
var PlaneWorkspaceResolvers = Plane[[]workspace.Resolver]{
	ID:           "workspace_resolvers",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []workspace.Resolver) ([]workspace.Resolver, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]workspace.Resolver]{
		StageID:       StageIDSessionOpen,
		CoalesceGroup: "session_open",
		Materialize: func(v []workspace.Resolver) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for i, r := range v {
				if r == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: fmt.Sprintf("workspace_resolver:%d", i)})
			}
			return occupants
		},
	},
}

// PlaneToolCatalogFilters declares the ToolCatalogFilters extension plane.
var PlaneToolCatalogFilters = Plane[[]toolcatalog.Filter]{
	ID:           "tool_catalog_filters",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []toolcatalog.Filter) ([]toolcatalog.Filter, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]toolcatalog.Filter]{
		StageID: StageIDToolCatalog,
		Materialize: func(v []toolcatalog.Filter) []DiagnosticOccupant {
			sorted := toolcatalog.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, f := range sorted {
				if f == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "tool_catalog:" + f.ID()})
			}
			return occupants
		},
		Privileges: func(v []toolcatalog.Filter) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{"auxiliary_requests"}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlaneToolCallPolicies declares the ToolCallPolicies extension plane.
var PlaneToolCallPolicies = Plane[[]toolpolicy.Policy]{
	ID:           "tool_call_policies",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []toolpolicy.Policy) ([]toolpolicy.Policy, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{
		StageID:       StageIDToolEventReaction,
		CoalesceGroup: "tool_reaction",
		Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant {
			sorted := toolpolicy.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, p := range sorted {
				if p == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "tool_policy:" + p.ID()})
			}
			return occupants
		},
	},
}

// PlaneToolCallFinalizers declares the ToolCallFinalizers extension plane.
var PlaneToolCallFinalizers = Plane[[]toolcall.Finalizer]{
	ID:           "tool_call_finalizers",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []toolcall.Finalizer) ([]toolcall.Finalizer, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]toolcall.Finalizer]{
		StageID:       StageIDToolEventReaction,
		CoalesceGroup: "tool_reaction",
		Materialize: func(v []toolcall.Finalizer) []DiagnosticOccupant {
			sorted := toolcall.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, f := range sorted {
				if f == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "tool_finalizer:" + f.ID()})
			}
			return occupants
		},
	},
}

// PlaneToolCallFinalizationMaxArgsBytes declares the ToolCallFinalizationMaxArgsBytes extension plane.
var PlaneToolCallFinalizationMaxArgsBytes = Plane[int]{
	ID:           "tool_call_finalization_max_args_bytes",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombReduce,
	},
	NilPolicy: NilNotApplicable,
	Validate: func(v int) error {
		if v < 0 {
			return fmt.Errorf("must be >= 0, got %d", v)
		}
		return nil
	},
	Combine: func(source SourceKind, current, incoming int) (int, error) {
		if incoming <= 0 {
			return current, nil
		}
		if current <= 0 || incoming < current {
			return incoming, nil
		}
		return current, nil
	},
}

// PlaneRequestTransforms declares the RequestTransforms extension plane.
var PlaneRequestTransforms = Plane[[]request.Transform]{
	ID:           "request_transforms",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilReject,
	Validate: func(v []request.Transform) error {
		for i, tr := range v {
			if tr == nil {
				return fmt.Errorf("RequestTransforms[%d] must not be nil", i)
			}
		}
		return nil
	},
	Combine: func(source SourceKind, current, incoming []request.Transform) ([]request.Transform, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]request.Transform]{
		StageID: StageIDRequestWide,
		Materialize: func(v []request.Transform) []DiagnosticOccupant {
			nonNil := make([]request.Transform, 0, len(v))
			for _, tr := range v {
				if tr != nil {
					nonNil = append(nonNil, tr)
				}
			}
			if len(nonNil) == 0 {
				return nil
			}
			sorted := request.MaterializeSorted(nonNil)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, tr := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: "request_transform:" + tr.ID()})
			}
			return occupants
		},
		Privileges: func(v []request.Transform) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{"auxiliary_requests"}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlanePreRequestHandlers declares the PreRequestHandlers extension plane.
var PlanePreRequestHandlers = Plane[[]prerequest.Handler]{
	ID:           "pre_request_handlers",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilReject,
	Validate: func(v []prerequest.Handler) error {
		for i, h := range v {
			if h == nil {
				return fmt.Errorf("PreRequestHandlers[%d] must not be nil", i)
			}
		}
		return nil
	},
	Combine: func(source SourceKind, current, incoming []prerequest.Handler) ([]prerequest.Handler, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]prerequest.Handler]{
		StageID: StageIDPreRequest,
		Materialize: func(v []prerequest.Handler) []DiagnosticOccupant {
			nonNil := make([]prerequest.Handler, 0, len(v))
			for _, h := range v {
				if h != nil {
					nonNil = append(nonNil, h)
				}
			}
			if len(nonNil) == 0 {
				return nil
			}
			sorted := prerequest.MaterializeSorted(nonNil)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, h := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: "pre_request:" + h.ID()})
			}
			return occupants
		},
		Privileges: func(v []prerequest.Handler) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{"auxiliary_requests"}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlaneRouteHintProviders declares the RouteHintProviders extension plane.
var PlaneRouteHintProviders = Plane[[]routehint.Provider]{
	ID:           "route_hint_providers",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []routehint.Provider) ([]routehint.Provider, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]routehint.Provider]{
		StageID: StageIDRouteHinting,
		Materialize: func(v []routehint.Provider) []DiagnosticOccupant {
			sorted := routehint.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, p := range sorted {
				if p == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "route_hint:" + p.ID()})
			}
			return occupants
		},
	},
}

// PlaneCompletionGates declares the CompletionGates extension plane.
var PlaneCompletionGates = Plane[[]completion.Gate]{
	ID:           "completion_gates",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []completion.Gate) ([]completion.Gate, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]completion.Gate]{
		StageID: StageIDCompletionGating,
		Materialize: func(v []completion.Gate) []DiagnosticOccupant {
			sorted := completion.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, g := range sorted {
				if g == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "completion_gate:" + g.ID()})
			}
			return occupants
		},
		Privileges: func(v []completion.Gate) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{"auxiliary_requests", "completion_gate"}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlaneAttemptTransforms declares the AttemptTransforms extension plane.
var PlaneAttemptTransforms = Plane[[]request.AttemptTransform]{
	ID:           "attempt_transforms",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature:          CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	NilPolicy: NilReject,
	Identity: func(v []request.AttemptTransform) (string, bool) {
		if len(v) > 0 && v[0] != nil {
			return v[0].ID(), true
		}
		return "", false
	},
	Validate: func(v []request.AttemptTransform) error {
		for i, at := range v {
			if at == nil {
				return fmt.Errorf("AttemptTransforms[%d] must not be nil", i)
			}
		}
		return nil
	},
	Combine: func(source SourceKind, current, incoming []request.AttemptTransform) ([]request.AttemptTransform, error) {
		if source == SourceGenerationBinder {
			if len(incoming) == 0 || incoming[0] == nil {
				return current, nil
			}
			incID := incoming[0].ID()
			out := make([]request.AttemptTransform, 0, len(current)+len(incoming))
			for _, t := range current {
				if t != nil && t.ID() == incID {
					continue
				}
				out = append(out, t)
			}
			out = append(out, incoming...)
			return out, nil
		}
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]request.AttemptTransform]{
		StageID: StageIDCandidateAttemptTransform,
		Materialize: func(v []request.AttemptTransform) []DiagnosticOccupant {
			nonNil := make([]request.AttemptTransform, 0, len(v))
			for _, tr := range v {
				if tr != nil {
					nonNil = append(nonNil, tr)
				}
			}
			if len(nonNil) == 0 {
				return nil
			}
			sorted := request.MaterializeAttemptsSorted(nonNil)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, tr := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: "attempt_transform:" + tr.ID()})
			}
			return occupants
		},
		Privileges: func(v []request.AttemptTransform) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{"auxiliary_requests"}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlaneStreamObserverFactories declares the StreamObserverFactories extension plane.
var PlaneStreamObserverFactories = Plane[[]response.StreamObserverFactory]{
	ID:           "stream_observer_factories",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature:          CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	NilPolicy: NilReject,
	Identity: func(v []response.StreamObserverFactory) (string, bool) {
		if len(v) > 0 && v[0] != nil {
			return v[0].ID(), true
		}
		return "", false
	},
	Validate: func(v []response.StreamObserverFactory) error {
		for i, f := range v {
			if f == nil {
				return fmt.Errorf("StreamObserverFactories[%d] must not be nil", i)
			}
		}
		return nil
	},
	Combine: func(source SourceKind, current, incoming []response.StreamObserverFactory) ([]response.StreamObserverFactory, error) {
		if source == SourceGenerationBinder {
			if len(incoming) == 0 || incoming[0] == nil {
				return current, nil
			}
			incID := incoming[0].ID()
			out := make([]response.StreamObserverFactory, 0, len(current)+len(incoming))
			for _, f := range current {
				if f != nil && f.ID() == incID {
					continue
				}
				out = append(out, f)
			}
			out = append(out, incoming...)
			return out, nil
		}
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]response.StreamObserverFactory]{
		StageID: StageIDFinalStreamObservation,
		Materialize: func(v []response.StreamObserverFactory) []DiagnosticOccupant {
			nonNil := make([]response.StreamObserverFactory, 0, len(v))
			for _, f := range v {
				if f != nil {
					nonNil = append(nonNil, f)
				}
			}
			if len(nonNil) == 0 {
				return nil
			}
			sorted := response.MaterializeSorted(nonNil)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, f := range sorted {
				occupants = append(occupants, DiagnosticOccupant{Label: "stream_observer:" + f.ID()})
			}
			return occupants
		},
	},
}

// PlaneTrafficObservers declares the TrafficObservers extension plane.
var PlaneTrafficObservers = Plane[[]traffic.Observer]{
	ID:           "traffic_observers",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
		Host:    CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []traffic.Observer) ([]traffic.Observer, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]traffic.Observer]{
		StageID:       StageIDTrafficObservation,
		CoalesceGroup: "traffic_observation",
		Materialize: func(v []traffic.Observer) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for i, o := range v {
				if o == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: fmt.Sprintf("traffic_observer:%d", i)})
			}
			return occupants
		},
	},
}

// PlaneUsageObservers declares the UsageObservers extension plane.
var PlaneUsageObservers = Plane[[]usage.Observer]{
	ID:           "usage_observers",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
		Host:    CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []usage.Observer) ([]usage.Observer, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]usage.Observer]{
		StageID:       StageIDTrafficObservation,
		CoalesceGroup: "traffic_observation",
		Materialize: func(v []usage.Observer) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for i, o := range v {
				if o == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: fmt.Sprintf("usage_observer:%d", i)})
			}
			return occupants
		},
	},
}

// PlaneRawCaptureSinks declares the RawCaptureSinks extension plane.
var PlaneRawCaptureSinks = Plane[[]traffic.RawCaptureSink]{
	ID:           "raw_capture_sinks",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []traffic.RawCaptureSink) ([]traffic.RawCaptureSink, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]traffic.RawCaptureSink]{
		StageID:       StageIDTrafficObservation,
		CoalesceGroup: "traffic_observation",
		Materialize: func(v []traffic.RawCaptureSink) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for i, s := range v {
				if s == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: fmt.Sprintf("raw_capture:%d", i)})
			}
			return occupants
		},
		Privileges: func(v []traffic.RawCaptureSink) PrivilegeProjection {
			if len(v) > 0 {
				return PrivilegeProjection{Flags: []string{"raw_capture"}}
			}
			return PrivilegeProjection{}
		},
	},
}

// PlaneTrafficRedactors declares the TrafficRedactors extension plane.
var PlaneTrafficRedactors = Plane[[]traffic.Redactor]{
	ID:           "traffic_redactors",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []traffic.Redactor) ([]traffic.Redactor, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]traffic.Redactor]{
		StageID:       StageIDTrafficObservation,
		CoalesceGroup: "traffic_observation",
		Materialize: func(v []traffic.Redactor) []DiagnosticOccupant {
			occupants := make([]DiagnosticOccupant, 0, len(v))
			for _, r := range v {
				if r == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "traffic_redactor:" + r.ID()})
			}
			return occupants
		},
	},
}

// PlaneCompactionObservers declares the CompactionObservers extension plane.
var PlaneCompactionObservers = Plane[[]compaction.Observer]{
	ID:           "compaction_observers",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []compaction.Observer) ([]compaction.Observer, error) {
		return append(current, incoming...), nil
	},
}

// PlaneCompactionPreservers declares the CompactionPreservers extension plane.
var PlaneCompactionPreservers = Plane[[]compaction.Preserver]{
	ID:           "compaction_preservers",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature:          CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	NilPolicy: NilReject,
	Identity: func(v []compaction.Preserver) (string, bool) {
		if len(v) > 0 && v[0] != nil {
			return v[0].ID(), true
		}
		return "", false
	},
	Validate: func(v []compaction.Preserver) error {
		for i, p := range v {
			if p == nil {
				return fmt.Errorf("CompactionPreservers[%d] must not be nil", i)
			}
		}
		return nil
	},
	Combine: func(source SourceKind, current, incoming []compaction.Preserver) ([]compaction.Preserver, error) {
		if source == SourceGenerationBinder {
			if len(incoming) == 0 || incoming[0] == nil {
				return current, nil
			}
			incID := incoming[0].ID()
			out := make([]compaction.Preserver, 0, len(current)+len(incoming))
			for _, p := range current {
				if p != nil && p.ID() == incID {
					continue
				}
				out = append(out, p)
			}
			out = append(out, incoming...)
			return out, nil
		}
		return append(current, incoming...), nil
	},
}

// PlaneSecretGuards declares the SecretGuards extension plane.
var PlaneSecretGuards = Plane[[]secretguard.Guard]{
	ID:           "secret_guards",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilNotApplicable,
	Combine: func(source SourceKind, current, incoming []secretguard.Guard) ([]secretguard.Guard, error) {
		return append(current, incoming...), nil
	},
	Diagnostics: DiagnosticDescriptor[[]secretguard.Guard]{
		StageID: StageIDSecretGuard,
		Materialize: func(v []secretguard.Guard) []DiagnosticOccupant {
			sorted := secretguard.MaterializeSorted(v)
			occupants := make([]DiagnosticOccupant, 0, len(sorted))
			for _, g := range sorted {
				if g == nil {
					continue
				}
				occupants = append(occupants, DiagnosticOccupant{Label: "secret_guard:" + g.ID()})
			}
			return occupants
		},
	},
}

// PlaneLocalTurnHandlers declares the LocalTurnHandlers extension plane.
var PlaneLocalTurnHandlers = Plane[[]localturn.Handler]{
	ID:           "local_turn_handlers",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
	},
	NilPolicy: NilReject,
	Validate: func(v []localturn.Handler) error {
		for i, h := range v {
			if localturn.IsNilHandler(h) {
				return fmt.Errorf("LocalTurnHandlers[%d] must not be nil", i)
			}
			if err := localturn.ValidateHandlerID(h.ID()); err != nil {
				return fmt.Errorf("LocalTurnHandlers[%d] invalid id: %w", i, err)
			}
		}
		return nil
	},
	Combine: func(source SourceKind, current, incoming []localturn.Handler) ([]localturn.Handler, error) {
		return append(current, incoming...), nil
	},
}

// PlaneTerminalDecisionProvider declares the TerminalDecisionProvider extension plane.
var PlaneTerminalDecisionProvider = Plane[terminaldecision.Provider]{
	ID:           "terminal_decision_provider",
	Multiplicity: MultExclusive,
	Rules: SourceRules{
		Feature: CombExclusive,
	},
	NilPolicy: NilReject,
	IsNil: func(v terminaldecision.Provider) bool {
		if v == nil {
			return true
		}
		_, err := terminaldecision.ProviderIdentity(v)
		return err != nil
	},
	Identity: func(v terminaldecision.Provider) (string, bool) {
		id, err := terminaldecision.ProviderIdentity(v)
		if err != nil {
			return "", false
		}
		return id, true
	},
	Validate: func(v terminaldecision.Provider) error {
		_, err := terminaldecision.ProviderIdentity(v)
		return err
	},
	Combine: func(source SourceKind, current, incoming terminaldecision.Provider) (terminaldecision.Provider, error) {
		return incoming, nil
	},
}

// StandardPlanes is the ordered slice of all standard feature planes.
var StandardPlanes = []PlaneDeclaration{
	PlaneSubmitHooks,
	PlaneRequestPartHooks,
	PlaneResponsePartHooks,
	PlaneToolReactors,
	PlaneSessionOpeners,
	PlaneWorkspaceResolvers,
	PlaneToolCatalogFilters,
	PlaneToolCallPolicies,
	PlaneToolCallFinalizers,
	PlaneToolCallFinalizationMaxArgsBytes,
	PlaneRequestTransforms,
	PlanePreRequestHandlers,
	PlaneRouteHintProviders,
	PlaneCompletionGates,
	PlaneAttemptTransforms,
	PlaneStreamObserverFactories,
	PlaneTrafficObservers,
	PlaneUsageObservers,
	PlaneRawCaptureSinks,
	PlaneTrafficRedactors,
	PlaneCompactionObservers,
	PlaneCompactionPreservers,
	PlaneSecretGuards,
	PlaneLocalTurnHandlers,
	PlaneTerminalDecisionProvider,
}
