package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func (e *Executor) assembleExecutorStream(ctx context.Context, prep *preparedRequest, plan *routePlanState, out attemptOpenResult) (lipapi.EventStream, error) {
	return streamAssembler{e}.assemble(ctx, prep, plan, out)
}

// assemble builds the retry-capable recv stream and applies
// interleaved-thinking wrappers when the opened candidate requires them.
func (a streamAssembler) assemble(ctx context.Context, prep *preparedRequest, plan *routePlanState, out attemptOpenResult) (lipapi.EventStream, error) {
	e := a.Executor
	fs, maxArgs := e.resolveToolCallFinalizers()
	terminal := newTurnTerminalWithALeg(prep.aScope, aLegEndBase)
	rs := &retryRecvStream{
		facts: newRecvTurnFacts(ctx, recvTurnFactsInput{
			baseline:               prep.baseline,
			traceID:                prep.traceID,
			aLegID:                 prep.aLeg.ALegID,
			recvViews:              prep.recvViews,
			recvViewsOK:            prep.recvViewsOK,
			routePrefs:             prep.routePrefs,
			secureTurn:             prep.secureTurn,
			secureTurnOK:           prep.secureTurnOK,
			metering:               prep.metering,
			requestAuth:            requestAuthorityFrom(ctx),
			billingAccountID:       prep.billingAccountID,
			billingCustomerPricing: prep.billingCustomerPricing,
			billingChargePolicy:    prep.billingChargePolicy,
			billingIdentityStamped: prep.billingIdentityStamped,
			billingCallID:          prep.billingCallID,
			billingCallState:       prep.billingCallState,
		}),
		executor:           e,
		bus:                prep.bus,
		attempt:            attemptSlot{},
		responsePipeline:   newResponsePipeline(prep.compactionOpenMeta),
		terminal:           terminal,
		recovery: newRecoveryController(recoveryControllerInput{
			executor:    e,
			bus:         prep.bus,
			aScope:      prep.aScope,
			budget:      plan.budget,
			ttft:        &plan.ttft,
			sel:         plan.sel,
			requestSize: plan.requestSize,
			session:     plan.session,
			excluded:    plan.excluded,
			rng:         plan.rng,
			affinityKey: plan.affinityKey,
			affinitySet: plan.affinitySet,
			interleaved: out.interleaved,
		}),
	}
	rs.bindResponsePipeline()
	rs.attempt.install(newAttemptSession(attemptSessionInput{
		inner:                 out.stream,
		bleg:                  out.bleg,
		cand:                  out.cand,
		authority:             e.newAttemptAuthorityLifecycle(out.authority, out.cand),
		accounting:            newAttemptAccountingTracker(e.now()),
		toolFinal:             newToolCallAssembler(fs, maxArgs, prep.baseline.Tools),
		promptCacheSource:     promptCacheObservationSource(out.stream),
		promptCacheController: promptCacheControllerFor(e.Backends[out.cand.Primary.Backend]),
		finalStreamObs:        &extensions.FinalStreamObservationSession{Log: e.Log, Metrics: e.ExtensionMetrics},
	}))
	rs.consumeBackendUsageEvidenceForAttempt(ctx, rs.attempt.require(), out.stream)
	views, viewsOK := rs.viewsFor(ctx)
	if err := rs.responsePipeline.openFinalStreamObservation(ctx, rs.facts, e, rs.attempt.require(), views, viewsOK, rs.isCommitted()); err != nil {
		if out.stream != nil {
			_ = out.stream.Close()
		}
		return nil, err
	}
	if e.shouldWrapHiddenInterleavedThinker(out.cand) {
		rs.isInterleavedThinker = true
		rs.terminal.deferALegEndToOuter()
		rec := e.newThinkerRecorder(out.cand, prep.baseline)
		prep.streamReturned = true
		return newHiddenInterleavedStream(rs, rec, out.interleaved), nil
	}
	if e.shouldWrapVisibleInterleavedThinker(out.cand) {
		rs.isInterleavedThinker = true
		rs.terminal.deferALegEndToOuter()
		rec := e.newThinkerRecorder(out.cand, prep.baseline)
		prep.streamReturned = true
		return newVisibleInterleavedStream(rs, rec, out.interleaved), nil
	}
	prep.streamReturned = true
	return rs, nil
}
