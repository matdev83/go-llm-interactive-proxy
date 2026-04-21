package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
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
	// Rand supplies weighted routing rolls. Common implementations (*math/rand.Rand)
	// are not safe for concurrent use; rng() wraps a non-nil Rand accordingly.
	Rand routing.Rng
	Now  func() time.Time
	// Log, when non-nil, receives structured orchestration decisions (diag.LogDecision).
	Log *slog.Logger

	rngOnce    sync.Once
	lockedRand routing.Rng // lazy: mutex-serialized view of Rand
}

var deterministicNow = time.Unix(1715620000, 0).UTC()

type lockedRng struct {
	mu   sync.Mutex
	base routing.Rng
}

func (l *lockedRng) Intn(n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.base.Intn(n)
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return deterministicNow
}

func (e *Executor) rng() routing.Rng {
	if e.Rand != nil {
		e.rngOnce.Do(func() {
			e.lockedRand = &lockedRng{base: e.Rand}
		})
		return e.lockedRand
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
	bus := e.Bus
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	if err := call.Validate(); err != nil {
		return nil, err
	}
	work := *call
	traceID := strings.TrimSpace(work.ID)
	if traceID == "" {
		traceID = diag.StableCallID(&work)
	}
	work.ID = traceID
	ctx = diag.WithCallDiag(ctx, traceID, "")
	if err := bus.RunSubmit(ctx, &work, nil); err != nil {
		return nil, err
	}
	aLeg, err := e.resolveALeg(ctx, work.Session)
	if err != nil {
		return nil, err
	}
	ctx = diag.WithCallDiag(ctx, traceID, aLeg.ALegID)
	sel, err := routing.Parse(work.Route.Selector)
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
		req := lipapi.RequiredCapabilities(work)
		be, ok := e.Backends[c.Primary.Backend]
		if !ok {
			return nil, fmt.Errorf("executor: unknown backend %q", c.Primary.Backend)
		}
		res := lipapi.Negotiate(req, be.Caps)
		if res.Kind == lipapi.NegotiationReject {
			lastReject = res
			diag.LogDecision(ctx, e.Log, "capability_reject", diag.AttrOpts{CallID: traceID},
				slog.String("decision", "exclude_candidate"),
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			excluded[c.Key] = struct{}{}
			continue
		}
		lastReject = lipapi.NegotiationResult{}
		if res.Kind == lipapi.NegotiationDowngrade {
			diag.LogDecision(ctx, e.Log, "capability_downgrade", diag.AttrOpts{CallID: traceID},
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			lipapi.ApplyNegotiatedDowngrades(&work, res)
		}
		if err := bus.RunRequestPartHooks(ctx, &work, sdk.PartMeta{}); err != nil {
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
		openCall, err := backendCallWithRouteParams(work, c)
		if err != nil {
			return nil, fmt.Errorf("executor: %w", err)
		}
		stream, err := be.Open(ctx, openCall, c)
		if err != nil {
			if lipapi.IsRecoverablePreOutput(err) {
				_ = e.recordAttempt(ctx, aLeg.ALegID, bleg, c, lipapi.AttemptSwallowedFailure, "recoverable pre-output (open)")
				diag.LogDecision(ctx, e.Log, "recoverable_pre_output_swallowed", diag.AttrOpts{CallID: traceID, BLegID: bleg.BLegID},
					slog.String("candidate_key", c.Key),
					slog.String("phase", "open"),
				)
				excluded[c.Key] = struct{}{}
				continue
			}
			_ = e.recordAttempt(ctx, aLeg.ALegID, bleg, c, lipapi.AttemptSurfacedFailure, err.Error())
			return nil, err
		}
		diag.LogDecision(ctx, e.Log, "backend_stream_opened", diag.AttrOpts{CallID: traceID, BLegID: bleg.BLegID},
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
			slog.String("model", c.Primary.Model),
		)
		return &retryRecvStream{
			executor: e,
			bus:      bus,
			call:     &work,
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

func backendCallWithRouteParams(work lipapi.Call, cand routing.AttemptCandidate) (lipapi.Call, error) {
	merged, err := lipapi.MergeRouteQueryIntoGenerationOptions(work.Options, cand.Primary.Params)
	if err != nil {
		return lipapi.Call{}, fmt.Errorf("route generation options: %w", err)
	}
	work.Options = merged
	return work, nil
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
	bus      *hooks.Bus
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
			if te, ok := lipapi.ToolEventFromEvent(ev); ok {
				res := s.bus.ApplyToolReactors(ctx, te, sdk.ToolMeta{})
				if !res.Emit {
					continue
				}
				if res.Event.Kind != "" {
					ev = lipapi.MergeToolEventInto(ev, res.Event)
				}
			}
			evp := ev
			if herr := s.bus.RunResponsePartHooks(ctx, &evp, sdk.PartMeta{}); herr != nil {
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
		diag.LogDecision(ctx, s.executor.Log, "recoverable_pre_output_swallowed", diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID},
			slog.String("candidate_key", s.cand.Key),
			slog.String("phase", "recv"),
		)
		_ = s.executor.recordAttempt(ctx, s.aLegID, s.bleg, s.cand, lipapi.AttemptSwallowedFailure, "recoverable pre-output (recv)")
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
	list, err := routing.ExpandFailover(s.sel, routing.PlanOptions{
		Excluded:   s.excluded,
		Session:    s.session,
		Rand:       s.rng,
		IsRetryPath: true,
	})
	if err != nil {
		if errors.Is(err, routing.ErrNoEligibleCandidate) && s.lastHardReject.Kind == lipapi.NegotiationReject {
			return false, s.lastHardReject.Err()
		}
		return false, err
	}
	c := list[0]
	work := *s.call
	req := lipapi.RequiredCapabilities(work)
	be, ok := s.executor.Backends[c.Primary.Backend]
	if !ok {
		return false, fmt.Errorf("executor: unknown backend %q", c.Primary.Backend)
	}
	res := lipapi.Negotiate(req, be.Caps)
	if res.Kind == lipapi.NegotiationReject {
		s.lastHardReject = res
		diag.LogDecision(ctx, s.executor.Log, "capability_reject", diag.AttrOpts{CallID: s.traceID},
			slog.String("decision", "exclude_candidate"),
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
		)
		s.excluded[c.Key] = struct{}{}
		return false, nil
	}
	s.lastHardReject = lipapi.NegotiationResult{}
	if res.Kind == lipapi.NegotiationDowngrade {
		diag.LogDecision(ctx, s.executor.Log, "capability_downgrade", diag.AttrOpts{CallID: s.traceID},
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
		)
		lipapi.ApplyNegotiatedDowngrades(&work, res)
	}
	if err := s.bus.RunRequestPartHooks(ctx, &work, sdk.PartMeta{}); err != nil {
		return false, err
	}
	if c.MarkedFirst {
		if err := s.executor.Store.SetWeightedFirstConsumed(ctx, s.aLegID, true); err != nil {
			return false, err
		}
		s.session.FirstRequestConsumed = true
	}
	bleg, err := s.executor.Store.NextBLeg(ctx, s.aLegID)
	if err != nil {
		return false, err
	}
	openCall, err := backendCallWithRouteParams(work, c)
	if err != nil {
		return false, err
	}
	nextStream, err := be.Open(ctx, openCall, c)
	if err != nil {
		if lipapi.IsRecoverablePreOutput(err) {
			_ = s.executor.recordAttempt(ctx, s.aLegID, bleg, c, lipapi.AttemptSwallowedFailure, "recoverable pre-output (open)")
			diag.LogDecision(ctx, s.executor.Log, "recoverable_pre_output_swallowed", diag.AttrOpts{CallID: s.traceID, BLegID: bleg.BLegID},
				slog.String("candidate_key", c.Key),
				slog.String("phase", "open"),
			)
			s.excluded[c.Key] = struct{}{}
			return false, nil
		}
		_ = s.executor.recordAttempt(ctx, s.aLegID, bleg, c, lipapi.AttemptSurfacedFailure, err.Error())
		return false, err
	}
	diag.LogDecision(ctx, s.executor.Log, "backend_stream_opened", diag.AttrOpts{CallID: s.traceID, BLegID: bleg.BLegID},
		slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend),
		slog.String("model", c.Primary.Model),
	)
	s.inner = nextStream
	s.bleg = bleg
	s.cand = c
	return true, nil
}

func (s *retryRecvStream) Close() error {
	if s.inner != nil {
		return s.inner.Close()
	}
	return nil
}
