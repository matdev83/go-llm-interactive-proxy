package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
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
	executor *Executor
	opener   replacementOpener

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
}

type recoveryControllerInput struct {
	executor      *Executor
	bus           *hooks.Bus
	aScope        *leglifecycle.ALeg
	budget        *attemptBudget
	ttft          *ttftBudget
	sel           *routing.Selector
	requestSize   routing.RequestSizeEstimate
	session       *routing.SessionRoutingState
	excluded      map[string]struct{}
	rng           routing.Rng
	affinityKey   affinity.Key
	affinitySet   bool
	interleaved   interleavedstate.State
	recoverPolicy *streamrecovery.Policy
}

func newRecoveryController(in recoveryControllerInput) *recoveryController {
	policy := in.recoverPolicy
	if policy == nil && in.executor != nil {
		policy = streamrecovery.NewPolicy(in.executor.StreamRecovery, in.executor.now())
	}
	r := &recoveryController{
		executor:      in.executor,
		budget:        in.budget,
		ttft:          in.ttft,
		sel:           in.sel,
		requestSize:   in.requestSize,
		session:       in.session,
		excluded:      in.excluded,
		rng:           in.rng,
		affinityKey:   in.affinityKey,
		affinitySet:   in.affinitySet,
		recoverPolicy: policy,
		interleaved:   in.interleaved,
	}
	// D10 is intentionally a bounded tranche-2 seam. The current candidate
	// opener remains authoritative; only its request/result translation lives in
	// this recovery component and can be replaced by the next pipeline spec.
	r.opener = newReplacementOpener(in.executor, in.bus, in.aScope)
	return r
}

// bindOpener supports focused runtime fixtures that construct a stream owner
// directly. Production assembly supplies the same collaborators at construction;
// this one-way bind does not create a second recovery seam.
func (r *recoveryController) bindOpener(e *Executor, bus *hooks.Bus, aScope *leglifecycle.ALeg) {
	if r == nil {
		return
	}
	if r.executor == nil {
		r.executor = e
	}
	if r.opener == nil {
		r.opener = newReplacementOpener(r.executor, bus, aScope)
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
	facts    recvTurnFacts
	recovery recoveryOpenSnapshot
	prior    priorAttemptOutcome
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
			isRetryPath:              true,
			lastReject:               p.lastReject,
			lastTransportReject:      p.lastTransportReject,
			lastAdmissionErr:         p.lastAdmissionErr,
			affinityKey:              p.affinityKey,
			affinitySet:              p.affinitySet,
			isContextLimitExhaustion: p.isContextLimitExhaustion,
			transformExcludes:        p.transformExcludes,
			interleaved:              p.interleaved,
			suppressThinker:          p.suppressThinker,
			suppressVisibleMemo:      p.suppressVisibleMemo,
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
		facts:    facts,
		recovery: r.openSnapshot(),
		prior:    priorOutcome,
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
	bus *hooks.Bus,
	aScope *leglifecycle.ALeg,
	state interleavedstate.State,
) (attemptOpenResult, error) {
	if r == nil || r.executor == nil {
		return attemptOpenResult{}, errors.New("runtime: interleaved opener unavailable")
	}
	p := r.openSnapshot()
	out, err := r.executor.tryPlanOpenOnce(ctx, attemptOpenParams{
		bus:                 bus,
		traceID:             facts.traceID,
		aLegID:              facts.aLegID,
		aScope:              aScope,
		baseline:            facts.baseline,
		failoverReq:         capabilities.NewFailoverRequirementSet(facts.baseline),
		sel:                 p.sel,
		requestSize:         p.requestSize,
		session:             p.session,
		excluded:            p.excluded,
		rng:                 p.rng,
		budget:              p.budget,
		ttft:                p.ttft,
		isRetryPath:         false,
		affinityKey:         p.affinityKey,
		affinitySet:         p.affinitySet,
		interleaved:         state,
		suppressThinker:     true,
		suppressVisibleMemo: true,
		billingCallID:       facts.billingCallID,
		billingCallState:    facts.billingCallState,
	})
	if err == nil {
		r.interleaved = out.interleaved
		r.suppressThinker = true
		r.suppressVisibleMemo = true
		r.resetPolicy(r.executor.now)
	}
	return out, err
}

// turnCommitted reads the sole request-terminal commitment authority. Recovery
// does not keep a second commitment flag and cannot open after output wins.
func (r *recoveryController) turnCommitted(terminal *turnTerminal) bool {
	return r != nil && terminal != nil && terminal.committed()
}

func (r *recoveryController) resetPolicy(now func() time.Time) {
	if r == nil || r.executor == nil {
		return
	}
	r.recoverPolicy = streamrecovery.NewPolicy(r.executor.StreamRecovery, now())
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
	if r == nil || r.executor == nil || r.executor.AffinityStore == nil || !r.affinitySet || !r.affinityKey.Valid() || attempt == nil {
		return
	}
	r.affinityCommitOnce.Do(func() {
		binding := affinity.BindingFromCandidate(r.affinityKey, attempt.cand, now, reason)
		if strings.TrimSpace(binding.BackendID) == "" {
			return
		}
		persistCtx := context.WithoutCancel(ctx)
		if err := r.executor.AffinityStore.Set(persistCtx, binding); err != nil {
			if r.executor.Log != nil {
				r.executor.Log.DebugContext(persistCtx, "affinity binding set failed", "error", err)
			}
			return
		}
		r.executor.noteRouteDecision(persistCtx, facts.traceID, "affinity_bind", binding.BackendID)
	})
}
