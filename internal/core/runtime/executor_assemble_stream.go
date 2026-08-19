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
	bindTurnTerminalRuntime(terminal, e)
	responsePipeline := newResponsePipelineForExecutor(e, prep.compactionOpenMeta)
	rsFacts := newRecvTurnFacts(ctx, recvTurnFactsInput{
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
	})
	rs := &retryRecvStream{
		facts:            rsFacts,
		attempt:          attemptSlot{},
		responsePipeline: responsePipeline,
		terminal:         terminal,
		recovery: newRecoveryController(recoveryControllerInput{
			opener:                        newReplacementOpener(e, prep.bus, prep.aScope),
			streamRecovery:                e.StreamRecovery,
			nowFn:                         e.now,
			logMemoStoreSkippedFn:         e.logInterleavedMemoStoreSkipped,
			logMemoCapturedFn:             e.logInterleavedMemoCaptured,
			logPhaseTransitionFn:          e.logInterleavedPhaseTransition,
			persistCapturedMemoFn:         e.persistCapturedMemo,
			openInterleavedContinuationFn: e.openInterleavedExecutorContinuation,
			logMemoPersistFailedFn:        e.logInterleavedMemoPersistFailed,
			appendTerminalLegFn:           e.appendIndependentTerminalLeg,
			commitAffinityFn:              recoveryCommitAffinityCallback(e),
			budget:                        plan.budget,
			ttft:                          &plan.ttft,
			sel:                           plan.sel,
			requestSize:                   plan.requestSize,
			session:                       plan.session,
			excluded:                      plan.excluded,
			rng:                           plan.rng,
			affinityKey:                   plan.affinityKey,
			affinitySet:                   plan.affinitySet,
			interleaved:                   out.interleaved,
		}),
	}
	responsePipeline.bindTerminalSnapshot(func() (bool, bool) {
		return terminal.committed(), terminal.accountingFinalized()
	})
	responsePipeline.bindCustomerUsage(func(ctx context.Context, text string, events []lipapi.Event) lipapi.Event {
		return reconstructCustomerUsageForResponse(ctx, responsePipeline.streamUsage, responsePipeline.log, rs.facts, rs.attempt.snapshot(), text, events)
	})
	rs.recovery.attemptFactory = func(opened replacementOpenResult, facts requestTerminalFacts) *attemptSession {
		fs, maxArgs := e.resolveToolCallFinalizers()
		return newAttemptSession(attemptSessionInput{
			inner: opened.stream, bleg: opened.bleg, cand: opened.cand,
			authority:             e.newAttemptAuthorityLifecycle(opened.authority, opened.cand),
			accounting:            newAttemptAccountingTracker(e.now()),
			toolFinal:             newToolCallAssembler(fs, maxArgs, facts.call.Tools),
			promptCacheSource:     promptCacheObservationSource(opened.stream),
			promptCacheController: promptCacheControllerFor(e.Backends[opened.cand.Primary.Backend]),
			finalStreamObs:        &extensions.FinalStreamObservationSession{Log: e.Log, Metrics: e.ExtensionMetrics},
			recordAttemptLoggedFn: e.recordAttemptLogged,
		})
	}
	rs.recovery.postOpenLeg = e.appendPostOpenTerminalLeg
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
		recordAttemptLoggedFn: e.recordAttemptLogged,
	}))
	rs.responsePipeline.consumeBackendUsageEvidenceForAttempt(ctx, rs.facts, rs.attempt.require(), out.stream)
	views, viewsOK := rs.facts.viewsFor(ctx)
	if err := rs.responsePipeline.openFinalStreamObservation(ctx, rs.facts, rs.attempt.require(), views, viewsOK, rs.terminal.committed()); err != nil {
		if out.stream != nil {
			_ = out.stream.Close()
		}
		return nil, err
	}
	if e.shouldWrapHiddenInterleavedThinker(out.cand) {
		rs.terminal.setInterleavedThinker()
		rs.terminal.deferALegEndToOuter()
		rec := e.newThinkerRecorder(out.cand, prep.baseline)
		prep.streamReturned = true
		return newHiddenInterleavedStream(rs, rec, out.interleaved), nil
	}
	if e.shouldWrapVisibleInterleavedThinker(out.cand) {
		rs.terminal.setInterleavedThinker()
		rs.terminal.deferALegEndToOuter()
		rec := e.newThinkerRecorder(out.cand, prep.baseline)
		prep.streamReturned = true
		return newVisibleInterleavedStream(rs, rec, out.interleaved), nil
	}
	prep.streamReturned = true
	return rs, nil
}
