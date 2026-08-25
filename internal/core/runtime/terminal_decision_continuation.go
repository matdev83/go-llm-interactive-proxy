package runtime

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview/sdkadapter"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/steering"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

const (
	continuationOverlayID     = "terminal-decision-continuation"
	continuationPendingReason = "terminal_decision_continuation_pending"
)

// runContinuationTransaction is the single publication boundary for a
// semantic continuation. All fallible B2 work happens while B1 remains the
// current attempt. Once the slot accepts B2, B1 is only settled as a detached
// prior attempt; no post-publication error can roll the slot back.
func runContinuationTransaction(ctx context.Context, t *turnTerminal, s *retryRecvStream, intent terminaldecision.ContinuationIntent) (bool, error) {
	return continuationTransactionWithOverlay(ctx, t, s, intent, continuationOverlayID)
}

func continuationTransactionWithOverlay(ctx context.Context, t *turnTerminal, s *retryRecvStream, intent terminaldecision.ContinuationIntent, overlayID string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	failure := func(stage string, cause error) error {
		return continuationPrePublicationFailureWithOverlay(ctx, t, s, stage, cause, overlayID)
	}
	if t == nil || s == nil || s.responsePipeline == nil {
		return false, errors.New("continuation: invalid transaction")
	}
	if err := intent.Validate(); err != nil {
		return false, failure("invalid intent", err)
	}
	if !t.supportsContinuation {
		return false, failure("unsupported continuation", nil)
	}
	b1 := s.attempt.snapshot()
	if b1 == nil {
		return false, failure("missing current attempt", nil)
	}
	if err := continuationContextError(ctx, t); err != nil {
		return false, failure("canceled before preparation", err)
	}

	aLegID := s.facts.aLegID
	steeringStore := t.steeringStore
	if steeringStore == nil {
		return false, failure("steering unavailable", nil)
	}
	if s.recovery == nil || s.recovery.opener == nil {
		return false, failure("continuation admission unavailable", nil)
	}

	resolver := func(rctx context.Context) (lipapi.Call, conversationview.Snapshot, error) {
		snap := s.facts.conversationSnapshot
		if snap.StateRevision == 0 && t.conversationReader != nil {
			if current, err := t.conversationReader.Snapshot(rctx, aLegID); err == nil {
				snap = current
			}
		}
		ingress := s.facts.ingressCall
		if len(ingress.Items) == 0 && len(ingress.Messages) == 0 {
			ingress = s.facts.baseline
		}
		return ingress, snap, nil
	}
	writer, err := sdkadapter.NewWriterWithObserver(steeringStore, aLegID, resolver, t.conversationObserver)
	if err != nil {
		return false, failure("steering unavailable", err)
	}
	_, err = writer.Put(ctx, steering.PutRequest{
		OverlayID: steering.OverlayID(overlayID),
		Message: steering.Message{
			Role: lipapi.RoleDeveloper,
			Text: intent.Instruction,
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              steering.ReasonCode(intent.ReasonCode),
	})
	if err != nil {
		return false, failure("steering persistence failed", err)
	}

	reader := t.conversationReader
	if reader == nil {
		if candidate, ok := conversationview.AsReader(steeringStore); ok {
			reader = candidate
		}
	}
	if reader == nil {
		return false, failure("conversation reader unavailable", nil)
	}
	snapN1, err := reader.Snapshot(ctx, aLegID)
	if err != nil {
		return false, failure("conversation snapshot failed", err)
	}
	ingress := s.facts.ingressCall
	if len(ingress.Items) == 0 && len(ingress.Messages) == 0 {
		ingress = s.facts.baseline
	}
	projectedBaseline, projection, err := conversationview.Project(ingress, snapN1)
	if err != nil {
		if observer := t.conversationObserver; observer != nil {
			conversationview.SafeObserver(observer).OnProjectionFailure(conversationview.StageEarly)
		}
		return false, failure("conversation materialization failed", err)
	}
	filteredBaseline, err := conversationview.FilterNeverBackend(ingress, snapN1)
	if err != nil {
		return false, failure("conversation materialization failed", err)
	}
	newFacts := s.facts.clone()
	newFacts.baseline = projectedBaseline
	newFacts.conversationSnapshot = snapN1
	if projection != nil {
		newFacts.conversationProvenance = projection.Provenance
	}
	newFacts.conversationFilteredBaseline = filteredBaseline
	newFacts.ingressCall = s.facts.ingressCall
	newFacts.continuationIntent = nextContinuationIntentFacts(s.facts.continuationIntent, b1, intent)
	if err := continuationContextError(ctx, t); err != nil {
		return false, failure("canceled before admission", err)
	}

	out, openErr := s.recovery.openContinuation(ctx, replacementOpenRequest{
		facts:       newFacts.terminalFacts(),
		pinnedFacts: newFacts,
		recovery:    s.recovery.openSnapshot(),
		prior:       priorAttemptOutcome{attempt: b1, retired: true},
		isRetryPath: false,
		interleaved: s.recovery.interleaved,
	})
	if openErr != nil || !out.opened || out.ready == nil {
		if out.ready != nil {
			out.ready.Dispose(ctx, continuationCause(openErr, context.Canceled))
		}
		return false, failure("continuation admission failed", openErr)
	}
	ready := out.ready
	if err := t.registerReplacement(ctx, out, ready); err != nil {
		ready.Dispose(ctx, err)
		return false, failure("continuation registration failed", err)
	}
	if ready.state != readyStatePrepared {
		if err := ready.Prepare(ctx, newFacts, s.responsePipeline, t.committed()); err != nil {
			ready.Dispose(ctx, err)
			return false, failure("continuation preparation failed", err)
		}
	}
	if err := continuationContextError(ctx, t); err != nil {
		ready.Dispose(ctx, err)
		return false, failure("canceled before publication", err)
	}
	_, published := s.attempt.swapIfOpen(ready)
	if !published {
		ready.Dispose(ctx, errors.New("continuation publication closed"))
		return false, failure("continuation publication closed", nil)
	}
	// B2 is a continuation of the same client-facing logical response. The
	// backend may emit a fresh response/message prefix, but those lifecycle
	// markers are not a second response and must not reach the frontend.
	s.responsePipeline.markContinuationLifecyclePending()

	// The slot is now irrevocably B2. Publish the new view before touching B1.
	s.facts.conversationSnapshot = snapN1
	if projection != nil {
		s.facts.conversationProvenance = projection.Provenance
	}
	s.facts.conversationFilteredBaseline = filteredBaseline
	s.facts.continuationIntent = newFacts.continuationIntent
	t.markCommitted(b1)
	settled := b1.TerminalizeAttempt(ctx, IntentSwallowedFailure, attemptEvidence{
		Command:       sdkterminal.CommandSwallowedAttempt,
		LegOutcome:    billing.LegOutcomeSwallowed,
		ObsOutcome:    response.OutcomeReplaced,
		RecordOutcome: lipapi.AttemptSwallowedFailure,
		RecordReason:  continuationPendingReason,
		TraceID:       b1.traceID,
		ALegID:        b1.bleg.ALegID,
		StartedAt:     b1.accounting.requestStartedAt,
	})
	if !settled.Result.Won || settled.Result.Err != nil || !continuationSettlementSucceeded(b1) {
		return true, errors.New("continuation: prior attempt settlement unavailable")
	}
	return true, nil
}

func nextContinuationIntentFacts(prior continuationIntentFacts, b1 *attemptSession, intent terminaldecision.ContinuationIntent) continuationIntentFacts {
	attempt := prior.attempt
	if !prior.set && b1 != nil && b1.bleg.Seq > 0 {
		if b1.bleg.Seq >= 1<<8 {
			attempt = ^uint8(0)
		} else {
			attempt = uint8(b1.bleg.Seq)
		}
	}
	if attempt < ^uint8(0) {
		attempt++
	}
	return continuationIntentFacts{
		trajectoryRef: intent.TrajectoryRef,
		controlRef:    intent.ControlRef,
		attempt:       attempt,
		set:           true,
	}
}

func continuationSettlementSucceeded(attempt *attemptSession) bool {
	if attempt == nil || attempt.authority.control == nil {
		return true
	}
	attempt.authority.control.mu.Lock()
	defer attempt.authority.control.mu.Unlock()
	return attempt.authority.control.terminal == authorityTerminalSettled
}

// Keep the transaction's terminal diagnostic deliberately classified. Upstream
// and persistence details are useful for logs at their owning boundary, but
// must not become a continuation control-plane payload.
func continuationPrePublicationFailureWithOverlay(ctx context.Context, t *turnTerminal, s *retryRecvStream, stage string, cause error, overlayID string) error {
	if overlayID == continuationOverlayID {
		_ = deactivateContinuationOverlay(ctx, t, s.facts.aLegID)
	} else {
		_ = deactivateContinuationOverlay(ctx, t, s.facts.aLegID)
	}
	cmd := sdkterminal.CommandNormalFinish
	if errors.Is(cause, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		cmd = sdkterminal.CommandCancel
	}
	request := s.facts.terminalFacts()
	t.claimRequestTerminal(ctx, cmd, coreterm.NewAccumulatorSnapshot(nil, t.committed()), func(cctx context.Context, _ coreterm.Outcome) error {
		t.settleOrReleaseRequestAuthority(cctx, s.responsePipeline, request)
		t.handoffBillingTurn(cctx, request, cmd)
		t.finishResponse(s.responsePipeline, s.attempt.snapshot())
		return nil
	})
	if errors.Is(cause, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("continuation: " + stage)
}

func deactivateContinuationOverlay(ctx context.Context, t *turnTerminal, aLegID string) error {
	if t == nil || t.steeringStore == nil || aLegID == "" {
		return nil
	}
	deactCtx, cancel := cleanupContext(ctx, defaultAuthorityCleanupTimeout)
	defer cancel()
	_, err := t.steeringStore.DeactivateSteering(deactCtx, aLegID, continuationOverlayID)
	if errors.Is(err, conversationview.ErrOverlayNotFound) || errors.Is(err, conversationview.ErrALegNotFound) {
		return nil
	}
	return err
}

func continuationContextError(ctx context.Context, t *turnTerminal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.finished() || (t.hasALeg() && t.aLegErr() != nil) {
		return errors.New("continuation terminal closed")
	}
	return nil
}

func continuationCause(err, fallback error) error {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
	}
	return fallback
}
