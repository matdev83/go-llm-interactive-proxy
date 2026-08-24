package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

func (a *attemptSession) receive(ctx context.Context, committed bool) (lipapi.Event, error) {
	if a == nil {
		return lipapi.Event{}, io.EOF
	}
	inner := a.loadInner()
	if inner == nil {
		return lipapi.Event{}, io.EOF
	}
	ev, err := safety.CallValue(safety.BoundaryBackend, "backend_recv", func() (lipapi.Event, error) {
		return inner.Recv(ctx)
	})
	if err != nil {
		var pe *safety.PanicError
		if errors.As(err, &pe) {
			err = mapStreamPanic(pe, committed)
		}
	}
	return ev, err
}

func backendReceivePanic(err error) bool {
	var pe *safety.PanicError
	return errors.As(err, &pe)
}

func runGuarded(name string, errorsList *[]error, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			*errorsList = append(*errorsList, fmt.Errorf("runtime: %s panicked: %v", name, r))
		}
	}()
	fn()
}

// attemptSession is the owner of one opened B-leg. Every field in this type
// has the same lifetime as the backend attempt and is discarded on replacement.
// The inner lock protects only the backend stream pointer; callers snapshot it
// and perform Cancel/Close without holding this lock.
type attemptSession struct {
	innerMu            sync.Mutex
	inner              lipapi.ManagedEventStream
	streamDisposed     bool
	pendingCancelCause atomic.Pointer[lipapi.CancelCause]
	terminalizing      atomic.Bool
	forceCloseOnce     sync.Once
	forceClose         chan struct{}
	usageMu            sync.Mutex
	billingMu          sync.Mutex

	internalUsageKeys  map[string]struct{}
	accumulatedUsage   []lipapi.Event
	billingLegRecorded bool

	cancelResult lipapi.CancelResult

	bleg              b2bua.BLegRecord
	cand              routing.AttemptCandidate
	authority         authorityLifecycle
	terminal          *streamTerminal
	aScope            *leglifecycle.ALeg
	releaseKind       authorityapp.ReleaseKind
	defaultCommand    sdkterminal.Command
	defaultLegOutcome billing.LegOutcome
	traceID           string
	billingCallID     billing.BillingCallID
	billingCallState  *billingCallState

	accounting            attemptAccountingTracker
	toolFinal             *toolCallAssembler
	promptCacheSource     promptcache.ObservationSource
	promptCacheController promptcache.Controller
	finalStreamObs        *extensions.FinalStreamObservationSession
	recordAttemptLoggedFn func(context.Context, recordAttemptParams, diag.AttrOpts)
	recordCancellationFn  func(obs CancellationObservation)

	emitBackendEgressFn func(context.Context, string, metering.AttemptOutcome, metering.SurfacedState, lipapi.Event)
	appendBillingLegFn  func(context.Context, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, billing.LegOutcome)
	now                 func() time.Time

	billingEnabled    func() bool
	operatorRateRef   func(context.Context, routing.Primary) billing.VersionRef
	billingWorkload   func(context.Context, string) billing.WorkloadIdentity
	observeBillingLeg func(context.Context, billing.CallLegUsageRecord)
	appendBillingLeg  func(context.Context, billing.BillingCallID, billing.CallLegUsageRecord)
	finalizeBilling   func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error)
}

func (a *attemptSession) claimBillingLegRecord() bool {
	if a == nil {
		return false
	}
	a.billingMu.Lock()
	defer a.billingMu.Unlock()
	if a.billingLegRecorded {
		return false
	}
	a.billingLegRecorded = true
	return true
}

type attemptSessionInput struct {
	inner          lipapi.ManagedEventStream
	streamDisposed bool
	bleg           b2bua.BLegRecord
	cand           routing.AttemptCandidate
	authority      authorityLifecycle
	aScope         *leglifecycle.ALeg

	traceID               string
	billingCallID         billing.BillingCallID
	billingCallState      *billingCallState
	accounting            attemptAccountingTracker
	toolFinal             *toolCallAssembler
	promptCacheSource     promptcache.ObservationSource
	promptCacheController promptcache.Controller
	finalStreamObs        *extensions.FinalStreamObservationSession
	recordAttemptLoggedFn func(context.Context, recordAttemptParams, diag.AttrOpts)

	emitBackendEgressFn func(context.Context, string, metering.AttemptOutcome, metering.SurfacedState, lipapi.Event)
	appendBillingLegFn  func(context.Context, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, billing.LegOutcome)
	now                 func() time.Time

	billingEnabled    func() bool
	operatorRateRef   func(context.Context, routing.Primary) billing.VersionRef
	billingWorkload   func(context.Context, string) billing.WorkloadIdentity
	observeBillingLeg func(context.Context, billing.CallLegUsageRecord)
	appendBillingLeg  func(context.Context, billing.BillingCallID, billing.CallLegUsageRecord)
	finalizeBilling   func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error)
}

func newAttemptSession(in attemptSessionInput) *attemptSession {
	return &attemptSession{
		inner: in.inner, streamDisposed: in.streamDisposed, bleg: in.bleg, cand: in.cand, authority: in.authority,
		forceClose: make(chan struct{}),
		terminal:   newStreamTerminal(sdkterminal.ScopeAttempt), aScope: in.aScope,
		releaseKind: authorityapp.ReleaseKindSwallowed, defaultCommand: sdkterminal.CommandBackendOpenFailure, defaultLegOutcome: billing.LegOutcomeFailed,
		traceID: in.traceID, billingCallID: in.billingCallID, billingCallState: in.billingCallState,
		accounting: in.accounting, toolFinal: in.toolFinal, promptCacheSource: in.promptCacheSource,
		promptCacheController: in.promptCacheController, finalStreamObs: in.finalStreamObs,
		recordAttemptLoggedFn: in.recordAttemptLoggedFn, emitBackendEgressFn: in.emitBackendEgressFn,
		appendBillingLegFn: in.appendBillingLegFn, now: in.now, billingEnabled: in.billingEnabled,
		operatorRateRef: in.operatorRateRef, billingWorkload: in.billingWorkload,
		observeBillingLeg: in.observeBillingLeg, appendBillingLeg: in.appendBillingLeg, finalizeBilling: in.finalizeBilling,
	}
}

func (a *attemptSession) recordCancellation(obs CancellationObservation) {
	if a != nil && a.recordCancellationFn != nil {
		a.recordCancellationFn(obs)
	}
}

func (a *attemptSession) recordAttemptLogged(ctx context.Context, p recordAttemptParams, attrs diag.AttrOpts) {
	if a != nil && a.recordAttemptLoggedFn != nil {
		a.recordAttemptLoggedFn(ctx, p, attrs)
	}
}

func (a *attemptSession) BLeg() b2bua.BLegRecord {
	if a == nil {
		return b2bua.BLegRecord{}
	}
	return a.bleg
}

func (a *attemptSession) Candidate() routing.AttemptCandidate {
	if a == nil {
		return routing.AttemptCandidate{}
	}
	return a.cand
}

func (a *attemptSession) loadInner() lipapi.ManagedEventStream {
	if a == nil {
		return nil
	}
	a.innerMu.Lock()
	defer a.innerMu.Unlock()
	return a.inner
}

func (a *attemptSession) storeInner(stream lipapi.ManagedEventStream) {
	if a == nil {
		return
	}
	a.innerMu.Lock()
	a.inner = stream
	a.innerMu.Unlock()
}

func (a *attemptSession) installBridgeStream(bridge lipapi.ManagedEventStream) {
	if a == nil {
		return
	}
	a.storeInner(bridge)
}

func (a *attemptSession) takeInner() lipapi.ManagedEventStream {
	if a == nil {
		return nil
	}
	a.innerMu.Lock()
	defer a.innerMu.Unlock()
	inner := a.inner
	a.inner = nil
	return inner
}

func (a *attemptSession) hasInner() bool {
	if a == nil {
		return false
	}
	a.innerMu.Lock()
	defer a.innerMu.Unlock()
	return a.inner != nil
}

type attemptLifecycleHandle struct {
	session *attemptSession
}

func (a *attemptSession) terminalizeForCancel(ctx context.Context, cause lipapi.CancelCause, cmd sdkterminal.Command, relKind authorityapp.ReleaseKind, legOutcome billing.LegOutcome) attemptTerminalResult {
	if a == nil {
		return attemptTerminalResult{Cancellation: lipapi.CancelResult{Mode: lipapi.CancelModeNone}}
	}
	intent := IntentCancellation
	if cmd == "" {
		cmd = a.defaultCommand
		if cmd == "" {
			cmd = sdkterminal.CommandCancel
		}
	}
	if relKind == "" {
		relKind = a.releaseKind
	}
	if legOutcome == "" {
		legOutcome = a.defaultLegOutcome
		if legOutcome == "" {
			legOutcome = billing.LegOutcomeCanceled
		}
	}
	if cause.Kind == lipapi.CancelRaceLoser || cmd == sdkterminal.CommandParallelLoser {
		intent, relKind, cmd, legOutcome = IntentParallelLoser, authorityapp.ReleaseKindLosing, sdkterminal.CommandParallelLoser, billing.LegOutcomeFailed
	} else if cmd == sdkterminal.CommandBackendOpenFailure {
		intent, relKind, cmd = IntentSwallowedFailure, authorityapp.ReleaseKindSwallowed, sdkterminal.CommandBackendOpenFailure
		if (ctx != nil && ctx.Err() != nil) || cause.Kind == lipapi.CancelContextDone {
			legOutcome = billing.LegOutcomeCanceled
		} else {
			legOutcome = billing.LegOutcomeFailed
		}
	}
	detail := cause.Detail
	if detail == "" {
		detail = string(cause.Kind)
	}
	return a.TerminalizeAttempt(ctx, intent, attemptEvidence{
		Command:       cmd,
		ReleaseKind:   relKind,
		CancelCause:   &cause,
		LegOutcome:    legOutcome,
		ObsOutcome:    response.OutcomeCancelled,
		RecordOutcome: lipapi.AttemptCancelled,
		RecordReason:  detail,
		BillingReason: detail,
		TraceID:       a.traceID,
		ALegID:        a.bleg.ALegID,
		StartedAt:     a.accounting.requestStartedAt,
	})
}

func (a *attemptSession) cancelViaLifecycle(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	return a.terminalizeForCancel(ctx, cause, a.defaultCommand, a.releaseKind, a.defaultLegOutcome).Cancellation
}

func (a *attemptSession) terminalizeForClose(cmd sdkterminal.Command, relKind authorityapp.ReleaseKind, legOutcome billing.LegOutcome) {
	if a == nil {
		return
	}
	if cmd == "" {
		cmd = a.defaultCommand
		if cmd == "" {
			cmd = sdkterminal.CommandClose
		}
	}
	if relKind == "" {
		relKind = a.releaseKind
	}
	if legOutcome == "" {
		legOutcome = a.defaultLegOutcome
		if legOutcome == "" {
			legOutcome = billing.LegOutcomeCanceled
		}
	}
	a.TerminalizeAttempt(context.Background(), IntentCancellation, attemptEvidence{
		Command:       cmd,
		ReleaseKind:   relKind,
		LegOutcome:    legOutcome,
		ObsOutcome:    response.OutcomeClosed,
		RecordOutcome: lipapi.AttemptCancelled,
		RecordReason:  "aleg close",
		BillingReason: "aleg close",
		TraceID:       a.traceID,
		ALegID:        a.bleg.ALegID,
		StartedAt:     a.accounting.requestStartedAt,
	})
}

func (a *attemptSession) closeViaLifecycle() error {
	if a.terminalizing.Load() && a.forceClose != nil {
		a.forceCloseOnce.Do(func() { close(a.forceClose) })
		return nil
	}
	a.terminalizeForClose(a.defaultCommand, a.releaseKind, a.defaultLegOutcome)
	return nil
}

func (h *attemptLifecycleHandle) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	if h == nil || h.session == nil {
		return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
	}
	return h.session.cancelViaLifecycle(ctx, cause)
}

func (h *attemptLifecycleHandle) Close() error {
	if h == nil || h.session == nil {
		return nil
	}
	return h.session.closeViaLifecycle()
}

func (h *attemptLifecycleHandle) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, io.EOF
}

func (a *attemptSession) lifecycleHandle() leglifecycle.BLegAttempt {
	if a == nil {
		return nil
	}
	return &attemptLifecycleHandle{session: a}
}

func (a *attemptSession) setPendingCancelCause(cause lipapi.CancelCause) {
	if a == nil {
		return
	}
	a.pendingCancelCause.Store(&cause)
}

// drainSidebandEvidence drains provider sideband usage evidence attached to
// the attempt that produced the source stream. Safe for nil receiver/pipeline.
func (a *attemptSession) drainSidebandEvidence(ctx context.Context, facts recvTurnFacts, p *responsePipeline) {
	if a == nil || p == nil {
		return
	}
	inner := a.loadInner()
	if inner == nil {
		return
	}
	p.consumeBackendUsageEvidenceForAttempt(ctx, facts, a, inner)
}

// attemptSlot protects only the current attempt pointer. It never holds its
// lock while receiving, cancelling, closing, terminalizing, or persisting.
type attemptSlot struct {
	mu                sync.Mutex
	current           *attemptSession
	publicationClosed bool
}

func (s *attemptSlot) snapshot() *attemptSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// require returns the installed attempt. Production stream construction and
// replacement install a complete session before exposing the stream; a
// missing session is therefore an internal construction error.
func (s *attemptSlot) require() *attemptSession {
	if s == nil {
		panic("runtime: attempt slot is nil")
	}
	if attempt := s.snapshot(); attempt != nil {
		return attempt
	}
	panic("runtime: attempt session not installed")
}

func (s *attemptSlot) publishReady(ready *readyAttempt) (old *attemptSession, published bool) {
	return s.swapIfOpen(ready)
}

// closePublicationAndSnapshot closes the replacement publication window and
// returns the attempt that was current at that boundary. Replacement Open may
// continue outside this lock, but swapIfOpen rejects its result after this point.
func (s *attemptSlot) closePublicationAndSnapshot() *attemptSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.publicationClosed = true
	current := s.current
	s.mu.Unlock()
	return current
}

// publicationClosed reports that Close has won the replacement publication
// boundary. Recv uses this narrow slot fact to avoid opening recovery work
// after Close has detached the current inner stream but before terminal
// completion becomes visible.
func (s *attemptSlot) publicationIsClosed() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publicationClosed
}

// swapIfOpen publishes a complete replacement only while the request slot is
// still live. It returns the detached prior attempt for callers that need to
// retain its ownership while performing effects outside the slot lock.
func (s *attemptSlot) swapIfOpen(ready *readyAttempt) (old *attemptSession, published bool) {
	if s == nil || ready == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publicationClosed {
		return s.current, false
	}
	next, err := ready.Consume()
	if err != nil {
		return s.current, false
	}
	old, s.current = s.current, next
	return old, true
}

// pendingSelectionEffects captures winner/interleaved effects as data until publication accepts.
type pendingSelectionEffects struct {
	interleaved interleavedstate.State
	memoUpdate  *interleavedthinking.PendingMemoUpdate
}

type pendingInvalidationKind int

const (
	invalidationNone pendingInvalidationKind = iota
	invalidationCancel
	invalidationClose
	invalidationDispose
)

type pendingInvalidation struct {
	kind        pendingInvalidationKind
	cancelCause *lipapi.CancelCause
	intent      attemptTerminalIntent
	evidence    *attemptEvidence
	err         error
}

type readyState int

const (
	readyStateActive readyState = iota
	readyStatePreparing
	readyStatePrepared
	readyStateConsumed
	readyStateDisposed
)

// readyAttempt is a private single-use capability that gates publication.
// It owns the unpublished attemptSession until publication or disposal.
type readyAttempt struct {
	mu                  sync.Mutex
	cond                *sync.Cond
	session             *attemptSession
	boundSess           *attemptSession
	pending             pendingSelectionEffects
	state               readyState
	opInFlight          bool
	pendingInvalidation *pendingInvalidation
	prepErr             error
	defaultReleaseKind  authorityapp.ReleaseKind
	defaultCommand      sdkterminal.Command
	defaultLegOutcome   billing.LegOutcome
}

func newReadyAttempt(session *attemptSession, pending pendingSelectionEffects) *readyAttempt {
	r := &readyAttempt{
		session:   session,
		boundSess: session,
		pending:   pending,
		state:     readyStateActive,
	}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *readyAttempt) hasPendingInvalidation() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingInvalidation != nil
}

func (r *readyAttempt) getCond() *sync.Cond {
	if r.cond == nil {
		r.cond = sync.NewCond(&r.mu)
	}
	return r.cond
}

func (r *readyAttempt) Pending() pendingSelectionEffects {
	if r == nil {
		return pendingSelectionEffects{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending
}

func (r *readyAttempt) Candidate() routing.AttemptCandidate {
	if r == nil {
		return routing.AttemptCandidate{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil {
		return routing.AttemptCandidate{}
	}
	return r.session.cand
}

func (r *readyAttempt) BLeg() b2bua.BLegRecord {
	if r == nil {
		return b2bua.BLegRecord{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil {
		return b2bua.BLegRecord{}
	}
	return r.session.bleg
}

func (r *readyAttempt) AuthorityState() attemptAuthorityState {
	if r == nil {
		return attemptAuthorityState{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil || r.session.authority.control == nil {
		return attemptAuthorityState{}
	}
	return r.session.authority.control.state
}

func (r *readyAttempt) InstallBridgeStream(bridge lipapi.ManagedEventStream) error {
	if r == nil {
		return errors.New("runtime: nil readyAttempt")
	}
	r.mu.Lock()
	cond := r.getCond()
	for r.opInFlight {
		cond.Wait()
	}
	if r.state == readyStateConsumed || r.state == readyStateDisposed || r.pendingInvalidation != nil {
		r.mu.Unlock()
		return errors.New("runtime: readyAttempt already consumed or disposed")
	}
	sess := r.session
	if sess == nil {
		r.mu.Unlock()
		return errors.New("runtime: nil session for bridge install")
	}
	r.opInFlight = true
	r.mu.Unlock()

	sess.installBridgeStream(bridge)

	r.mu.Lock()
	r.opInFlight = false
	cond.Broadcast()
	if r.pendingInvalidation != nil || r.state == readyStateDisposed {
		r.mu.Unlock()
		return errors.New("runtime: readyAttempt canceled or disposed during bridge install")
	}
	r.mu.Unlock()
	return nil
}

// Prepare completes all fallible attempt-local readiness work (sideband drain and
// final stream observation) while the attempt remains unpublished. On readiness failure,
// the capability transitions to disposed and terminalizes the unpublished session exactly once.
func (r *readyAttempt) Prepare(ctx context.Context, facts recvTurnFacts, p *responsePipeline, committed bool) error {
	if r == nil {
		return errors.New("runtime: nil readyAttempt")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	cond := r.getCond()
	for r.opInFlight {
		cond.Wait()
	}
	if r.state == readyStateConsumed || r.state == readyStateDisposed || r.pendingInvalidation != nil {
		r.mu.Unlock()
		if r.prepErr != nil {
			return r.prepErr
		}
		return errors.New("runtime: readyAttempt already consumed or disposed")
	}
	if r.state == readyStatePrepared {
		r.mu.Unlock()
		return nil
	}
	sess := r.session
	if sess == nil || p == nil {
		r.state = readyStateDisposed
		r.session = nil
		r.pending = pendingSelectionEffects{}
		err := errors.New("runtime: nil session for readyAttempt prepare")
		if p == nil {
			err = errors.New("runtime: nil responsePipeline for readyAttempt prepare")
		}
		r.prepErr = err
		cond.Broadcast()
		r.mu.Unlock()

		if sess != nil {
			sess.abortPreReturn(ctx, err)
		}
		return err
	}
	r.opInFlight = true
	r.state = readyStatePreparing
	r.mu.Unlock()

	// 1. Immediate sideband evidence consumption outside mutex
	sess.drainSidebandEvidence(ctx, facts, p)

	// 2. Open final stream observation outside mutex
	pCtx := facts.projectContext(ctx, p.log)
	views, viewsOK := facts.viewsFor(pCtx)
	err := p.openFinalStreamObservation(pCtx, facts, sess, views, viewsOK, committed)

	r.mu.Lock()
	r.opInFlight = false
	if r.pendingInvalidation != nil {
		if err != nil {
			r.prepErr = err
		} else {
			r.prepErr = errors.New("runtime: attempt canceled during prepare")
		}
		cond.Broadcast()
		r.mu.Unlock()
		return r.prepErr
	}
	if err != nil {
		r.state = readyStateDisposed
		r.prepErr = err
		r.session = nil
		r.pending = pendingSelectionEffects{}
		cond.Broadcast()
		r.mu.Unlock()

		if sess != nil {
			sess.abortPreReturn(ctx, err)
		}
		return err
	}

	r.state = readyStatePrepared
	r.prepErr = nil
	cond.Broadcast()
	r.mu.Unlock()
	return nil
}

func (r *readyAttempt) IsConsumed() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == readyStateConsumed || r.state == readyStateDisposed
}

func (r *readyAttempt) Consume() (*attemptSession, error) {
	if r == nil {
		return nil, errors.New("runtime: nil readyAttempt")
	}
	r.mu.Lock()
	cond := r.getCond()
	for r.opInFlight {
		cond.Wait()
	}
	if r.state == readyStateConsumed || r.state == readyStateDisposed || r.pendingInvalidation != nil {
		r.mu.Unlock()
		return nil, errors.New("runtime: readyAttempt already consumed")
	}
	if r.state == readyStateActive {
		r.mu.Unlock()
		return nil, errors.New("runtime: readyAttempt not prepared")
	}
	if r.state != readyStatePrepared {
		r.mu.Unlock()
		return nil, fmt.Errorf("runtime: readyAttempt not prepared (state=%v)", r.state)
	}
	r.state = readyStateConsumed
	sess := r.session
	r.session = nil
	r.pending = pendingSelectionEffects{}
	if sess != nil {
		sess.releaseKind = ""
		sess.defaultCommand = sdkterminal.CommandCancel
		sess.defaultLegOutcome = billing.LegOutcomeCanceled
	}
	cond.Broadcast()
	r.mu.Unlock()
	if sess == nil {
		return nil, errors.New("runtime: readyAttempt session is nil")
	}
	return sess, nil
}

func (r *readyAttempt) markStreamDisposed() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		r.session.streamDisposed = true
	}
}

type readyLifecycleHandle struct {
	ready *readyAttempt
}

func (h *readyLifecycleHandle) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	if h == nil || h.ready == nil {
		return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
	}
	return h.ready.cancelViaLifecycle(ctx, cause)
}

func (h *readyLifecycleHandle) Close() error {
	if h == nil || h.ready == nil {
		return nil
	}
	return h.ready.closeViaLifecycle()
}

func (h *readyLifecycleHandle) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, io.EOF
}

func (r *readyAttempt) lifecycleHandle() leglifecycle.BLegAttempt {
	if r == nil {
		return nil
	}
	return &readyLifecycleHandle{ready: r}
}

func (r *readyAttempt) setDefaultEvidence(releaseKind authorityapp.ReleaseKind, cmd sdkterminal.Command, legOutcome billing.LegOutcome) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultReleaseKind = releaseKind
	r.defaultCommand = cmd
	r.defaultLegOutcome = legOutcome
	if r.session != nil {
		r.session.releaseKind = releaseKind
		r.session.defaultCommand = cmd
		r.session.defaultLegOutcome = legOutcome
	}
}

func (r *readyAttempt) setPending(p pendingSelectionEffects) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.pending = p
	r.mu.Unlock()
}

func (r *readyAttempt) cancelViaLifecycle(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	if r == nil {
		return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
	}
	r.mu.Lock()
	if r.state == readyStateConsumed {
		sess := r.boundSess
		r.mu.Unlock()
		if sess != nil {
			return sess.cancelViaLifecycle(ctx, cause)
		}
		return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
	}
	if r.state == readyStateDisposed {
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		sess := r.boundSess
		r.mu.Unlock()
		if cr, ok := storedCancelResult(sess); ok {
			return cr
		}
		return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
	}
	if r.opInFlight {
		if r.pendingInvalidation == nil {
			r.pendingInvalidation = &pendingInvalidation{
				kind:        invalidationCancel,
				cancelCause: &cause,
			}
			r.state = readyStateDisposed
			cond := r.getCond()
			for r.opInFlight {
				cond.Wait()
			}
			sess := r.session
			r.session = nil
			r.pending = pendingSelectionEffects{}
			relKind := r.defaultReleaseKind
			cmd := r.defaultCommand
			legOutcome := r.defaultLegOutcome
			cond.Broadcast()
			r.mu.Unlock()

			if sess != nil {
				termRes := r.terminalizeSessionForCancel(ctx, sess, cause, cmd, relKind, legOutcome)
				return termRes.Cancellation
			}
			return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
		}
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		sess := r.boundSess
		r.mu.Unlock()
		if cr, ok := storedCancelResult(sess); ok {
			return cr
		}
		return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
	}

	r.state = readyStateDisposed
	sess := r.session
	r.session = nil
	r.pending = pendingSelectionEffects{}
	relKind := r.defaultReleaseKind
	cmd := r.defaultCommand
	legOutcome := r.defaultLegOutcome
	cond := r.getCond()
	cond.Broadcast()
	r.mu.Unlock()

	if sess != nil {
		termRes := r.terminalizeSessionForCancel(ctx, sess, cause, cmd, relKind, legOutcome)
		return termRes.Cancellation
	}
	return lipapi.CancelResult{Mode: lipapi.CancelModeNone}
}

func (r *readyAttempt) terminalizeSessionForCancel(
	ctx context.Context,
	sess *attemptSession,
	cause lipapi.CancelCause,
	cmd sdkterminal.Command,
	relKind authorityapp.ReleaseKind,
	legOutcome billing.LegOutcome,
) attemptTerminalResult {
	return sess.terminalizeForCancel(ctx, cause, cmd, relKind, legOutcome)
}

func (r *readyAttempt) closeViaLifecycle() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.state == readyStateConsumed {
		sess := r.boundSess
		r.mu.Unlock()
		if sess != nil {
			return sess.closeViaLifecycle()
		}
		return nil
	}
	if r.state == readyStateDisposed {
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return nil
	}
	if r.opInFlight {
		if r.pendingInvalidation == nil {
			r.pendingInvalidation = &pendingInvalidation{
				kind: invalidationClose,
			}
			r.state = readyStateDisposed
			cond := r.getCond()
			for r.opInFlight {
				cond.Wait()
			}
			sess := r.session
			r.session = nil
			r.pending = pendingSelectionEffects{}
			relKind, cmd, legOutcome := r.defaultReleaseKind, r.defaultCommand, r.defaultLegOutcome
			cond.Broadcast()
			r.mu.Unlock()

			if sess != nil {
				r.terminalizeSessionForClose(sess, cmd, relKind, legOutcome)
			}
			return nil
		}
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return nil
	}

	r.state = readyStateDisposed
	sess := r.session
	r.session = nil
	r.pending = pendingSelectionEffects{}
	relKind, cmd, legOutcome := r.defaultReleaseKind, r.defaultCommand, r.defaultLegOutcome
	cond := r.getCond()
	cond.Broadcast()
	r.mu.Unlock()

	if sess != nil {
		r.terminalizeSessionForClose(sess, cmd, relKind, legOutcome)
	}
	return nil
}

func (r *readyAttempt) terminalizeSessionForClose(
	sess *attemptSession,
	cmd sdkterminal.Command,
	relKind authorityapp.ReleaseKind,
	legOutcome billing.LegOutcome,
) {
	sess.terminalizeForClose(cmd, relKind, legOutcome)
}

func (r *readyAttempt) DisposeWithEvidence(ctx context.Context, intent attemptTerminalIntent, evidence attemptEvidence) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.state == readyStateConsumed || r.state == readyStateDisposed {
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return
	}
	if r.opInFlight {
		if r.pendingInvalidation == nil {
			r.pendingInvalidation = &pendingInvalidation{
				kind:     invalidationDispose,
				intent:   intent,
				evidence: &evidence,
			}
			r.state = readyStateDisposed
			cond := r.getCond()
			for r.opInFlight {
				cond.Wait()
			}
			sess := r.session
			r.session = nil
			r.pending = pendingSelectionEffects{}
			cond.Broadcast()
			r.mu.Unlock()

			if sess != nil {
				if evidence.TraceID == "" {
					evidence.TraceID = sess.traceID
				}
				if evidence.ALegID == "" {
					evidence.ALegID = sess.bleg.ALegID
				}
				if evidence.StartedAt.IsZero() {
					evidence.StartedAt = sess.accounting.requestStartedAt
				}
				sess.TerminalizeAttempt(ctx, intent, evidence)
			}
			return
		}
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return
	}

	r.state = readyStateDisposed
	sess := r.session
	r.session = nil
	r.pending = pendingSelectionEffects{}
	cond := r.getCond()
	cond.Broadcast()
	r.mu.Unlock()

	if sess != nil {
		if evidence.TraceID == "" {
			evidence.TraceID = sess.traceID
		}
		if evidence.ALegID == "" {
			evidence.ALegID = sess.bleg.ALegID
		}
		if evidence.StartedAt.IsZero() {
			evidence.StartedAt = sess.accounting.requestStartedAt
		}
		sess.TerminalizeAttempt(ctx, intent, evidence)
	}
}

func (r *readyAttempt) Dispose(ctx context.Context, err error) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.state == readyStateConsumed || r.state == readyStateDisposed {
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return
	}
	if r.opInFlight {
		if r.pendingInvalidation == nil {
			r.pendingInvalidation = &pendingInvalidation{
				kind: invalidationDispose,
				err:  err,
			}
			r.state = readyStateDisposed
			cond := r.getCond()
			for r.opInFlight {
				cond.Wait()
			}
			sess := r.session
			r.session = nil
			r.pending = pendingSelectionEffects{}
			cond.Broadcast()
			r.mu.Unlock()

			if sess != nil {
				if err == nil {
					err = errors.New("runtime: attempt aborted before return")
				}
				outcome := billing.LegOutcomeFailed
				if errors.Is(err, context.Canceled) {
					outcome = billing.LegOutcomeCanceled
				}
				evidence := attemptEvidence{
					Command:      sdkterminal.CommandBackendOpenFailure,
					LegOutcome:   outcome,
					Usage:        emptyOperatorUsageShell(),
					Err:          err,
					RecordReason: err.Error(),
					TraceID:      sess.traceID,
					ALegID:       sess.bleg.ALegID,
					StartedAt:    sess.accounting.requestStartedAt,
				}
				sess.TerminalizeAttempt(ctx, IntentPreReturnAbort, evidence)
			}
			return
		}
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return
	}

	r.state = readyStateDisposed
	sess := r.session
	r.session = nil
	r.pending = pendingSelectionEffects{}
	cond := r.getCond()
	cond.Broadcast()
	r.mu.Unlock()

	if sess != nil {
		if err == nil {
			err = errors.New("runtime: attempt aborted before return")
		}
		outcome := billing.LegOutcomeFailed
		if errors.Is(err, context.Canceled) {
			outcome = billing.LegOutcomeCanceled
		}
		evidence := attemptEvidence{
			Command:      sdkterminal.CommandBackendOpenFailure,
			LegOutcome:   outcome,
			Usage:        emptyOperatorUsageShell(),
			Err:          err,
			RecordReason: err.Error(),
			TraceID:      sess.traceID,
			ALegID:       sess.bleg.ALegID,
			StartedAt:    sess.accounting.requestStartedAt,
		}
		sess.TerminalizeAttempt(ctx, IntentPreReturnAbort, evidence)
	}
}

func (sess *attemptSession) abortPreReturn(ctx context.Context, err error) {
	if sess == nil {
		return
	}
	if err == nil {
		err = errors.New("runtime: attempt aborted before return")
	}
	outcome := billing.LegOutcomeFailed
	if errors.Is(err, context.Canceled) {
		outcome = billing.LegOutcomeCanceled
	}
	evidence := attemptEvidence{
		Command:      sdkterminal.CommandBackendOpenFailure,
		LegOutcome:   outcome,
		Usage:        emptyOperatorUsageShell(),
		Err:          err,
		RecordReason: err.Error(),
	}
	sess.TerminalizeAttempt(ctx, IntentPreReturnAbort, evidence)
}

// attemptTerminalIntent describes the reason/intent for terminalizing the attempt.
type attemptTerminalIntent string

const (
	IntentSuccess              attemptTerminalIntent = "success"
	IntentSwallowedFailure     attemptTerminalIntent = "swallowed_failure"
	IntentSurfacedFailure      attemptTerminalIntent = "surfaced_failure"
	IntentCancellation         attemptTerminalIntent = "cancellation"
	IntentTimeout              attemptTerminalIntent = "timeout"
	IntentReplacement          attemptTerminalIntent = "replacement"
	IntentParallelLoser        attemptTerminalIntent = "parallel_loser"
	IntentOpenReadinessFailure attemptTerminalIntent = "open_readiness_failure"
	IntentPublicationDenied    attemptTerminalIntent = "publication_denied"
	IntentPreReturnAbort       attemptTerminalIntent = "pre_return_abort"
)

type attemptEvidence struct {
	Command          sdkterminal.Command
	ReleaseKind      authorityapp.ReleaseKind
	LegOutcome       billing.LegOutcome
	Usage            lipapi.Event
	Err              error
	ObsOutcome       response.StreamOutcome
	TraceID          string
	ALegID           string
	Snapshot         *coreterm.AccumulatorSnapshot
	RecordOutcome    lipapi.AttemptOutcome
	RecordReason     string
	BillingReason    string
	StartedAt        time.Time
	StreamFallback   lipapi.Event
	BillingState     *billingCallState
	BillingCallID    billing.BillingCallID
	Committed        bool
	BillingLegFn     func(ctx context.Context, started, finished time.Time, outcome billing.LegOutcome)
	ObserveEvent     *lipapi.Event
	AuthorityPrepare func(context.Context) (usageEv lipapi.Event, authorityEv lipapi.Event, ok bool, err error)
	CancelCause      *lipapi.CancelCause
}

type attemptTerminalResult struct {
	Result       coreterm.Result
	Cancellation lipapi.CancelResult
}

func (a *attemptSession) makeSwallowedEvidence(facts recvTurnFacts, p *responsePipeline, committed bool, reason string, err error) attemptEvidence {
	var snapshot *coreterm.AccumulatorSnapshot
	var usage lipapi.Event
	var fallback lipapi.Event
	if p != nil {
		snap := p.accumulatorSnapshot()
		snapshot = &snap
		usage = p.operatorUsageForFinalize()
		fallback = p.billingEvidenceFallback()
	}
	tFacts := facts.terminalFacts()
	return attemptEvidence{
		ReleaseKind:    authorityapp.ReleaseKindSwallowed,
		Usage:          usage,
		TraceID:        facts.traceID,
		ALegID:         facts.aLegID,
		Snapshot:       snapshot,
		RecordReason:   reason,
		Err:            err,
		StartedAt:      a.accounting.requestStartedAt,
		StreamFallback: fallback,
		BillingState:   tFacts.billingState,
		BillingCallID:  tFacts.billingCallID,
		Committed:      committed,
	}
}

func (a *attemptSession) terminalizeSwallowed(ctx context.Context, facts recvTurnFacts, p *responsePipeline, committed bool, reason string, err error) {
	if a == nil {
		return
	}
	ev := a.makeSwallowedEvidence(facts, p, committed, reason, err)
	ev.Command = sdkterminal.CommandSwallowedAttempt
	ev.LegOutcome = billing.LegOutcomeSwallowed
	ev.ObsOutcome = response.OutcomeReplaced
	ev.RecordOutcome = lipapi.AttemptSwallowedFailure
	a.TerminalizeAttempt(ctx, IntentSwallowedFailure, ev)
}

func (a *attemptSession) terminalizeEarlyCancellation(ctx context.Context, facts recvTurnFacts, p *responsePipeline, committed bool, reason string, err error) {
	if a == nil {
		return
	}
	ev := a.makeSwallowedEvidence(facts, p, committed, reason, err)
	ev.Command = sdkterminal.CommandCancel
	ev.LegOutcome = billing.LegOutcomeCanceled
	ev.ObsOutcome = response.OutcomeCancelled
	ev.RecordOutcome = lipapi.AttemptCancelled
	ev.BillingReason = reason
	a.TerminalizeAttempt(ctx, IntentCancellation, ev)
}

func (a *attemptSession) TerminalizeAttempt(ctx context.Context, intent attemptTerminalIntent, evidence attemptEvidence) attemptTerminalResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil || a.terminal == nil {
		return attemptTerminalResult{Result: coreterm.Result{Err: sdkterminal.ErrInvalid}}
	}
	a.terminalizing.Store(true)
	defer a.terminalizing.Store(false)

	cmd := evidence.Command
	if cmd == "" || !cmd.AllowsScope(sdkterminal.ScopeAttempt) {
		cmd = mapIntentToCommand(intent)
	}

	res := a.terminal.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		if evidence.Snapshot != nil {
			return *evidence.Snapshot
		}
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}, func(cctx context.Context, out coreterm.Outcome) error {
		var errorsList []error
		innerStream := a.takeInner()
		var cancelRes lipapi.CancelResult
		contextsCanceled := errors.Is(evidence.Err, context.Canceled) || errors.Is(cctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
		isCancel, cause := deriveAttemptCancellation(intent, evidence, a.pendingCancelCause.Load(), contextsCanceled)

		// 2. Cancel/close when intent requires.
		if innerStream != nil && !a.streamDisposed {
			a.drainStreamUsageEvidence(innerStream)
			closeInner := func() error {
				return safety.Call(safety.BoundaryBackend, "backend_stream_close", func() error { return innerStream.Close() })
			}
			shouldCancel := (intent != IntentSuccess) || cause.Kind != ""
			var closeErr error
			if shouldCancel {
				if cause.Kind == "" {
					cause = lipapi.CancelCause{Kind: lipapi.CancelContextDone}
				}
				cancelRes, closeErr = leglifecycle.BoundedCancelAndClose(cctx, defaultAuthorityCleanupTimeout,
					func(cancelCtx context.Context) lipapi.CancelResult {
						var result lipapi.CancelResult
						_ = safety.Call(safety.BoundaryBackend, "backend_stream_cancel", func() error {
							result = innerStream.Cancel(cancelCtx, cause)
							return nil
						})
						return result
					},
					closeInner,
					a.forceClose,
				)
				if cancelRes.Err != nil && !errors.Is(cancelRes.Err, context.Canceled) {
					errorsList = append(errorsList, cancelRes.Err)
				}
				termObs := a.recordCancellationTelemetry(cause, cancelRes, innerStream, isCancel)
				a.logAttemptCanceled(cctx, evidence, termObs)
				a.drainStreamUsageEvidence(innerStream)
			} else {
				closeErr = closeInner()
			}
			if closeErr != nil {
				var pe *safety.PanicError
				if errors.As(closeErr, &pe) {
					// Isolate close panic, log at debug, do not fail terminal effect
					if a.finalStreamObs != nil && a.finalStreamObs.Log != nil {
						traceID := strings.TrimSpace(evidence.TraceID)
						if traceID == "" {
							traceID = strings.TrimSpace(a.traceID)
						}
						aLegID := strings.TrimSpace(evidence.ALegID)
						if aLegID == "" {
							aLegID = strings.TrimSpace(a.bleg.ALegID)
						}
						logCtx := diag.EnsureCallDiag(cctx, traceID, aLegID)
						attrs := diag.IsolatedCrashAttrs(logCtx, pe, diag.CrashAttrOpts{
							AttrOpts:   diag.AttrOpts{CallID: traceID, BLegID: a.bleg.BLegID},
							AttemptSeq: int(a.bleg.Seq),
						})
						attrs = diag.AppendIsolatedCrashStack(attrs, pe)
						a.finalStreamObs.Log.LogAttrs(logCtx, slog.LevelError, "isolated_panic_backend_stream_close", attrs...)
					}
				} else if !errors.Is(closeErr, context.Canceled) {
					errorsList = append(errorsList, fmt.Errorf("runtime: failed to close inner stream: %w", closeErr))
				}
			}
			a.drainStreamUsageEvidence(innerStream)
		} else if isCancel {
			termObs := a.recordCancellationTelemetry(cause, cancelRes, nil, isCancel)
			a.logAttemptCanceled(cctx, evidence, termObs)
		} else if innerStream != nil {
			a.drainStreamUsageEvidence(innerStream)
		}

		a.innerMu.Lock()
		a.cancelResult = cancelRes
		a.innerMu.Unlock()

		// 3. Pre-terminal preparation (AuthorityPrepare / token accounting).
		var preparedUsageEv lipapi.Event
		var preparedOK bool
		if evidence.AuthorityPrepare != nil {
			runGuarded("authority prepare", &errorsList, func() {
				usageEv, authorityEv, ok, err := evidence.AuthorityPrepare(cctx)
				if err != nil {
					errorsList = append(errorsList, err)
				}
				preparedUsageEv, preparedOK = usageEv, ok
				if ok {
					evidence.Usage = authorityEv
					if authorityEv.Kind == "" {
						evidence.Usage = usageEv
					}
				}
			})
		}

		// 4. Finish final-stream observation (Finish) - observe before finish inside winner.
		if a.finalStreamObs != nil {
			obsOutcome := response.OutcomeFailed
			if intent == IntentSuccess {
				obsOutcome = response.OutcomeSuccessReleased
			} else if intent == IntentCancellation || intent == IntentTimeout {
				obsOutcome = response.OutcomeCancelled
			} else if intent == IntentReplacement || intent == IntentSwallowedFailure {
				obsOutcome = response.OutcomeReplaced
			} else if cmd == sdkterminal.CommandClose {
				obsOutcome = response.OutcomeClosed
			}
			if evidence.ObsOutcome != "" {
				obsOutcome = evidence.ObsOutcome
			}
			if len(errorsList) > 0 {
				obsOutcome = response.OutcomeFailed
			}

			// If synthesized usage was generated, observe it before finishing.
			if preparedOK && preparedUsageEv.Kind != "" {
				runGuarded("finalStreamObs.Observe", &errorsList, func() {
					if err := extensions.RunFinalStreamObservationStage(cctx, a.finalStreamObs.Log, a.finalStreamObs.Metrics, a.finalStreamObs, preparedUsageEv, evidence.Committed); err != nil {
						errorsList = append(errorsList, err)
						obsOutcome = response.OutcomeFailed
					}
				})
			}

			if evidence.ObserveEvent != nil {
				runGuarded("finalStreamObs.Observe", &errorsList, func() {
					if err := extensions.RunFinalStreamObservationStage(cctx, a.finalStreamObs.Log, a.finalStreamObs.Metrics, a.finalStreamObs, *evidence.ObserveEvent, evidence.Committed); err != nil {
						errorsList = append(errorsList, err)
						obsOutcome = response.OutcomeFailed
					}
				})
			}
			runGuarded("finalStreamObs.Finish", &errorsList, func() {
				a.finalStreamObs.Finish(cctx, obsOutcome)
			})
		}

		// 5. Finalize/release attempt authority.
		if a.authority.control != nil && !a.authority.Settled() {
			runGuarded("authority finalize", &errorsList, func() {
				usage := a.usageOrAccumulated(evidence.Usage)
				switch intent {
				case IntentSuccess:
					_ = a.authority.Settle(cctx, authorityapp.SettlementKindFinal, usage, false)
					a.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindFinal, usage)
				case IntentCancellation:
					if a.authority.Settled() {
						a.authority.ReconcileAuthoritative(cctx, usage)
					} else if evidence.ReleaseKind != "" {
						a.authority.finalizeIncurredOrRelease(cctx, evidence.ReleaseKind, usage)
					} else {
						_ = a.authority.Settle(cctx, authorityapp.SettlementKindCancellation, usage, true)
					}
					a.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindCancellation, usage)
				case IntentTimeout:
					kind := evidence.ReleaseKind
					if kind == "" {
						kind = authorityapp.ReleaseKindLosing
					}
					a.authority.finalizeIncurredOrRelease(cctx, kind, usage)
					a.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindLosing, usage)
				case IntentSwallowedFailure, IntentPublicationDenied, IntentReplacement:
					kind := evidence.ReleaseKind
					if kind == "" {
						kind = authorityapp.ReleaseKindSwallowed
					}
					a.authority.finalizeIncurredOrRelease(cctx, kind, usage)
					a.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindPartial, usage)
				case IntentSurfacedFailure:
					_ = a.authority.Settle(cctx, authorityapp.SettlementKindPartial, usage, false)
					a.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindPartial, usage)
				case IntentParallelLoser:
					a.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindLosing, usage)
				default:
					kind := evidence.ReleaseKind
					if kind == "" {
						kind = mapIntentToReleaseKind(intent, a.authority.backendAttempted)
					}
					a.authority.finalizeIncurredOrRelease(cctx, kind, usage)
				}
			})
		}

		// 6. Emit attempt egress metering (emitBackendEgressMeteringFact).
		if a.emitBackendEgressFn != nil && a.authority.backendAttempted != nil && a.authority.backendAttempted.Load() {
			runGuarded("emitBackendEgress", &errorsList, func() {
				outcomeMeter := metering.AttemptOutcomeFailed
				switch intent {
				case IntentSuccess:
					outcomeMeter = metering.AttemptOutcomeWinner
				case IntentCancellation, IntentTimeout:
					outcomeMeter = metering.AttemptOutcomeCanceled
				case IntentParallelLoser:
					outcomeMeter = metering.AttemptOutcomeLoser
				}
				surfaced := metering.SurfacedNo
				if intent == IntentSuccess {
					surfaced = metering.SurfacedYes
				}
				usage := a.usageOrAccumulated(evidence.Usage)
				a.emitBackendEgressFn(cctx, a.bleg.BLegID, outcomeMeter, surfaced, usage)
			})
		}

		// 7. Release/end B-leg lifecycle (ReleaseBLeg / ALeg scope).
		if a.aScope != nil && a.bleg.BLegID != "" {
			runGuarded("ReleaseBLeg", &errorsList, func() {
				a.aScope.ReleaseBLeg(a.bleg.BLegID)
			})
		}

		// 8. Append billing leg evidence exactly once.
		if a.claimBillingLegRecord() {
			runGuarded("append billing leg", &errorsList, func() {
				started := evidence.StartedAt
				if started.IsZero() {
					started = a.accounting.requestStartedAt
				}
				var nowFn func() time.Time
				if a.now != nil {
					nowFn = a.now
				} else {
					nowFn = time.Now
				}
				finished := nowFn()
				if started.IsZero() {
					started = finished
				}
				legOutcome := evidence.LegOutcome
				if legOutcome == "" {
					legOutcome = mapCommandToLegOutcome(cmd)
				}
				if evidence.BillingLegFn != nil {
					evidence.BillingLegFn(cctx, started, finished, legOutcome)
					return
				}
				traceID := strings.TrimSpace(evidence.TraceID)
				if traceID == "" {
					traceID = strings.TrimSpace(a.traceID)
				}
				aLegID := strings.TrimSpace(evidence.ALegID)
				if aLegID == "" {
					aLegID = strings.TrimSpace(a.bleg.ALegID)
				}
				billingState := evidence.BillingState
				if billingState == nil {
					billingState = a.billingCallState
				}
				callID := evidence.BillingCallID
				if callID == "" {
					callID = a.billingCallID
				}
				if callID == "" && billingState != nil {
					callID = billingState.callID
				}
				blegID := strings.TrimSpace(a.bleg.BLegID)
				if blegID == "" {
					blegID = billingSyntheticBLegID(a.bleg.Seq)
				}
				if billingState != nil {
					if a.bleg.Seq > 0 {
						billingState.noteAllocatedBLeg(blegID, a.bleg.Seq)
					}
					billingState.noteLegTimes(started, finished)
				}
				surfaced := billing.SurfacedNo
				if cmd == sdkterminal.CommandNormalFinish || evidence.Committed {
					surfaced = billing.SurfacedYes
				}
				streamEv := a.augmentBillingUsage(evidence.StreamFallback, evidence.Usage)
				finalizeReason := "record_leg"
				if evidence.BillingReason != "" {
					finalizeReason = evidence.BillingReason
				} else if evidence.RecordReason != "" {
					finalizeReason = evidence.RecordReason
				}
				finalizeEv := streamEv
				if billingState != nil && a.finalizeBilling != nil {
					if ev, ok := billingState.finalizeOnce(cctx, execbackend.BillingFinalizationInput{
						TraceID: traceID,
						ALegID:  aLegID,
						BLegID:  strings.TrimSpace(a.bleg.BLegID),
						Backend: strings.TrimSpace(a.cand.Primary.Backend),
						Model:   strings.TrimSpace(a.cand.Primary.Model),
						Reason:  finalizeReason,
					}, func(cctx2 context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
						return a.finalizeBilling(cctx2, in)
					}); ok {
						finalizeEv = ev
					}
				}
				var opRef billing.VersionRef
				if a.operatorRateRef != nil {
					opRef = a.operatorRateRef(cctx, a.cand.Primary)
				}
				var workload billing.WorkloadIdentity
				if a.billingWorkload != nil {
					workload = a.billingWorkload(cctx, aLegID)
				}
				legRecord := billingLegRecord(billingLegDraft{
					callID:          callID,
					aLegID:          aLegID,
					bLegID:          a.bleg.BLegID,
					seq:             a.bleg.Seq,
					primary:         a.cand.Primary,
					startedAt:       started,
					finishedAt:      finished,
					command:         cmd,
					outcome:         legOutcome,
					surfaced:        surfaced,
					finalize:        finalizeEv,
					stream:          streamEv,
					operatorRateRef: opRef,
					workload:        workload,
				})
				if a.observeBillingLeg != nil {
					a.observeBillingLeg(cctx, legRecord)
				}
				if a.appendBillingLeg != nil && callID != "" {
					a.appendBillingLeg(cctx, callID, legRecord)
				}
				if a.observeBillingLeg == nil && a.appendBillingLeg == nil && a.appendBillingLegFn != nil {
					a.appendBillingLegFn(cctx, a.bleg, a.cand.Primary, started, finished, legOutcome)
				}
			})
		}

		// 9. Record attempt outcome/evidence (recordAttemptLogged).
		if a.recordAttemptLoggedFn != nil && a.bleg.BLegID != "" {
			runGuarded("recordAttemptLogged", &errorsList, func() {
				var outcomeLog lipapi.AttemptOutcome
				if errors.Is(evidence.Err, context.Canceled) || errors.Is(cctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
					outcomeLog = lipapi.AttemptCancelled
				} else {
					switch intent {
					case IntentSuccess:
						outcomeLog = lipapi.AttemptSuccess
					case IntentCancellation, IntentTimeout, IntentParallelLoser:
						outcomeLog = lipapi.AttemptCancelled
					case IntentSwallowedFailure, IntentPublicationDenied:
						outcomeLog = lipapi.AttemptSwallowedFailure
					default:
						outcomeLog = lipapi.AttemptSurfacedFailure
					}
				}
				if evidence.RecordOutcome != "" {
					outcomeLog = evidence.RecordOutcome
				}
				reasonLog := string(intent)
				if evidence.RecordReason != "" {
					reasonLog = evidence.RecordReason
				}
				a.recordAttemptLoggedFn(cctx, recordAttemptParams{
					ALegID:    evidence.ALegID,
					BLeg:      a.bleg,
					Cand:      a.cand,
					Outcome:   outcomeLog,
					Reason:    reasonLog,
					DetailErr: evidence.Err,
				}, diag.AttrOpts{CallID: evidence.TraceID, BLegID: a.bleg.BLegID})
			})
		}

		// 9. Discard attempt-local state.
		a.accounting = attemptAccountingTracker{}
		a.toolFinal = nil
		a.promptCacheSource = nil
		a.promptCacheController = nil

		if evidence.Err != nil && intent == IntentSurfacedFailure {
			errorsList = append(errorsList, evidence.Err)
		}

		if len(errorsList) > 0 {
			return errors.Join(errorsList...)
		}
		return nil
	})

	a.innerMu.Lock()
	finalCancel := a.cancelResult
	a.innerMu.Unlock()
	if finalCancel.Mode == "" {
		finalCancel.Mode = lipapi.CancelModeNone
	}
	return attemptTerminalResult{Result: res, Cancellation: finalCancel}
}

func mapIntentToCommand(intent attemptTerminalIntent) sdkterminal.Command {
	switch intent {
	case IntentSuccess:
		return sdkterminal.CommandNormalFinish
	case IntentSwallowedFailure, IntentPublicationDenied, IntentReplacement:
		return sdkterminal.CommandSwallowedAttempt
	case IntentSurfacedFailure:
		return sdkterminal.CommandPartialError
	case IntentCancellation:
		return sdkterminal.CommandCancel
	case IntentTimeout:
		return sdkterminal.CommandTimeout
	case IntentParallelLoser:
		return sdkterminal.CommandParallelLoser
	case IntentOpenReadinessFailure, IntentPreReturnAbort:
		return sdkterminal.CommandBackendOpenFailure
	default:
		return sdkterminal.CommandPartialError
	}
}

func mapIntentToReleaseKind(intent attemptTerminalIntent, backendAttempted *atomic.Bool) authorityapp.ReleaseKind {
	incurred := backendAttempted != nil && backendAttempted.Load()
	switch intent {
	case IntentSuccess:
		return authorityapp.ReleaseKindLosing
	case IntentSwallowedFailure, IntentPublicationDenied:
		return authorityapp.ReleaseKindSwallowed
	case IntentParallelLoser:
		return authorityapp.ReleaseKindLosing
	case IntentOpenReadinessFailure, IntentPreReturnAbort:
		if incurred {
			return authorityapp.ReleaseKindLosing
		}
		return authorityapp.ReleaseKindAdmissionFailure
	default:
		return authorityapp.ReleaseKindLosing
	}
}

func mapCommandToLegOutcome(cmd sdkterminal.Command) billing.LegOutcome {
	switch cmd {
	case sdkterminal.CommandNormalFinish:
		return billing.LegOutcomeWinner
	case sdkterminal.CommandSwallowedAttempt:
		return billing.LegOutcomeSwallowed
	case sdkterminal.CommandParallelLoser:
		return billing.LegOutcomeLoser
	case sdkterminal.CommandCancel, sdkterminal.CommandClose:
		return billing.LegOutcomeCanceled
	default:
		return billing.LegOutcomeFailed
	}
}
