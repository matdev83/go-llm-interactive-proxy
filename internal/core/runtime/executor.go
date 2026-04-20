package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// Backend opens a canonical event stream for one route candidate.
type Backend struct {
	Caps lipapi.BackendCaps
	Open func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error)
}

// Executor orchestrates hooks, capability negotiation, routing, B2BUA, and backend attempts.
type Executor struct {
	Store    b2bua.Store
	Bus      *hooks.Bus
	Backends map[string]Backend // key: routing.Primary.Backend (non-empty)
	Rand     routing.Rng
	Now      func() time.Time
	// Log, when non-nil, receives structured orchestration decisions (diag.LogDecision).
	Log *slog.Logger
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) rng() routing.Rng {
	if e.Rand != nil {
		return e.Rand
	}
	return rand.New(rand.NewSource(1))
}

// Execute runs submit hooks, resolves the A-leg, plans routes, negotiates per attempt,
// and returns a stream. Recoverable pre-output failures may consume additional B-legs
// before the returned stream yields events.
func (e *Executor) Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	if e == nil || e.Store == nil || call == nil {
		return nil, fmt.Errorf("executor: invalid arguments")
	}
	if e.Bus == nil {
		e.Bus = hooks.New(hooks.Config{})
	}
	if err := call.Validate(); err != nil {
		return nil, err
	}
	traceID := strings.TrimSpace(call.ID)
	if traceID == "" {
		traceID = diag.NewTraceID()
	}
	ctx = diag.WithTraceID(ctx, traceID)
	if err := e.Bus.RunSubmit(ctx, call, nil); err != nil {
		return nil, err
	}
	aLeg, err := e.resolveALeg(ctx, call.Session)
	if err != nil {
		return nil, err
	}
	ctx = diag.WithALeg(ctx, aLeg.ALegID)
	sel, err := routing.Parse(call.Route.Selector)
	if err != nil {
		return nil, err
	}
	session := &routing.SessionRoutingState{FirstRequestConsumed: aLeg.WeightedFirstConsumed}
	excluded := map[string]struct{}{}
	var lastReject lipapi.NegotiationResult

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		list, err := routing.ExpandFailover(sel, routing.PlanOptions{
			Excluded: excluded,
			Session:  session,
			Rand:     e.rng(),
		})
		if err != nil {
			if errors.Is(err, routing.ErrNoEligibleCandidate) && lastReject.Kind == lipapi.NegotiationReject {
				return nil, lastReject.Err()
			}
			if errors.Is(err, routing.ErrNoEligibleCandidate) {
				return nil, err
			}
			return nil, err
		}
		c := list[0]
		work := *call
		req := lipapi.RequiredCapabilities(work)
		be, ok := e.Backends[c.Primary.Backend]
		if !ok {
			return nil, fmt.Errorf("executor: unknown backend %q", c.Primary.Backend)
		}
		res := lipapi.Negotiate(req, be.Caps)
		if res.Kind == lipapi.NegotiationReject {
			lastReject = res
			diag.LogDecision(ctx, e.Log, "capability_reject", diag.AttrOpts{CallID: strings.TrimSpace(call.ID)},
				slog.String("decision", "exclude_candidate"),
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			excluded[c.Key] = struct{}{}
			continue
		}
		lastReject = lipapi.NegotiationResult{}
		if res.Kind == lipapi.NegotiationDowngrade {
			diag.LogDecision(ctx, e.Log, "capability_downgrade", diag.AttrOpts{CallID: strings.TrimSpace(call.ID)},
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			lipapi.ApplyNegotiatedDowngrades(&work, res)
		}
		if err := e.Bus.RunRequestPartHooks(ctx, &work, sdk.PartMeta{}); err != nil {
			return nil, err
		}
		if c.MarkedFirst {
			if err := e.Store.SetWeightedFirstConsumed(ctx, aLeg.ALegID, true); err != nil {
				return nil, err
			}
			session.FirstRequestConsumed = true
		}
		bleg, err := e.Store.NextBLeg(ctx, aLeg.ALegID)
		if err != nil {
			return nil, err
		}
		stream, err := be.Open(ctx, work, c)
		if err != nil {
			if lipapi.IsRecoverablePreOutput(err) {
				_ = e.recordAttempt(ctx, aLeg.ALegID, bleg, c, lipapi.AttemptSwallowedFailure, "recoverable pre-output (open)")
				diag.LogDecision(ctx, e.Log, "recoverable_pre_output_swallowed", diag.AttrOpts{CallID: strings.TrimSpace(call.ID), BLegID: bleg.BLegID},
					slog.String("candidate_key", c.Key),
					slog.String("phase", "open"),
				)
				excluded[c.Key] = struct{}{}
				continue
			}
			_ = e.recordAttempt(ctx, aLeg.ALegID, bleg, c, lipapi.AttemptSurfacedFailure, err.Error())
			return nil, err
		}
		diag.LogDecision(ctx, e.Log, "backend_stream_opened", diag.AttrOpts{CallID: strings.TrimSpace(call.ID), BLegID: bleg.BLegID},
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
			slog.String("model", c.Primary.Model),
		)
		return &retryRecvStream{
			executor: e,
			call:     call,
			aLegID:   aLeg.ALegID,
			traceID:  traceID,
			sel:      sel,
			session:  session,
			excluded: excluded,
			rng:      e.rng(),
			inner:    stream,
			bleg:     bleg,
			cand:     c,
		}, nil
	}
}

func (e *Executor) resolveALeg(ctx context.Context, sess lipapi.SessionRef) (b2bua.ALegRecord, error) {
	if sess.ALegID != "" {
		rec, err := e.Store.GetALeg(ctx, sess.ALegID)
		if err == nil {
			return rec, nil
		}
		if !errors.Is(err, b2bua.ErrALegNotFound) {
			return b2bua.ALegRecord{}, err
		}
	}
	if sess.ContinuityKey != "" {
		rec, err := e.Store.ResolveALeg(ctx, sess.ContinuityKey)
		if err == nil {
			return rec, nil
		}
		if !errors.Is(err, b2bua.ErrALegNotFound) {
			return b2bua.ALegRecord{}, err
		}
		return e.Store.CreateALeg(ctx, sess.ContinuityKey)
	}
	return e.Store.CreateALeg(ctx, "")
}

func (e *Executor) recordAttempt(ctx context.Context, aLegID string, bleg b2bua.BLegRecord, cand routing.AttemptCandidate, out lipapi.AttemptOutcome, reason string) error {
	now := e.now()
	rec := lipapi.AttemptRecord{
		BLegID:         bleg.BLegID,
		ALegID:         aLegID,
		Seq:            bleg.Seq,
		BackendID:      cand.Primary.Backend,
		EffectiveModel: cand.Primary.Model,
		StartedAt:      now,
		FinishedAt:     now,
		Outcome:        out,
		Reason:         reason,
	}
	// Store mutations must not be skipped when the request context is already canceled.
	return e.Store.RecordAttempt(context.WithoutCancel(ctx), rec)
}

type retryRecvStream struct {
	executor *Executor
	call     *lipapi.Call
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

func (s *retryRecvStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.finished {
		return lipapi.Event{}, io.EOF
	}
	ctx = diag.WithTraceID(ctx, s.traceID)
	ctx = diag.WithALeg(ctx, s.aLegID)
	for {
		if s.inner == nil {
			if err := s.openReplacement(ctx); err != nil {
				return lipapi.Event{}, err
			}
		}
		ev, err := s.inner.Recv(ctx)
		if err == nil {
			if te, ok := lipapi.ToolEventFromEvent(ev); ok {
				res := s.executor.Bus.ApplyToolReactors(ctx, te, sdk.ToolMeta{})
				if !res.Emit {
					continue
				}
				// Pass-through: original stream event is unchanged for Pass; Rewrite would need mapping.
			}
			evp := ev
			if herr := s.executor.Bus.RunResponsePartHooks(ctx, &evp, sdk.PartMeta{}); herr != nil {
				return lipapi.Event{}, herr
			}
			ev = evp
			if lipapi.OutputCommitted(ev) {
				s.committed = true
			}
			if ev.Kind == lipapi.EventResponseFinished {
				_ = s.executor.recordAttempt(ctx, s.aLegID, s.bleg, s.cand, lipapi.AttemptSuccess, "")
				s.finished = true
			}
			return ev, nil
		}
		if errors.Is(err, io.EOF) {
			if !s.finished {
				_ = s.executor.recordAttempt(ctx, s.aLegID, s.bleg, s.cand, lipapi.AttemptSurfacedFailure, "stream ended without response_finished")
			}
			s.finished = true
			return lipapi.Event{}, io.EOF
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			reason := err.Error()
			if reason == "" {
				reason = "cancelled"
			}
			_ = s.executor.recordAttempt(ctx, s.aLegID, s.bleg, s.cand, lipapi.AttemptCancelled, reason)
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
			_ = s.executor.recordAttempt(ctx, s.aLegID, s.bleg, s.cand, lipapi.AttemptSurfacedFailure, surfErr.Error())
			return lipapi.Event{}, surfErr
		}
		diag.LogDecision(ctx, s.executor.Log, "recoverable_pre_output_swallowed", diag.AttrOpts{CallID: strings.TrimSpace(s.call.ID), BLegID: s.bleg.BLegID},
			slog.String("candidate_key", s.cand.Key),
			slog.String("phase", "recv"),
		)
		_ = s.executor.recordAttempt(ctx, s.aLegID, s.bleg, s.cand, lipapi.AttemptSwallowedFailure, "recoverable pre-output (recv)")
		_ = s.inner.Close()
		s.inner = nil
		s.excluded[s.cand.Key] = struct{}{}
	}
}

func (s *retryRecvStream) openReplacement(ctx context.Context) error {
	ctx = diag.WithTraceID(ctx, s.traceID)
	ctx = diag.WithALeg(ctx, s.aLegID)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		list, err := routing.ExpandFailover(s.sel, routing.PlanOptions{
			Excluded: s.excluded,
			Session:  s.session,
			Rand:     s.rng,
		})
		if err != nil {
			if errors.Is(err, routing.ErrNoEligibleCandidate) && s.lastHardReject.Kind == lipapi.NegotiationReject {
				return s.lastHardReject.Err()
			}
			return err
		}
		c := list[0]
		work := *s.call
		req := lipapi.RequiredCapabilities(work)
		be, ok := s.executor.Backends[c.Primary.Backend]
		if !ok {
			return fmt.Errorf("executor: unknown backend %q", c.Primary.Backend)
		}
		res := lipapi.Negotiate(req, be.Caps)
		if res.Kind == lipapi.NegotiationReject {
			s.lastHardReject = res
			diag.LogDecision(ctx, s.executor.Log, "capability_reject", diag.AttrOpts{CallID: strings.TrimSpace(s.call.ID)},
				slog.String("decision", "exclude_candidate"),
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			s.excluded[c.Key] = struct{}{}
			continue
		}
		s.lastHardReject = lipapi.NegotiationResult{}
		if res.Kind == lipapi.NegotiationDowngrade {
			diag.LogDecision(ctx, s.executor.Log, "capability_downgrade", diag.AttrOpts{CallID: strings.TrimSpace(s.call.ID)},
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			lipapi.ApplyNegotiatedDowngrades(&work, res)
		}
		if err := s.executor.Bus.RunRequestPartHooks(ctx, &work, sdk.PartMeta{}); err != nil {
			return err
		}
		if c.MarkedFirst {
			if err := s.executor.Store.SetWeightedFirstConsumed(ctx, s.aLegID, true); err != nil {
				return err
			}
			s.session.FirstRequestConsumed = true
		}
		bleg, err := s.executor.Store.NextBLeg(ctx, s.aLegID)
		if err != nil {
			return err
		}
		stream, err := be.Open(ctx, work, c)
		if err != nil {
			if lipapi.IsRecoverablePreOutput(err) {
				_ = s.executor.recordAttempt(ctx, s.aLegID, bleg, c, lipapi.AttemptSwallowedFailure, "recoverable pre-output (open)")
				diag.LogDecision(ctx, s.executor.Log, "recoverable_pre_output_swallowed", diag.AttrOpts{CallID: strings.TrimSpace(s.call.ID), BLegID: bleg.BLegID},
					slog.String("candidate_key", c.Key),
					slog.String("phase", "open"),
				)
				s.excluded[c.Key] = struct{}{}
				continue
			}
			_ = s.executor.recordAttempt(ctx, s.aLegID, bleg, c, lipapi.AttemptSurfacedFailure, err.Error())
			return err
		}
		diag.LogDecision(ctx, s.executor.Log, "backend_stream_opened", diag.AttrOpts{CallID: strings.TrimSpace(s.call.ID), BLegID: bleg.BLegID},
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
			slog.String("model", c.Primary.Model),
		)
		s.inner = stream
		s.bleg = bleg
		s.cand = c
		return nil
	}
}

func (s *retryRecvStream) Close() error {
	if s.inner != nil {
		return s.inner.Close()
	}
	return nil
}
