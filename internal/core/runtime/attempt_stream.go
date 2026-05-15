package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	accountingledger "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/ledger"
	accountingobs "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/observability"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	coretraffic "github.com/matdev83/go-llm-interactive-proxy/internal/core/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
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
	isContextLimitExhaustion bool

	innerMu            sync.Mutex
	inner              lipapi.ManagedEventStream
	bleg               b2bua.BLegRecord
	cand               routing.AttemptCandidate
	committed          bool
	finished           bool
	endOnce            sync.Once
	affinityKey        affinity.Key
	affinitySet        bool
	affinityCommitOnce sync.Once

	// recvViews / routePrefs preserve [execctx] values from prepare so Recv callers can pass a bare HTTP context.
	recvViews   execctx.Views
	recvViewsOK bool
	routePrefs  []string

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
	aScope                   *leglifecycle.ALeg
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
		s.executor.Log.DebugContext(ctx, "retry_recv inner stream close",
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
	if s == nil {
		return
	}
	s.endOnce.Do(func() {
		if s.aScope != nil {
			s.aScope.End()
		}
	})
}

// recvHookMeta returns identifiers for response-path hooks after B-leg allocation.
func (s *retryRecvStream) recvExecContext(parent context.Context) context.Context {
	ctx := diag.EnsureCallDiag(parent, s.traceID, s.aLegID)
	if s.recvViewsOK {
		ctx = execctx.WithViews(ctx, s.recvViews)
	}
	if s.secureTurnOK {
		ctx = execctx.WithSecureSessionTurn(ctx, s.secureTurn)
	}
	if len(s.routePrefs) > 0 {
		ctx = execctx.WithRouteCandidatePreferences(ctx, s.routePrefs)
	}
	if s.executor != nil && s.executor.Log != nil {
		ctx = hooks.WithDiagnosticsLogger(ctx, s.executor.Log)
	}
	return ctx
}

func (s *retryRecvStream) recvHookMeta() (sdk.PartMeta, sdk.ToolMeta) {
	pm := sdk.PartMeta{
		TraceID:    s.traceID,
		ALegID:     s.aLegID,
		BLegID:     s.bleg.BLegID,
		AttemptSeq: s.bleg.Seq,
	}
	tm := sdk.ToolMeta{
		TraceID:    s.traceID,
		ALegID:     s.aLegID,
		BLegID:     s.bleg.BLegID,
		AttemptSeq: s.bleg.Seq,
	}
	return pm, tm
}

func (s *retryRecvStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s == nil {
		return lipapi.Event{}, errNilRetryRecvStream
	}
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if s.finished {
		return lipapi.Event{}, io.EOF
	}
	if len(s.recoverDrain) > 0 {
		ev := s.recoverDrain[0]
		s.recoverDrain = s.recoverDrain[1:]
		if ev.Kind == lipapi.EventResponseFinished && !s.tokenAccountingFinalized {
			if usageEv, ok, err := s.finalizeTokenAccounting(ctx, ev); err != nil {
				return lipapi.Event{}, err
			} else if ok {
				s.recoverDrain = append([]lipapi.Event{ev}, s.recoverDrain...)
				s.tokenAccountingFinalized = true
				return s.emitSynthesizedUsage(ctx, usageEv)
			}
		}
		if ev.Kind == lipapi.EventResponseFinished {
			s.finished = true
			s.finishALegScope()
		}
		return ev, nil
	}
	ctx = s.recvExecContext(ctx)
	for {
		if ev, ok := s.popGateDrainHead(); ok {
			ev = s.emitGateDrained(ctx, ev)
			s.markOutputCommitted(ev)
			s.accounting.observeClientEvent(s.now(), ev)
			s.rememberClientEvent(ev)
			if err := s.beforeEmitClientFacing(ctx, ev); err != nil {
				if s.executor != nil && s.executor.SecureSessionRecordingMandatory {
					return lipapi.Event{}, err
				}
				if s.executor != nil && s.executor.Log != nil {
					s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", err)
				}
			}
			s.commitAffinityIfOutput(ctx, ev)
			pm, _ := s.recvHookMeta()
			s.emitTrafficPTC(ctx, ev, pm)
			return ev, nil
		}
		var inner lipapi.ManagedEventStream
		for {
			inner = s.loadInner()
			if inner != nil {
				break
			}
			opened, err := s.tryReplacementIteration(ctx)
			if err != nil {
				return lipapi.Event{}, err
			}
			if !opened {
				return stream.DefaultKeepaliveEvent(), nil
			}
		}
		recvCtx := ctx
		var cancelRecv context.CancelFunc = func() {}
		ttftDeadline := ttftContextDeadline{}
		if !s.committed && s.ttft != nil {
			recvCtx, cancelRecv, ttftDeadline = s.ttft.scopedContext(ctx, s.now(), s.cand.Key, s.cand.Primary.TTFTTimeout)
		}
		recvCtx, cancelRecv, idleDeadline := s.scopedIdleContext(recvCtx, cancelRecv, s.now())
		ev, err := safety.CallValue(safety.BoundaryBackend, "backend_recv", func() (lipapi.Event, error) {
			return inner.Recv(recvCtx)
		})
		cancelRecv()
		if err != nil {
			var pe *safety.PanicError
			if errors.As(err, &pe) {
				err = mapStreamPanic(pe, s.committed)
			}
		}
		if err != nil && s.aScope != nil {
			if scopeErr := s.aScope.Err(); errors.Is(scopeErr, leglifecycle.ErrALegCanceled) {
				s.executor.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    s.aLegID,
					BLeg:      s.bleg,
					Cand:      s.cand,
					Outcome:   lipapi.AttemptCancelled,
					Reason:    "a-leg canceled",
					DetailErr: scopeErr,
				}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
				_ = s.takeAndNilInner()
				s.persistCancellationBilling(ctx, "a-leg canceled")
				s.finished = true
				s.finishALegScope()
				return lipapi.Event{}, scopeErr
			}
		}
		if err == nil {
			recvAt := s.now()
			s.accounting.observeBackendEvent(recvAt, ev)
			s.accounting.observeUsage(ev)
			pm, tm := s.recvHookMeta()
			s.emitTrafficBTP(ctx, ev, pm)
			ev = s.enrichUsageCost(ev)
			s.emitUsage(ctx, ev)
			if te, ok := lipapi.ToolEventFromEvent(ev); ok {
				if err := s.applyToolPolicies(ctx, te, tm); err != nil {
					s.executor.recordAttemptLogged(ctx, recordAttemptParams{
						ALegID:    s.aLegID,
						BLeg:      s.bleg,
						Cand:      s.cand,
						Outcome:   lipapi.AttemptSurfacedFailure,
						Reason:    attemptReasonDetail(err),
						DetailErr: err,
					}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
					return lipapi.Event{}, err
				}
				res := s.bus.ApplyToolReactors(ctx, te, tm)
				if res.Err != nil {
					return lipapi.Event{}, res.Err
				}
				if !res.Emit {
					continue
				}
				if res.Event.Kind != "" {
					ev = lipapi.MergeToolEventInto(ev, res.Event)
				}
			}
			evp := ev
			if herr := s.bus.RunResponsePartHooks(ctx, &evp, pm); herr != nil {
				return lipapi.Event{}, herr
			}
			ev = evp
			gates := s.completionGatesFromContext(ctx)
			if len(gates) > 0 {
				out, gerr := s.completionGatedEmit(ctx, gates, ev)
				if errors.Is(gerr, errGateContinueInner) {
					continue
				}
				if gerr != nil {
					return lipapi.Event{}, gerr
				}
				out = s.emitGateDrained(ctx, out)
				s.markOutputCommitted(out)
				s.accounting.observeClientEvent(s.now(), out)
				if s.recoverPolicy != nil {
					s.recoverPolicy.ObserveClientEvent(out, s.now())
				}
				if err := s.beforeEmitClientFacing(ctx, out); err != nil {
					if s.executor != nil && s.executor.SecureSessionRecordingMandatory {
						return lipapi.Event{}, err
					}
					if s.executor != nil && s.executor.Log != nil {
						s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", err)
					}
				}
				s.commitAffinityIfOutput(ctx, out)
				s.emitTrafficPTC(ctx, out, pm)
				return out, nil
			}
			if lipapi.OutputCommitted(ev) {
				s.markOutputCommitted(ev)
			}
			s.accounting.observeClientEvent(s.now(), ev)
			if s.recoverPolicy != nil {
				s.recoverPolicy.ObserveClientEvent(ev, s.now())
			}
			if ev.Kind == lipapi.EventResponseFinished {
				s.rememberClientEvent(ev)
				if usageEv, ok, err := s.finalizeTokenAccounting(ctx, ev); err != nil {
					return lipapi.Event{}, err
				} else if ok {
					s.recoverDrain = append([]lipapi.Event{ev}, s.recoverDrain...)
					s.tokenAccountingFinalized = true
					return s.emitSynthesizedUsage(ctx, usageEv)
				}
				s.executor.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:  s.aLegID,
					BLeg:    s.bleg,
					Cand:    s.cand,
					Outcome: lipapi.AttemptSuccess,
				}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
				s.finished = true
				s.finishALegScope()
			} else {
				s.rememberClientEvent(ev)
			}
			if err := s.beforeEmitClientFacing(ctx, ev); err != nil {
				if s.executor != nil && s.executor.SecureSessionRecordingMandatory {
					return lipapi.Event{}, err
				}
				if s.executor != nil && s.executor.Log != nil {
					s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", err)
				}
			}
			s.commitAffinityIfOutput(ctx, ev)
			s.emitTrafficPTC(ctx, ev, pm)
			return ev, nil
		}
		if errors.Is(err, io.EOF) {
			s.recordPartialTokenAccounting(ctx, "stream ended without response_finished", io.EOF)
			// Truncated upstream: never run completion gates on a partial buffer (replace gates could
			// synthesize response_finished and mask the failure).
			gates := s.completionGatesFromContext(ctx)
			if len(gates) > 0 && !s.gateLive && len(s.gateBuf) > 0 && !extensions.StreamFinished(s.gateBuf) {
				s.gateBuf = nil
			}
			if s.recoverPolicy != nil {
				dec := s.recoverPolicy.DecideEOF(io.EOF, s.now())
				if dec.Kind == streamrecovery.DecisionFinishPostOutput {
					s.executor.recordAttemptLogged(ctx, recordAttemptParams{
						ALegID:    s.aLegID,
						BLeg:      s.bleg,
						Cand:      s.cand,
						Outcome:   lipapi.AttemptSuccess,
						Reason:    dec.Reason,
						DetailErr: io.EOF,
					}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
					if dec.Warning.Kind != "" {
						s.recoverDrain = append(s.recoverDrain, dec.Warning)
					}
					s.recoverDrain = append(s.recoverDrain, dec.Finish)
					ev := s.recoverDrain[0]
					s.recoverDrain = s.recoverDrain[1:]
					if ev.Kind == lipapi.EventResponseFinished {
						s.finished = true
						s.finishALegScope()
					}
					return ev, nil
				}
			}
			if !s.finished {
				s.executor.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    s.aLegID,
					BLeg:      s.bleg,
					Cand:      s.cand,
					Outcome:   lipapi.AttemptSurfacedFailure,
					Reason:    "stream ended without response_finished",
					DetailErr: io.EOF,
				}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
			}
			s.finished = true
			s.finishALegScope()
			return lipapi.Event{}, io.EOF
		}
		if idleDeadline.expired(recvCtx, err) {
			dec := s.recoverPolicy.DecideIdle(s.now())
			if dec.Kind == streamrecovery.DecisionFinishPostOutput {
				s.executor.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    s.aLegID,
					BLeg:      s.bleg,
					Cand:      s.cand,
					Outcome:   lipapi.AttemptSuccess,
					Reason:    dec.Reason,
					DetailErr: context.DeadlineExceeded,
				}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
				if c := s.takeAndNilInner(); c != nil {
					s.cancelAndCloseInner(ctx, c, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone, Detail: dec.Reason})
				}
				if dec.Warning.Kind != "" {
					s.recoverDrain = append(s.recoverDrain, dec.Warning)
				}
				s.recoverDrain = append(s.recoverDrain, dec.Finish)
				ev := s.recoverDrain[0]
				s.recoverDrain = s.recoverDrain[1:]
				if ev.Kind == lipapi.EventResponseFinished {
					s.finished = true
					s.finishALegScope()
				}
				return ev, nil
			}
			if dec.Kind == streamrecovery.DecisionRecoverPreOutput {
				s.executor.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    s.aLegID,
					BLeg:      s.bleg,
					Cand:      s.cand,
					Outcome:   lipapi.AttemptSwallowedFailure,
					Reason:    dec.Reason,
					DetailErr: dec.Err,
				}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
				if c := s.takeAndNilInner(); c != nil {
					s.cancelAndCloseInner(ctx, c, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone, Detail: dec.Reason})
				}
				s.excluded[s.cand.Key] = struct{}{}
				continue
			}
		}
		if ttftDeadline.expired(recvCtx, err) && !s.committed {
			ttftScope := ttftDeadline.scope
			if ttftScope == ttftTimeoutLeaf {
				tf := ttftFailure(ttftScope, s.cand.Key)
				s.executor.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    s.aLegID,
					BLeg:      s.bleg,
					Cand:      s.cand,
					Outcome:   lipapi.AttemptSwallowedFailure,
					Reason:    ttftAttemptReason(ttftScope),
					DetailErr: tf,
				}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
				if c := s.takeAndNilInner(); c != nil {
					if cerr := c.Close(); cerr != nil && s.executor != nil && s.executor.Log != nil {
						s.executor.Log.DebugContext(ctx, "retry_recv inner stream close",
							"reason", "leaf_ttft_timeout",
							"error", cerr,
						)
					}
				}
				s.excluded[s.cand.Key] = struct{}{}
				continue
			}
			tf := ttftFailure(ttftScope, s.cand.Key)
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.aLegID,
				BLeg:      s.bleg,
				Cand:      s.cand,
				Outcome:   lipapi.AttemptSurfacedFailure,
				Reason:    ttftAttemptReason(ttftScope),
				DetailErr: tf,
			}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
			if c := s.takeAndNilInner(); c != nil {
				if cerr := c.Close(); cerr != nil && s.executor != nil && s.executor.Log != nil {
					s.executor.Log.DebugContext(ctx, "retry_recv inner stream close",
						"reason", "global_ttft_timeout",
						"error", cerr,
					)
				}
			}
			s.finished = true
			s.finishALegScope()
			return lipapi.Event{}, lipapi.ErrTTFTTimeout
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			reason := cancellationAttemptReason(ctx, err)
			if s.executor != nil && s.executor.Log != nil && err != nil {
				s.executor.Log.DebugContext(ctx, "retry_recv context cancellation",
					"reason", reason,
					"recv_error_detail", diag.TruncErrDetail(err, attemptReasonMaxRunes),
				)
			}
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.aLegID,
				BLeg:      s.bleg,
				Cand:      s.cand,
				Outcome:   lipapi.AttemptCancelled,
				Reason:    reason,
				DetailErr: err,
			}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
			if c := s.takeAndNilInner(); c != nil {
				if s.aScope != nil {
					_ = s.aScope.Cancel(ctx, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone})
				} else {
					s.cancelAndCloseInner(ctx, c, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone})
				}
			}
			s.persistCancellationBilling(ctx, reason)
			s.finished = true
			s.finishALegScope()
			return lipapi.Event{}, err
		}
		if s.committed || !lipapi.IsRecoverablePreOutput(err) {
			surfErr := err
			if s.committed && lipapi.IsRecoverablePreOutput(err) {
				surfErr = &lipapi.UpstreamFailure{
					Phase:        lipapi.PhasePostOutput,
					Recoverable:  false,
					Reason:       attemptReasonDetail(err),
					CandidateKey: s.cand.Key,
				}
			}
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.aLegID,
				BLeg:      s.bleg,
				Cand:      s.cand,
				Outcome:   lipapi.AttemptSurfacedFailure,
				Reason:    attemptReasonDetail(surfErr),
				DetailErr: surfErr,
			}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
			s.recordPartialTokenAccounting(ctx, attemptReasonDetail(surfErr), surfErr)
			return lipapi.Event{}, surfErr
		}
		var log *slog.Logger
		if s.executor != nil {
			log = s.executor.Log
		}
		diag.LogDecision(ctx, log, "recoverable_pre_output_swallowed",
			diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID},
			slog.String("candidate_key", s.cand.Key),
			slog.String("phase", "recv"),
		)
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:    s.aLegID,
			BLeg:      s.bleg,
			Cand:      s.cand,
			Outcome:   lipapi.AttemptSwallowedFailure,
			Reason:    "recoverable pre-output (recv)",
			DetailErr: err,
		}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
		s.recordPartialTokenAccounting(ctx, "recoverable pre-output (recv)", err)
		if c := s.takeAndNilInner(); c != nil {
			if cerr := c.Close(); cerr != nil && s.executor != nil && s.executor.Log != nil {
				s.executor.Log.DebugContext(ctx, "retry_recv inner stream close",
					"reason", "recoverable_pre_output",
					"error", cerr,
				)
			}
		}
		s.excluded[s.cand.Key] = struct{}{}
	}
}

func (s *retryRecvStream) commitAffinityIfOutput(ctx context.Context, ev lipapi.Event) {
	if lipapi.OutputCommitted(ev) {
		s.commitAffinity(ctx, "output_committed")
	}
}

func (s *retryRecvStream) markOutputCommitted(ev lipapi.Event) {
	if lipapi.OutputCommitted(ev) {
		s.committed = true
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

func (s *retryRecvStream) persistCancellationBilling(ctx context.Context, reason string) {
	if s == nil || s.accounting.usageObserved {
		return
	}
	if s.finalizeBillingAfterCancel(ctx, reason) {
		return
	}
	s.recordCancellationBillingMarker(ctx, reason)
}

func (s *retryRecvStream) recordCancellationBillingMarker(ctx context.Context, reason string) {
	if s == nil || s.accounting.usageObserved {
		return
	}
	raw, _ := json.Marshal(map[string]any{
		"billing_basis": "estimated_after_a_leg_cancellation",
		"reason":        strings.TrimSpace(reason),
	})
	ev := lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		CostSource:   accounting.CostSourceEstimated,
		RawUsageJSON: string(raw),
	}
	persistCtx := context.WithoutCancel(ctx)
	if err := s.beforeEmitClientFacing(persistCtx, ev); err != nil && s.executor != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(persistCtx, "secure_session cancellation billing marker", "error", err)
	}
	s.emitUsage(persistCtx, ev)
}

func (s *retryRecvStream) finalizeBillingAfterCancel(ctx context.Context, reason string) bool {
	if s == nil || s.executor == nil || s.executor.Backends == nil {
		return false
	}
	be, ok := s.executor.Backends[strings.TrimSpace(s.cand.Primary.Backend)]
	if !ok || be.FinalizeBilling == nil {
		return false
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	ev, err := be.FinalizeBilling(persistCtx, execbackend.BillingFinalizationInput{
		TraceID: strings.TrimSpace(s.traceID),
		ALegID:  strings.TrimSpace(s.aLegID),
		BLegID:  strings.TrimSpace(s.bleg.BLegID),
		Backend: strings.TrimSpace(s.cand.Primary.Backend),
		Model:   strings.TrimSpace(s.cand.Primary.Model),
		Reason:  strings.TrimSpace(reason),
	})
	if err != nil {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(persistCtx, "billing finalizer after cancellation", "error", err)
		}
		return false
	}
	if ev.Kind != lipapi.EventUsageDelta {
		return false
	}
	s.accounting.observeUsage(ev)
	if recErr := s.beforeEmitClientFacing(persistCtx, ev); recErr != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(persistCtx, "secure_session billing finalizer marker", "error", recErr)
	}
	s.emitUsage(persistCtx, ev)
	return true
}

func (s *retryRecvStream) enrichUsageCost(ev lipapi.Event) lipapi.Event {
	if s == nil || s.executor == nil || ev.Kind != lipapi.EventUsageDelta || ev.CostNanoUnits > 0 {
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
	return ev
}

func (s *retryRecvStream) emitTrafficBTP(ctx context.Context, ev lipapi.Event, pm sdk.PartMeta) {
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
			s.executor.Log.DebugContext(ctx, "retry_recv traffic marshal skipped", "leg", sdktraffic.LegBTP, "error", err)
		}
		return
	}
	meta := sdktraffic.CaptureMeta{
		TraceID:    pm.TraceID,
		ALegID:     pm.ALegID,
		BLegID:     pm.BLegID,
		AttemptSeq: pm.AttemptSeq,
		BackendID:  strings.TrimSpace(s.cand.Primary.Backend),
	}
	bundle.Emit(
		ctx,
		sdktraffic.LegBTP,
		meta,
		"lip/canonical+json",
		"application/json",
		b,
	)
}

func (s *retryRecvStream) emitTrafficPTC(ctx context.Context, ev lipapi.Event, pm sdk.PartMeta) {
	if ev.Kind == lipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode {
		return
	}
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
			s.executor.Log.DebugContext(ctx, "retry_recv traffic marshal skipped", "leg", sdktraffic.LegPTC, "error", err)
		}
		return
	}
	meta := sdktraffic.CaptureMeta{
		TraceID:    pm.TraceID,
		ALegID:     pm.ALegID,
		BLegID:     pm.BLegID,
		AttemptSeq: pm.AttemptSeq,
		BackendID:  strings.TrimSpace(s.cand.Primary.Backend),
	}
	bundle.Emit(
		ctx,
		sdktraffic.LegPTC,
		meta,
		"lip/canonical+json",
		"application/json",
		b,
	)
}

// cancellationAttemptReason returns a low-cardinality bucket for attempt records when
// recv ends due to context cancellation or deadline.
func cancellationAttemptReason(ctx context.Context, recvErr error) string {
	if recvErr != nil {
		if errors.Is(recvErr, context.Canceled) {
			return "context canceled"
		}
		if errors.Is(recvErr, context.DeadlineExceeded) {
			return "context deadline exceeded"
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.Canceled) {
			return "context canceled"
		}
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return "context deadline exceeded"
		}
		return "context done"
	}
	return "cancelled"
}

// tryReplacementIteration performs one planning + open attempt for recv-phase failover.
// It returns opened=true when s.inner is ready, opened=false when the caller should emit
// a keepalive (Req 5.5) and invoke Recv again, or a non-nil error when the replacement path is exhausted.
func (s *retryRecvStream) tryReplacementIteration(ctx context.Context) (opened bool, err error) {
	ctx = diag.EnsureCallDiag(ctx, s.traceID, s.aLegID)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.committed && s.secureRecvRecordingHardStop && s.executor != nil && s.executor.SecureSessionRecordingMandatory {
		return false, &lipapi.UpstreamFailure{
			Phase:        lipapi.PhasePostOutput,
			Recoverable:  false,
			Reason:       "secure session mandatory recorder failure after committed output",
			CandidateKey: strings.TrimSpace(s.cand.Key),
		}
	}
	if s.aScope != nil {
		if err := s.aScope.Err(); err != nil {
			return false, err
		}
	}
	out, err := s.executor.tryPlanOpenOnce(attemptOpenParams{
		ctx:                      ctx,
		bus:                      s.bus,
		traceID:                  s.traceID,
		aLegID:                   s.aLegID,
		baseline:                 s.baseline,
		sel:                      s.sel,
		requestSize:              s.requestSize,
		session:                  s.session,
		excluded:                 s.excluded,
		rng:                      s.rng,
		budget:                   s.budget,
		ttft:                     s.ttft,
		isRetryPath:              true,
		lastReject:               &s.lastHardReject,
		affinityKey:              s.affinityKey,
		affinitySet:              s.affinitySet,
		isContextLimitExhaustion: &s.isContextLimitExhaustion,
	})
	if err != nil {
		return false, err
	}
	if !out.opened {
		return false, nil
	}
	if s.aScope != nil {
		if err := s.aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{
			ID:      out.bleg.BLegID,
			Attempt: lifecycleAttempt(out.stream),
		}); err != nil {
			return false, err
		}
	}
	s.storeInner(out.stream)
	s.bleg = out.bleg
	s.cand = out.cand
	s.seenEvents = nil
	s.visibleText.Reset()
	s.tokenAccountingFinalized = false
	s.accounting = newAttemptAccountingTracker(s.now())
	if s.executor != nil {
		s.recoverPolicy = streamrecovery.NewPolicy(s.executor.StreamRecovery, s.now())
	}
	return true, nil
}

func (s *retryRecvStream) Close() error {
	if s == nil {
		return nil
	}
	c := s.takeAndNilInner()
	if c == nil {
		s.finishALegScope()
		return nil
	}
	if !s.finished {
		if s.aScope != nil {
			_ = s.aScope.Cancel(context.Background(), leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
			s.finishALegScope()
			return nil
		}
		_ = c.Cancel(context.Background(), leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
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
		Session:    meta.Session,
		Workspace:  meta.Workspace,
	}
	if v, ok := execctx.FromContext(ctx); ok {
		polMeta.Principal = v.Principal
		polMeta.Session = v.Session
		polMeta.Workspace = v.Workspace
	}
	return extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
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
}

func (s *retryRecvStream) emitUsage(ctx context.Context, ev lipapi.Event) {
	if s == nil || s.executor == nil || s.executor.RuntimeSnapshot == nil || ev.Kind != lipapi.EventUsageDelta {
		return
	}
	obs := s.executor.RuntimeSnapshot.UsageObserver()
	if obs == nil {
		return
	}
	principalID := ""
	if v, ok := execctx.FromContext(ctx); ok {
		principalID = v.Principal.ID
	}
	model := ""
	if s.cand.Primary.Model != "" {
		model = s.cand.Primary.Model
	}
	if err := obs.OnUsage(ctx, usage.Event{
		TraceID:          strings.TrimSpace(s.traceID),
		ALegID:           strings.TrimSpace(s.aLegID),
		BLegID:           strings.TrimSpace(s.bleg.BLegID),
		PrincipalID:      strings.TrimSpace(principalID),
		SessionID:        strings.TrimSpace(s.baseline.Session.CorrelationID()),
		AttemptSeq:       int(s.bleg.Seq),
		BackendID:        strings.TrimSpace(s.cand.Primary.Backend),
		Model:            strings.TrimSpace(model),
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens:  ev.ReasoningTokens,
		TotalTokens:      ev.TotalTokens,
		CostNanoUnits:    ev.CostNanoUnits,
		Currency:         strings.TrimSpace(ev.Currency),
		CostSource:       strings.TrimSpace(ev.CostSource),
		RawUsageJSON:     strings.TrimSpace(ev.RawUsageJSON),
		RecordedAt:       s.executor.now(),
	}); err != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "usage observer error", "error", err)
	}
}

func (s *retryRecvStream) emitSynthesizedUsage(ctx context.Context, ev lipapi.Event) (lipapi.Event, error) {
	s.accounting.observeClientEvent(s.now(), ev)
	if s.recoverPolicy != nil {
		s.recoverPolicy.ObserveClientEvent(ev, s.now())
	}
	s.rememberClientEvent(ev)
	if err := s.beforeEmitClientFacing(ctx, ev); err != nil {
		if s.executor != nil && s.executor.SecureSessionRecordingMandatory {
			return lipapi.Event{}, err
		}
		if s.executor != nil && s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", err)
		}
	}
	pm, _ := s.recvHookMeta()
	s.emitTrafficPTC(ctx, ev, pm)
	s.emitUsage(ctx, ev)
	return ev, nil
}

func (s *retryRecvStream) finalizeTokenAccounting(ctx context.Context, finish lipapi.Event) (lipapi.Event, bool, error) {
	if s == nil || s.executor == nil || s.executor.StreamUsage == nil {
		return lipapi.Event{}, false, nil
	}
	started := s.now()
	events := append([]lipapi.Event(nil), s.seenEvents...)
	events = append(events, finish)
	result, err := s.executor.StreamUsage.Reconstruct(ctx, accountingstream.Input{
		Backend:    strings.TrimSpace(s.cand.Primary.Backend),
		Model:      strings.TrimSpace(s.cand.Primary.Model),
		Call:       s.baseline,
		OutputText: s.visibleText.String(),
		Events:     events,
	})
	if err != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "token accounting stream reconstruction", "error", err)
	}
	if len(result.Events) == 0 {
		return lipapi.Event{}, false, nil
	}
	usageEv := mergeUsageEventsForClient(result.Events, tokenAccountingHasProviderUsage(s.seenEvents))
	duration := s.now().Sub(started)
	if duration <= 0 {
		duration = time.Nanosecond
	}
	if err := s.recordTokenAccountingLedger(ctx, result.Events, "", "", duration); err != nil {
		if s.executor.LedgerWriteRequired {
			return lipapi.Event{}, false, err
		}
	}
	return usageEv, true, nil
}

func mergeUsageEvents(events []lipapi.Event) lipapi.Event {
	return mergeUsageEventsForClient(events, false)
}

func mergeUsageEventsForClient(events []lipapi.Event, skipProviderBillable bool) lipapi.Event {
	out := lipapi.Event{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{}}
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		if len(ev.UsageScopes) > 0 {
			for _, scope := range ev.UsageScopes {
				if skipProviderBillable && scope.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
					continue
				}
				out.UsageScopes = append(out.UsageScopes, scope)
			}
			continue
		}
		if skipProviderBillable && ev.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
			continue
		}
		out.UsageScopes = append(out.UsageScopes, lipapi.ScopedUsageDelta{
			InputTokens:      ev.InputTokens,
			OutputTokens:     ev.OutputTokens,
			CacheReadTokens:  ev.CacheReadTokens,
			CacheWriteTokens: ev.CacheWriteTokens,
			ReasoningTokens:  ev.ReasoningTokens,
			TotalTokens:      ev.TotalTokens,
			Accounting:       ev.Accounting,
		})
	}
	if len(out.UsageScopes) > 0 {
		first := out.UsageScopes[0]
		out.InputTokens = first.InputTokens
		out.OutputTokens = first.OutputTokens
		out.CacheReadTokens = first.CacheReadTokens
		out.CacheWriteTokens = first.CacheWriteTokens
		out.ReasoningTokens = first.ReasoningTokens
		out.TotalTokens = first.TotalTokens
		out.Accounting = first.Accounting
	}
	return out
}

func tokenAccountingHasProviderUsage(events []lipapi.Event) bool {
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		if ev.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
			return true
		}
		for _, scope := range ev.UsageScopes {
			if scope.Accounting.Plane == lipapi.UsagePlaneProviderBillable {
				return true
			}
		}
	}
	return false
}

func (s *retryRecvStream) recordTokenAccountingLedger(ctx context.Context, events []lipapi.Event, unavailableReason, failureReason string, duration time.Duration) error {
	if s == nil || s.executor == nil || s.executor.Ledger == nil {
		return nil
	}
	for _, ev := range events {
		if ev.Kind != lipapi.EventUsageDelta {
			continue
		}
		scopes := ev.UsageScopes
		if len(scopes) == 0 {
			scopes = []lipapi.ScopedUsageDelta{{
				InputTokens:      ev.InputTokens,
				OutputTokens:     ev.OutputTokens,
				CacheReadTokens:  ev.CacheReadTokens,
				CacheWriteTokens: ev.CacheWriteTokens,
				ReasoningTokens:  ev.ReasoningTokens,
				TotalTokens:      ev.TotalTokens,
				Accounting:       ev.Accounting,
			}}
		}
		for _, scope := range scopes {
			if scope.Accounting.Plane == lipapi.UsagePlaneUnknown {
				continue
			}
			record := accountingledger.Record{
				RequestID:         strings.TrimSpace(s.baseline.ID),
				AttemptID:         strings.TrimSpace(s.bleg.BLegID),
				Backend:           strings.TrimSpace(s.cand.Primary.Backend),
				Model:             strings.TrimSpace(s.cand.Primary.Model),
				Plane:             scope.Accounting.Plane,
				InputTokens:       scope.InputTokens,
				OutputTokens:      scope.OutputTokens,
				CacheReadTokens:   scope.CacheReadTokens,
				CacheWriteTokens:  scope.CacheWriteTokens,
				ReasoningTokens:   scope.ReasoningTokens,
				TotalTokens:       scope.TotalTokens,
				Metadata:          scope.Accounting,
				CreatedAt:         s.now(),
				UnavailableReason: unavailableReason,
				FailureReason:     failureReason,
			}
			if record.RequestID == "" {
				record.RequestID = strings.TrimSpace(s.traceID)
			}
			if err := s.executor.Ledger.Record(ctx, record); err != nil {
				if s.executor.Log != nil {
					s.executor.Log.DebugContext(ctx, "token accounting ledger record", "error", err)
				}
				s.recordTokenAccountingObservation(ctx, record, err, duration)
				return err
			}
			s.recordTokenAccountingObservation(ctx, record, nil, duration)
		}
	}
	return nil
}

func (s *retryRecvStream) recordPartialTokenAccounting(ctx context.Context, reason string, err error) {
	if s == nil || s.executor == nil || s.executor.Ledger == nil {
		return
	}
	events := tokenAccountingUsageEvents(s.seenEvents)
	if len(events) == 0 {
		return
	}
	duration := s.now().Sub(s.accounting.requestStartedAt)
	if duration <= 0 {
		duration = time.Nanosecond
	}
	_ = s.recordTokenAccountingLedger(ctx, events, reason, reason, duration)
}

func (s *retryRecvStream) recordTokenAccountingObservation(ctx context.Context, record accountingledger.Record, err error, duration time.Duration) {
	if s == nil || s.executor == nil || s.executor.TokenAccountingObservability == nil {
		return
	}
	obs, err := accountingobs.NewObservation(accountingobs.Input{
		Labels: accountingobs.Labels{
			Backend:   record.Backend,
			Model:     record.Model,
			Plane:     accountingobs.Plane(record.Plane),
			Source:    accountingobs.Source(record.Metadata.Source),
			Authority: accountingobs.Authority(record.Metadata.Authority),
		},
		Status:            observationStatus(record, err),
		UnavailableReason: record.UnavailableReason,
		Err:               err,
		Duration:          duration,
		OccurredAt:        record.CreatedAt,
	})
	if err != nil {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "token accounting observation", "error", err)
		}
		return
	}
	s.executor.TokenAccountingObservability.Record(obs)
}

func observationStatus(record accountingledger.Record, err error) accountingobs.Status {
	if err != nil || record.FailureReason != "" {
		return accountingobs.StatusUnavailable
	}
	return accountingobs.StatusSuccess
}

func tokenAccountingUsageEvents(events []lipapi.Event) []lipapi.Event {
	out := []lipapi.Event{}
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			out = append(out, ev)
		}
	}
	return out
}

func (s *retryRecvStream) rememberClientEvent(ev lipapi.Event) {
	if s == nil {
		return
	}
	if ev.Kind == lipapi.EventResponseFinished {
		for _, seen := range s.seenEvents {
			if seen.Kind == lipapi.EventResponseFinished {
				return
			}
		}
	}
	if ev.Kind == lipapi.EventTextDelta {
		s.visibleText.WriteString(ev.Delta)
	}
	s.seenEvents = append(s.seenEvents, ev)
}
