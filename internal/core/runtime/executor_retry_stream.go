package runtime

// Stream lifecycle helpers (loadInner, storeInner, Close, etc.) and the
// recv-phase support surface (stream-evidence
// seam, traffic emission) for retryRecvStream. Response/event evidence and
// completion-gate, logical-tool, and response-observation state live in
// responsePipeline. The inner-loop control (Recv and tryReplacementIteration) has been extracted
// to executor_recv_loop.go; the retryRecvStream type itself, its error
// sentinel, and the lipapi.EventStream interface assertion remain here.

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// retryRecvStream is the small recv-phase EventStream facade: it wraps a
// backend stream and coordinates the five lifetime owners for failover and
// terminal completion without owning their state.
//
// Concurrency: one goroutine calls Recv until completion (lipapi.EventStream). Close may run
// concurrently with Recv blocked on the active inner stream; Close forwards to that attempt stream
// and does not clear the attempt pointer. Recv clears the attempt stream on cancellation and
// recoverable-recv teardown paths.
// Recv must not be called concurrently from multiple goroutines; the stream is not multi-Recv-safe.
type retryRecvStream struct {
	// facts is the sole request-lifetime receive authority. It contains only
	// immutable request facts; retry, event, terminal, attempt, and lock state
	// remains owned by cohesive collaborators.
	facts recvTurnFacts

	responsePipeline *responsePipeline

	attempt  attemptSlot
	terminal *turnTerminal
	recovery *recoveryController
}

var _ lipapi.EventStream = (*retryRecvStream)(nil)

var errNilRetryRecvStream = errors.New("runtime: nil retryRecvStream")

type idleContextDeadline struct {
	active bool
	parent context.Context
}

func (d idleContextDeadline) expired(_ context.Context, err error) bool {
	return d.active && d.parent != nil && d.parent.Err() == nil && errors.Is(err, context.DeadlineExceeded)
}

func cancelAndCloseInner(ctx context.Context, c lipapi.ManagedEventStream, cause leglifecycle.CancelCause, logger *slog.Logger) {
	if c == nil {
		return
	}
	_ = c.Cancel(ctx, cause)
	if cerr := c.Close(); cerr != nil && logger != nil {
		logger.DebugContext(ctx, "retry_recv inner stream close", "reason", string(cause.Kind), "error", cerr)
	}
}

func lifecycleAttempt(stream lipapi.EventStream) leglifecycle.BLegAttempt {
	if stream == nil {
		return nil
	}
	if managed, ok := stream.(leglifecycle.BLegAttempt); ok {
		return managed
	}
	return lipapi.CloseOnlyManagedStream{Stream: stream}
}

func (s *retryRecvStream) Close() error {
	if s == nil {
		return nil
	}
	current := s.attempt.closePublicationAndSnapshot()
	s.responsePipeline.clearAttemptState(s.attempt.snapshot())
	var c lipapi.ManagedEventStream
	if current != nil {
		c = current.detachStream()
	}
	// lipapi.EventStream.Close has no caller context. Project a detached
	// request context from immutable facts; no mutable context cache belongs on
	// the EventStream facade.
	ctx := s.responsePipeline.withDecisionEvidence(s.facts.projectContext(context.Background(), s.responsePipeline.log), s.terminal)
	if c == nil {
		s.terminal.closeWithoutInner(ctx, s.facts.terminalFacts(), current, s.responsePipeline)
		s.terminal.endALeg(aLegEndBase)
		return nil
	}
	if !s.terminal.finished() {
		_ = s.terminal.cancelForClose(ctx, c)
		s.terminal.closeWithInner(ctx, s.facts.terminalFacts(), current, s.responsePipeline)
		if s.terminal != nil && s.terminal.hasALeg() {
			s.terminal.endALeg(aLegEndBase)
			return nil
		}
	}
	s.terminal.endALeg(aLegEndBase)
	return s.terminal.closeBackend(ctx, s.facts.terminalFacts(), current, s.responsePipeline, c)
}

func gateBufHasCommittedOutput(buf []lipapi.Event) bool {
	return slices.ContainsFunc(buf, lipapi.OutputCommitted)
}
