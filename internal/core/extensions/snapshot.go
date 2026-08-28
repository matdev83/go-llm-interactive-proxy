package extensions

import (
	"context"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

type snapCtxKey struct{}

// SecretGuardPlane is the frozen secret-guard composition bound into a request snapshot.
type SecretGuardPlane struct {
	Guards             []secretguard.Guard
	MatcherResolver    secretguard.MatcherResolver
	DecisionObserver   secretguard.Observer
	AuditFailurePolicy secretguard.AuditFailurePolicy
	AccessMode         string
	ConfigVersion      string
}

// RequestRuntimeSnapshot is a per-build binding of hook chains and service facades published
// onto each request context (design §15B, task 4.2). Many request goroutines may read the same
// pointer without synchronization; callers must treat it as frozen after construction: do not
// replace fields, mutate the embedded [*hooks.Bus], or swap facade implementations. Config reload
// or rebinding must publish a new snapshot (new [RequestRuntimeSnapshot] value and new executor
// wiring from [github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle.Build]).
type RequestRuntimeSnapshot struct {
	hookBus                 *hooks.Bus
	state                   state.Store
	aux                     auxiliary.Client
	obs                     traffic.Observer
	usageObs                usage.Observer
	raw                     traffic.RawCaptureSink
	ws                      workspace.Resolver
	sessionOpeners          []session.Opener
	toolCatalogFilters      []toolcatalog.Filter
	toolCallPolicies        []toolpolicy.Policy
	toolCallFinalizers      []toolcall.Finalizer
	requestTransforms       []request.Transform
	preRequestHandlers      []prerequest.Handler
	routeHintProviders      []routehint.Provider
	completionGates         []completion.Gate
	attemptTransforms       []request.AttemptTransform
	streamObserverFactories []response.StreamObserverFactory
	trafficRedactors        []traffic.Redactor
	compactionObservers     []compaction.Observer
	compactionPreservers    []compaction.Preserver
	secretGuardPlane        SecretGuardPlane
	policyObserver          policydecision.Observer
	timeoutBudget           TimeoutBudgetSource
	timeoutGuard            *ProviderTimeoutGuard
	localTurnHandlers       []localturn.Handler
	featurePlanes           lipfeature.FrozenPlaneSet
	gen                     int64
}

// SnapshotOptions configures optional facades; zero value uses disabled placeholders.
type SnapshotOptions struct {
	State                   state.Store
	Aux                     auxiliary.Client
	TrafficObserver         traffic.Observer
	UsageObserver           usage.Observer
	RawCapture              traffic.RawCaptureSink
	Workspace               workspace.Resolver
	SessionOpeners          []session.Opener
	ToolCatalogFilters      []toolcatalog.Filter
	ToolCallPolicies        []toolpolicy.Policy
	ToolCallFinalizers      []toolcall.Finalizer
	RequestTransforms       []request.Transform
	PreRequestHandlers      []prerequest.Handler
	RouteHintProviders      []routehint.Provider
	CompletionGates         []completion.Gate
	AttemptTransforms       []request.AttemptTransform
	StreamObserverFactories []response.StreamObserverFactory
	TrafficRedactors        []traffic.Redactor
	// CompactionObservers subscribe to typed, fail-open proxy-derived compaction
	// lifecycle observations. Nil defaults to an empty frozen slice.
	CompactionObservers []compaction.Observer
	// CompactionPreservers are ordered content-bearing preservation callbacks.
	// Nil defaults to an empty frozen slice.
	CompactionPreservers []compaction.Preserver
	SecretGuardPlane     SecretGuardPlane
	// PolicyObserver receives normalized policy decision evidence. Nil defaults to a
	// disabled no-op observer so deployments without policy evidence keep current request
	// outcomes (requirements 7.6, 10.5).
	PolicyObserver policydecision.Observer
	// TimeoutBudgetSource is the frozen per-request source of decision-provider evaluation
	// budgets. Nil defaults to [DefaultTimeoutBudgetSource] (zero budget for every
	// stage/provider) so legacy extension behavior is unchanged (requirements 6.3, 10.5).
	TimeoutBudgetSource TimeoutBudgetSource
	LocalTurnHandlers   []localturn.Handler
	FeaturePlanes       lipfeature.FrozenPlaneSet
	// TerminalDecisionProvider is frozen with the request snapshot. A nil
	// provider preserves the platform's no-provider behavior.
	TerminalDecisionProvider terminaldecision.Provider
	Generation               int64
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
	finalizers := toolcall.MaterializeSorted(opts.ToolCallFinalizers)
	transforms := slices.Clone(opts.RequestTransforms)
	preReqs := prerequest.MaterializeSorted(opts.PreRequestHandlers)
	routeHints := slices.Clone(opts.RouteHintProviders)
	compGates := slices.Clone(opts.CompletionGates)
	attemptXforms := request.MaterializeAttemptsSorted(requireNonNilAttemptTransforms(opts.AttemptTransforms))
	streamObs := response.MaterializeSorted(requireNonNilStreamObserverFactories(opts.StreamObserverFactories))
	reds := traffic.MaterializeSortedRedactors(opts.TrafficRedactors)
	compactObs := slices.Clone(opts.CompactionObservers)
	compactPreservers := slices.Clone(opts.CompactionPreservers)
	plane := opts.SecretGuardPlane
	// Snapshot owns cloning/sorting for secret guards (same contract as tool policies).
	plane.Guards = secretguard.MaterializeSorted(plane.Guards)
	if secretguard.IsNilObserver(plane.DecisionObserver) {
		plane.DecisionObserver = nil
	}
	if plane.AuditFailurePolicy == "" {
		plane.AuditFailurePolicy = secretguard.AuditFailClosed
	}
	polObs := opts.PolicyObserver
	if polObs == nil {
		polObs = policydecision.NoopObserver{}
	}
	budget := opts.TimeoutBudgetSource
	if budget == nil {
		budget = DefaultTimeoutBudgetSource{}
	}
	localTurns := localturn.MaterializeSorted(opts.LocalTurnHandlers)
	featurePlanes := opts.FeaturePlanes
	if featurePlanes.IsZero() && opts.TerminalDecisionProvider != nil {
		cset := lipfeature.NewContributionSet()
		_ = lipfeature.Contribute(cset, lipfeature.PlaneTerminalDecisionProvider, "snapshot_compat", opts.TerminalDecisionProvider)
		featurePlanes = cset.Freeze()
	}
	return &RequestRuntimeSnapshot{
		hookBus:                 bus,
		state:                   st,
		aux:                     ax,
		obs:                     ob,
		usageObs:                uob,
		raw:                     raw,
		ws:                      ws,
		sessionOpeners:          openers,
		toolCatalogFilters:      catalog,
		toolCallPolicies:        policies,
		toolCallFinalizers:      finalizers,
		requestTransforms:       transforms,
		preRequestHandlers:      preReqs,
		routeHintProviders:      routeHints,
		completionGates:         compGates,
		attemptTransforms:       attemptXforms,
		streamObserverFactories: streamObs,
		trafficRedactors:        reds,
		compactionObservers:     compactObs,
		compactionPreservers:    compactPreservers,
		secretGuardPlane:        plane,
		policyObserver:          polObs,
		timeoutBudget:           budget,
		timeoutGuard:            NewProviderTimeoutGuard(),
		localTurnHandlers:       localTurns,
		featurePlanes:           featurePlanes,
		gen:                     opts.Generation,
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

func (s *RequestRuntimeSnapshot) ToolCallFinalizers() []toolcall.Finalizer {
	if s == nil {
		return nil
	}
	return slices.Clone(s.toolCallFinalizers)
}

func (s *RequestRuntimeSnapshot) ToolCallFinalizersExecution() []toolcall.Finalizer {
	if s == nil {
		return nil
	}
	return s.toolCallFinalizers
}

// RequestTransforms returns a defensive copy of frozen request-wide transforms (may be empty).
func (s *RequestRuntimeSnapshot) RequestTransforms() []request.Transform {
	if s == nil {
		return nil
	}
	return slices.Clone(s.requestTransforms)
}

// PreRequestHandlers returns a defensive copy of frozen pre-request admission handlers (may be empty).
func (s *RequestRuntimeSnapshot) PreRequestHandlers() []prerequest.Handler {
	if s == nil {
		return nil
	}
	return slices.Clone(s.preRequestHandlers)
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

// AttemptTransforms returns a defensive copy of frozen candidate attempt transforms (may be empty).
func (s *RequestRuntimeSnapshot) AttemptTransforms() []request.AttemptTransform {
	if s == nil {
		return nil
	}
	return slices.Clone(s.attemptTransforms)
}

// StreamObserverFactories returns a defensive copy of frozen stream observer factories (may be empty).
func (s *RequestRuntimeSnapshot) StreamObserverFactories() []response.StreamObserverFactory {
	if s == nil {
		return nil
	}
	return slices.Clone(s.streamObserverFactories)
}

func requireNonNilAttemptTransforms(in []request.AttemptTransform) []request.AttemptTransform {
	for _, t := range in {
		if t == nil {
			panic("extensions: SnapshotOptions.AttemptTransforms contains nil entry")
		}
	}
	return in
}

func requireNonNilStreamObserverFactories(in []response.StreamObserverFactory) []response.StreamObserverFactory {
	for _, f := range in {
		if f == nil {
			panic("extensions: SnapshotOptions.StreamObserverFactories contains nil entry")
		}
	}
	return in
}

// TrafficRedactors returns a defensive copy of frozen redactors for the traffic pipeline (may be empty).
func (s *RequestRuntimeSnapshot) TrafficRedactors() []traffic.Redactor {
	if s == nil {
		return nil
	}
	return slices.Clone(s.trafficRedactors)
}

// CompactionObservers returns a defensive copy of the frozen compaction
// observer slice (may be empty). Mutating the returned slice does not affect
// the snapshot; the snapshot's internal backing slice must never be mutated.
func (s *RequestRuntimeSnapshot) CompactionObservers() []compaction.Observer {
	if s == nil {
		return nil
	}
	return slices.Clone(s.compactionObservers)
}

// CompactionPreservers returns a defensive copy of the frozen content-bearing
// preservation callback slice. Mutating the returned slice does not affect the
// snapshot.
func (s *RequestRuntimeSnapshot) CompactionPreservers() []compaction.Preserver {
	if s == nil {
		return nil
	}
	return slices.Clone(s.compactionPreservers)
}

// SecretGuardPlane returns a defensive copy of the SecretGuardPlane configuration
// (guards slice cloned). Prefer [RequestRuntimeSnapshot.SecretGuardExecutionPlane]
// for the runtime executor hot path.
func (s *RequestRuntimeSnapshot) SecretGuardPlane() SecretGuardPlane {
	if s == nil {
		return SecretGuardPlane{}
	}
	plane := s.secretGuardPlane
	plane.Guards = slices.Clone(plane.Guards)
	return plane
}

// SecretGuardExecutionPlane returns the frozen secret-guard plane without cloning the
// guard slice. MatcherResolver, DecisionObserver, and policy/config fields are safe to
// read; Guards is the snapshot's internal backing store in MaterializeSorted order and
// must not be mutated.
func (s *RequestRuntimeSnapshot) SecretGuardExecutionPlane() SecretGuardPlane {
	if s == nil {
		return SecretGuardPlane{}
	}
	return s.secretGuardPlane
}

// Generation is an opaque build stamp (e.g. config reload generation in a future spec).
func (s *RequestRuntimeSnapshot) Generation() int64 {
	if s == nil {
		return 0
	}
	return s.gen
}

// PolicyObserver returns the frozen policy decision observer bound at snapshot construction
// (requirements 7.6, 10.5). A nil snapshot returns the disabled no-op default so callers can
// always invoke the returned observer without nil checks. The returned observer is an
// interface value; it is treated as frozen for the lifetime of the snapshot.
func (s *RequestRuntimeSnapshot) PolicyObserver() policydecision.Observer {
	if s == nil || s.policyObserver == nil {
		return policydecision.NoopObserver{}
	}
	return s.policyObserver
}

// TimeoutBudgetSource returns the frozen per-request decision-provider timeout budget source
// bound at snapshot construction (requirements 6.3, 10.5). A nil snapshot returns the default
// zero-budget source so callers can always invoke TimeoutFor without nil checks.
func (s *RequestRuntimeSnapshot) TimeoutBudgetSource() TimeoutBudgetSource {
	if s == nil || s.timeoutBudget == nil {
		return DefaultTimeoutBudgetSource{}
	}
	return s.timeoutBudget
}

// ProviderTimeoutGuard returns the snapshot-scoped guard used to contain
// uncooperative bounded providers. It is shared across requests for this immutable
// snapshot so one stuck stage/provider cannot accumulate one goroutine per request.
func (s *RequestRuntimeSnapshot) ProviderTimeoutGuard() *ProviderTimeoutGuard {
	if s == nil {
		return nil
	}
	return s.timeoutGuard
}

// LocalTurnHandlers returns a defensive copy of frozen local-turn handlers (may be empty).
// Mutating the returned slice does not affect the snapshot.
func (s *RequestRuntimeSnapshot) LocalTurnHandlers() []localturn.Handler {
	if s == nil {
		return nil
	}
	return slices.Clone(s.localTurnHandlers)
}

// LocalTurnHandlersExecution returns the frozen local-turn handler slice in execution order
// (sorted by Order then ID). The returned slice must not be mutated.
func (s *RequestRuntimeSnapshot) LocalTurnHandlersExecution() []localturn.Handler {
	if s == nil {
		return nil
	}
	return s.localTurnHandlers
}

func (s *RequestRuntimeSnapshot) featurePlaneSet() lipfeature.FrozenPlaneSet {
	if s == nil {
		return lipfeature.FrozenPlaneSet{}
	}
	return s.featurePlanes
}

// TerminalDecisionProvider returns the generation provider captured by this
// immutable request snapshot, or nil when no provider is active.
func (s *RequestRuntimeSnapshot) TerminalDecisionProvider() terminaldecision.Provider {
	return lipfeature.Get(s.featurePlaneSet(), lipfeature.PlaneTerminalDecisionProvider)
}

// TerminalDecisionProviderIdentity returns the frozen identity of the generation provider
// captured by this immutable request snapshot, if present.
func (s *RequestRuntimeSnapshot) TerminalDecisionProviderIdentity() (string, bool) {
	return lipfeature.FrozenIdentity(s.featurePlaneSet(), lipfeature.PlaneTerminalDecisionProvider)
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
