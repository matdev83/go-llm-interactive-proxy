package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// retryRecvStream wraps a backend stream and performs recv-phase failover within attempt budget.
type retryRecvStream struct {
	executor *Executor
	bus      *hooks.Bus
	// baseline is the post-submit immutable logical client request (per-attempt state derives via CloneCall).
	baseline lipapi.Call
	budget   *attemptBudget

	aLegID   string
	traceID  string
	sel      *routing.Selector
	session  *routing.SessionRoutingState
	excluded map[string]struct{}
	rng      routing.Rng

	lastHardReject lipapi.NegotiationResult

	inner     lipapi.EventStream
	bleg      b2bua.BLegRecord
	cand      routing.AttemptCandidate
	committed bool
	finished  bool
}

var _ lipapi.EventStream = (*retryRecvStream)(nil)

var errNilRetryRecvStream = errors.New("runtime: nil retryRecvStream")

// recvHookMeta returns identifiers for response-path hooks after B-leg allocation.
func (s *retryRecvStream) recvHookMeta() (sdk.PartMeta, sdk.ToolMeta) {
	pm := sdk.PartMeta{
		TraceID:    s.traceID,
		ALegID:     s.aLegID,
		BLegID:     s.bleg.BLegID,
		AttemptSeq: s.bleg.Seq,
	}
	tm := sdk.ToolMeta{
		TraceID:    s.traceID,
		ALegID:     s.aLegID,
		BLegID:     s.bleg.BLegID,
		AttemptSeq: s.bleg.Seq,
	}
	return pm, tm
}

func (s *retryRecvStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s == nil {
		return lipapi.Event{}, errNilRetryRecvStream
	}
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if s.finished {
		return lipapi.Event{}, io.EOF
	}
	ctx = diag.WithCallDiag(ctx, s.traceID, s.aLegID)
	for {
		for s.inner == nil {
			opened, err := s.tryReplacementIteration(ctx)
			if err != nil {
				return lipapi.Event{}, err
			}
			if !opened {
				return stream.DefaultKeepaliveEvent(), nil
			}
		}
		ev, err := s.inner.Recv(ctx)
		if err == nil {
			pm, tm := s.recvHookMeta()
			if te, ok := lipapi.ToolEventFromEvent(ev); ok {
				res := s.bus.ApplyToolReactors(ctx, te, tm)
				if res.Err != nil {
					return lipapi.Event{}, res.Err
				}
				if !res.Emit {
					continue
				}
				if res.Event.Kind != "" {
					ev = lipapi.MergeToolEventInto(ev, res.Event)
				}
			}
			evp := ev
			if herr := s.bus.RunResponsePartHooks(ctx, &evp, pm); herr != nil {
				return lipapi.Event{}, herr
			}
			ev = evp
			if lipapi.OutputCommitted(ev) {
				s.committed = true
			}
			if ev.Kind == lipapi.EventResponseFinished {
				_ = s.executor.recordAttempt(ctx, recordAttemptParams{
					ALegID:  s.aLegID,
					BLeg:    s.bleg,
					Cand:    s.cand,
					Outcome: lipapi.AttemptSuccess,
				})
				s.finished = true
			}
			return ev, nil
		}
		if errors.Is(err, io.EOF) {
			if !s.finished {
				_ = s.executor.recordAttempt(ctx, recordAttemptParams{
					ALegID:  s.aLegID,
					BLeg:    s.bleg,
					Cand:    s.cand,
					Outcome: lipapi.AttemptSurfacedFailure,
					Reason:  "stream ended without response_finished",
				})
			}
			s.finished = true
			return lipapi.Event{}, io.EOF
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			reason := err.Error()
			if reason == "" {
				reason = "cancelled"
			}
			_ = s.executor.recordAttempt(ctx, recordAttemptParams{
				ALegID:  s.aLegID,
				BLeg:    s.bleg,
				Cand:    s.cand,
				Outcome: lipapi.AttemptCancelled,
				Reason:  reason,
			})
			_ = s.inner.Close()
			s.inner = nil
			s.finished = true
			return lipapi.Event{}, err
		}
		if s.committed || !lipapi.IsRecoverablePreOutput(err) {
			surfErr := err
			if s.committed && lipapi.IsRecoverablePreOutput(err) {
				surfErr = &lipapi.UpstreamFailure{
					Phase:        lipapi.PhasePostOutput,
					Recoverable:  false,
					Reason:       err.Error(),
					CandidateKey: s.cand.Key,
				}
			}
			_ = s.executor.recordAttempt(ctx, recordAttemptParams{
				ALegID:  s.aLegID,
				BLeg:    s.bleg,
				Cand:    s.cand,
				Outcome: lipapi.AttemptSurfacedFailure,
				Reason:  surfErr.Error(),
			})
			return lipapi.Event{}, surfErr
		}
		diag.LogDecision(ctx, s.executor.Log, "recoverable_pre_output_swallowed", diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID},
			slog.String("candidate_key", s.cand.Key),
			slog.String("phase", "recv"),
		)
		_ = s.executor.recordAttempt(ctx, recordAttemptParams{
			ALegID:  s.aLegID,
			BLeg:    s.bleg,
			Cand:    s.cand,
			Outcome: lipapi.AttemptSwallowedFailure,
			Reason:  "recoverable pre-output (recv)",
		})
		_ = s.inner.Close()
		s.inner = nil
		s.excluded[s.cand.Key] = struct{}{}
	}
}

// tryReplacementIteration performs one planning + open attempt for recv-phase failover.
// It returns opened=true when s.inner is ready, opened=false when the caller should emit
// a keepalive (Req 5.5) and invoke Recv again, or a non-nil error when the replacement path is exhausted.
func (s *retryRecvStream) tryReplacementIteration(ctx context.Context) (opened bool, err error) {
	ctx = diag.WithCallDiag(ctx, s.traceID, s.aLegID)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	out, err := s.executor.tryPlanOpenOnce(attemptOpenParams{
		ctx:         ctx,
		bus:         s.bus,
		traceID:     s.traceID,
		aLegID:      s.aLegID,
		baseline:    s.baseline,
		sel:         s.sel,
		session:     s.session,
		excluded:    s.excluded,
		rng:         s.rng,
		budget:      s.budget,
		isRetryPath: true,
		lastReject:  &s.lastHardReject,
	})
	if err != nil {
		return false, err
	}
	if !out.opened {
		return false, nil
	}
	s.inner = out.stream
	s.bleg = out.bleg
	s.cand = out.cand
	return true, nil
}

func (s *retryRecvStream) Close() error {
	if s == nil {
		return nil
	}
	if s.inner != nil {
		return s.inner.Close()
	}
	return nil
}
