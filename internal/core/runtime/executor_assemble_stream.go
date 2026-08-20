package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func (e *Executor) assembleExecutorStream(ctx context.Context, prep *preparedRequest, plan *routePlanState, out openedAttempt) (lipapi.EventStream, error) {
	return streamAssembler{e}.assemble(ctx, prep, plan, out)
}

// assemble builds the retry-capable recv stream and applies
// interleaved-thinking wrappers when the opened candidate requires them.
func (a streamAssembler) assemble(ctx context.Context, prep *preparedRequest, plan *routePlanState, out openedAttempt) (lipapi.EventStream, error) {
	e := a.Executor
	if prep.recvTurnFacts.billingCallID == "" && prep.billingCallID != "" {
		prep.recvTurnFacts.billingCallID = prep.billingCallID
		prep.recvTurnFacts.billingCallState = prep.billingCallState
	}
	if prep.aLegID == "" && prep.identity != nil {
		prep.recvTurnFacts = newRecvTurnFacts(ctx, recvTurnFactsInput{
			baseline:         *prep.call,
			traceID:          prep.identity.traceID,
			aLegID:           prep.identity.aLeg.ALegID,
			secureTurn:       prep.identity.secureTurn,
			secureTurnOK:     prep.identity.secureTurnOK,
			billingCallID:    prep.billingCallID,
			billingCallState: prep.billingCallState,
		})
	}
	terminal := newTurnTerminalWithALeg(prep.aScope, aLegEndBase)
	bindTurnTerminalRuntime(terminal, e)
	responsePipeline := newResponsePipelineForExecutor(e, prep.compactionOpenMeta)
	rsFacts := prep.recvTurnFacts
	rsFacts.captureBoundModelViews(ctx)
	plan.progress.interleaved = out.interleaved
	plan.progress.opener = newReplacementOpener(e, prep.bus, prep.aScope)
	rs := &retryRecvStream{
		facts:            rsFacts,
		attempt:          attemptSlot{},
		responsePipeline: responsePipeline,
		terminal:         terminal,
		recovery:         plan.progress,
	}
	responsePipeline.bindTerminalSnapshot(func() (bool, bool) {
		return terminal.committed(), terminal.accountingFinalized()
	})
	responsePipeline.bindCustomerUsage(func(ctx context.Context, text string, events []lipapi.Event) lipapi.Event {
		return reconstructCustomerUsageForResponse(ctx, responsePipeline.streamUsage, responsePipeline.log, rs.facts, rs.attempt.snapshot(), text, events)
	})
	rs.recovery.postOpenLeg = e.appendPostOpenTerminalLeg
	rs.attempt.install(out.session)
	rs.responsePipeline.consumeBackendUsageEvidenceForAttempt(ctx, rs.facts, rs.attempt.require(), out.session.inner)
	views, viewsOK := rs.facts.viewsFor(ctx)
	if err := rs.responsePipeline.openFinalStreamObservation(ctx, rs.facts, rs.attempt.require(), views, viewsOK, rs.terminal.committed()); err != nil {
		if out.session.inner != nil {
			_ = out.session.inner.Close()
		}
		return nil, err
	}
	var stream lipapi.EventStream = rs
	if e.shouldWrapHiddenInterleavedThinker(out.session.cand) {
		rs.terminal.setInterleavedThinker()
		rs.terminal.deferALegEndToOuter()
		stream = newHiddenInterleavedStream(rs, e.newThinkerRecorder(out.session.cand, *prep.call), out.interleaved)
	} else if e.shouldWrapVisibleInterleavedThinker(out.session.cand) {
		rs.terminal.setInterleavedThinker()
		rs.terminal.deferALegEndToOuter()
		stream = newVisibleInterleavedStream(rs, e.newThinkerRecorder(out.session.cand, *prep.call), out.interleaved)
	}
	if prep.guard != nil {
		prep.guard.Handoff()
	}
	return stream, nil
}
