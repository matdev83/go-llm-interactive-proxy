package runtime

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func (e *Executor) assembleExecutorStream(ctx context.Context, prep *preparedRequest, plan *routePlanState, out openedAttempt) (lipapi.EventStream, error) {
	return streamAssembler{e}.assemble(ctx, prep, plan, out)
}

// assemble builds the retry-capable recv stream and applies
// interleaved-thinking wrappers when the opened candidate requires them.
func (a streamAssembler) assemble(ctx context.Context, prep *preparedRequest, plan *routePlanState, out openedAttempt) (lipapi.EventStream, error) {
	e := a.Executor
	prep.ensureRecvTurnFacts(ctx)
	terminal := newTurnTerminalWithALeg(prep.aScope, aLegEndBase)
	if e != nil && e.RuntimeSnapshot != nil && prep.terminalDecisionEnabled {
		terminal.terminalDecisionProvider = e.RuntimeSnapshot.TerminalDecisionProvider()
		terminal.terminalDecisionProviderID, terminal.terminalDecisionProviderHasID = e.RuntimeSnapshot.TerminalDecisionProviderIdentity()
	}
	if prep.call != nil {
		terminal.supportsContinuation = supportsContinuationForCall(*prep.call)
	}
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
	terminal.continuationTransaction = func(cctx context.Context, intent terminaldecision.ContinuationIntent) (bool, error) {
		return runContinuationTransaction(cctx, terminal, rs, intent)
	}
	responsePipeline.bindTerminalSnapshot(func() (bool, bool) {
		return terminal.committed(), terminal.accountingFinalized()
	})
	responsePipeline.bindCustomerUsage(func(ctx context.Context, text string, events []lipapi.Event) lipapi.Event {
		return reconstructCustomerUsageForResponse(ctx, responsePipeline.streamUsage, responsePipeline.log, rs.facts, rs.attempt.snapshot(), text, events)
	})

	// All fallible assembly before atomic publish.
	if out.ready == nil {
		return nil, errors.New("runtime: nil ready for stream assembly")
	}
	// Fallible preparation: sideband drain and final observation before publish.
	if err := out.ready.Prepare(ctx, rsFacts, responsePipeline, terminal.committed()); err != nil {
		return nil, err
	}
	tx := &streamAssemblyTx{
		ready: out.ready,
		guard: prep.guard,
		slot:  &rs.attempt,
	}
	defer func() {
		if !tx.committed {
			tx.Rollback(ctx, errors.New("initial stream assembly failed before return"))
		}
	}()

	var stream lipapi.EventStream = rs
	// Determine candidate for wrapper selection without consuming ready.
	cand := out.ready.Candidate()
	if e.shouldWrapHiddenInterleavedThinker(cand) {
		rs.terminal.setInterleavedThinker()
		rs.terminal.deferALegEndToOuter()
		stream = newHiddenInterleavedStream(rs, e.newThinkerRecorder(cand, *prep.call), out.interleaved)
	} else if e.shouldWrapVisibleInterleavedThinker(cand) {
		rs.terminal.setInterleavedThinker()
		rs.terminal.deferALegEndToOuter()
		stream = newVisibleInterleavedStream(rs, e.newThinkerRecorder(cand, *prep.call), out.interleaved)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return stream, nil
}

type streamAssemblyTx struct {
	ready     *readyAttempt
	guard     *preStreamGuard
	slot      *attemptSlot
	committed bool
}

func (tx *streamAssemblyTx) Commit() error {
	if tx == nil || tx.committed {
		return nil
	}
	if tx.ready == nil {
		return errors.New("runtime: nil ready for commit")
	}
	if tx.slot != nil {
		if _, published := tx.slot.publishReady(tx.ready); !published {
			return errors.New("runtime: publication closed")
		}
	}
	tx.committed = true
	if tx.guard != nil {
		tx.guard.Handoff()
	}
	return nil
}

func (tx *streamAssemblyTx) Rollback(ctx context.Context, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if tx == nil || tx.committed {
		return
	}
	tx.committed = true
	if tx.ready != nil {
		tx.ready.Dispose(ctx, err)
	}
}
