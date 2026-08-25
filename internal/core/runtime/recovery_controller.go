package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var (
	errRecoveryPriorAttemptNotRetired = errors.New("runtime: recovery prior attempt not retired")
	errRecoveryTurnCommitted          = errors.New("runtime: recovery turn already committed")
)

type recoveryEnvironment interface {
	now() time.Time
	logInterleavedMemoStoreSkipped(ctx context.Context, traceID, reason string, interrupted bool)
	logInterleavedMemoCaptured(ctx context.Context, traceID string, memo interleavedthinking.MemoState)
	logInterleavedPhaseTransition(ctx context.Context, traceID string)
	persistCapturedMemo(ctx context.Context, aLegID string, state interleavedstate.State, memo interleavedthinking.MemoState) (interleavedstate.State, error)
	openInterleavedExecutorContinuation(ctx context.Context, from *retryRecvStream, state interleavedstate.State) (*retryRecvStream, error)
	logInterleavedMemoPersistFailed(ctx context.Context, traceID string, err error)
	noteRouteDecision(ctx context.Context, traceID, decision, detail string)
}

type recoveryController struct {
	e                   recoveryEnvironment
	affinityStore       affinity.Store
	log                 *slog.Logger
	streamRecovery      streamrecovery.Config
	opener              replacementOpener
	budget              *attemptBudget
	ttft                *ttftBudget
	sel                 *routing.Selector
	requestSize         routing.RequestSizeEstimate
	session             *routing.SessionRoutingState
	excluded            map[string]struct{}
	rng                 routing.Rng
	affinityKey         affinity.Key
	affinitySet         bool
	affinityCommitOnce  *sync.Once
	recoverPolicy       *streamrecovery.Policy
	interleaved         interleavedstate.State
	suppressThinker     bool
	suppressVisibleMemo bool
	failures            *candidateFailureHistory
}
type recoveryControllerInput struct {
	e              recoveryEnvironment
	affinityStore  affinity.Store
	log            *slog.Logger
	streamRecovery streamrecovery.Config
	opener         replacementOpener
	budget         *attemptBudget
	ttft           *ttftBudget
	sel            *routing.Selector
	requestSize    routing.RequestSizeEstimate
	session        *routing.SessionRoutingState
	excluded       map[string]struct{}
	rng            routing.Rng
	affinityKey    affinity.Key
	affinitySet    bool
	interleaved    interleavedstate.State
	recoverPolicy  *streamrecovery.Policy
}

func newRecoveryController(in recoveryControllerInput) *recoveryController {
	failures := in.budget.getFailures()
	if failures.progress != nil {
		r := failures.progress
		if in.opener != nil {
			r.opener = in.opener
		}
		r.interleaved = in.interleaved
		if in.recoverPolicy != nil {
			r.recoverPolicy = in.recoverPolicy
		}
		if r.affinityCommitOnce == nil {
			r.affinityCommitOnce = &sync.Once{}
		}
		return r
	}
	policy := in.recoverPolicy
	if policy == nil {
		var now time.Time
		if in.e != nil {
			now = in.e.now()
		} else {
			now = time.Now()
		}
		policy = streamrecovery.NewPolicy(in.streamRecovery, now)
	}
	r := &recoveryController{
		e:                  in.e,
		affinityStore:      in.affinityStore,
		log:                in.log,
		streamRecovery:     in.streamRecovery,
		opener:             in.opener,
		budget:             in.budget,
		ttft:               in.ttft,
		sel:                in.sel,
		requestSize:        in.requestSize,
		session:            in.session,
		excluded:           in.excluded,
		rng:                in.rng,
		affinityKey:        in.affinityKey,
		affinitySet:        in.affinitySet,
		affinityCommitOnce: &sync.Once{},
		recoverPolicy:      policy,
		interleaved:        in.interleaved,
		failures:           failures,
	}
	failures.progress = r
	return r
}

func (r *recoveryController) getFailures() *candidateFailureHistory {
	if r == nil {
		return &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
	}
	if r.failures == nil {
		if r.budget != nil && r.budget.failures != nil {
			r.failures = r.budget.failures
		} else {
			r.failures = &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
		}
	}
	return r.failures
}

func (r *recoveryController) scopedIdleContext(parent context.Context, parentCancel context.CancelFunc, now time.Time) (context.Context, context.CancelFunc, idleContextDeadline) {
	if r == nil || r.recoverPolicy == nil || parent == nil {
		return parent, parentCancel, idleContextDeadline{}
	}
	deadline, ok := r.recoverPolicy.IdleDeadline()
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

type recvRecoveryDecision struct {
	finish             bool
	recover            bool
	continuePostOutput bool
	reason             string
	err                error
	warning            lipapi.Event
	finishEvent        lipapi.Event
}

func (r *recoveryController) idleRecvDecision(now time.Time) recvRecoveryDecision {
	if r == nil || r.recoverPolicy == nil {
		return recvRecoveryDecision{}
	}
	dec := r.recoverPolicy.DecideIdle(now)
	return recvRecoveryDecision{
		finish:             dec.Kind == streamrecovery.DecisionFinishPostOutput,
		recover:            dec.Kind == streamrecovery.DecisionRecoverPreOutput,
		continuePostOutput: dec.Kind == streamrecovery.DecisionContinuePostOutput,
		reason:             dec.Reason, err: dec.Err, warning: dec.Warning, finishEvent: dec.Finish,
	}
}

func (r *recoveryController) eofRecvDecision(now time.Time) recvRecoveryDecision {
	if r == nil || r.recoverPolicy == nil {
		return recvRecoveryDecision{}
	}
	dec := r.recoverPolicy.DecideEOF(io.EOF, now)
	return recvRecoveryDecision{
		finish:             dec.Kind == streamrecovery.DecisionFinishPostOutput,
		recover:            dec.Kind == streamrecovery.DecisionRecoverPreOutput,
		continuePostOutput: dec.Kind == streamrecovery.DecisionContinuePostOutput,
		reason:             dec.Reason, err: dec.Err, warning: dec.Warning, finishEvent: dec.Finish,
	}
}

func (r *recoveryController) genericErrorRecvDecision(err error, now time.Time) recvRecoveryDecision {
	if r == nil || r.recoverPolicy == nil || err == nil {
		return recvRecoveryDecision{}
	}
	dec := r.recoverPolicy.DecideEOF(err, now)
	return recvRecoveryDecision{
		finish:             dec.Kind == streamrecovery.DecisionFinishPostOutput,
		recover:            dec.Kind == streamrecovery.DecisionRecoverPreOutput,
		continuePostOutput: dec.Kind == streamrecovery.DecisionContinuePostOutput,
		reason:             dec.Reason, err: dec.Err, warning: dec.Warning, finishEvent: dec.Finish,
	}
}

func (r *recoveryController) bindOpener(e *Executor, bus *hooks.Bus, aScope *leglifecycle.ALeg) {
	if r == nil {
		return
	}
	if r.opener == nil {
		r.opener = newReplacementOpener(e, bus, aScope)
	}
	if e != nil {
		r.e = e
		r.affinityStore = e.AffinityStore
		r.log = e.Log
		r.streamRecovery = e.StreamRecovery
	}
}

type recoveryOpenSnapshot struct {
	progress *recoveryController
	facts    routeFacts
}

func (r *recoveryController) openSnapshot() recoveryOpenSnapshot {
	return recoveryOpenSnapshot{
		progress: r,
		facts: routeFacts{
			sel:         r.sel,
			requestSize: r.requestSize,
			affinityKey: r.affinityKey,
			affinitySet: r.affinitySet,
			rng:         r.rng,
		},
	}
}

type priorAttemptOutcome struct {
	attempt *attemptSession
	retired bool
}
type replacementOpenRequest struct {
	facts               requestTerminalFacts
	pinnedFacts         recvTurnFacts
	recovery            recoveryOpenSnapshot
	prior               priorAttemptOutcome
	isRetryPath         bool
	interleaved         interleavedstate.State
	suppressThinker     bool
	suppressVisibleMemo bool
}
type replacementOpenResult struct {
	opened      bool
	registered  bool
	stream      lipapi.ManagedEventStream
	bleg        b2bua.BLegRecord
	cand        routing.AttemptCandidate
	authority   attemptAuthorityState
	interleaved interleavedstate.State
	ready       *readyAttempt
}
type replacementIterationResult struct {
	opened bool
	open   replacementOpenResult
	next   *readyAttempt
}
type replacementOpener func(context.Context, replacementOpenRequest) (replacementOpenResult, error)

func newReplacementOpener(e *Executor, bus *hooks.Bus, aScope *leglifecycle.ALeg) replacementOpener {
	return func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		if e == nil {
			return replacementOpenResult{}, errors.New("runtime: nil replacement opener executor")
		}
		p := req.recovery
		mode := openModeRetry
		if !req.isRetryPath {
			mode = openModeGuardContinuation
		}
		out, err := e.openNext(ctx, openNextRequest{
			reqFacts: requestFacts{
				recvTurnFacts:       req.pinnedFacts,
				bus:                 bus,
				aScope:              aScope,
				suppressThinker:     req.suppressThinker,
				suppressVisibleMemo: req.suppressVisibleMemo,
			},
			routeFacts:  p.facts,
			progress:    p.progress,
			mode:        mode,
			interleaved: req.interleaved,
		})
		if err != nil {
			return replacementOpenResult{}, err
		}
		res := replacementOpenResult{
			opened:      out.ready != nil,
			registered:  out.ready != nil,
			interleaved: out.interleaved,
			ready:       out.ready,
		}
		if out.ready != nil {
			res.bleg = out.ready.BLeg()
			res.cand = out.ready.Candidate()
			res.authority = out.ready.AuthorityState()
		}
		return res, nil
	}
}

func (r *recoveryController) openContinuation(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
	if r == nil || r.opener == nil {
		return replacementOpenResult{}, errors.New("runtime: replacement opener unavailable")
	}
	if !req.prior.retired {
		return replacementOpenResult{}, errRecoveryPriorAttemptNotRetired
	}
	// Semantic continuation is not a retry/replacement: allow committed and
	// enter normal admission with isRetryPath false.
	return r.opener(ctx, req)
}

func (r *recoveryController) openReplacement(ctx context.Context, request requestTerminalFacts, prior *attemptSession, committed bool) (replacementOpenResult, error) {
	if r == nil || r.opener == nil {
		return replacementOpenResult{}, errors.New("runtime: replacement opener unavailable")
	}
	if committed {
		return replacementOpenResult{}, errRecoveryTurnCommitted
	}
	priorOutcome := priorAttemptOutcome{
		attempt: prior,
		retired: prior == nil || prior.authority.Settled() || !prior.authority.IsActive(),
	}
	if !priorOutcome.retired {
		return replacementOpenResult{}, errRecoveryPriorAttemptNotRetired
	}
	out, err := r.opener(ctx, replacementOpenRequest{
		facts:               request,
		pinnedFacts:         request.toRecvTurnFacts(ctx),
		recovery:            r.openSnapshot(),
		prior:               priorOutcome,
		isRetryPath:         true,
		interleaved:         r.interleaved,
		suppressThinker:     r.suppressThinker,
		suppressVisibleMemo: r.suppressVisibleMemo,
	})
	if err == nil {
		r.interleaved = out.interleaved
	}
	return out, err
}

func (r *recoveryController) openInterleavedAttempt(
	ctx context.Context,
	facts recvTurnFacts,
	state interleavedstate.State,
) (openedAttempt, error) {
	if r == nil || r.opener == nil {
		return openedAttempt{}, errors.New("runtime: interleaved opener unavailable")
	}
	out, err := r.opener(ctx, replacementOpenRequest{
		facts:               facts.terminalFacts(),
		pinnedFacts:         facts.clone(),
		recovery:            r.openSnapshot(),
		prior:               priorAttemptOutcome{retired: true},
		isRetryPath:         false,
		interleaved:         state,
		suppressThinker:     true,
		suppressVisibleMemo: true,
	})
	if err == nil {
		r.interleaved = out.interleaved
		r.suppressThinker = true
		r.suppressVisibleMemo = true
		var nowFn func() time.Time
		if r.e != nil {
			nowFn = r.e.now
		} else {
			nowFn = time.Now
		}
		r.resetPolicy(nowFn)
	}
	return openedAttempt{
		ready:       out.ready,
		interleaved: out.interleaved,
	}, err
}

func (r *recoveryController) resetPolicy(now func() time.Time) {
	if r == nil {
		return
	}
	var cfg streamrecovery.Config
	var t time.Time
	if r.e != nil {
		cfg = r.streamRecovery
		t = r.e.now()
	} else if now != nil {
		t = now()
	} else {
		t = time.Now()
	}
	r.recoverPolicy = streamrecovery.NewPolicy(cfg, t)
}

func (r *recoveryController) logMemoStoreSkipped(ctx context.Context, traceID, reason string, interrupted bool) {
	if r != nil && r.e != nil {
		r.e.logInterleavedMemoStoreSkipped(ctx, traceID, reason, interrupted)
	}
}

func (r *recoveryController) logMemoCaptured(ctx context.Context, traceID string, memo interleavedthinking.MemoState) {
	if r != nil && r.e != nil {
		r.e.logInterleavedMemoCaptured(ctx, traceID, memo)
	}
}

func (r *recoveryController) logPhaseTransition(ctx context.Context, traceID string) {
	if r != nil && r.e != nil {
		r.e.logInterleavedPhaseTransition(ctx, traceID)
	}
}

func (r *recoveryController) persistCapturedMemo(ctx context.Context, aLegID string, state interleavedstate.State, memo interleavedthinking.MemoState) (interleavedstate.State, error) {
	if r == nil || r.e == nil {
		return state, errors.New("runtime: interleaved memo persistence unavailable")
	}
	return r.e.persistCapturedMemo(ctx, aLegID, state, memo)
}

func (r *recoveryController) openInterleavedContinuation(ctx context.Context, from *retryRecvStream, state interleavedstate.State) (*retryRecvStream, error) {
	if r == nil || r.e == nil {
		return nil, errors.New("runtime: interleaved continuation opener unavailable")
	}
	return r.e.openInterleavedExecutorContinuation(ctx, from, state)
}

func (r *recoveryController) logMemoPersistFailed(ctx context.Context, traceID string, err error) {
	if r != nil && r.e != nil {
		r.e.logInterleavedMemoPersistFailed(ctx, traceID, err)
	}
}

func (r *recoveryController) exclude(key string) {
	if r == nil {
		return
	}
	if r.excluded == nil {
		r.excluded = make(map[string]struct{})
	}
	r.excluded[key] = struct{}{}
}

func (r *recoveryController) commitAffinity(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, now time.Time, reason string) {
	if r == nil || r.e == nil || !r.affinitySet || !r.affinityKey.Valid() || attempt == nil || r.affinityStore == nil {
		return
	}
	if r.affinityCommitOnce == nil {
		r.affinityCommitOnce = &sync.Once{}
	}
	r.affinityCommitOnce.Do(func() {
		binding := affinity.BindingFromCandidate(r.affinityKey, attempt.cand, now, reason)
		if strings.TrimSpace(binding.BackendID) == "" {
			return
		}
		persistCtx := context.WithoutCancel(ctx)
		if err := r.affinityStore.Set(persistCtx, binding); err != nil {
			if r.log != nil {
				r.log.DebugContext(persistCtx, "affinity binding set failed", "error", err)
			}
			return
		}
		r.e.noteRouteDecision(persistCtx, request.traceID, "affinity_bind", binding.BackendID)
	})
}

func (r *recoveryController) tryReplacementIteration(ctx context.Context, request requestTerminalFacts, prior *attemptSession, committed bool) (replacementIterationResult, error) {
	if r == nil || prior == nil {
		return replacementIterationResult{}, errors.New("runtime: recovery controller or replacement attempt unavailable")
	}
	ctx = diag.EnsureCallDiag(ctx, request.traceID, request.aLegID)
	if err := ctx.Err(); err != nil {
		return replacementIterationResult{}, err
	}
	if request.replacementBlocked {
		return replacementIterationResult{}, &lipapi.UpstreamFailureError{Phase: lipapi.PhasePostOutput, Recoverable: false, Reason: "secure session mandatory recorder failure after committed output", CandidateKey: strings.TrimSpace(prior.cand.Key)}
	}
	out, err := r.openReplacement(ctx, request, prior, committed)
	if err != nil || !out.opened {
		return replacementIterationResult{}, err
	}
	if out.ready == nil {
		return replacementIterationResult{}, errors.New("runtime: replacement attempt construction unavailable")
	}
	return replacementIterationResult{opened: true, open: out, next: out.ready}, nil
}
