package runtime

// Stream lifecycle helpers (loadInner, storeInner, Close, handleRecvSuccess,
// handleRecvEOF, etc.) and the recv-phase support surface (completion gates,
// stream-evidence seam, traffic emission) for retryRecvStream. The
// inner-loop control (Recv and tryReplacementIteration) has been extracted
// to executor_recv_loop.go; the retryRecvStream type itself, its error
// sentinel, and the lipapi.EventStream interface assertion remain here.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	coretraffic "github.com/matdev83/go-llm-interactive-proxy/internal/core/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// retryRecvStream wraps a backend stream and performs recv-phase failover within attempt budget.
//
// Concurrency: one goroutine calls Recv until completion (lipapi.EventStream). Close may run
// concurrently with Recv blocked on the active inner stream; Close forwards to that inner stream
// and does not clear s.inner. Recv clears inner on cancellation and recoverable-recv teardown paths.
// Recv must not be called concurrently from multiple goroutines; the stream is not multi-Recv-safe.
type retryRecvStream struct {
	seenEvents  []lipapi.Event
	visibleText strings.Builder
	executor    *Executor
	bus         *hooks.Bus
	// baseline is the post-submit immutable logical client request (per-attempt state derives via CloneCall).
	baseline lipapi.Call
	budget   *attemptBudget
	ttft     *ttftBudget

	aLegID      string
	traceID     string
	sel         *routing.Selector
	requestSize routing.RequestSizeEstimate
	session     *routing.SessionRoutingState
	excluded    map[string]struct{}
	rng         routing.Rng

	lastHardReject           lipapi.NegotiationResult
	lastHardTransportReject  lipapi.TransportNegotiationResult
	isContextLimitExhaustion bool
	transformExcludes        transformExcludeTracker

	innerMu            sync.Mutex
	inner              lipapi.ManagedEventStream
	bleg               b2bua.BLegRecord
	cand               routing.AttemptCandidate
	authority          authorityLifecycle
	committed          atomic.Bool
	finished           atomic.Bool
	endOnce            sync.Once
	affinityKey        affinity.Key
	affinitySet        bool
	affinityCommitOnce sync.Once

	// recvViews / routePrefs preserve [execctx] values from prepare so Recv callers can pass a bare HTTP context.
	recvViews   execctx.Views
	recvViewsOK bool
	routePrefs  []string
	cachedCtxMu sync.Mutex
	lastParent  context.Context
	cachedCtx   context.Context

	// boundRegistry / boundCatalog / nativeResolver freeze the request-bound
	// model views captured at assemble time so recv-phase replacement with a
	// bare context cannot fall back to a live catalog/registry after refresh.
	boundRegistry   modelregistry.BoundView
	boundRegistryOK bool
	boundCatalog    modelcatalog.BoundView
	boundCatalogOK  bool
	nativeResolver  routing.NativeModelResolver
	modelViewID     modelview.Identity
	modelViewIDOK   bool

	// metering retains the prepare-time RequestHolder so Recv/terminal paths can
	// reattach it when callers pass a bare context (auxiliary child streams).
	metering *checkpoint.RequestHolder
	// requestAuth retains prepare-time request-authority state so Recv/settle paths
	// can reattach it when callers pass a bare context (mirrors metering).
	requestAuth *requestAuthorityState
	// customer records released client-visible content for FE egress settlement.
	customer *customerEvidenceAccumulator

	// secureTurn preserves validated secure-session ids for attempt trace/outcome on recv paths.
	secureTurn   execctx.SecureSessionTurn
	secureTurnOK bool
	// secureRecvRecordingHardStop blocks recv-phase B-leg replacement after a mandatory recorder failure
	// once client-visible output is committed for this stream.
	secureRecvRecordingHardStop bool

	// Completion gates (R8): buffer canonical post-hook events until finish or overflow, then emit drain queue.
	gateBuf   []lipapi.Event
	gateDrain []lipapi.Event
	gateLive  bool

	accounting attemptAccountingTracker

	recoverPolicy            *streamrecovery.Policy
	recoverDrain             []lipapi.Event
	tokenAccountingFinalized bool
	// lastAuthorityUsage is the accounting fact used for authority settlement
	// and unreserved usage. The synthesized event returned to the client may
	// intentionally omit provider-billable scopes, so keep the two views
	// separate.
	lastAuthorityUsage lipapi.Event
	// lastCustomerUsage caches client-visible reconstruction from finalize for
	// settle/FE egress when StreamUsage is unavailable on a later path.
	lastCustomerUsage lipapi.Event
	aScope            *leglifecycle.ALeg

	// interleaved is the current interleaved-thinking state (cycle cursor + memo reference)
	// for the A-leg, threaded across recv-phase failover iterations so retry continues from
	// the latest persisted state.
	interleaved interleavedstate.State
	// holdALegEnd defers A-leg scope teardown until an outer coordinator (hidden interleaved
	// continuation) finishes the combined thinker+executor logical request.
	holdALegEnd bool
	// suppressThinker keeps recv-phase failover inside an interleaved executor continuation
	// from selecting another thinker branch in the same logical request.
	suppressThinker bool
	// suppressVisibleMemo skips visible memo reinjection on recv-phase failover within the
	// same interleaved executor continuation turn.
	suppressVisibleMemo bool
	// lastParallelFailure preserves aggregated parallel-arm failure context across recv-phase
	// replacement iterations so eventual ErrNoEligibleCandidate surfaces root causes.
	lastParallelFailure error

	// toolFinal is the per-B-leg completed-tool-call assembler (nil when inactive).
	toolFinal *toolCallAssembler

	// requestTerm / attemptTerm are CAS terminal owners for this stream lifecycle
	// (phase 4.2). Lazy-initialized via ensureTerminals for test-constructed streams.
	requestTerm *streamTerminal
	attemptTerm *streamTerminal
	// eventsMu guards seenEvents / visibleText against Close concurrent with Recv.
	eventsMu sync.Mutex

	finalStreamObs *extensions.FinalStreamObservationSession
}

var _ lipapi.EventStream = (*retryRecvStream)(nil)

var errNilRetryRecvStream = errors.New("runtime: nil retryRecvStream")

func (s *retryRecvStream) loadInner() lipapi.ManagedEventStream {
	s.innerMu.Lock()
	defer s.innerMu.Unlock()
	return s.inner
}

func (s *retryRecvStream) storeInner(stream lipapi.ManagedEventStream) {
	s.innerMu.Lock()
	s.inner = stream
	s.innerMu.Unlock()
}

func (s *retryRecvStream) now() time.Time {
	if s != nil && s.executor != nil {
		return s.executor.now()
	}
	return time.Now()
}

func (s *retryRecvStream) isFinished() bool {
	return s != nil && s.finished.Load()
}

func (s *retryRecvStream) markFinished() {
	if s != nil {
		// Terminal ownership: every finish path clears attempt-local assembler
		// state here (normal response_finished, recover-drain finish, gate finish,
		// error/EOF/Close finishes). Nonterminal clears stay at their call sites.
		s.resetToolFinal()
		s.finished.Store(true)
	}
}

func (s *retryRecvStream) isCommitted() bool {
	return s != nil && s.committed.Load()
}

func (s *retryRecvStream) markCommitted() {
	if s != nil {
		s.committed.Store(true)
		s.authority.markOutputCommitted()
	}
}

// takeAndNilInner clears s.inner and returns the previous value; the caller should Close it when non-nil.
func (s *retryRecvStream) takeAndNilInner() lipapi.ManagedEventStream {
	s.innerMu.Lock()
	c := s.inner
	s.inner = nil
	s.innerMu.Unlock()
	return c
}

func (s *retryRecvStream) cancelAndCloseInner(
	ctx context.Context,
	c lipapi.ManagedEventStream,
	cause leglifecycle.CancelCause,
) {
	if c == nil {
		return
	}
	_ = c.Cancel(ctx, cause)
	if cerr := c.Close(); cerr != nil && s.executor != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(
			ctx, "retry_recv inner stream close",
			"reason", string(cause.Kind),
			"error", cerr,
		)
	}
}

type idleContextDeadline struct {
	active bool
	parent context.Context
}

func (d idleContextDeadline) expired(_ context.Context, err error) bool {
	return d.active && d.parent != nil && d.parent.Err() == nil && errors.Is(err, context.DeadlineExceeded)
}

func (s *retryRecvStream) scopedIdleContext(
	parent context.Context,
	parentCancel context.CancelFunc,
	now time.Time,
) (context.Context, context.CancelFunc, idleContextDeadline) {
	if s == nil || s.recoverPolicy == nil || parent == nil {
		return parent, parentCancel, idleContextDeadline{}
	}
	deadline, ok := s.recoverPolicy.IdleDeadline()
	if !ok {
		return parent, parentCancel, idleContextDeadline{}
	}
	if !now.Before(deadline) {
		deadline = now
	}
	if parentDeadline, ok := parent.Deadline(); ok && !deadline.Before(parentDeadline) {
		return parent, parentCancel, idleContextDeadline{}
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	return ctx, func() {
		cancel()
		parentCancel()
	}, idleContextDeadline{active: true, parent: parent}
}

func lifecycleAttempt(stream lipapi.EventStream) leglifecycle.BLegAttempt {
	if stream == nil {
		return nil
	}
	if managed, ok := stream.(leglifecycle.BLegAttempt); ok {
		return managed
	}
	return lipapi.CloseOnlyManagedStream{Stream: stream}
}

func (s *retryRecvStream) finishALegScope() {
	if s == nil || s.holdALegEnd {
		return
	}
	s.endOnce.Do(func() {
		if s.aScope != nil {
			s.aScope.End()
		}
	})
}

// recvExecContext attaches request metadata to parent and returns a child context.
// It caches the result based on parent to avoid repeated allocations in Recv.
func (s *retryRecvStream) recvExecContext(parent context.Context) context.Context {
	s.cachedCtxMu.Lock()
	defer s.cachedCtxMu.Unlock()

	if s.lastParent == parent && s.cachedCtx != nil {
		return s.cachedCtx
	}

	ctx := diag.EnsureCallDiag(parent, s.traceID, s.aLegID)
	if s.metering != nil {
		ctx = withMeteringHolder(ctx, s.metering)
	}
	if s.requestAuth != nil {
		ctx = withRequestAuthority(ctx, s.requestAuth)
	}
	if s.recvViewsOK {
		ctx = execctx.WithViews(ctx, s.recvViews)
	}
	if s.secureTurnOK {
		ctx = execctx.WithSecureSessionTurn(ctx, s.secureTurn)
	}
	if len(s.routePrefs) > 0 {
		ctx = execctx.WithRouteCandidatePreferences(ctx, s.routePrefs)
	}
	if s.boundRegistryOK {
		ctx = modelregistry.WithBoundView(ctx, s.boundRegistry)
	}
	if s.boundCatalogOK {
		ctx = modelcatalog.WithBoundView(ctx, s.boundCatalog)
	}
	if s.nativeResolver != nil {
		ctx = routing.WithNativeModelResolver(ctx, s.nativeResolver)
	}
	if s.modelViewIDOK {
		ctx = modelview.WithIdentity(ctx, s.modelViewID)
	}
	if s.executor != nil && s.executor.Log != nil {
		ctx = hooks.WithDiagnosticsLogger(ctx, s.executor.Log)
	}
	ctx = s.withDecisionEvidence(ctx)

	s.lastParent = parent
	s.cachedCtx = ctx
	return ctx
}

// captureBoundModelViews freezes request-bound registry/catalog/resolver/identity
// onto the stream exactly once from the prepare/assemble context. Recv-phase
// replacement must reattach these views rather than loading a second live view.
func captureBoundModelViews(s *retryRecvStream, ctx context.Context) {
	if s == nil {
		return
	}
	if v, ok := modelregistry.BoundViewFromContext(ctx); ok {
		s.boundRegistry = v
		s.boundRegistryOK = true
	}
	if v, ok := modelcatalog.BoundViewFromContext(ctx); ok {
		s.boundCatalog = v
		s.boundCatalogOK = true
	}
	if r, ok := routing.NativeModelResolverFromContext(ctx); ok {
		s.nativeResolver = r
	}
	if id, ok := modelview.FromContext(ctx); ok {
		s.modelViewID = id
		s.modelViewIDOK = true
	}
}

// copyBoundModelViews copies frozen model-view fields from src onto dst (parallel
// / interleaved continuation streams must retain the same immutable view).
func copyBoundModelViews(dst, src *retryRecvStream) {
	if dst == nil || src == nil {
		return
	}
	dst.boundRegistry = src.boundRegistry
	dst.boundRegistryOK = src.boundRegistryOK
	dst.boundCatalog = src.boundCatalog
	dst.boundCatalogOK = src.boundCatalogOK
	dst.nativeResolver = src.nativeResolver
	dst.modelViewID = src.modelViewID
	dst.modelViewIDOK = src.modelViewIDOK
}

func (s *retryRecvStream) recvHookMeta() (sdk.PartMeta, sdk.ToolMeta) {
	pm := sdk.PartMeta{
		TraceID:    s.traceID,
		ALegID:     s.aLegID,
		BLegID:     s.bleg.BLegID,
		BackendID:  strings.TrimSpace(s.cand.Primary.Backend),
		AttemptSeq: s.bleg.Seq,
	}
	tm := sdk.ToolMeta{
		TraceID:    s.traceID,
		ALegID:     s.aLegID,
		BLegID:     s.bleg.BLegID,
		AttemptSeq: s.bleg.Seq,
	}
	// Authoritative scope/identity from the request-scoped execctx views snapshot
	// kept on the stream so stream-stage reactors see proxy-validated attribution
	// even when the recv ctx is a bare HTTP context (requirement 2.6, 9.1).
	if v, ok := s.viewsFor(nil); ok { //nolint:staticcheck // SA1012: intentional nil context to force fallback to the stream snapshot
		tm.Principal = v.Principal
		tm.Scope = v.Scope
		tm.Session = v.Session
		tm.Workspace = v.Workspace
	}
	return pm, tm
}

// viewsFor returns authoritative request views from ctx, falling back to the
// stream snapshot when the current recv context does not carry execctx views.
func (s *retryRecvStream) viewsFor(ctx context.Context) (execctx.Views, bool) {
	if v, ok := execctx.FromContext(ctx); ok {
		return v, true
	}
	if s != nil && s.recvViewsOK {
		return s.recvViews, true
	}
	return execctx.Views{}, false
}

func (s *retryRecvStream) commitAffinityIfOutput(ctx context.Context, ev lipapi.Event) {
	if lipapi.OutputCommitted(ev) {
		s.commitAffinity(ctx, "output_committed")
	}
}

func (s *retryRecvStream) markOutputCommitted(ev lipapi.Event) {
	if lipapi.OutputCommitted(ev) {
		s.markCommitted()
		if s.ttft != nil {
			s.ttft.markCommitted()
		}
	}
}

func (s *retryRecvStream) commitAffinity(ctx context.Context, reason string) {
	if s == nil || s.executor == nil || s.executor.AffinityStore == nil || !s.affinitySet || !s.affinityKey.Valid() {
		return
	}
	s.affinityCommitOnce.Do(func() {
		binding := affinity.BindingFromCandidate(s.affinityKey, s.cand, s.now(), reason)
		if strings.TrimSpace(binding.BackendID) == "" {
			return
		}
		persistCtx := context.WithoutCancel(ctx)
		if err := s.executor.AffinityStore.Set(persistCtx, binding); err != nil {
			if s.executor.Log != nil {
				s.executor.Log.DebugContext(persistCtx, "affinity binding set failed", "error", err)
			}
			return
		}
		s.executor.noteRouteDecision(persistCtx, s.traceID, "affinity_bind", binding.BackendID)
	})
}

func (s *retryRecvStream) emitTrafficBTP(ctx context.Context, ev lipapi.Event, pm sdk.PartMeta) {
	s.emitTraffic(ctx, sdktraffic.LegBTP, ev, pm)
}

func (s *retryRecvStream) emitTrafficPTC(ctx context.Context, ev lipapi.Event, pm sdk.PartMeta) {
	if ev.Kind == lipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode {
		return
	}
	s.emitTraffic(ctx, sdktraffic.LegPTC, ev, pm)
}

func (s *retryRecvStream) emitTraffic(ctx context.Context, leg sdktraffic.Leg, ev lipapi.Event, pm sdk.PartMeta) {
	if s.executor == nil || s.executor.RuntimeSnapshot == nil {
		return
	}
	bundle := coretraffic.PortBundleFromSnapshot(s.executor.RuntimeSnapshot)
	if bundle.EmitIsNoop() {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "retry_recv traffic marshal skipped", "leg", leg, "error", err)
		}
		return
	}
	sc := scopeFromCtx(ctx)
	meta := sdktraffic.CaptureMeta{
		TraceID:     pm.TraceID,
		ALegID:      pm.ALegID,
		BLegID:      pm.BLegID,
		AttemptSeq:  pm.AttemptSeq,
		BackendID:   strings.TrimSpace(s.cand.Primary.Backend),
		PrincipalID: strings.TrimSpace(sc.PrincipalID.String()),
		Scope:       sc,
	}
	bundle.Emit(
		ctx,
		leg,
		meta,
		"lip/canonical+json",
		"application/json",
		b,
	)
}

// resetToolFinal clears assembler state at attempt-lifecycle transitions that
// are not (or may not be) paired with markFinished: B-leg replacement,
// finalizer reject/error before finish, Close (may already be finished), and
// EOF/error entry (some branches defer finish to recoverDrain).
// Terminal finishes clear via markFinished.
func (s *retryRecvStream) resetToolFinal() {
	if s == nil || s.toolFinal == nil {
		return
	}
	s.toolFinal.clear()
}

func (s *retryRecvStream) popToolFinalDrain() (lipapi.Event, bool) {
	if s == nil || s.toolFinal == nil {
		return lipapi.Event{}, false
	}
	return s.toolFinal.popDrain()
}

func (s *retryRecvStream) Close() error {
	if s == nil {
		return nil
	}
	s.resetToolFinal()
	c := s.takeAndNilInner()
	ctx := context.Background()
	if c == nil {
		if !s.isFinished() {
			s.runStreamTerminal(ctx, sdkterminal.CommandClose, func(cctx context.Context) error {
				s.finishFinalStreamObservation(cctx, response.OutcomeClosed)
				s.persistCancellationBilling(cctx, "client closed")
				s.markFinished()
				return nil
			})
			if !s.isFinished() {
				s.markFinished()
			}
		}
		s.finishALegScope()
		return nil
	}
	if !s.isFinished() {
		if s.aScope != nil {
			_ = s.aScope.Cancel(ctx, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		} else {
			_ = c.Cancel(ctx, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		}
		s.runStreamTerminal(ctx, sdkterminal.CommandClose, func(cctx context.Context) error {
			s.finishFinalStreamObservation(cctx, response.OutcomeClosed)
			s.persistCancellationBilling(cctx, "client closed")
			s.markFinished()
			return nil
		})
		if !s.isFinished() {
			s.markFinished()
		}
		if s.aScope != nil {
			s.finishALegScope()
			return nil
		}
	}
	s.finishALegScope()
	err := safety.Call(safety.BoundaryBackend, "backend_stream_close", func() error {
		return c.Close()
	})
	if err == nil {
		return nil
	}
	var pe *safety.PanicError
	if errors.As(err, &pe) {
		s.runStreamTerminal(ctx, sdkterminal.CommandPanic, func(context.Context) error { return nil })
		if s.executor != nil && s.executor.Log != nil {
			// lipapi.EventStream.Close has no context; use Background plus call/leg ids from EnsureCallDiag so
			// isolated-panic logs still correlate by trace_id / b_leg. Request-scoped trace fields are omitted here.
			logCtx := diag.EnsureCallDiag(context.Background(), s.traceID, s.aLegID)
			attrs := diag.IsolatedCrashAttrs(logCtx, pe, diag.CrashAttrOpts{
				AttrOpts:   diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID},
				AttemptSeq: int(s.bleg.Seq),
			})
			attrs = diag.AppendIsolatedCrashStack(attrs, pe)
			s.executor.Log.LogAttrs(logCtx, slog.LevelError, "isolated_panic_backend_stream_close", attrs...)
		}
		return nil
	}
	return err
}

func (s *retryRecvStream) applyToolPolicies(ctx context.Context, te lipapi.ToolEvent, meta sdk.ToolMeta) error {
	if s == nil || s.executor == nil || s.executor.RuntimeSnapshot == nil {
		return nil
	}
	policies := s.executor.RuntimeSnapshot.ToolCallPoliciesExecution()
	if len(policies) == 0 {
		return nil
	}
	polMeta := toolpolicy.Meta{
		TraceID:    strings.TrimSpace(meta.TraceID),
		ALegID:     strings.TrimSpace(meta.ALegID),
		BLegID:     strings.TrimSpace(meta.BLegID),
		AttemptSeq: meta.AttemptSeq,
		Principal:  meta.Principal,
		Scope:      meta.Scope,
		Session:    meta.Session,
		Workspace:  meta.Workspace,
	}
	if v, ok := s.viewsFor(ctx); ok {
		polMeta.Principal = v.Principal
		polMeta.Scope = v.Scope
		polMeta.Session = v.Session
		polMeta.Workspace = v.Workspace
	}
	err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx:      ctx,
		Log:      s.executor.Log,
		Obs:      s.executor.ExtensionMetrics,
		Policies: policies,
		Event:    te,
		Meta:     polMeta,
		Svc: toolpolicy.Services{
			State: s.executor.RuntimeSnapshot.State(),
			Aux:   s.executor.RuntimeSnapshot.Aux(),
		},
	})
	return err
}

// enrichUsageCost is the per-event cost transform called inline in the recv hot
// path. It fills in cost, currency, and price-catalog-derived cost source on
// usage deltas that the backend did not annotate itself, so downstream usage
// observers and authority settlement see consistent per-event evidence.
// When EconomicsRater is attached, it is the exclusive pricing authority and
// catalog EstimateCost must not silently substitute (requirements 6.3, 6.4, 12.1).
func (s *retryRecvStream) enrichUsageCost(ev lipapi.Event) lipapi.Event {
	if s == nil || s.executor == nil || ev.Kind != lipapi.EventUsageDelta || ev.CostPresent {
		return ev
	}
	if s.executor.EconomicsRater != nil {
		rated, err := s.executor.rateMonetaryExposure(context.Background(), economics.RatingRequest{
			Perspective: metering.PerspectiveOperator,
			BackendID:   strings.TrimSpace(s.cand.Primary.Backend),
			Model:       strings.TrimSpace(s.cand.Primary.Model),
			Quantities:  usageEventRatingQuantities(ev),
			At:          s.executor.now(),
		})
		if err != nil {
			if strings.TrimSpace(ev.CostSource) == "" {
				ev.CostSource = accounting.CostSourceUnavailable
			}
			return ev
		}
		ev.CostNanoUnits = rated.Money.NanoUnits
		ev.Currency = rated.Money.Currency
		ev.CostSource = accounting.CostSourceEstimated
		if src := strings.TrimSpace(rated.Source); src != "" {
			ev.CostSource = src
		}
		ev.CostPresent = true
		return ev
	}
	model := strings.TrimSpace(s.cand.Primary.Model)
	res := accounting.EstimateCost(accounting.CostInput{
		Backend: strings.TrimSpace(s.cand.Primary.Backend),
		Model:   model,
		Usage: accounting.TokenUsage{
			InputTokens:      int64(ev.InputTokens),
			OutputTokens:     int64(ev.OutputTokens),
			CacheReadTokens:  int64(ev.CacheReadTokens),
			CacheWriteTokens: int64(ev.CacheWriteTokens),
			ReasoningTokens:  int64(ev.ReasoningTokens),
		},
	}, s.executor.AccountingPriceCatalog)
	if res.Source == accounting.CostSourceUnavailable {
		if strings.TrimSpace(ev.CostSource) == "" {
			ev.CostSource = accounting.CostSourceUnavailable
		}
		return ev
	}
	ev.CostNanoUnits = res.NanoUnits
	ev.Currency = res.Currency
	ev.CostSource = res.Source
	ev.CostPresent = true
	return ev
}

// withDecisionEvidence attaches the policy decision evidence seam and stream
// evidence callbacks so stream-stage runners project and emit per-provider
// evidence themselves (requirements 3.3, 3.4, 4.2, 9.1). Runtime only
// establishes the seam; it does not emit aggregate runtime evidence.
func (s *retryRecvStream) withDecisionEvidence(ctx context.Context) context.Context {
	if s == nil || s.executor == nil || s.executor.RuntimeSnapshot == nil {
		return ctx
	}
	snap := s.executor.RuntimeSnapshot
	emitter := s.executor.policyEvidenceEmitter(snap)
	// Attach the seam whenever a snapshot is present so non-default timeout budgets
	// are enforced on stream stages even without a policy observer. Emitter stays
	// nil for the no-op observer default so no evidence/logs are produced.
	ev := &extensions.DecisionEvidence{
		Emitter:               emitter,
		TimeoutBudget:         snap.TimeoutBudgetSource(),
		TimeoutGuard:          snap.ProviderTimeoutGuard(),
		OutputCommittedSource: s.isCommitted,
	}
	ctx = extensions.WithDecisionEvidence(ctx, ev)
	ctx = hooks.WithToolReactorEvidence(ctx, extensions.NewToolReactorEvidenceFunc(ev))
	ctx = extensions.WithAttemptEvidence(ctx, extensions.NewAttemptEvidenceFunc(ev))
	return ctx
}

func gateBufHasCommittedOutput(buf []lipapi.Event) bool {
	return slices.ContainsFunc(buf, lipapi.OutputCommitted)
}

func (s *retryRecvStream) completionSnapshot(ctx context.Context) *extensions.RequestRuntimeSnapshot {
	if snap := extensions.RequestRuntimeSnapshotFromContext(ctx); snap != nil {
		return snap
	}
	if s.executor != nil {
		return s.executor.RuntimeSnapshot
	}
	return nil
}

func (s *retryRecvStream) completionGatesFromContext(ctx context.Context) []completion.Gate {
	var fallback extensions.CompletionGatesView
	if s.executor != nil {
		fallback = s.executor.RuntimeSnapshot
	}
	return extensions.CompletionGatesFromContext(ctx, fallback)
}

func (s *retryRecvStream) popGateDrainHead() (lipapi.Event, bool) {
	if len(s.gateDrain) == 0 {
		return lipapi.Event{}, false
	}
	ev := s.gateDrain[0]
	s.gateDrain = s.gateDrain[1:]
	return ev, true
}

func (s *retryRecvStream) emitGateDrained(ctx context.Context, ev lipapi.Event) lipapi.Event {
	if lipapi.OutputCommitted(ev) {
		s.markCommitted()
	}
	if ev.Kind == lipapi.EventResponseFinished {
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:  s.aLegID,
			BLeg:    s.bleg,
			Cand:    s.cand,
			Outcome: lipapi.AttemptSuccess,
		}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
		s.markFinished()
	}
	return ev
}

func (s *retryRecvStream) completionGatedEmit(
	ctx context.Context,
	gates []completion.Gate,
	ev lipapi.Event,
) (lipapi.Event, error) {
	if s.gateLive {
		return ev, nil
	}
	limits := completion.DefaultBufferLimits()
	if s.executor != nil && s.executor.CompletionBufferLimits.MaxEvents > 0 {
		limits = s.executor.CompletionBufferLimits
	}
	if len(s.gateBuf) == 0 {
		maxEv := limits.MaxEvents
		if maxEv <= 0 {
			maxEv = completion.DefaultBufferLimits().MaxEvents
		}
		const prealloc = 64
		capN := min(prealloc, maxEv)
		s.gateBuf = make([]lipapi.Event, 0, capN)
	}
	s.gateBuf = append(s.gateBuf, ev)
	if extensions.CompletionGateBufferExceeded(limits, len(s.gateBuf)) {
		s.gateLive = true
		s.gateDrain = slices.Clone(s.gateBuf)
		s.gateBuf = nil
		if len(s.gateDrain) == 0 {
			return lipapi.Event{}, errors.New("runtime: completion gate overflow with empty buffer")
		}
		first := s.gateDrain[0]
		s.gateDrain = s.gateDrain[1:]
		return first, nil
	}
	if ev.Kind == lipapi.EventResponseFinished {
		snap := s.completionSnapshot(ctx)
		meta := completion.Meta{
			TraceID:    s.traceID,
			ALegID:     s.aLegID,
			BLegID:     s.bleg.BLegID,
			AttemptSeq: s.bleg.Seq,
		}
		// Authoritative scope/identity from the request-scoped execctx views so
		// completion-gate decision evidence carries proxy-validated attribution
		// (requirement 2.1, 2.6, 9.1). completion.Meta exposes Scope/Session/
		// Workspace; Principal is carried via Scope.PrincipalID.
		if v, ok := s.viewsFor(ctx); ok {
			meta.Scope = v.Scope
			meta.Session = v.Session
			meta.Workspace = v.Workspace
		}
		svc := completion.Services{}
		if snap != nil {
			svc.State = snap.State()
			svc.Aux = snap.Aux()
		}
		var stageLog *slog.Logger
		if s.executor != nil {
			stageLog = s.executor.Log
		}
		committed := s.isCommitted()
		committedForPanic := committed || gateBufHasCommittedOutput(s.gateBuf)
		gateResult, err := safety.CallValue(safety.BoundaryStream, "completion_gate_chain", func() (extensions.CompletionGateChainResult, error) {
			return extensions.ApplyCompletionGateChain(ctx, gates, meta, s.gateBuf, committed, svc, stageLog)
		})
		if err != nil {
			var pe *safety.PanicError
			if errors.As(err, &pe) {
				s.gateBuf = nil
				s.gateDrain = nil
				s.gateLive = false
				return lipapi.Event{}, mapStreamPanic(pe, committedForPanic)
			}
			s.gateBuf = nil
			return lipapi.Event{}, err
		}
		out := gateResult.Events
		s.gateBuf = nil
		if len(out) == 0 {
			return lipapi.Event{}, errors.New("runtime: completion gate produced empty stream")
		}
		if gateResult.Replaced {
			if err := s.cycleFinalStreamObservation(ctx, response.OutcomeGateReplaced); err != nil {
				return lipapi.Event{}, err
			}
		}
		s.gateDrain = out[1:]
		return out[0], nil
	}
	return lipapi.Event{}, errGateContinueInner
}
