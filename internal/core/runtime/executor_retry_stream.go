package runtime

// Stream lifecycle helpers (loadInner, storeInner, Close, handleRecvSuccess,
// handleRecvEOF, etc.) and the recv-phase support surface (stream-evidence
// seam, traffic emission) for retryRecvStream. Response/event evidence and
// completion-gate, logical-tool, and response-observation state live in
// responsePipeline. The inner-loop control (Recv and tryReplacementIteration) has been extracted
// to executor_recv_loop.go; the retryRecvStream type itself, its error
// sentinel, and the lipapi.EventStream interface assertion remain here.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)

// retryRecvStream is the recv-phase and terminal turn owner: it wraps a backend
// stream, performs recv-phase failover within attempt budget, and settles or
// releases attempt authority at terminal.
//
// Concurrency: one goroutine calls Recv until completion (lipapi.EventStream). Close may run
// concurrently with Recv blocked on the active inner stream; Close forwards to that attempt stream
// and does not clear the attempt pointer. Recv clears the attempt stream on cancellation and
// recoverable-recv teardown paths.
// Recv must not be called concurrently from multiple goroutines; the stream is not multi-Recv-safe.
type retryRecvStream struct {
	// facts is the sole request-lifetime receive authority. It contains only
	// immutable request facts; retry, event, terminal, attempt, and lock state
	// remains owned by cohesive collaborators.
	facts recvTurnFacts

	*responsePipeline
	executor *Executor
	bus      *hooks.Bus

	attempt  attemptSlot
	terminal *turnTerminal
	recovery *recoveryController

	cachedCtxMu sync.Mutex
	lastParent  context.Context
	cachedCtx   context.Context

	isInterleavedThinker bool
}

// consumeBackendUsageEvidenceForAttempt binds provider sideband evidence to
// the attempt that supplied the source stream. A replacement can publish while
// a retired source is still being drained; using the slot's current attempt in
// that case would move usage and dedupe state onto the replacement.
func (s *retryRecvStream) consumeBackendUsageEvidenceForAttempt(
	ctx context.Context,
	attempt *attemptSession,
	inner lipapi.ManagedEventStream,
) {
	if attempt == nil || inner == nil {
		return
	}
	source, ok := inner.(lipapi.UsageEvidenceSource)
	if !ok {
		return
	}
	for _, ev := range source.DrainUsageEvidence() {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		if !attempt.rememberUsageEvidenceOnce(ev) {
			continue
		}
		s.rememberInternalUsage(ev)
		attempt.accounting.observeUsage(ev)
		s.responsePipeline.emitUsage(ctx, s.executor, s.facts, attempt, ev)
	}
}

var _ lipapi.EventStream = (*retryRecvStream)(nil)

var errNilRetryRecvStream = errors.New("runtime: nil retryRecvStream")

func (s *retryRecvStream) now() time.Time {
	if s != nil && s.executor != nil {
		return s.executor.now()
	}
	return time.Now()
}

func (s *retryRecvStream) isFinished() bool {
	if s == nil {
		return false
	}
	if s.responsePipeline != nil && !s.responsePipeline.bound() {
		// Production assembly binds immediately after construction. Keep this
		// nil-safe fallback for focused tests that use direct stream literals.
		s.bindResponsePipeline()
	}
	return s.terminal != nil && s.terminal.finished()
}

func (s *retryRecvStream) bindResponsePipeline() {
	if s == nil {
		return
	}
	s.responsePipeline.bindTerminalSnapshot(func() (bool, bool) {
		if s.terminal == nil {
			return false, false
		}
		return s.terminal.committed(), s.terminal.accountingFinalized()
	})
	s.responsePipeline.bindCustomerUsage(func(ctx context.Context, text string, events []lipapi.Event) lipapi.Event {
		return reconstructCustomerUsageForResponse(ctx, s.executor, s.facts, s.attempt.snapshot(), text, events)
	})
}

func (s *retryRecvStream) markFinished() {
	if s != nil {
		if s.terminal == nil || !s.terminal.markFinished() {
			return
		}
		// Terminal ownership: every finish path clears attempt-local assembler
		// state here (normal response_finished, recover-drain finish, gate finish,
		// error/EOF/Close finishes). Nonterminal clears stay at their call sites.
		clearAttemptToolState(s.responsePipeline, s.attempt.snapshot())
	}
}

func (s *retryRecvStream) isCommitted() bool {
	return s != nil && s.terminal != nil && s.terminal.committed()
}

func (s *retryRecvStream) markCommitted() {
	if s != nil {
		if s.terminal != nil {
			s.terminal.markCommitted(s.attempt.snapshot())
		}
	}
}

// cachedExecContext returns the request-scoped exec context derived from the most
// recent Recv parent, or nil when Recv never ran. Close and other caller-less
// terminal paths derive from it via context.WithoutCancel so work that must
// outlive request cancellation still sees request-scoped values.
func (s *retryRecvStream) cachedExecContext() context.Context {
	if s == nil {
		return nil
	}
	s.cachedCtxMu.Lock()
	defer s.cachedCtxMu.Unlock()
	return s.cachedCtx
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
	if s == nil || s.recovery == nil || s.recovery.recoverPolicy == nil || parent == nil {
		return parent, parentCancel, idleContextDeadline{}
	}
	deadline, ok := s.recovery.recoverPolicy.IdleDeadline()
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

// recvExecContext attaches request metadata to parent and returns a child context.
// It caches the result based on parent to avoid repeated allocations in Recv.
func (s *retryRecvStream) recvExecContext(parent context.Context) context.Context {
	s.cachedCtxMu.Lock()
	defer s.cachedCtxMu.Unlock()

	if s.lastParent == parent && s.cachedCtx != nil {
		return s.cachedCtx
	}

	var logger *slog.Logger
	if s.executor != nil && s.executor.Log != nil {
		logger = s.executor.Log
	}
	ctx := s.facts.projectContext(parent, logger)
	ctx = s.withDecisionEvidence(ctx)

	s.lastParent = parent
	s.cachedCtx = ctx
	return ctx
}

func (s *retryRecvStream) recvHookMeta() (sdk.PartMeta, sdk.ToolMeta) {
	attempt := s.attempt.snapshot()
	var bleg b2bua.BLegRecord
	var cand routing.AttemptCandidate
	if attempt != nil {
		bleg = attempt.bleg
		cand = attempt.cand
	}
	traceID, aLegID := s.facts.traceID, s.facts.aLegID
	pm := sdk.PartMeta{
		TraceID:    traceID,
		ALegID:     aLegID,
		BLegID:     bleg.BLegID,
		BackendID:  strings.TrimSpace(cand.Primary.Backend),
		AttemptSeq: bleg.Seq,
	}
	tm := sdk.ToolMeta{
		TraceID:    traceID,
		ALegID:     aLegID,
		BLegID:     bleg.BLegID,
		AttemptSeq: bleg.Seq,
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
	if s.facts.recvViewsOK {
		return cloneRecvViews(s.facts.recvViews), true
	}
	return execctx.Views{}, false
}

func (s *retryRecvStream) markOutputCommitted(ev lipapi.Event) {
	if lipapi.OutputCommitted(ev) {
		s.markCommitted()
		if s.recovery != nil && s.recovery.ttft != nil {
			s.recovery.ttft.markCommitted()
		}
	}
}

func (s *retryRecvStream) popToolFinalDrain() (lipapi.Event, bool) {
	if s == nil {
		return lipapi.Event{}, false
	}
	attempt := s.attempt.snapshot()
	if attempt == nil || attempt.toolFinal == nil {
		return lipapi.Event{}, false
	}
	return attempt.toolFinal.popDrain()
}

func (s *retryRecvStream) Close() error {
	if s == nil {
		return nil
	}
	current := s.attempt.closePublicationAndSnapshot()
	clearAttemptToolState(s.responsePipeline, s.attempt.snapshot())
	var c lipapi.ManagedEventStream
	if current != nil {
		c = current.takeInner()
	}
	// lipapi.EventStream.Close has no caller context. Terminal Close work must
	// outlive request cancellation, so detach cancel from the last Recv parent
	// when one was observed; Background only when no parent exists.
	ctx := s.cachedExecContext()
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	if c == nil {
		if !s.isFinished() {
			s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandClose, current, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
				s.responsePipeline.finishFinalStreamObservation(cctx, current, response.OutcomeClosed)
				s.persistCancellationBilling(cctx, current, "client closed")
				s.markFinished()
				return nil
			}, func(cctx context.Context, _ coreterm.Outcome) error {
				s.recordBillingLegForAttempt(cctx, current, sdkterminal.CommandClose)
				s.terminal.handoffBillingTurn(cctx, s.facts, s.executor, sdkterminal.CommandClose)
				return nil
			})
			if !s.isFinished() {
				s.markFinished()
			}
		}
		s.terminal.endALeg(aLegEndBase)
		return nil
	}
	if !s.isFinished() {
		if s.terminal != nil && s.terminal.hasALeg() {
			_ = s.terminal.cancelALeg(ctx, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		} else {
			_ = c.Cancel(ctx, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		}
		s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandClose, current, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
			s.responsePipeline.finishFinalStreamObservation(cctx, current, response.OutcomeClosed)
			s.persistCancellationBilling(cctx, current, "client closed")
			s.markFinished()
			return nil
		}, func(cctx context.Context, _ coreterm.Outcome) error {
			s.recordBillingLegForAttempt(cctx, current, sdkterminal.CommandClose)
			s.terminal.handoffBillingTurn(cctx, s.facts, s.executor, sdkterminal.CommandClose)
			return nil
		})
		if !s.isFinished() {
			s.markFinished()
		}
		if s.terminal != nil && s.terminal.hasALeg() {
			s.terminal.endALeg(aLegEndBase)
			return nil
		}
	}
	s.terminal.endALeg(aLegEndBase)
	err := safety.Call(safety.BoundaryBackend, "backend_stream_close", func() error {
		return c.Close()
	})
	if err == nil {
		return nil
	}
	var pe *safety.PanicError
	if errors.As(err, &pe) {
		attempt := s.attempt.require()
		s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandPanic, attempt, s.accumulatorSnapshot(), func(context.Context, coreterm.Outcome) error { return nil }, func(cctx context.Context, _ coreterm.Outcome) error {
			s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandPanic)
			s.terminal.handoffBillingTurn(cctx, s.facts, s.executor, sdkterminal.CommandPanic)
			return nil
		})
		if s.executor != nil && s.executor.Log != nil {
			// lipapi.EventStream.Close has no context; EnsureCallDiag guarantees call/leg ids
			// on the detached close context so isolated-panic logs still correlate by trace_id / b_leg.
			logCtx := diag.EnsureCallDiag(ctx, s.facts.traceID, s.facts.aLegID)
			attrs := diag.IsolatedCrashAttrs(logCtx, pe, diag.CrashAttrOpts{
				AttrOpts:   diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID},
				AttemptSeq: int(attempt.bleg.Seq),
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
	for _, ev := range buf {
		if lipapi.OutputCommitted(ev) {
			return true
		}
	}
	return false
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
