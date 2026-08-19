package runtime

import (
	"context"

	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// These fixture helpers keep legacy characterization tests focused on their
// assertions while constructing the same explicit owner calls production uses.
// They are test-only and intentionally capture the attempt before terminal
// effects begin.
func testTerminalOwners(s *retryRecvStream) (request, attempt *streamTerminal) {
	if s == nil || s.terminal == nil {
		return nil, nil
	}
	request = s.terminal.requestTerminal()
	if current := s.attempt.snapshot(); current != nil {
		attempt = current.terminal
	}
	return request, attempt
}

func testTerminalizeRequest(s *retryRecvStream, ctx context.Context, cmd sdkterminal.Command, effects func(context.Context) error) coreterm.Result {
	if s == nil || s.terminal == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	attempt := s.attempt.snapshot()
	wrapped := func(cctx context.Context, _ coreterm.Outcome) error {
		if effects == nil {
			return nil
		}
		return effects(cctx)
	}
	requestAfter := func(cctx context.Context, _ coreterm.Outcome) error {
		if !cmd.AllowsScope(sdkterminal.ScopeRequest) {
			return nil
		}
		s.terminal.recordBillingLegForAttempt(cctx, s.facts.terminalFacts(), attempt, attempt.terminalEvidence(), cmd, s.responsePipeline.billingEvidenceFallback(), cmd == sdkterminal.CommandNormalFinish, s.facts.billingCallState)
		s.terminal.handoffBillingTurn(cctx, s.facts.terminalFacts(), cmd)
		return nil
	}
	return s.terminal.terminalizeSnapshot(ctx, cmd, attempt, s.responsePipeline.accumulatorSnapshot(), wrapped, requestAfter)
}

func testTerminalizeRequestForAttempt(s *retryRecvStream, ctx context.Context, cmd sdkterminal.Command, attempt *attemptSession, effects func(context.Context) error) coreterm.Result {
	if s == nil || s.terminal == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	wrapped := func(cctx context.Context, _ coreterm.Outcome) error {
		if effects == nil {
			return nil
		}
		return effects(cctx)
	}
	requestAfter := func(cctx context.Context, _ coreterm.Outcome) error {
		if !cmd.AllowsScope(sdkterminal.ScopeRequest) {
			return nil
		}
		s.terminal.recordBillingLegForAttempt(cctx, s.facts.terminalFacts(), attempt, attempt.terminalEvidence(), cmd, s.responsePipeline.billingEvidenceFallback(), cmd == sdkterminal.CommandNormalFinish, s.facts.billingCallState)
		s.terminal.handoffBillingTurn(cctx, s.facts.terminalFacts(), cmd)
		return nil
	}
	return s.terminal.terminalizeSnapshot(ctx, cmd, attempt, s.responsePipeline.accumulatorSnapshot(), wrapped, requestAfter)
}

func testTerminalizeAttempt(s *retryRecvStream, ctx context.Context, cmd sdkterminal.Command, effects func(context.Context) error) coreterm.Result {
	if s == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	attempt := s.attempt.snapshot()
	return testTerminalizeAttemptForAttempt(s, ctx, cmd, attempt, effects)
}

func testTerminalizeAttemptForAttempt(s *retryRecvStream, ctx context.Context, cmd sdkterminal.Command, attempt *attemptSession, effects func(context.Context) error) coreterm.Result {
	if s == nil || attempt == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	return attempt.terminalizeSnapshot(ctx, cmd, s.responsePipeline.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		var err error
		if effects != nil {
			err = effects(cctx)
		}
		s.terminal.recordBillingLegForAttempt(cctx, s.facts.terminalFacts(), attempt, attempt.terminalEvidence(), cmd, s.responsePipeline.billingEvidenceFallback(), cmd == sdkterminal.CommandNormalFinish, s.facts.billingCallState)
		return err
	})
}

func testTurnTerminalize(turn *turnTerminal, ctx context.Context, cmd sdkterminal.Command, attempt *attemptSession, snapshot func() coreterm.AccumulatorSnapshot, effects func(context.Context, coreterm.Outcome) error) coreterm.Result {
	var evidence coreterm.AccumulatorSnapshot
	if snapshot != nil {
		evidence = snapshot()
	}
	return turn.terminalizeSnapshot(ctx, cmd, attempt, evidence, effects, nil)
}
