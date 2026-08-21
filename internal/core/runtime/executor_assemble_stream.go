package runtime

import (
	"context"
	"errors"

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

	// --- TASK 2.2 & 2.3 — Atomic readiness and stream assembly handoff ---
	// 1. Prepare ready attempt while unpublished
	ready, err := e.prepareReadyAttempt(ctx, out.session, rs.facts, responsePipeline, terminal.committed(), out.interleaved, out.memoUpdate)
	if err != nil {
		// prepareReadyAttempt already called AbortBeforeReturn on failure.
		return nil, err
	}

	// 2. Set up the assembly transaction to hold the ready attempt and request guard
	tx := &streamAssemblyTx{
		ready: ready,
		guard: prep.guard,
	}
	defer func() {
		if !tx.committed {
			tx.Rollback(ctx, errors.New("initial stream assembly failed before return"))
		}
	}()

	// 3. Consume the ready capability and install the session in the slot
	session, err := ready.Consume()
	if err != nil {
		return nil, err
	}
	rs.attempt.install(session)

	var stream lipapi.EventStream = rs
	if e.shouldWrapHiddenInterleavedThinker(session.cand) {
		rs.terminal.setInterleavedThinker()
		rs.terminal.deferALegEndToOuter()
		stream = newHiddenInterleavedStream(rs, e.newThinkerRecorder(session.cand, *prep.call), out.interleaved)
	} else if e.shouldWrapVisibleInterleavedThinker(session.cand) {
		rs.terminal.setInterleavedThinker()
		rs.terminal.deferALegEndToOuter()
		stream = newVisibleInterleavedStream(rs, e.newThinkerRecorder(session.cand, *prep.call), out.interleaved)
	}

	// 4. Non-fallible commit to finalize publication and handoff request ownership
	tx.Commit()

	return stream, nil
}

type streamAssemblyTx struct {
	ready     *readyAttempt
	guard     *preStreamGuard
	committed bool
}

func (tx *streamAssemblyTx) Commit() {
	if tx.committed {
		return
	}
	tx.committed = true
	if tx.guard != nil {
		tx.guard.Handoff()
	}
}

func (tx *streamAssemblyTx) Rollback(ctx context.Context, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if tx.committed {
		return
	}
	tx.committed = true
	if tx.ready != nil {
		tx.ready.Dispose(ctx, err)
	}
}
