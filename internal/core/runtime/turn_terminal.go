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
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

// aLegEndMode names the one owner permitted to end the request's A-leg.
// Interleaved thinker/executor wrappers keep the A-leg open until their outer
// boundary; ordinary streams end it at their own terminal boundary.
type aLegEndMode uint8

const terminalDecisionEvaluationBudget = 250 * time.Millisecond

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
	log                     *slog.Logger
	now                     func() time.Time
	billingEnabled          func() bool
	operatorRateRef         func(context.Context, routing.Primary) billing.VersionRef
	billingWorkload         func(context.Context, string) billing.WorkloadIdentity
	observeBillingLeg       func(context.Context, billing.CallLegUsageRecord)
	appendBillingLeg        func(context.Context, billing.BillingCallID, billing.CallLegUsageRecord)
	appendBillingCall       func(context.Context, billing.CallUsageRecord) error
	logBillingAppendFailure func(context.Context, string, string, error)
	finalizeBilling         func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error)
	releaseRequestAuthority func(context.Context) error
	settleRequestAuthority  func(context.Context, []metering.Fact) error
	emitFrontendEgress      func(context.Context, string, lipapi.Event) (metering.Fact, bool)
	meteringRecorderPresent bool
	emitBackendEgress       func(context.Context, string, metering.AttemptOutcome, metering.SurfacedState, lipapi.Event)

	// supportsContinuation indicates whether the A-side frontend can legally
	// stitch a continuation leg onto the same logical response. Zero value is
	// false (conservative unsupported) but all production constructors set it to
	// true for known stitchable frontends. Tests may set it to false to prove
	// a clean final without raw frame concatenation.
	supportsContinuation bool

	steeringStore        conversationview.SteeringStore
	conversationReader   conversationview.Reader
	conversationObserver conversationview.Observer

	terminalDecisionMu     sync.Mutex
	terminalDecisionFlight *terminalDecisionFlight

	// terminalDecisionProvider is generation-owned when wired by the
	// composition root. It remains nil until provider projection is implemented;
	// the evaluator's nil fast path preserves existing behavior.
	terminalDecisionProvider      terminaldecision.Provider
	terminalDecisionProviderID    string
	terminalDecisionProviderHasID bool
	// terminalDecisionAuxiliary is captured once from the immutable request
	// snapshot and copied into every terminal-decision input for this turn.
	terminalDecisionAuxiliary      auxiliary.Client
	terminalDecisionAuxiliaryBound bool

	// continuationTransaction is the one core-owned publication seam for a
	// provider continuation. It is installed when the receive stream is
	// assembled; the terminal owner only invokes it after a provisional
	// decision has returned Continue.
	continuationTransaction func(context.Context, terminaldecision.ContinuationIntent) (bool, error)
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
	t.emitBackendEgress = e.emitBackendEgressMeteringFact
	t.steeringStore = e.conversationViewSteeringStore()
	t.conversationReader = e.conversationViewReader()
	t.conversationObserver = e.conversationViewObserver()
	if !t.terminalDecisionAuxiliaryBound && e.RuntimeSnapshot != nil {
		t.terminalDecisionAuxiliary = e.RuntimeSnapshot.Aux()
		t.terminalDecisionAuxiliaryBound = true
	}
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
		terminal.supportsContinuation = parent.supportsContinuation
		terminal.steeringStore = parent.steeringStore
		terminal.conversationReader = parent.conversationReader
		terminal.conversationObserver = parent.conversationObserver
		terminal.terminalDecisionProvider = parent.terminalDecisionProvider
		terminal.terminalDecisionProviderID = parent.terminalDecisionProviderID
		terminal.terminalDecisionProviderHasID = parent.terminalDecisionProviderHasID
		terminal.terminalDecisionAuxiliary = parent.terminalDecisionAuxiliary
		terminal.terminalDecisionAuxiliaryBound = parent.terminalDecisionAuxiliaryBound
		terminal.continuationTransaction = parent.continuationTransaction
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

func (t *turnTerminal) terminalDecisionProviderIdentity() (string, bool) {
	if t == nil {
		return "", false
	}
	t.terminalDecisionMu.Lock()
	defer t.terminalDecisionMu.Unlock()
	return t.terminalDecisionProviderID, t.terminalDecisionProviderHasID
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

// finishResponseAtBoundary completes a response event at the receive-phase
// ownership boundary. An interleaved thinker has only completed its B-leg;
// the outer stream still owns the shared request terminal until the executor
// phase (or an authoritative abort) completes it.
func (t *turnTerminal) finishResponseAtBoundary(response *responsePipeline, attempt *attemptSession, endALeg bool) bool {
	if t == nil {
		return false
	}
	if t.isInterleavedThinker() {
		clearAttemptToolState(response, attempt)
		return true
	}
	finished := t.finishResponse(response, attempt)
	if endALeg {
		t.endALeg(aLegEndBase)
	}
	return finished
}

func (t *turnTerminal) settleOrReleaseRequestAuthority(ctx context.Context, p *responsePipeline, request requestTerminalFacts) {
	if t.committed() {
		if p == nil {
			return
		}
		_ = t.settleRequestAuthorityWithFrontendEgress(ctx, p.usageEvidenceOrEmpty(), request, p)
	} else if t.releaseRequestAuthority != nil {
		_ = t.releaseRequestAuthority(ctx)
	}
}

func (t *turnTerminal) terminalizeTurn(ctx context.Context, cmd sdkterminal.Command, intent attemptTerminalIntent, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, evidence attemptEvidence, snapshot coreterm.AccumulatorSnapshot) bool {
	input := t.terminalDecisionInput(cmd, request, attempt, p, snapshot)
	_, published := t.terminalizeTurnDecisionWithPublication(ctx, t.terminalDecisionProvider, input, cmd, intent, request, attempt, p, evidence, snapshot)
	return published
}

// terminalizeTurnWithDecision is the production terminal path used by the
// focused contract tests and by future generation wiring. Continue remains
// provisional until the installed core transaction publishes a B-leg.
func (t *turnTerminal) terminalizeTurnWithDecision(ctx context.Context, provider terminaldecision.Provider, input terminaldecision.Input, cmd sdkterminal.Command, attempt *attemptSession) coreterm.Result {
	if t == nil || t.request == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	if provider != nil {
		providerID := provider.ID()
		t.terminalDecisionMu.Lock()
		if !t.terminalDecisionProviderHasID {
			t.terminalDecisionProviderID = providerID
			t.terminalDecisionProviderHasID = true
		}
		t.terminalDecisionMu.Unlock()
	}
	snapshot := coreterm.NewAccumulatorSnapshot(nil, input.Candidate.OutputCommitted)
	evidence := attemptEvidence{
		Command:      cmd,
		LegOutcome:   decisionLegOutcome(cmd),
		Snapshot:     &snapshot,
		Committed:    input.Candidate.OutputCommitted,
		RecordReason: string(input.Candidate.Cause),
		TraceID:      input.Request.TraceID,
		ALegID:       input.Request.ALegID,
	}
	result, _ := t.terminalizeTurnDecisionWithPublication(ctx, provider, input, cmd, decisionIntent(cmd), requestTerminalFacts{}, attempt, nil, evidence, snapshot)
	return result
}

// terminalizeTurnDecision is the single bridge between provisional candidate
// evaluation and the existing request/attempt owners. No owner is claimed
// until evaluation has returned a final decision.
func (t *turnTerminal) terminalizeTurnDecision(ctx context.Context, provider terminaldecision.Provider, input terminaldecision.Input, cmd sdkterminal.Command, intent attemptTerminalIntent, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, evidence attemptEvidence, snapshot coreterm.AccumulatorSnapshot) coreterm.Result {
	result, _ := t.terminalizeTurnDecisionWithPublication(ctx, provider, input, cmd, intent, request, attempt, p, evidence, snapshot)
	return result
}

func (t *turnTerminal) terminalizeTurnDecisionWithPublication(ctx context.Context, provider terminaldecision.Provider, input terminaldecision.Input, cmd sdkterminal.Command, intent attemptTerminalIntent, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, evidence attemptEvidence, snapshot coreterm.AccumulatorSnapshot) (coreterm.Result, bool) {
	decision := t.sharedTerminalDecision(ctx, provider, input)
	return t.terminalizeTurnAfterDecision(ctx, decision, cmd, intent, request, attempt, p, evidence, snapshot)
}

func (t *turnTerminal) terminalizeTurnAfterDecision(ctx context.Context, decision terminalDecisionOutcome, cmd sdkterminal.Command, intent attemptTerminalIntent, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, evidence attemptEvidence, snapshot coreterm.AccumulatorSnapshot) (coreterm.Result, bool) {
	if decision.Decision.Kind == terminaldecision.DecisionContinue {
		if decision.Decision.Continue == nil || t.continuationTransaction == nil {
			return coreterm.Result{State: t.requestTerminal().Owner().State()}, false
		}
		published, err := t.continuationTransaction(ctx, *decision.Decision.Continue)
		if published {
			return coreterm.Result{State: t.requestTerminal().Owner().State()}, true
		}
		if err != nil && t.finished() {
			// The existing transaction owns pre-publication failure
			// finalization. Do not claim or settle the request a second time.
			return coreterm.Result{State: t.requestTerminal().Owner().State(), Err: err}, false
		}
		if err != nil {
			decision.Decision = allowStopDecision("continuation_failed")
			evidence.Err = err
		} else {
			return coreterm.Result{State: t.requestTerminal().Owner().State()}, false
		}
	}
	if decision.Decision.Kind == terminaldecision.DecisionSurfaceFailure {
		if cmd != sdkterminal.CommandCancel && cmd != sdkterminal.CommandClose && cmd != sdkterminal.CommandTimeout {
			cmd = sdkterminal.CommandPartialError
		}
		intent = IntentSurfacedFailure
		evidence.Command = cmd
		evidence.LegOutcome = billing.LegOutcomeFailed
	}
	if decision.OutputCommitted {
		snapshot = coreterm.NewAccumulatorSnapshot(snapshot.Bytes(), true)
		evidence.Committed = true
	}

	deactErr := deactivateContinuationOverlay(ctx, t, request.aLegID)
	if deactErr != nil {
		if evidence.Err == nil {
			evidence.Err = deactErr
		}
		if cmd == sdkterminal.CommandNormalFinish {
			cmd = sdkterminal.CommandPartialError
		}
	}
	if attempt != nil {
		attempt.TerminalizeAttempt(ctx, intent, evidence)
	}
	result := t.claimRequestTerminal(ctx, cmd, snapshot, func(cctx context.Context, _ coreterm.Outcome) error {
		t.settleOrReleaseRequestAuthority(cctx, p, request)
		t.handoffBillingTurn(cctx, request, cmd)
		t.finishResponse(p, attempt)
		return deactErr
	})
	if !t.finished() {
		t.finishResponse(p, attempt)
	}
	return result, false
}

func (t *turnTerminal) terminalDecisionInput(cmd sdkterminal.Command, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, snapshot coreterm.AccumulatorSnapshot) terminaldecision.Input {
	var bLegID string
	if attempt != nil {
		bLegID = attempt.bleg.BLegID
	}
	reference := strings.TrimSpace(bLegID)
	if reference == "" {
		reference = strings.TrimSpace(request.traceID)
	}
	if reference == "" {
		reference = string(cmd)
	}
	requestID := strings.TrimSpace(request.call.ID)
	if requestID == "" {
		requestID = strings.TrimSpace(request.traceID)
	}
	if requestID == "" {
		requestID = reference
	}
	policy := request.terminalDecisionPolicy
	if strings.TrimSpace(policy.Revision) == "" {
		policy.Revision = "0"
	}
	evidence := projectTerminalDecisionEvidence(request, attempt, p)
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:           decisionCandidateCause(cmd),
			Reference:       reference,
			OutputCommitted: snapshot.OutputCommitted(),
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: requestID,
			TraceID:   request.traceID,
			ALegID:    request.aLegID,
			BLegID:    bLegID,
		},
		Policy: policy,
		Continuation: terminaldecision.ContinuationEvidence{
			TrajectoryRef: evidence.Lineage.TrajectoryRef,
			Attempt:       evidence.Lineage.Attempt,
		},
		Evidence:  evidence,
		Auxiliary: t.terminalDecisionAuxiliary,
		Deadline:  time.Now().Add(terminalDecisionEvaluationBudget),
	}
}

func decisionCandidateCause(cmd sdkterminal.Command) terminaldecision.CandidateCause {
	switch cmd {
	case sdkterminal.CommandCancel, sdkterminal.CommandClose, sdkterminal.CommandTimeout:
		return terminaldecision.CandidateCauseCancellation
	case sdkterminal.CommandGateReplacement:
		return terminaldecision.CandidateCauseAuthorityDenied
	case sdkterminal.CommandNormalFinish:
		return terminaldecision.CandidateCauseNormal
	case sdkterminal.CommandEOF:
		return terminaldecision.CandidateCauseTransport
	default:
		return terminaldecision.CandidateCauseProviderError
	}
}

func decisionIntent(cmd sdkterminal.Command) attemptTerminalIntent {
	switch cmd {
	case sdkterminal.CommandCancel, sdkterminal.CommandClose:
		return IntentCancellation
	case sdkterminal.CommandTimeout:
		return IntentTimeout
	case sdkterminal.CommandNormalFinish:
		return IntentSuccess
	default:
		return IntentSurfacedFailure
	}
}

func decisionLegOutcome(cmd sdkterminal.Command) billing.LegOutcome {
	switch cmd {
	case sdkterminal.CommandCancel, sdkterminal.CommandClose, sdkterminal.CommandTimeout:
		return billing.LegOutcomeCanceled
	default:
		return billing.LegOutcomeFailed
	}
}

func (t *turnTerminal) makeBaseEvidence(request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, snapshot *coreterm.AccumulatorSnapshot) attemptEvidence {
	var started time.Time
	if attempt != nil {
		started = attempt.accountingStartedAt()
	}
	return attemptEvidence{
		Usage:          p.operatorUsageForFinalize(),
		TraceID:        request.traceID,
		ALegID:         request.aLegID,
		Snapshot:       snapshot,
		StartedAt:      started,
		StreamFallback: p.billingEvidenceFallback(),
		BillingState:   request.billingState,
		BillingCallID:  request.billingCallID,
		Committed:      t.committed(),
	}
}

func (t *turnTerminal) terminalizeWithEvidence(
	ctx context.Context,
	request requestTerminalFacts,
	attempt *attemptSession,
	p *responsePipeline,
	cmd sdkterminal.Command,
	intent attemptTerminalIntent,
	legOutcome billing.LegOutcome,
	obsOutcome response.StreamOutcome,
	recOutcome lipapi.AttemptOutcome,
	reason string,
	cause error,
	prep func(context.Context) (lipapi.Event, lipapi.Event, bool, error),
) bool {
	if t == nil || p == nil {
		return false
	}
	snapshot := p.accumulatorSnapshot()
	ev := t.makeBaseEvidence(request, attempt, p, &snapshot)
	ev.Command = cmd
	ev.LegOutcome = legOutcome
	ev.ObsOutcome = obsOutcome
	ev.RecordOutcome = recOutcome
	ev.RecordReason = reason
	ev.Err = cause
	ev.AuthorityPrepare = prep
	switch cmd {
	case sdkterminal.CommandCancel:
		ev.BillingReason = reason
		ev.CancelCause = &lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: reason}
	case sdkterminal.CommandTimeout:
		ev.CancelCause = &lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "timeout"}
	}
	return t.terminalizeTurn(ctx, cmd, intent, request, attempt, p, ev, snapshot)
}

func (t *turnTerminal) closeClose(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
	if t == nil || p == nil || t.finished() {
		return
	}
	snapshot := p.accumulatorSnapshot()
	ev := t.makeBaseEvidence(request, attempt, p, &snapshot)
	ev.Command = sdkterminal.CommandClose
	ev.LegOutcome = billing.LegOutcomeCanceled
	ev.ObsOutcome = response.OutcomeClosed
	ev.RecordOutcome = lipapi.AttemptCancelled
	ev.RecordReason = "client closed"
	ev.BillingReason = "client closed"
	ev.CancelCause = &lipapi.CancelCause{Kind: lipapi.CancelClientGone, Detail: "client closed"}
	decision := t.sharedTerminalDecision(ctx, t.terminalDecisionProvider, t.terminalDecisionInput(sdkterminal.CommandClose, request, attempt, p, snapshot))
	if decision.Decision.Kind == terminaldecision.DecisionContinue {
		return
	}
	if attempt != nil {
		attempt.TerminalizeAttempt(ctx, IntentCancellation, ev)
	}
	if t.hasALeg() {
		_ = t.cancelALeg(ctx, lipapi.CancelCause{Kind: lipapi.CancelClientGone})
	}
	t.terminalizeTurnAfterDecision(ctx, decision, sdkterminal.CommandClose, IntentCancellation, request, nil, p, ev, snapshot)
}

func (t *turnTerminal) isALegCanceled(err error) bool {
	return errors.Is(err, leglifecycle.ErrALegCanceled)
}

// terminalizePartialFailure owns the terminal side of a response/encoder
// failure. Event transformation stays on responsePipeline; this method only
// applies the irreversible terminal and billing consequences.
func (t *turnTerminal) terminalizePartialFailure(ctx context.Context, p *responsePipeline, request requestTerminalFacts, attempt *attemptSession, cmd sdkterminal.Command, reason string, cause error) bool {
	if attempt == nil {
		return false
	}
	return t.terminalizeWithEvidence(ctx, request, attempt, p, cmd, IntentSurfacedFailure, billing.LegOutcomeFailed, response.OutcomeFailed, lipapi.AttemptSurfacedFailure, reason, cause, nil)
}

func (t *turnTerminal) partialFailure(ctx context.Context, p *responsePipeline, request requestTerminalFacts, attempt *attemptSession, encoderFailure bool, cause error) bool {
	cmd := sdkterminal.CommandPartialError
	if encoderFailure {
		cmd = sdkterminal.CommandFrontendEncoderFailure
	}
	return t.terminalizePartialFailure(ctx, p, request, attempt, cmd, attemptReasonDetail(cause), cause)
}

func (t *turnTerminal) terminalizeEOF(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) bool {
	if attempt == nil {
		return false
	}
	return t.terminalizeWithEvidence(ctx, request, attempt, p, sdkterminal.CommandEOF, IntentSurfacedFailure, billing.LegOutcomeFailed, response.OutcomeFailed, lipapi.AttemptSurfacedFailure, "stream ended without response_finished", io.EOF, nil)
}

func (t *turnTerminal) terminalizeTimeout(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) bool {
	if attempt == nil {
		return false
	}
	return t.terminalizeWithEvidence(ctx, request, attempt, p, sdkterminal.CommandTimeout, IntentTimeout, billing.LegOutcomeCanceled, response.OutcomeFailed, lipapi.AttemptCancelled, "timeout", context.DeadlineExceeded, nil)
}

func (t *turnTerminal) terminalizeCancellation(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, reason string, timeout bool) bool {
	if attempt == nil {
		return false
	}
	cmd := sdkterminal.CommandCancel
	intent := IntentCancellation
	if timeout {
		cmd = sdkterminal.CommandTimeout
		intent = IntentTimeout
	}
	return t.terminalizeWithEvidence(ctx, request, attempt, p, cmd, intent, billing.LegOutcomeCanceled, response.OutcomeCancelled, lipapi.AttemptCancelled, reason, nil, func(cctx context.Context) (lipapi.Event, lipapi.Event, bool, error) {
		if t.finalizeBillingAfterCancel(cctx, attempt, reason, request, p) {
			t.reconcileOrSettleCancellationAuthorityForAttempt(cctx, attempt, p)
		} else {
			t.settleCancellationAuthorityForAttempt(cctx, attempt, p)
		}
		usageEv := p.operatorUsageForFinalize()
		return usageEv, usageEv, true, nil
	})
}

func (t *turnTerminal) terminalizeSurfacedFailure(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, surfErr error, panicFailure bool) bool {
	cmd := sdkterminal.CommandPartialError
	if panicFailure {
		cmd = sdkterminal.CommandPanic
	}
	return t.terminalizePartialFailure(ctx, p, request, attempt, cmd, attemptReasonDetail(surfErr), surfErr)
}

func (t *turnTerminal) terminalizeReplacementFailure(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) bool {
	return t.terminalizePartialFailure(ctx, p, request, attempt, sdkterminal.CommandPartialError, "replacement failure", nil)
}

// terminalizeGateReplacement owns the post-output mandatory-recording stop.
// Recv only sequences this terminal claim before asking recovery to open again.
func (t *turnTerminal) terminalizeGateReplacement(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) error {
	if t == nil || attempt == nil || p == nil {
		return nil
	}
	gateErr := &lipapi.UpstreamFailureError{
		Phase:        lipapi.PhasePostOutput,
		Recoverable:  false,
		Reason:       "secure session mandatory recorder failure after committed output",
		CandidateKey: strings.TrimSpace(attempt.cand.Key),
	}
	snapshot := p.accumulatorSnapshot()
	ev := t.makeBaseEvidence(request, attempt, p, &snapshot)
	ev.Command = sdkterminal.CommandGateReplacement
	ev.LegOutcome = billing.LegOutcomeFailed
	ev.ObsOutcome = response.OutcomeFailed
	ev.RecordOutcome = lipapi.AttemptSurfacedFailure
	ev.RecordReason = gateErr.Reason
	ev.Err = gateErr
	decision := t.sharedTerminalDecision(ctx, t.terminalDecisionProvider, t.terminalDecisionInput(sdkterminal.CommandGateReplacement, request, attempt, p, snapshot))
	if decision.Decision.Kind == terminaldecision.DecisionContinue {
		return nil
	}
	attempt.TerminalizeAttempt(ctx, IntentSurfacedFailure, ev)
	t.claimRequestTerminal(ctx, sdkterminal.CommandGateReplacement, snapshot, func(cctx context.Context, _ coreterm.Outcome) error {
		t.handoffBillingTurn(cctx, request, sdkterminal.CommandGateReplacement)
		return nil
	})
	return gateErr
}

func (t *turnTerminal) registerReplacement(ctx context.Context, out replacementOpenResult, next *readyAttempt) error {
	if t == nil || next == nil || out.registered {
		return nil
	}
	if err := t.registerBLeg(ctx, leglifecycle.BLegHandle{ID: out.bleg.BLegID, Attempt: next.lifecycleHandle()}); err != nil {
		next.Dispose(ctx, err)
		return err
	}
	return nil
}

func (t *turnTerminal) cleanupUnpublishedReplacement(ctx context.Context, next *readyAttempt) {
	if next == nil {
		return
	}
	next.Dispose(context.Background(), errors.New("publication closed"))
}

// emitSynthesizedUsage is the terminal-owned handoff for usage reconstructed
// at response completion. Response observation remains delegated to the named
// pipeline; this owner applies the resulting output/commit consequences.
func (t *turnTerminal) emitSynthesizedUsage(ctx context.Context, ev lipapi.Event, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) (lipapi.Event, error) {
	if t == nil || p == nil || attempt == nil {
		return lipapi.Event{}, nil
	}
	attempt.observeAccountingClientEvent(p.nowTime(), ev)
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

// terminalizeRequest coordinates logical request/A-leg terminal and request
// billing closure only. It MUST NOT accept attempt or attemptEffects; all
// attempt-owned effects go through attemptSession.TerminalizeAttempt.
func (t *turnTerminal) terminalizeRequest(
	ctx context.Context,
	cmd sdkterminal.Command,
	snapshot coreterm.AccumulatorSnapshot,
	requestEffects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	return t.claimRequestTerminal(ctx, cmd, snapshot, requestEffects)
}

func (t *turnTerminal) claimRequestTerminal(
	ctx context.Context,
	cmd sdkterminal.Command,
	snapshot coreterm.AccumulatorSnapshot,
	requestEffects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	if t == nil || t.request == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	if !cmd.AllowsScope(sdkterminal.ScopeRequest) {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	if t.committed() && !snapshot.OutputCommitted() {
		snapshot = coreterm.NewAccumulatorSnapshot(snapshot.Bytes(), true)
	}
	r := t.request.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		return snapshot.Clone()
	}, func(cctx context.Context, out coreterm.Outcome) error {
		if requestEffects != nil {
			return requestEffects(cctx, out)
		}
		return nil
	})
	if !r.Won && cmd == sdkterminal.CommandGateReplacement && errors.Is(r.Err, sdkterminal.ErrOutputCommitted) && requestEffects != nil {
		_ = requestEffects(ctx, r.Outcome)
	}
	return r
}
