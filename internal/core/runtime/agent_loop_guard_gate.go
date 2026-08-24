package runtime

import (
	"context"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/continuationsafety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// LoopGuard wraps a stopgate.Gate for runtime wiring.
// Nil value means guard fully disabled.
type LoopGuard = loopguardRuntime

type loopguardRuntime struct {
	gate *stopgate.Gate
}

// newLoopGuardForTest builds an enabled guard with the supplied verifier and default test bounds.
func newLoopGuardForTest(verifier stopguard.Verifier) *loopguardRuntime {
	gate := stopgate.New(stopgate.Ports{Verifier: verifier, Now: time.Now}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: 3,
		NoProgressLimit:          2,
	})
	return &loopguardRuntime{gate: gate}
}

// newLoopGuardWithGate wraps an existing gate. Nil gate returns nil.
func newLoopGuardWithGate(gate *stopgate.Gate) *loopguardRuntime {
	if gate == nil {
		return nil
	}
	return &loopguardRuntime{gate: gate}
}

// NewLoopGuard wraps an existing gate for production composition.
func NewLoopGuard(gate *stopgate.Gate) *LoopGuard {
	if gate == nil {
		return nil
	}
	return &LoopGuard{gate: gate}
}

// ObserveCandidate delegates to the underlying gate for wiring tests.
func (g *LoopGuard) ObserveCandidate(ctx context.Context, tf stopgate.TerminalFacts) stopgate.Outcome {
	if g == nil || g.gate == nil {
		return stopgate.Outcome{Action: stopguard.ActionForwardTerminal, HoldReleased: true, Reason: "nil guard"}
	}
	return g.gate.ObserveCandidate(ctx, tf)
}

const guardContinuationPendingReason = "guard_continuation_pending_6_3"

// settleSwallowedBAttempt settles the B-attempt as swallowed for a held CONTINUE without re-invoking verifier.
func (t *turnTerminal) settleSwallowedBAttempt(ctx context.Context, attempt *attemptSession) {
	if attempt == nil {
		return
	}
	attempt.TerminalizeAttempt(ctx, IntentSwallowedFailure, attemptEvidence{
		Command:       sdkterminal.CommandSwallowedAttempt,
		LegOutcome:    billing.LegOutcomeSwallowed,
		ObsOutcome:    response.OutcomeReplaced,
		RecordOutcome: lipapi.AttemptSwallowedFailure,
		RecordReason:  guardContinuationPendingReason,
		TraceID:       attempt.traceID,
		ALegID:        attempt.bleg.ALegID,
		StartedAt:     attempt.accounting.requestStartedAt,
	})
}

// guardHeldFallback handles a held CONTINUE: settles B-attempt, logs, creates controlled fallback and finishes A-side per source.
// It is the single helper for all four recv chokepoints to avoid duplication and preserve source-specific A-leg semantics.
func (t *turnTerminal) guardHeldFallback(ctx context.Context, attempt *attemptSession, p *responsePipeline, source, reason string) lipapi.Event {
	t.settleSwallowedBAttempt(ctx, attempt)
	if t != nil && t.log != nil {
		r := guardContinuationPendingReason
		if strings.TrimSpace(reason) != "" {
			r = boundGuardReason(reason + " " + guardContinuationPendingReason)
		}
		t.log.DebugContext(ctx, "agent_loop_guard_hold", "source", boundGuardReason(source), "reason", boundGuardReason(r))
	}
	fallback := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: guardContinuationPendingReason}
	t.finishResponse(p, attempt)
	switch source {
	case "dispatch_nongated", "recovery_drain":
		t.endALeg(aLegEndBase)
	case "dispatch_gated", "gate_drain":
		// no A-leg end for gated
	default:
	}
	return fallback
}

func boundGuardReason(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

func (t *turnTerminal) isLoopGuardEnabled() bool {
	return t != nil && t.loopGuard != nil && t.loopGuard.gate != nil
}

// agentLoopGuardHoldCandidate consults the guard for a backend clean response_finished candidate.
// nil-guard fast path returns held=false. Builds minimal TerminalFacts and calls ObserveCandidate.
// Returns held=true only when Outcome.Action==continue_leg && !HoldReleased.
func (t *turnTerminal) agentLoopGuardHoldCandidate(ctx context.Context, facts requestTerminalFacts, attempt *attemptSession, p *responsePipeline, ev lipapi.Event) (held bool, releaseReason string) {
	if !t.isLoopGuardEnabled() {
		return false, ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	candidate := stopguard.Candidate{
		Cause:              stopguard.CauseNormalEnd,
		OutputCommitted:    t.committed(),
		ExplicitCompletion: false,
	}
	tf := stopgate.TerminalFacts{
		Candidate:            candidate,
		Tail:                 continuationsafety.TailState{},
		Prior:                continuationsafety.PriorSummary{},
		Bounds:               lipcont.Bounds{},
		SafeNativeResume:     false,
		SuppressVerification: false,
	}
	outcome := t.loopGuard.gate.ObserveCandidate(ctx, tf)
	if outcome.Action == stopguard.ActionContinueLeg && !outcome.HoldReleased {
		return true, boundGuardReason(outcome.Reason)
	}
	return false, boundGuardReason(outcome.Reason)
}

// finishResponseGuarded is the single chokepoint wrapper for backend clean-finish terminal claims.
// When guard holds (continue_leg without HoldReleased) it withholds finishResponse/endALeg and logs a bounded debug diagnostic.
// It also settles the current B-side attempt exactly once via CAS so a replayed finish cannot re-settle (Req 9.1).
// Otherwise it executes the original finish sequence for the given source.
func (t *turnTerminal) finishResponseGuarded(ctx context.Context, facts requestTerminalFacts, attempt *attemptSession, p *responsePipeline, ev lipapi.Event, source string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	held, reason := t.agentLoopGuardHoldCandidate(ctx, facts, attempt, p, ev)
	if held {
		if attempt != nil {
			// Settle B-attempt exactly once; dedupe via attempt terminal CAS + attemptLogged atomic.
			// Use swallowed intent to keep billing/authority consistent while A-side stays open.
			attempt.TerminalizeAttempt(ctx, IntentSwallowedFailure, attemptEvidence{
				Command:       sdkterminal.CommandSwallowedAttempt,
				LegOutcome:    billing.LegOutcomeSwallowed,
				ObsOutcome:    response.OutcomeReplaced,
				RecordOutcome: lipapi.AttemptSwallowedFailure,
				RecordReason:  guardContinuationPendingReason,
				TraceID:       attempt.traceID,
				ALegID:        attempt.bleg.ALegID,
				StartedAt:     attempt.accounting.requestStartedAt,
			})
		}
		if t != nil && t.log != nil {
			r := guardContinuationPendingReason
			if strings.TrimSpace(reason) != "" {
				r = boundGuardReason(reason + " " + guardContinuationPendingReason)
			}
			t.log.DebugContext(ctx, "agent_loop_guard_hold", "source", boundGuardReason(source), "reason", boundGuardReason(r))
		}
		return false
	}
	if t == nil {
		return false
	}
	if t.isInterleavedThinker() {
		clearAttemptToolState(p, attempt)
		return true
	}
	finished := t.finishResponse(p, attempt)
	switch source {
	case "dispatch_nongated", "recovery_drain":
		t.endALeg(aLegEndBase)
	case "dispatch_gated", "gate_drain":
		// no A-leg end
	default:
		// default to finish only; callers that need A-leg end use explicit source
	}
	_ = finished
	return true
}

var _ = lipapi.EventResponseFinished
