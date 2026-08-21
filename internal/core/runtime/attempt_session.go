package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
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

func (a *attemptSession) cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	if a == nil {
		return lipapi.CancelResult{}
	}
	inner := a.loadInner()
	if inner == nil {
		return lipapi.CancelResult{}
	}
	return inner.Cancel(ctx, cause)
}

// cancelAndClose is an owner stream-teardown method — detaches and closes
// backend stream without settling authority/billing; callers pair with
// turnTerminal settlement for request-scoped flows.
func (a *attemptSession) cancelAndClose(ctx context.Context, cause lipapi.CancelCause, logger *slog.Logger) {
	if a == nil {
		return
	}
	if inner := a.takeInner(); inner != nil {
		cancelAndCloseInner(ctx, inner, cause, logger)
	}
}

func (a *attemptSession) releaseSwallowedAuthority(ctx context.Context, p *responsePipeline) {
	if a == nil || p == nil || a.authority.Settled() {
		return
	}
	a.authority.finalizeIncurredOrRelease(ctx, authorityapp.ReleaseKindSwallowed, p.operatorUsageForFinalize())
}

// attemptSession is the owner of one opened B-leg. Every field in this type
// has the same lifetime as the backend attempt and is discarded on replacement.
// The inner lock protects only the backend stream pointer; callers snapshot it
// and perform Cancel/Close without holding this lock.
type attemptSession struct {
	innerMu   sync.Mutex
	inner     lipapi.ManagedEventStream
	usageMu   sync.Mutex
	billingMu sync.Mutex

	internalUsageKeys  map[string]struct{}
	billingLegRecorded bool

	bleg      b2bua.BLegRecord
	cand      routing.AttemptCandidate
	authority authorityLifecycle
	terminal  *streamTerminal
	aScope    *leglifecycle.ALeg

	accounting            attemptAccountingTracker
	toolFinal             *toolCallAssembler
	promptCacheSource     promptcache.ObservationSource
	promptCacheController promptcache.Controller
	finalStreamObs        *extensions.FinalStreamObservationSession
	recordAttemptLoggedFn func(context.Context, recordAttemptParams, diag.AttrOpts)

	emitBackendEgressFn func(context.Context, string, metering.AttemptOutcome, metering.SurfacedState, lipapi.Event)
	appendBillingLegFn  func(context.Context, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, billing.LegOutcome)
	now                 func() time.Time
}

// claimBillingLegRecord is scoped to one attemptSession, which represents one
// B-leg. It prevents overlapping request/attempt terminal effects from
// appending more than one leg record while allowing a replacement attempt its
// own independent record. The claim is retained even if a downstream observer
// or append fails, matching the former stream-level dedupe mark-before-append
// behavior; call-closure retry remains independently owned by turnTerminal.
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

// rememberUsageEvidenceOnce keeps provider sideband dedupe scoped to the B-leg
// that produced it. A replacement attempt owns an independent key set.
func (a *attemptSession) rememberUsageEvidenceOnce(ev lipapi.Event) bool {
	if a == nil || ev.Accounting.DedupeKey == "" {
		return false
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	if a.internalUsageKeys == nil {
		a.internalUsageKeys = make(map[string]struct{})
	}
	key := ev.Accounting.DedupeKey
	if _, exists := a.internalUsageKeys[key]; exists {
		return false
	}
	a.internalUsageKeys[key] = struct{}{}
	return true
}

// attemptSessionInput keeps attempt construction explicit. It deliberately
// does not expose a generic state bag: each owner is initialized from one
// attemptOpenResult and its backend-specific controls.
type attemptSessionInput struct {
	inner     lipapi.ManagedEventStream
	bleg      b2bua.BLegRecord
	cand      routing.AttemptCandidate
	authority authorityLifecycle
	aScope    *leglifecycle.ALeg

	accounting            attemptAccountingTracker
	toolFinal             *toolCallAssembler
	promptCacheSource     promptcache.ObservationSource
	promptCacheController promptcache.Controller
	finalStreamObs        *extensions.FinalStreamObservationSession
	recordAttemptLoggedFn func(context.Context, recordAttemptParams, diag.AttrOpts)

	emitBackendEgressFn func(context.Context, string, metering.AttemptOutcome, metering.SurfacedState, lipapi.Event)
	appendBillingLegFn  func(context.Context, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, billing.LegOutcome)
	now                 func() time.Time
}

func newAttemptSession(in attemptSessionInput) *attemptSession {
	return &attemptSession{
		inner:     in.inner,
		bleg:      in.bleg,
		cand:      in.cand,
		authority: in.authority,
		terminal:  newStreamTerminal(sdkterminal.ScopeAttempt),
		aScope:    in.aScope,

		accounting:            in.accounting,
		toolFinal:             in.toolFinal,
		promptCacheSource:     in.promptCacheSource,
		promptCacheController: in.promptCacheController,
		finalStreamObs:        in.finalStreamObs,
		recordAttemptLoggedFn: in.recordAttemptLoggedFn,

		emitBackendEgressFn: in.emitBackendEgressFn,
		appendBillingLegFn:  in.appendBillingLegFn,
		now:                 in.now,
	}
}

func (a *attemptSession) recordAttemptLogged(ctx context.Context, p recordAttemptParams, attrs diag.AttrOpts) {
	if a != nil && a.recordAttemptLoggedFn != nil {
		a.recordAttemptLoggedFn(ctx, p, attrs)
	}
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

// detachStream detaches the backend stream at-most-once. It is the only
// allowed detached-close path outside TerminalizeAttempt/cancelAndClose.
func (a *attemptSession) detachStream() lipapi.ManagedEventStream {
	return a.takeInner()
}

// closeDetached detaches and closes the backend stream if present. It is safe
// for nil receiver and nil inner.
func (a *attemptSession) closeDetached(ctx context.Context) error {
	inner := a.takeInner()
	if inner == nil {
		return nil
	}
	return inner.Close()
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

func (s *attemptSlot) install(next *attemptSession) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
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

// readyAttempt is a private single-use capability that gates publication.
type readyAttempt struct {
	session  *attemptSession
	pending  pendingSelectionEffects
	consumed bool
	mu       sync.Mutex
}

func (r *readyAttempt) Consume() (*attemptSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.consumed {
		return nil, errors.New("runtime: readyAttempt already consumed")
	}
	r.consumed = true
	return r.session, nil
}

func (r *readyAttempt) Dispose(ctx context.Context, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.consumed {
		return
	}
	r.consumed = true
	if r.session != nil {
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
		r.session.TerminalizeAttempt(ctx, IntentPreReturnAbort, evidence)
	}
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
	Command       sdkterminal.Command
	ReleaseKind   authorityapp.ReleaseKind
	LegOutcome    billing.LegOutcome
	Usage         lipapi.Event
	Err           error
	ObsOutcome    response.StreamOutcome
	TraceID       string
	ALegID        string
	Snapshot      *coreterm.AccumulatorSnapshot
	RecordOutcome lipapi.AttemptOutcome
	RecordReason  string
	StartedAt     time.Time
	BillingLegFn  func(ctx context.Context, started, finished time.Time, outcome billing.LegOutcome)
}

type attemptTerminalResult struct {
	Result coreterm.Result
}

func (a *attemptSession) TerminalizeAttempt(ctx context.Context, intent attemptTerminalIntent, evidence attemptEvidence) attemptTerminalResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil || a.terminal == nil {
		return attemptTerminalResult{Result: coreterm.Result{Err: sdkterminal.ErrInvalid}}
	}

	cmd := evidence.Command
	if cmd == "" {
		cmd = mapIntentToCommand(intent)
	}

	res := a.terminal.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		if evidence.Snapshot != nil {
			return *evidence.Snapshot
		}
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}, func(cctx context.Context, out coreterm.Outcome) error {
		var errorsList []error

		// 1. Detach backend stream at most once (takeInner).
		innerStream := a.takeInner()

		// 2. Cancel/close when intent requires.
		if innerStream != nil {
			shouldCancel := (intent == IntentCancellation || intent == IntentTimeout || intent == IntentPreReturnAbort)
			if shouldCancel {
				cancelCtx, cancel := cleanupContext(cctx, defaultAuthorityCleanupTimeout)
				cause := lipapi.CancelCause{Kind: lipapi.CancelClientGone}
				if intent == IntentTimeout {
					cause = lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "timeout"}
				} else if evidence.Err != nil {
					cause = lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: evidence.Err.Error()}
				}
				_ = innerStream.Cancel(cancelCtx, cause)
				cancel()
			}
			if err := innerStream.Close(); err != nil {
				errorsList = append(errorsList, fmt.Errorf("runtime: failed to close inner stream: %w", err))
			}
		}

		// 3. Finish final-stream observation (Finish).
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

			func() {
				defer func() {
					if r := recover(); r != nil {
						errorsList = append(errorsList, fmt.Errorf("runtime: finalStreamObs.Finish panicked: %v", r))
					}
				}()
				a.finalStreamObs.Finish(cctx, obsOutcome)
			}()
		}

		// 4. Finalize/release attempt authority (finalizeIncurredOrRelease).
		if a.authority.control != nil && !a.authority.Settled() {
			func() {
				defer func() {
					if r := recover(); r != nil {
						errorsList = append(errorsList, fmt.Errorf("runtime: authority finalize panicked: %v", r))
					}
				}()
				usage := evidence.Usage
				if usage.Kind == "" {
					usage = emptyOperatorUsageShell()
				}
				switch intent {
				case IntentSuccess:
					_ = a.authority.Settle(cctx, authorityapp.SettlementKindFinal, usage, false)
					a.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindFinal, usage)
				case IntentCancellation, IntentTimeout:
					if a.authority.Settled() {
						a.authority.ReconcileAuthoritative(cctx, usage)
					} else {
						_ = a.authority.Settle(cctx, authorityapp.SettlementKindCancellation, usage, true)
					}
					a.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindCancellation, usage)
				case IntentSwallowedFailure, IntentPublicationDenied, IntentReplacement:
					kind := evidence.ReleaseKind
					if kind == "" {
						kind = authorityapp.ReleaseKindSwallowed
					}
					a.authority.finalizeIncurredOrRelease(cctx, kind, usage)
					a.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindPartial, usage)
				default:
					kind := evidence.ReleaseKind
					if kind == "" {
						kind = mapIntentToReleaseKind(intent, a.authority.backendAttempted)
					}
					a.authority.finalizeIncurredOrRelease(cctx, kind, usage)
				}
			}()
		}

		// 5. Emit attempt egress metering (emitBackendEgressMeteringFact).
		if a.emitBackendEgressFn != nil && a.authority.backendAttempted != nil && a.authority.backendAttempted.Load() {
			func() {
				defer func() {
					if r := recover(); r != nil {
						errorsList = append(errorsList, fmt.Errorf("runtime: emitBackendEgress panicked: %v", r))
					}
				}()
				outcomeMeter := metering.AttemptOutcomeFailed
				if intent == IntentSuccess {
					outcomeMeter = metering.AttemptOutcomeWinner
				} else if intent == IntentCancellation || intent == IntentTimeout {
					outcomeMeter = metering.AttemptOutcomeCanceled
				} else if intent == IntentParallelLoser {
					outcomeMeter = metering.AttemptOutcomeLoser
				}
				surfaced := metering.SurfacedNo
				if intent == IntentSuccess {
					surfaced = metering.SurfacedYes
				}
				usage := evidence.Usage
				if usage.Kind == "" {
					usage = emptyOperatorUsageShell()
				}
				a.emitBackendEgressFn(cctx, a.bleg.BLegID, outcomeMeter, surfaced, usage)
			}()
		}

		// 6. Release/end B-leg lifecycle (ReleaseBLeg / ALeg scope).
		if a.aScope != nil && a.bleg.BLegID != "" {
			func() {
				defer func() {
					if r := recover(); r != nil {
						errorsList = append(errorsList, fmt.Errorf("runtime: ReleaseBLeg panicked: %v", r))
					}
				}()
				a.aScope.ReleaseBLeg(a.bleg.BLegID)
			}()
		}

		// 7. Append billing leg evidence exactly once.
		if a.claimBillingLegRecord() && a.bleg.BLegID != "" {
			func() {
				defer func() {
					if r := recover(); r != nil {
						errorsList = append(errorsList, fmt.Errorf("runtime: append billing leg panicked: %v", r))
					}
				}()
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
				} else if a.appendBillingLegFn != nil {
					a.appendBillingLegFn(cctx, a.bleg, a.cand.Primary, started, finished, legOutcome)
				}
			}()
		}

		// 8. Record attempt outcome/evidence (recordAttemptLogged).
		if a.recordAttemptLoggedFn != nil && a.bleg.BLegID != "" {
			func() {
				defer func() {
					if r := recover(); r != nil {
						errorsList = append(errorsList, fmt.Errorf("runtime: recordAttemptLogged panicked: %v", r))
					}
				}()
				outcomeLog := lipapi.AttemptSuccess
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
			}()
		}

		// 9. Discard attempt-local state.
		a.accounting = attemptAccountingTracker{}
		a.toolFinal = nil
		a.promptCacheSource = nil
		a.promptCacheController = nil

		if len(errorsList) > 0 {
			return errors.Join(errorsList...)
		}
		return nil
	})

	return attemptTerminalResult{Result: res}
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
