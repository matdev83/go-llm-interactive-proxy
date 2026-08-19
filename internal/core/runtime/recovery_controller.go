package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
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

// recoveryController owns request-lifetime state that survives a recv-phase
// attempt replacement. Attempt-local state remains on attemptSession and
// request terminal truth remains on turnTerminal.
type recoveryController struct {
	opener replacementOpener

	streamRecovery                streamrecovery.Config
	nowFn                         func() time.Time
	logMemoStoreSkippedFn         func(context.Context, string, string, bool)
	logMemoCapturedFn             func(context.Context, string, interleavedthinking.MemoState)
	logPhaseTransitionFn          func(context.Context, string)
	persistCapturedMemoFn         func(context.Context, string, interleavedstate.State, interleavedthinking.MemoState) (interleavedstate.State, error)
	openInterleavedContinuationFn func(context.Context, *retryRecvStream, interleavedstate.State) (*retryRecvStream, error)
	logMemoPersistFailedFn        func(context.Context, string, error)
	appendTerminalLegFn           func(context.Context, *billingCallState, string, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, billing.LegOutcome)
	commitAffinityFn              func(context.Context, affinity.Binding, string)

	budget      *attemptBudget
	ttft        *ttftBudget
	sel         *routing.Selector
	requestSize routing.RequestSizeEstimate
	session     *routing.SessionRoutingState
	excluded    map[string]struct{}
	rng         routing.Rng

	lastHardReject           lipapi.NegotiationResult
	lastHardTransportReject  lipapi.TransportNegotiationResult
	lastAdmissionErr         error
	isContextLimitExhaustion bool
	transformExcludes        transformExcludeTracker

	affinityKey        affinity.Key
	affinitySet        bool
	affinityCommitOnce sync.Once

	recoverPolicy       *streamrecovery.Policy
	interleaved         interleavedstate.State
	suppressThinker     bool
	suppressVisibleMemo bool
	lastParallelFailure error
	attemptFactory      func(replacementOpenResult, recvTurnFacts) *attemptSession
	postOpenLeg         func(context.Context, *billingCallState, string, b2bua.BLegRecord, routing.Primary, time.Time, time.Time)
}

type recoveryControllerInput struct {
	opener                        replacementOpener
	budget                        *attemptBudget
	ttft                          *ttftBudget
	sel                           *routing.Selector
	requestSize                   routing.RequestSizeEstimate
	session                       *routing.SessionRoutingState
	excluded                      map[string]struct{}
	rng                           routing.Rng
	affinityKey                   affinity.Key
	affinitySet                   bool
	interleaved                   interleavedstate.State
	recoverPolicy                 *streamrecovery.Policy
	streamRecovery                streamrecovery.Config
	nowFn                         func() time.Time
	logMemoStoreSkippedFn         func(context.Context, string, string, bool)
	logMemoCapturedFn             func(context.Context, string, interleavedthinking.MemoState)
	logPhaseTransitionFn          func(context.Context, string)
	persistCapturedMemoFn         func(context.Context, string, interleavedstate.State, interleavedthinking.MemoState) (interleavedstate.State, error)
	openInterleavedContinuationFn func(context.Context, *retryRecvStream, interleavedstate.State) (*retryRecvStream, error)
	logMemoPersistFailedFn        func(context.Context, string, error)
	appendTerminalLegFn           func(context.Context, *billingCallState, string, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, billing.LegOutcome)
	commitAffinityFn              func(context.Context, affinity.Binding, string)
}

func newRecoveryController(in recoveryControllerInput) *recoveryController {
	policy := in.recoverPolicy
	if policy == nil && in.nowFn != nil {
		policy = streamrecovery.NewPolicy(in.streamRecovery, in.nowFn())
	}
	r := &recoveryController{
		opener:                        in.opener,
		streamRecovery:                in.streamRecovery,
		nowFn:                         in.nowFn,
		logMemoStoreSkippedFn:         in.logMemoStoreSkippedFn,
		logMemoCapturedFn:             in.logMemoCapturedFn,
		logPhaseTransitionFn:          in.logPhaseTransitionFn,
		persistCapturedMemoFn:         in.persistCapturedMemoFn,
		openInterleavedContinuationFn: in.openInterleavedContinuationFn,
		logMemoPersistFailedFn:        in.logMemoPersistFailedFn,
		appendTerminalLegFn:           in.appendTerminalLegFn,
		commitAffinityFn:              in.commitAffinityFn,
		budget:                        in.budget,
		ttft:                          in.ttft,
		sel:                           in.sel,
		requestSize:                   in.requestSize,
		session:                       in.session,
		excluded:                      in.excluded,
		rng:                           in.rng,
		affinityKey:                   in.affinityKey,
		affinitySet:                   in.affinitySet,
		recoverPolicy:                 policy,
		interleaved:                   in.interleaved,
	}
	return r
}

// bindOpener supports focused runtime fixtures that construct a stream owner
// directly. Production assembly supplies the same collaborators at construction;
// this one-way bind does not create a second recovery seam.
func (r *recoveryController) bindOpener(e *Executor, bus *hooks.Bus, aScope *leglifecycle.ALeg) {
	if r == nil {
		return
	}
	if r.opener == nil {
		r.opener = newReplacementOpener(e, bus, aScope)
	}
	if e != nil {
		if r.nowFn == nil {
			r.nowFn = e.now
		}
		if r.streamRecovery == (streamrecovery.Config{}) {
			r.streamRecovery = e.StreamRecovery
		}
		if r.logMemoStoreSkippedFn == nil {
			r.logMemoStoreSkippedFn = e.logInterleavedMemoStoreSkipped
		}
		if r.logMemoCapturedFn == nil {
			r.logMemoCapturedFn = e.logInterleavedMemoCaptured
		}
		if r.logPhaseTransitionFn == nil {
			r.logPhaseTransitionFn = e.logInterleavedPhaseTransition
		}
		if r.persistCapturedMemoFn == nil {
			r.persistCapturedMemoFn = e.persistCapturedMemo
		}
		if r.openInterleavedContinuationFn == nil {
			r.openInterleavedContinuationFn = e.openInterleavedExecutorContinuation
		}
		if r.logMemoPersistFailedFn == nil {
			r.logMemoPersistFailedFn = e.logInterleavedMemoPersistFailed
		}
		if r.appendTerminalLegFn == nil {
			r.appendTerminalLegFn = e.appendIndependentTerminalLeg
		}
		if r.commitAffinityFn == nil {
			r.commitAffinityFn = recoveryCommitAffinityCallback(e)
		}
	}
}

func recoveryCommitAffinityCallback(e *Executor) func(context.Context, affinity.Binding, string) {
	if e == nil {
		return nil
	}
	return func(ctx context.Context, binding affinity.Binding, traceID string) {
		persistCtx := context.WithoutCancel(ctx)
		if e.AffinityStore == nil {
			return
		}
		if err := e.AffinityStore.Set(persistCtx, binding); err != nil {
			if e.Log != nil {
				e.Log.DebugContext(persistCtx, "affinity binding set failed", "error", err)
			}
			return
		}
		e.noteRouteDecision(persistCtx, traceID, "affinity_bind", binding.BackendID)
	}
}

// recoveryOpenSnapshot is the adapter-facing view of controller state. The
// pointed-to values remain owned by recoveryController and are mutated only by
// the current recv owner through this component.
type recoveryOpenSnapshot struct {
	sel                      *routing.Selector
	requestSize              routing.RequestSizeEstimate
	session                  *routing.SessionRoutingState
	excluded                 map[string]struct{}
	rng                      routing.Rng
	budget                   *attemptBudget
	ttft                     *ttftBudget
	lastReject               *lipapi.NegotiationResult
	lastTransportReject      *lipapi.TransportNegotiationResult
	lastAdmissionErr         *error
	affinityKey              affinity.Key
	affinitySet              bool
	isContextLimitExhaustion *bool
	transformExcludes        *transformExcludeTracker
	interleaved              interleavedstate.State
	suppressThinker          bool
	suppressVisibleMemo      bool
	lastParallelFailure      *error
}

func (r *recoveryController) openSnapshot() recoveryOpenSnapshot {
	return recoveryOpenSnapshot{
		sel:                      r.sel,
		requestSize:              r.requestSize,
		session:                  r.session,
		excluded:                 r.excluded,
		rng:                      r.rng,
		budget:                   r.budget,
		ttft:                     r.ttft,
		lastReject:               &r.lastHardReject,
		lastTransportReject:      &r.lastHardTransportReject,
		lastAdmissionErr:         &r.lastAdmissionErr,
		affinityKey:              r.affinityKey,
		affinitySet:              r.affinitySet,
		isContextLimitExhaustion: &r.isContextLimitExhaustion,
		transformExcludes:        &r.transformExcludes,
		interleaved:              r.interleaved,
		suppressThinker:          r.suppressThinker,
		suppressVisibleMemo:      r.suppressVisibleMemo,
		lastParallelFailure:      &r.lastParallelFailure,
	}
}

type priorAttemptOutcome struct {
	attempt *attemptSession
	retired bool
}

// replacementOpenRequest/result are the narrow D10 adapter seam. Consumers do
// not need to know the upstream attemptOpenParams representation.
type replacementOpenRequest struct {
	facts               recvTurnFacts
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
}

type replacementOpener func(context.Context, replacementOpenRequest) (replacementOpenResult, error)

// newReplacementOpener is the documented D10 upstream bridge. Recovery owns
// only this narrow open operation; all other executor behavior enters through
// individually typed callbacks installed at construction.
func newReplacementOpener(e *Executor, bus *hooks.Bus, aScope *leglifecycle.ALeg) replacementOpener {
	return func(ctx context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		if e == nil {
			return replacementOpenResult{}, errors.New("runtime: nil replacement opener executor")
		}
		p := req.recovery
		out, err := e.tryPlanOpenOnce(ctx, attemptOpenParams{
			bus:                      bus,
			traceID:                  req.facts.traceID,
			aLegID:                   req.facts.aLegID,
			aScope:                   aScope,
			baseline:                 req.facts.baseline,
			failoverReq:              capabilities.NewFailoverRequirementSet(req.facts.baseline),
			sel:                      p.sel,
			requestSize:              p.requestSize,
			session:                  p.session,
			excluded:                 p.excluded,
			rng:                      p.rng,
			budget:                   p.budget,
			ttft:                     p.ttft,
			isRetryPath:              req.isRetryPath,
			lastReject:               p.lastReject,
			lastTransportReject:      p.lastTransportReject,
			lastAdmissionErr:         p.lastAdmissionErr,
			affinityKey:              p.affinityKey,
			affinitySet:              p.affinitySet,
			isContextLimitExhaustion: p.isContextLimitExhaustion,
			transformExcludes:        p.transformExcludes,
			interleaved:              req.interleaved,
			suppressThinker:          req.suppressThinker,
			suppressVisibleMemo:      req.suppressVisibleMemo,
			lastParallelFailure:      p.lastParallelFailure,
			billingCallID:            req.facts.billingCallID,
			billingCallState:         req.facts.billingCallState,
		})
		if err != nil {
			return replacementOpenResult{}, err
		}
		return replacementOpenResult{
			opened:      out.opened,
			registered:  out.registered,
			stream:      out.stream,
			bleg:        out.bleg,
			cand:        out.cand,
			authority:   out.authority,
			interleaved: out.interleaved,
		}, nil
	}
}

func (r *recoveryController) openReplacement(ctx context.Context, facts recvTurnFacts, terminal *turnTerminal, prior *attemptSession) (replacementOpenResult, error) {
	if r == nil || r.opener == nil {
		return replacementOpenResult{}, errors.New("runtime: replacement opener unavailable")
	}
	if r.turnCommitted(terminal) {
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
		facts:               facts,
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

// openInterleavedAttempt keeps the continuation's upstream translation beside
// the D10 replacement adapter. The continuation still uses the existing
// planner/open algorithm; only the recovery-owned inputs are projected here.
func (r *recoveryController) openInterleavedAttempt(
	ctx context.Context,
	facts recvTurnFacts,
	state interleavedstate.State,
) (attemptOpenResult, error) {
	if r == nil || r.opener == nil {
		return attemptOpenResult{}, errors.New("runtime: interleaved opener unavailable")
	}
	out, err := r.opener(ctx, replacementOpenRequest{
		facts:               facts,
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
		r.resetPolicy(r.nowFn)
	}
	return attemptOpenResult{
		opened:      out.opened,
		registered:  out.registered,
		stream:      out.stream,
		bleg:        out.bleg,
		cand:        out.cand,
		authority:   out.authority,
		interleaved: out.interleaved,
	}, err
}

// turnCommitted reads the sole request-terminal commitment authority. Recovery
// does not keep a second commitment flag and cannot open after output wins.
func (r *recoveryController) turnCommitted(terminal *turnTerminal) bool {
	return r != nil && terminal != nil && terminal.committed()
}

func (r *recoveryController) resetPolicy(now func() time.Time) {
	if r == nil || now == nil {
		return
	}
	r.recoverPolicy = streamrecovery.NewPolicy(r.streamRecovery, now())
}

func (r *recoveryController) buildReplacementAttempt(out replacementOpenResult, facts recvTurnFacts) *attemptSession {
	if r == nil || r.attemptFactory == nil {
		return nil
	}
	return r.attemptFactory(out, facts)
}

func (r *recoveryController) logMemoStoreSkipped(ctx context.Context, traceID, reason string, interrupted bool) {
	if r != nil && r.logMemoStoreSkippedFn != nil {
		r.logMemoStoreSkippedFn(ctx, traceID, reason, interrupted)
	}
}

func (r *recoveryController) logMemoCaptured(ctx context.Context, traceID string, memo interleavedthinking.MemoState) {
	if r != nil && r.logMemoCapturedFn != nil {
		r.logMemoCapturedFn(ctx, traceID, memo)
	}
}

func (r *recoveryController) logPhaseTransition(ctx context.Context, traceID string) {
	if r != nil && r.logPhaseTransitionFn != nil {
		r.logPhaseTransitionFn(ctx, traceID)
	}
}

func (r *recoveryController) persistCapturedMemo(ctx context.Context, aLegID string, state interleavedstate.State, memo interleavedthinking.MemoState) (interleavedstate.State, error) {
	if r == nil || r.persistCapturedMemoFn == nil {
		return state, errors.New("runtime: interleaved memo persistence unavailable")
	}
	return r.persistCapturedMemoFn(ctx, aLegID, state, memo)
}

func (r *recoveryController) openInterleavedContinuation(ctx context.Context, from *retryRecvStream, state interleavedstate.State) (*retryRecvStream, error) {
	if r == nil || r.openInterleavedContinuationFn == nil {
		return nil, errors.New("runtime: interleaved continuation opener unavailable")
	}
	return r.openInterleavedContinuationFn(ctx, from, state)
}

func (r *recoveryController) logMemoPersistFailed(ctx context.Context, traceID string, err error) {
	if r != nil && r.logMemoPersistFailedFn != nil {
		r.logMemoPersistFailedFn(ctx, traceID, err)
	}
}

func (r *recoveryController) appendTerminalLeg(ctx context.Context, state *billingCallState, aLegID string, bleg b2bua.BLegRecord, primary routing.Primary, started, finished time.Time, outcome billing.LegOutcome) {
	if r != nil && r.appendTerminalLegFn != nil {
		r.appendTerminalLegFn(ctx, state, aLegID, bleg, primary, started, finished, outcome)
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

func (r *recoveryController) commitAffinity(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, now time.Time, reason string) {
	if r == nil || r.commitAffinityFn == nil || !r.affinitySet || !r.affinityKey.Valid() || attempt == nil {
		return
	}
	r.affinityCommitOnce.Do(func() {
		binding := affinity.BindingFromCandidate(r.affinityKey, attempt.cand, now, reason)
		if strings.TrimSpace(binding.BackendID) == "" {
			return
		}
		r.commitAffinityFn(ctx, binding, facts.traceID)
	})
}
