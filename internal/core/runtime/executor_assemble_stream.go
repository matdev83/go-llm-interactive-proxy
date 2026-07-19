package runtime

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// assembleExecutorStream builds the retry-capable recv stream and applies
// interleaved-thinking wrappers when the opened candidate requires them.
func (e *Executor) assembleExecutorStream(prep *preparedRequest, plan *routePlanState, out attemptOpenResult) (lipapi.EventStream, error) {
	fs, maxArgs := e.resolveToolCallFinalizers()
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
		authority:     e.newAttemptAuthorityLifecycle(out.authority, out.cand),
		affinityKey:   plan.affinityKey,
		affinitySet:   plan.affinitySet,
		recvViews:     prep.recvViews,
		recvViewsOK:   prep.recvViewsOK,
		routePrefs:    prep.routePrefs,
		secureTurn:    prep.secureTurn,
		secureTurnOK:  prep.secureTurnOK,
		metering:      prep.metering,
		requestAuth:   requestAuthorityFrom(prep.ctx),
		customer:      newCustomerEvidenceAccumulator(),
		accounting:    newAttemptAccountingTracker(e.now()),
		recoverPolicy: streamrecovery.NewPolicy(e.StreamRecovery, e.now()),
		aScope:        prep.aScope,
		interleaved:   out.interleaved,
		toolFinal:     newToolCallAssembler(fs, maxArgs, prep.baseline.Tools),
		requestTerm:   newStreamTerminal(sdkterminal.ScopeRequest),
		attemptTerm:   newStreamTerminal(sdkterminal.ScopeAttempt),
	}
	rs.storeInner(out.stream)
	if err := rs.openFinalStreamObservation(prep.ctx); err != nil {
		if out.stream != nil {
			_ = out.stream.Close()
		}
		return nil, err
	}
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
