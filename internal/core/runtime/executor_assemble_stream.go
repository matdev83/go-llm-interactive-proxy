package runtime

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// assembleExecutorStream builds the retry-capable recv stream and applies
// interleaved-thinking wrappers when the opened candidate requires them.
func (e *Executor) assembleExecutorStream(prep *preparedRequest, plan *routePlanState, out attemptOpenResult) (lipapi.EventStream, error) {
	rs := &retryRecvStream{
		executor:      e,
		bus:           prep.bus,
		baseline:      prep.baseline,
		budget:        plan.budget,
		ttft:          &plan.ttft,
		aLegID:        prep.aLeg.ALegID,
		traceID:       prep.traceID,
		sel:           plan.sel,
		requestSize:   plan.requestSize,
		session:       plan.session,
		excluded:      plan.excluded,
		rng:           plan.rng,
		bleg:          out.bleg,
		cand:          out.cand,
		affinityKey:   plan.affinityKey,
		affinitySet:   plan.affinitySet,
		recvViews:     prep.recvViews,
		recvViewsOK:   prep.recvViewsOK,
		routePrefs:    prep.routePrefs,
		secureTurn:    prep.secureTurn,
		secureTurnOK:  prep.secureTurnOK,
		accounting:    newAttemptAccountingTracker(e.now()),
		recoverPolicy: streamrecovery.NewPolicy(e.StreamRecovery, e.now()),
		aScope:        prep.aScope,
		interleaved:   out.interleaved,
	}
	rs.storeInner(out.stream)
	if e.shouldWrapHiddenInterleavedThinker(out.cand) {
		rs.holdALegEnd = true
		rec := e.newThinkerRecorder(out.cand, prep.baseline)
		prep.streamReturned = true
		return newHiddenInterleavedStream(rs, rec, out.interleaved), nil
	}
	if e.shouldWrapVisibleInterleavedThinker(out.cand) {
		rs.holdALegEnd = true
		rec := e.newThinkerRecorder(out.cand, prep.baseline)
		prep.streamReturned = true
		return newVisibleInterleavedStream(rs, rec, out.interleaved), nil
	}
	prep.streamReturned = true
	return rs, nil
}
