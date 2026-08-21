package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// aLegEndMode names the one owner permitted to end the request's A-leg.
// Interleaved thinker/executor wrappers keep the A-leg open until their outer
// boundary; ordinary streams end it at their own terminal boundary.
type aLegEndMode uint8

const (
	aLegEndBase aLegEndMode = iota
	aLegEndOuter
)

// aLegEndAuthority is the one concrete lifecycle owner shared by the base
// thinker and executor terminal views of an interleaved turn. Request/attempt
// terminal state stays separate; only A-leg lifecycle and end-once truth is
// shared across those views.
type aLegEndAuthority struct {
	scope *leglifecycle.ALeg
	mode  aLegEndMode
	once  sync.Once
}

// turnTerminal owns the logical request terminal, request-lifetime
// commitment/finished facts, and A-leg end authority. Attempt terminal
// ownership deliberately remains on each replaceable attemptSession;
// Terminalize composes with an explicitly snapshotted attempt instead of
// retaining one here.
type turnTerminal struct {
	request                  *streamTerminal
	aLegEndAuthority         *aLegEndAuthority
	commitment               atomic.Bool
	completion               atomic.Bool
	accountingFinalizedState atomic.Bool
	// interleavedThinker records the construction-time role of this terminal
	// view. It is terminal orchestration metadata, not facade state.
	interleavedThinker bool

	billingClosureMu      sync.Mutex
	billingClosureSuccess bool

	// Terminal-side runtime operations are injected individually at the
	// composition boundary. The terminal owner never receives the broad
	// Executor service surface.
	log                        *slog.Logger
	now                        func() time.Time
	billingEnabled             func() bool
	operatorRateRef            func(context.Context, routing.Primary) billing.VersionRef
	billingWorkload            func(context.Context, string) billing.WorkloadIdentity
	observeBillingLeg          func(context.Context, billing.CallLegUsageRecord)
	appendBillingLeg           func(context.Context, billing.BillingCallID, billing.CallLegUsageRecord)
	appendBillingCall          func(context.Context, billing.CallUsageRecord) error
	logBillingAppendFailure    func(context.Context, string, string, error)
	finalizeBilling            func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error)
	releaseRequestAuthority    func(context.Context) error
	settleRequestAuthority     func(context.Context, []metering.Fact) error
	emitFrontendEgress         func(context.Context, string, lipapi.Event) (metering.Fact, bool)
	meteringRecorderPresent    bool
	requestAuthorityNeedsRetry func(context.Context) bool
	emitBackendEgress          func(context.Context, string, metering.AttemptOutcome, metering.SurfacedState, lipapi.Event)
}

func bindTurnTerminalRuntime(t *turnTerminal, e *Executor) {
	if t == nil || e == nil {
		return
	}
	t.log = e.Log
	t.now = e.now
	t.billingEnabled = e.billingEnabled
	t.operatorRateRef = e.operatorRateRef
	t.billingWorkload = e.billingWorkloadIdentityForALeg
	t.observeBillingLeg = e.observeBillingLeg
	t.appendBillingLeg = e.appendIndependentCallLeg
	if e.TerminalUsageSink != nil {
		t.appendBillingCall = e.TerminalUsageSink.AppendCall
	}
	t.logBillingAppendFailure = e.logBillingUsageAppendFailure
	t.finalizeBilling = e.callFinalizeBilling
	t.releaseRequestAuthority = e.releaseRequestAuthority
	t.settleRequestAuthority = e.settleRequestAuthority
	t.emitFrontendEgress = e.emitFrontendEgressMeteringFact
	t.meteringRecorderPresent = e.MeteringRecorder != nil
	t.requestAuthorityNeedsRetry = func(ctx context.Context) bool {
		if e.RequestCoordinator == nil {
			return false
		}
		st := requestAuthorityFrom(ctx)
		return st != nil && !st.Settled
	}
	t.emitBackendEgress = e.emitBackendEgressMeteringFact
}

func (t *turnTerminal) nowTime() time.Time {
	if t != nil && t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *turnTerminal) setInterleavedThinker() {
	if t != nil {
		t.interleavedThinker = true
	}
}

func (t *turnTerminal) isInterleavedThinker() bool {
	return t != nil && t.interleavedThinker
}

func newTurnTerminal() *turnTerminal {
	return newTurnTerminalWithALeg(nil, aLegEndBase)
}

func newTurnTerminalWithALeg(aLeg *leglifecycle.ALeg, endMode aLegEndMode) *turnTerminal {
	return &turnTerminal{
		request:          newStreamTerminal(sdkterminal.ScopeRequest),
		aLegEndAuthority: &aLegEndAuthority{scope: aLeg, mode: endMode},
	}
}

// newTurnTerminalWithSharedALeg gives a continuation its own request terminal
// while preserving one shared A-leg lifecycle/end authority for the complete
// thinker/executor turn.
func newTurnTerminalWithSharedALeg(parent *turnTerminal) *turnTerminal {
	terminal := &turnTerminal{request: newStreamTerminal(sdkterminal.ScopeRequest)}
	if parent != nil {
		terminal.aLegEndAuthority = parent.aLegEndAuthority
	}
	return terminal
}

// deferALegEndToOuter is a construction-time, one-way ownership handoff used
// by the interleaved wrapper when a stream was assembled with base ownership
// before the wrapper decision was applied. It cannot move ownership back to
// the base stream and must run before any terminal operation is exposed.
func (t *turnTerminal) deferALegEndToOuter() bool {
	if t == nil {
		return false
	}
	if t.aLegEndAuthority == nil {
		return false
	}
	if t.aLegEndAuthority.mode == aLegEndOuter {
		return true
	}
	if t.aLegEndAuthority.mode != aLegEndBase {
		return false
	}
	t.aLegEndAuthority.mode = aLegEndOuter
	return true
}

// aLegScope is a transitional construction seam for upstream open helpers.
// All lifecycle mutations and A-leg end ownership remain behind turnTerminal.
func (t *turnTerminal) aLegScope() *leglifecycle.ALeg {
	if t == nil || t.aLegEndAuthority == nil {
		return nil
	}
	return t.aLegEndAuthority.scope
}

func (t *turnTerminal) hasALeg() bool {
	return t != nil && t.aLegEndAuthority != nil && t.aLegEndAuthority.scope != nil
}

func (t *turnTerminal) aLegErr() error {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return nil
	}
	return t.aLegEndAuthority.scope.Err()
}

func (t *turnTerminal) registerBLeg(ctx context.Context, h leglifecycle.BLegHandle) error {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return nil
	}
	return t.aLegEndAuthority.scope.RegisterBLeg(ctx, h)
}

func (t *turnTerminal) cancelALeg(ctx context.Context, cause lipapi.CancelCause) error {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return nil
	}
	return t.aLegEndAuthority.scope.Cancel(ctx, cause)
}

func (t *turnTerminal) releaseBLeg(id string) {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return
	}
	t.aLegEndAuthority.scope.ReleaseBLeg(id)
}

// endALeg is the sole A-leg end authority. It returns true only to the caller
// that performed the once-only end. A base caller cannot end an outer-owned
// interleaved A-leg (and vice versa).
func (t *turnTerminal) endALeg(mode aLegEndMode) bool {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil || t.aLegEndAuthority.mode != mode {
		return false
	}
	ended := false
	t.aLegEndAuthority.once.Do(func() {
		t.aLegEndAuthority.scope.End()
		ended = true
	})
	return ended
}

// requestTerminal returns the request-scope stream terminal for narrow runtime
// integration assertions. Attempt ownership is intentionally not exposed here.
func (t *turnTerminal) requestTerminal() *streamTerminal {
	if t == nil {
		return nil
	}
	return t.request
}

// Committed reports the sole request-lifetime output-commit truth.
func (t *turnTerminal) committed() bool {
	return t != nil && t.commitment.Load()
}

// MarkCommitted publishes request-level commitment and sends a one-way
// notification to the explicitly snapshotted attempt authority. The attempt
// notification is intentionally not used as request truth and is repeated for
// idempotent marks so a newly current attempt can observe the commitment.
func (t *turnTerminal) markCommitted(attempt *attemptSession) {
	if t == nil {
		return
	}
	t.commitment.Store(true)
	if attempt != nil {
		attempt.authority.markOutputCommitted()
	}
}

// Finished reports whether request terminal completion has been published.
func (t *turnTerminal) finished() bool {
	return t != nil && t.completion.Load()
}

// MarkFinished publishes request completion exactly once and reports whether
// this caller performed that publication.
func (t *turnTerminal) markFinished() bool {
	return t != nil && t.completion.CompareAndSwap(false, true)
}

// finishResponse applies the request-terminal finished transition and clears
// attempt-local response assembly state at the same ownership boundary.
func (t *turnTerminal) finishResponse(response *responsePipeline, attempt *attemptSession) bool {
	if t == nil || !t.markFinished() {
		return false
	}
	clearAttemptToolState(response, attempt)
	return true
}

func (t *turnTerminal) cancelForClose(ctx context.Context, inner lipapi.ManagedEventStream) error {
	if t != nil && t.hasALeg() {
		return t.cancelALeg(ctx, lipapi.CancelCause{Kind: lipapi.CancelClientGone})
	}
	if inner == nil {
		return nil
	}
	return inner.Cancel(ctx, lipapi.CancelCause{Kind: lipapi.CancelClientGone}).Err
}

func (t *turnTerminal) closeWithoutInner(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
	if t == nil || p == nil || t.finished() {
		return
	}
	t.terminalizeSnapshot(ctx, sdkterminal.CommandClose, attempt, p.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		p.finishFinalStreamObservation(cctx, attempt, response.OutcomeClosed)
		t.persistCancellationBilling(cctx, attempt, "client closed", request, p)
		t.finishResponse(p, attempt)
		return nil
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordBillingLegForAttempt(cctx, request, attempt, attempt.terminalEvidence(), sdkterminal.CommandClose, p.billingEvidenceFallback(), t.committed(), request.billingState)
		t.handoffBillingTurn(cctx, request, sdkterminal.CommandClose)
		return nil
	})
	if !t.finished() {
		t.finishResponse(p, attempt)
	}
}

func (t *turnTerminal) closeWithInner(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
	t.closeWithoutInner(ctx, request, attempt, p)
}

func (t *turnTerminal) closeBackend(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, inner lipapi.ManagedEventStream) error {
	if inner == nil {
		return nil
	}
	err := safety.Call(safety.BoundaryBackend, "backend_stream_close", func() error {
		return inner.Close()
	})
	if err == nil {
		return nil
	}
	var pe *safety.PanicError
	if !errors.As(err, &pe) {
		return err
	}
	if attempt == nil {
		attempt = &attemptSession{}
	}
	t.terminalizeSnapshot(ctx, sdkterminal.CommandPanic, attempt, p.accumulatorSnapshot(), func(context.Context, coreterm.Outcome) error { return nil }, func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordBillingLegForAttempt(cctx, request, attempt, attempt.terminalEvidence(), sdkterminal.CommandPanic, p.billingEvidenceFallback(), t.committed(), request.billingState)
		t.handoffBillingTurn(cctx, request, sdkterminal.CommandPanic)
		return nil
	})
	if p != nil && p.log != nil {
		logCtx := diag.EnsureCallDiag(ctx, request.traceID, request.aLegID)
		attrs := diag.IsolatedCrashAttrs(logCtx, pe, diag.CrashAttrOpts{
			AttrOpts:   diag.AttrOpts{CallID: request.traceID, BLegID: attempt.bleg.BLegID},
			AttemptSeq: int(attempt.bleg.Seq),
		})
		attrs = diag.AppendIsolatedCrashStack(attrs, pe)
		p.log.LogAttrs(logCtx, slog.LevelError, "isolated_panic_backend_stream_close", attrs...)
	}
	return nil
}

func (t *turnTerminal) isALegCanceled(err error) bool {
	return errors.Is(err, leglifecycle.ErrALegCanceled)
}

// terminalizePartialFailure owns the terminal side of a response/encoder
// failure. Event transformation stays on responsePipeline; this method only
// applies the irreversible terminal and billing consequences.
func (t *turnTerminal) terminalizePartialFailure(ctx context.Context, p *responsePipeline, request requestTerminalFacts, attempt *attemptSession, cmd sdkterminal.Command, reason string, cause error) {
	if t == nil || attempt == nil || p == nil {
		return
	}
	_ = t.terminalizeSnapshot(ctx, cmd, attempt, p.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		if !attempt.authority.Settled() {
			t.recordPartialTokenAccounting(cctx, attempt, reason, cause, request, p)
		}
		p.finishFinalStreamObservation(cctx, attempt, response.OutcomeFailed)
		t.finishResponse(p, attempt)
		return nil
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordBillingLegForAttempt(cctx, request, attempt, attempt.terminalEvidence(), cmd, p.billingEvidenceFallback(), t.committed(), request.billingState)
		t.handoffBillingTurn(cctx, request, cmd)
		return nil
	})
	if !t.finished() {
		t.finishResponse(p, attempt)
	}
}

func (t *turnTerminal) partialFailure(ctx context.Context, p *responsePipeline, request requestTerminalFacts, attempt *attemptSession, encoderFailure bool, cause error) {
	cmd := sdkterminal.CommandPartialError
	if encoderFailure {
		cmd = sdkterminal.CommandFrontendEncoderFailure
	}
	t.terminalizePartialFailure(ctx, p, request, attempt, cmd, attemptReasonDetail(cause), cause)
}

func (t *turnTerminal) terminalizeEOF(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
	if t == nil || attempt == nil || p == nil {
		return
	}
	t.terminalizeSnapshot(ctx, sdkterminal.CommandEOF, attempt, p.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordPartialTokenAccounting(cctx, attempt, "stream ended without response_finished", io.EOF, request, p)
		p.finishFinalStreamObservation(cctx, attempt, response.OutcomeFailed)
		t.finishResponse(p, attempt)
		return nil
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordBillingLegForAttempt(cctx, request, attempt, attempt.terminalEvidence(), sdkterminal.CommandEOF, p.billingEvidenceFallback(), t.committed(), request.billingState)
		t.handoffBillingTurn(cctx, request, sdkterminal.CommandEOF)
		return nil
	})
	if !t.finished() {
		t.finishResponse(p, attempt)
	}
}

func (t *turnTerminal) terminalizeTimeout(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
	if t == nil || attempt == nil || p == nil {
		return
	}
	t.terminalizeSnapshot(ctx, sdkterminal.CommandTimeout, attempt, p.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		attempt.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindLosing, p.operatorUsageForFinalize())
		p.finishFinalStreamObservation(cctx, attempt, response.OutcomeFailed)
		t.finishResponse(p, attempt)
		return nil
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordBillingLegForAttempt(cctx, request, attempt, attempt.terminalEvidence(), sdkterminal.CommandTimeout, p.billingEvidenceFallback(), t.committed(), request.billingState)
		t.handoffBillingTurn(cctx, request, sdkterminal.CommandTimeout)
		return nil
	})
	if !t.finished() {
		t.finishResponse(p, attempt)
	}
}

func (t *turnTerminal) terminalizeCancellation(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, reason string, timeout bool) {
	if t == nil || attempt == nil || p == nil {
		return
	}
	cmd := sdkterminal.CommandCancel
	if timeout {
		cmd = sdkterminal.CommandTimeout
	}
	t.terminalizeSnapshot(ctx, cmd, attempt, p.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		t.persistCancellationBilling(cctx, attempt, reason, request, p)
		p.finishFinalStreamObservation(cctx, attempt, response.OutcomeCancelled)
		t.finishResponse(p, attempt)
		return nil
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordBillingLegForAttempt(cctx, request, attempt, attempt.terminalEvidence(), cmd, p.billingEvidenceFallback(), t.committed(), request.billingState)
		t.handoffBillingTurn(cctx, request, cmd)
		return nil
	})
	if !t.finished() {
		t.finishResponse(p, attempt)
	}
}

func (t *turnTerminal) terminalizeSurfacedFailure(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, surfErr error, panicFailure bool) {
	if t == nil || attempt == nil || p == nil {
		return
	}
	cmd := sdkterminal.CommandPartialError
	if panicFailure {
		cmd = sdkterminal.CommandPanic
	}
	t.terminalizeSnapshot(ctx, cmd, attempt, p.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordPartialTokenAccounting(cctx, attempt, attemptReasonDetail(surfErr), surfErr, request, p)
		p.finishFinalStreamObservation(cctx, attempt, response.OutcomeFailed)
		t.finishResponse(p, attempt)
		return nil
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordBillingLegForAttempt(cctx, request, attempt, attempt.terminalEvidence(), cmd, p.billingEvidenceFallback(), t.committed(), request.billingState)
		t.handoffBillingTurn(cctx, request, cmd)
		return nil
	})
	if !t.finished() {
		t.finishResponse(p, attempt)
	}
}

func (t *turnTerminal) terminalizeALegCancellation(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, reason string) {
	if t == nil || attempt == nil || p == nil {
		return
	}
	t.terminalizeSnapshot(ctx, sdkterminal.CommandCancel, attempt, p.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		t.persistCancellationBilling(cctx, attempt, reason, request, p)
		p.finishFinalStreamObservation(cctx, attempt, response.OutcomeCancelled)
		t.finishResponse(p, attempt)
		return nil
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordBillingLegForAttempt(cctx, request, attempt, attempt.terminalEvidence(), sdkterminal.CommandCancel, p.billingEvidenceFallback(), t.committed(), request.billingState)
		t.handoffBillingTurn(cctx, request, sdkterminal.CommandCancel)
		return nil
	})
	if !t.finished() {
		t.finishResponse(p, attempt)
	}
}

func (t *turnTerminal) terminalizeReplacementFailure(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
	if t == nil || attempt == nil || p == nil {
		return
	}
	t.terminalizeSnapshot(ctx, sdkterminal.CommandPartialError, attempt, p.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		p.finishFinalStreamObservation(cctx, attempt, response.OutcomeFailed)
		t.finishResponse(p, attempt)
		return nil
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordBillingLegForAttempt(cctx, request, attempt, attempt.terminalEvidence(), sdkterminal.CommandPartialError, p.billingEvidenceFallback(), t.committed(), request.billingState)
		t.handoffBillingTurn(cctx, request, sdkterminal.CommandPartialError)
		return nil
	})
	if !t.finished() {
		t.finishResponse(p, attempt)
	}
}

// terminalizeGateReplacement owns the post-output mandatory-recording stop.
// Recv only sequences this terminal claim before asking recovery to open again.
func (t *turnTerminal) terminalizeGateReplacement(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) error {
	if t == nil || attempt == nil || p == nil {
		return nil
	}
	_ = t.terminalizeSnapshot(ctx, sdkterminal.CommandGateReplacement, attempt, p.accumulatorSnapshot(), nil, func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordBillingLegForAttempt(cctx, request, attempt, attempt.terminalEvidence(), sdkterminal.CommandGateReplacement, p.billingEvidenceFallback(), t.committed(), request.billingState)
		t.handoffBillingTurn(cctx, request, sdkterminal.CommandGateReplacement)
		return nil
	})
	return &lipapi.UpstreamFailureError{
		Phase: lipapi.PhasePostOutput, Recoverable: false,
		Reason:       "secure session mandatory recorder failure after committed output",
		CandidateKey: strings.TrimSpace(attempt.cand.Key),
	}
}

func (t *turnTerminal) registerReplacement(ctx context.Context, out replacementOpenResult, next *attemptSession) error {
	if t == nil || next == nil || out.registered {
		return nil
	}
	if err := t.registerBLeg(ctx, leglifecycle.BLegHandle{ID: out.bleg.BLegID, Attempt: lifecycleAttempt(out.stream)}); err != nil {
		evidence := attemptEvidence{
			Command:     sdkterminal.CommandSwallowedAttempt,
			ReleaseKind: authorityapp.ReleaseKindSwallowed,
			LegOutcome:  billing.LegOutcomeSwallowed,
		}
		_ = next.TerminalizeAttempt(ctx, IntentOpenReadinessFailure, evidence)
		return err
	}
	return nil
}

func (t *turnTerminal) cleanupUnpublishedReplacement(ctx context.Context, next *attemptSession) {
	if next == nil {
		return
	}
	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()
	_cause := errors.New("publication closed")
	_outcome := billing.LegOutcomeFailed
	if errors.Is(_cause, context.Canceled) {
		_outcome = billing.LegOutcomeCanceled
	}
	_evidence := attemptEvidence{Command: sdkterminal.CommandBackendOpenFailure, LegOutcome: _outcome, Usage: emptyOperatorUsageShell(), Err: _cause, RecordReason: _cause.Error()}
	next.TerminalizeAttempt(cleanupCtx, IntentPreReturnAbort, _evidence)
}

func (t *turnTerminal) terminalizeSwallowedAttempt(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
	if t == nil || attempt == nil || p == nil {
		return
	}
	if attempt.finalStreamObs != nil {
		attempt.finalStreamObs.Finish(ctx, response.OutcomeReplaced)
	}
	_ = attempt.terminalizeSnapshot(ctx, sdkterminal.CommandSwallowedAttempt, p.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		t.recordSwallowedAttempt(cctx, request, attempt, p)
		return nil
	})
}

func (t *turnTerminal) releaseSwallowedAttempt(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
	if t == nil || attempt == nil || p == nil || attempt.authority.Settled() {
		return
	}
	t.terminalizeSwallowedAttempt(ctx, request, attempt, p)
}

func (t *turnTerminal) recordSwallowedAttempt(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
	if t == nil || attempt == nil || p == nil {
		return
	}
	usageEv := p.operatorUsageForFinalize()
	attempt.authority.ApplyUnreservedUsage(ctx, authorityapp.SettlementKindPartial, usageEv)
	t.emitBackendEgressMeteringFactForAttempt(ctx, attempt, metering.AttemptOutcomeFailed, metering.SurfacedNo, usageEv)
	t.recordBillingLegForAttempt(ctx, request, attempt, attempt.terminalEvidence(), sdkterminal.CommandSwallowedAttempt, p.billingEvidenceFallback(), t.committed(), request.billingState)
}

// emitSynthesizedUsage is the terminal-owned handoff for usage reconstructed
// at response completion. Response observation remains delegated to the named
// pipeline; this owner applies the resulting output/commit consequences.
func (t *turnTerminal) emitSynthesizedUsage(ctx context.Context, ev lipapi.Event, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) (lipapi.Event, error) {
	if t == nil || p == nil || attempt == nil {
		return lipapi.Event{}, nil
	}
	attempt.accounting.observeClientEvent(p.nowTime(), ev)
	pm := sdk.PartMeta{TraceID: request.traceID, ALegID: request.aLegID, BLegID: attempt.bleg.BLegID, AttemptSeq: attempt.bleg.Seq, BackendID: strings.TrimSpace(attempt.cand.Primary.Backend)}
	out, recording, err := p.observeSynthesizedUsage(ctx, ev, request, attempt, pm, t.committed())
	if err != nil {
		cmd := sdkterminal.CommandPartialError
		if recording.mandatory() {
			cmd = sdkterminal.CommandFrontendEncoderFailure
		}
		t.terminalizePartialFailure(ctx, p, request, attempt, cmd, attemptReasonDetail(err), err)
		return lipapi.Event{}, err
	}
	if lipapi.OutputCommitted(out) {
		t.markOutputCommittedForAttempt(out, attempt, nil)
	}
	p.emitUsageTerminal(ctx, request, attempt, out)
	return out, nil
}

func (t *turnTerminal) markOutputCommittedForAttempt(ev lipapi.Event, attempt *attemptSession, recovery *recoveryController) {
	if t == nil || !lipapi.OutputCommitted(ev) {
		return
	}
	t.markCommitted(attempt)
	if recovery != nil && recovery.ttft != nil {
		recovery.ttft.markCommitted()
	}
}

func (t *turnTerminal) accountingFinalized() bool {
	return t != nil && t.accountingFinalizedState.Load()
}

func (t *turnTerminal) claimAccountingFinalization() bool {
	return t != nil && t.accountingFinalizedState.CompareAndSwap(false, true)
}

func (t *turnTerminal) unclaimAccountingFinalization() {
	if t != nil {
		t.accountingFinalizedState.Store(false)
	}
}

// terminalizeSnapshot composes request and explicitly captured attempt
// ownership according to the command's declared scopes. The evidence snapshot
// and attempt pointer are values supplied by the caller before any terminal
// work starts; this prevents a replacement from changing the B-leg attributed
// to an in-flight terminal effect.
//
// attemptEffects are the attempt-local effects. requestEffects are the
// request-after effects (including request-level economic closure). Keeping the
// pair explicit avoids a broad effects bag while preserving the old nested
// terminal semantics: an attempt winner runs attemptEffects once, an attempt
// loser falls back to the request invocation, and requestEffects runs once for
// the request winner.
func (t *turnTerminal) terminalizeSnapshot(
	ctx context.Context,
	cmd sdkterminal.Command,
	attempt *attemptSession,
	snapshot coreterm.AccumulatorSnapshot,
	attemptEffects func(context.Context, coreterm.Outcome) error,
	requestEffects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	if t == nil || t.request == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	if !cmd.AllowsScope(sdkterminal.ScopeRequest) {
		if attempt == nil {
			return coreterm.Result{Err: sdkterminal.ErrInvalid}
		}
		return attempt.terminalizeSnapshot(ctx, cmd, snapshot, attemptEffects)
	}

	if t.committed() && !snapshot.OutputCommitted() {
		snapshot = coreterm.NewAccumulatorSnapshot(snapshot.Bytes(), true)
	}

	r := t.request.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		return snapshot.Clone()
	}, func(cctx context.Context, out coreterm.Outcome) error {
		var effectErr error
		if cmd.AllowsScope(sdkterminal.ScopeAttempt) && attempt != nil {
			attemptResult := attempt.terminalizeSnapshot(cctx, cmd, out.Snapshot, attemptEffects)
			if attemptResult.Won {
				effectErr = attemptResult.Err
			} else if attemptEffects != nil {
				// A settled/conflicting attempt still allows the request owner to
				// apply the request-scoped fallback effect once.
				effectErr = attemptEffects(cctx, out)
			}
		} else if attemptEffects != nil {
			effectErr = attemptEffects(cctx, out)
		}
		if requestEffects != nil {
			if requestErr := requestEffects(cctx, out); effectErr == nil {
				effectErr = requestErr
			}
		}
		return effectErr
	})
	// A committed gate replacement cannot claim the request owner, but it still
	// closes the request-level billing/economic window. The closure owner
	// supplies its own once/dedupe guard, so competing rejected gate attempts
	// may safely invoke this narrow request-after seam.
	if !r.Won && cmd == sdkterminal.CommandGateReplacement && errors.Is(r.Err, sdkterminal.ErrOutputCommitted) && requestEffects != nil {
		_ = requestEffects(ctx, r.Outcome)
	}
	return r
}

// terminalizeSnapshot is the attempt owner operation used by turnTerminal and
// by explicit attempt-retirement paths. It accepts a captured evidence value
// and never consults the mutable attempt slot.
func (a *attemptSession) terminalizeSnapshot(
	ctx context.Context,
	cmd sdkterminal.Command,
	snapshot coreterm.AccumulatorSnapshot,
	effects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	if a == nil || a.terminal == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	return a.terminal.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		return snapshot.Clone()
	}, effects)
}
