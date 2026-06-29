package runtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const cancelLosersTimeout = 5 * time.Second

func (e *Executor) logParallelRacePanic(ctx context.Context, pe *safety.PanicError, message string, attrOpts diag.AttrOpts) {
	if e == nil || e.Log == nil || pe == nil {
		return
	}
	attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{AttrOpts: attrOpts})
	attrs = diag.AppendIsolatedCrashStack(attrs, pe)
	e.Log.LogAttrs(ctx, slog.LevelError, message, attrs...)
}

func selectorHasParallelArm(sel *routing.Selector) bool {
	if sel == nil {
		return false
	}
	for _, alt := range sel.Alternatives {
		if alt.Parallel != nil {
			return true
		}
	}
	return false
}

type parallelLeg struct {
	cand        routing.AttemptCandidate
	bleg        b2bua.BLegRecord
	stream      lipapi.ManagedEventStream
	delay       time.Duration
	recvErr     error
	interleaved interleavedstate.State
	memoUpdate  *interleavedthinking.PendingMemoUpdate
}

func releaseBLegs(scope *leglifecycle.ALeg, legs []*parallelLeg) {
	if scope == nil {
		return
	}
	for _, leg := range legs {
		scope.ReleaseBLeg(leg.bleg.BLegID)
	}
}

func (e *Executor) tryOpenParallelGroup(
	p attemptOpenParams,
	candidates []routing.AttemptCandidate,
	nextCycle *interleavedstate.CycleState,
	stickyBackendID string,
	stickyBinding bool,
) (attemptOpenResult, error) {
	var zero attemptOpenResult
	ctx := p.ctx
	interleaved := p.interleaved
	cycleAdvance := nextCycle
	maxHandicap := time.Duration(0)
	for _, c := range candidates {
		if c.Handicap > maxHandicap {
			maxHandicap = c.Handicap
		}
	}

	type legEntry struct {
		cand       routing.AttemptCandidate
		startDelay time.Duration
	}
	entries := make([]legEntry, len(candidates))
	for i, c := range candidates {
		entries[i] = legEntry{
			cand:       c,
			startDelay: maxHandicap - c.Handicap,
		}
	}
	slices.SortStableFunc(entries, func(a, b legEntry) int {
		return cmp.Compare(a.startDelay, b.startDelay)
	})

	if p.budget != nil {
		limited := make([]legEntry, 0, len(entries))
		for _, entry := range entries {
			if !p.budget.tryAcquire() {
				break
			}
			limited = append(limited, entry)
		}
		if len(limited) == 0 {
			return zero, fmt.Errorf("executor: %w", lipapi.ErrMaxRouteAttempts)
		}
		entries = limited
	}
	if cycleAdvance != nil {
		interleaved.Cycle = *cycleAdvance
		if err := e.persistInterleavedState(ctx, p.aLegID, interleaved); err != nil {
			return zero, fmt.Errorf("executor: persist interleaved cycle: %w", err)
		}
		p.interleaved = interleaved
	}

	var (
		mu        sync.Mutex
		winnerIdx = -1
		winnerBuf []lipapi.Event
		legs      = make([]parallelLeg, len(entries))
		wg        sync.WaitGroup
		fatalErr  error
	)
	winnerCh := make(chan struct{}, 1)

	raceCtx, raceCancel := context.WithCancel(ctx)
	defer raceCancel()

	// Broadcast channel: closed once by any handicapped leg that fails terminally,
	// waking all goroutines waiting in their handicap delay simultaneously.
	fastForwardCh := make(chan struct{})
	var fastForwardOnce sync.Once

	broadcastFastForward := func() {
		fastForwardOnce.Do(func() { close(fastForwardCh) })
	}

	for i, entry := range entries {
		legs[i] = parallelLeg{cand: entry.cand, delay: entry.startDelay}
	}

	for idx, entry := range entries {
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					pe := safety.Capture(safety.BoundaryBackend, "parallel_race_leg", r)
					e.logParallelRacePanic(ctx, pe, "executor: isolated panic in parallel race leg", diag.AttrOpts{CallID: p.traceID})
				}
			}()

			if entry.startDelay > 0 {
				timer := time.NewTimer(entry.startDelay)
				select {
				case <-timer.C:
				case <-raceCtx.Done():
					timer.Stop()
					return
				case <-fastForwardCh:
					timer.Stop()
				}
			}

			mu.Lock()
			if winnerIdx >= 0 {
				mu.Unlock()
				return
			}
			mu.Unlock()

			legParams := p
			legParams.excluded = map[string]struct{}{}
			legParams.lastReject = nil
			legParams.lastTransportReject = nil
			legParams.isContextLimitExhaustion = nil
			legParams.deferMemoInjectionCommit = true
			// Parallel legs reserve attempt-budget slots before launch to avoid racy over-open.
			legParams.budget = nil

			out, err := e.openPlannedCandidate(legParams, entry.cand, nil, stickyBackendID, stickyBinding)
			if err != nil {
				if isParallelFatalErr(err) {
					stopRace := false
					mu.Lock()
					if winnerIdx < 0 {
						fatalErr = errors.Join(fatalErr, err)
						stopRace = true
					}
					mu.Unlock()
					if stopRace {
						raceCancel()
						select {
						case winnerCh <- struct{}{}:
						default:
						}
					}
				}
				if entry.cand.Handicap > 0 {
					broadcastFastForward()
				}
				return
			}
			if !out.opened {
				if entry.cand.Handicap > 0 {
					broadcastFastForward()
				}
				return
			}
			if p.aScope != nil {
				if err := p.aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{
					ID:      out.bleg.BLegID,
					Attempt: lifecycleAttempt(out.stream),
				}); err != nil {
					if !errors.Is(err, leglifecycle.ErrALegCanceled) {
						_ = out.stream.Close()
					}
					stopRace := false
					mu.Lock()
					if winnerIdx < 0 {
						fatalErr = errors.Join(fatalErr, err)
						stopRace = true
					}
					mu.Unlock()
					if stopRace {
						raceCancel()
						select {
						case winnerCh <- struct{}{}:
						default:
						}
					}
					return
				}
			}

			legCtx := raceCtx
			var legCancel context.CancelFunc = func() {}
			ttftDeadline := ttftContextDeadline{}
			if p.ttft != nil {
				legCtx, legCancel, ttftDeadline = p.ttft.scopedContext(raceCtx, e.now(), entry.cand.Key, entry.cand.Primary.TTFTTimeout)
			}
			defer legCancel()

			mu.Lock()
			legs[idx].stream = out.stream
			legs[idx].bleg = out.bleg
			legs[idx].interleaved = out.interleaved
			legs[idx].memoUpdate = out.memoUpdate
			if winnerIdx >= 0 {
				mu.Unlock()
				return
			}
			mu.Unlock()

			var preBuf []lipapi.Event
			for {
				ev, err := out.stream.Recv(legCtx)
				if err != nil {
					mu.Lock()
					if winnerIdx < 0 {
						if ttftDeadline.expired(legCtx, err) {
							if ttftDeadline.scope == ttftTimeoutGlobal {
								if fatalErr == nil {
									fatalErr = lipapi.ErrTTFTTimeout
								}
								mu.Unlock()
								raceCancel()
								select {
								case winnerCh <- struct{}{}:
								default:
								}
								return
							}
							legs[idx].recvErr = ttftFailure(ttftDeadline.scope, entry.cand.Key)
						} else {
							legs[idx].recvErr = err
						}
						if entry.cand.Handicap > 0 {
							broadcastFastForward()
						}
					}
					mu.Unlock()
					return
				}
				preBuf = append(preBuf, ev)
				if isWinningEvent(ev) {
					mu.Lock()
					if winnerIdx >= 0 {
						mu.Unlock()
						return
					}
					winnerIdx = idx
					winnerBuf = preBuf
					mu.Unlock()
					raceCancel()
					select {
					case winnerCh <- struct{}{}:
					default:
					}
					return
				}
			}
		})
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				pe := safety.Capture(safety.BoundaryWorker, "parallel_race_waiter", r)
				e.logParallelRacePanic(ctx, pe, "executor: isolated panic in parallel race waiter", diag.AttrOpts{CallID: p.traceID})
			}
		}()
		wg.Wait()
		select {
		case winnerCh <- struct{}{}:
		default:
		}
	}()

	select {
	case <-winnerCh:
	case <-ctx.Done():
		raceCancel()
		return zero, ctx.Err()
	}

	// Wait for all leg goroutines to finish so every opened stream is visible in the legs slice.
	wg.Wait()

	mu.Lock()
	fatal := fatalErr
	winner := winnerIdx
	mu.Unlock()

	if fatal != nil {
		return zero, fmt.Errorf("executor: parallel race aborted: %w", fatal)
	}

	if winner < 0 {
		var parallelFailure error
		var failedLegs []*parallelLeg
		for i := range legs {
			if legs[i].stream == nil {
				parallelFailure = errors.Join(parallelFailure,
					fmt.Errorf("candidate %q did not open a stream", legs[i].cand.Key))
				continue
			}
			failure := legs[i].recvErr
			if failure == nil {
				failure = errors.New("parallel leg ended before winner")
			}
			parallelFailure = errors.Join(parallelFailure,
				fmt.Errorf("candidate %q failed before winner: %w", legs[i].cand.Key, failure))
			e.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    p.aLegID,
				BLeg:      legs[i].bleg,
				Cand:      legs[i].cand,
				Outcome:   lipapi.AttemptSwallowedFailure,
				Reason:    attemptReasonDetail(failure),
				DetailErr: failure,
			}, diag.AttrOpts{CallID: p.traceID, BLegID: legs[i].bleg.BLegID})
			failedLegs = append(failedLegs, &legs[i])
		}
		if len(failedLegs) > 0 {
			cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
			defer cleanupCancel()
			if cerr := cancelLosers(cleanupCtx, failedLegs); cerr != nil {
				parallelFailure = errors.Join(parallelFailure, cerr)
			}
			releaseBLegs(p.aScope, failedLegs)
		}
		if skipped := len(candidates) - len(legs); skipped > 0 {
			parallelFailure = errors.Join(parallelFailure,
				fmt.Errorf("parallel race skipped %d leg(s) due max-attempt budget", skipped))
		}
		if parallelFailure == nil {
			parallelFailure = errors.New("parallel race failed without winner")
		}
		parallelFailure = fmt.Errorf("executor: parallel race arm failed: %w", parallelFailure)
		if p.lastParallelFailure != nil {
			*p.lastParallelFailure = parallelFailure
		}
		for _, c := range candidates {
			p.excluded[c.Key] = struct{}{}
		}
		return attemptOpenResult{interleaved: interleaved}, nil
	}
	if p.lastParallelFailure != nil {
		*p.lastParallelFailure = nil
	}
	committedInterleaved, err := e.commitMemoInjection(ctx, p.aLegID, legs[winner].interleaved, legs[winner].memoUpdate)
	if err != nil {
		cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
		defer cleanupCancel()
		var toClean []*parallelLeg
		for i := range legs {
			if legs[i].stream != nil {
				toClean = append(toClean, &legs[i])
			}
		}
		cleanupErr := cancelLosers(cleanupCtx, toClean)
		releaseBLegs(p.aScope, toClean)
		return zero, errors.Join(err, cleanupErr)
	}
	legs[winner].interleaved = committedInterleaved

	var losers []*parallelLeg
	for i := range legs {
		if i == winner {
			continue
		}
		if legs[i].stream != nil {
			if legs[i].recvErr != nil && !errors.Is(legs[i].recvErr, context.Canceled) &&
				!errors.Is(legs[i].recvErr, context.DeadlineExceeded) {
				e.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    p.aLegID,
					BLeg:      legs[i].bleg,
					Cand:      legs[i].cand,
					Outcome:   lipapi.AttemptSwallowedFailure,
					Reason:    attemptReasonDetail(legs[i].recvErr),
					DetailErr: legs[i].recvErr,
				}, diag.AttrOpts{CallID: p.traceID, BLegID: legs[i].bleg.BLegID})
			} else {
				e.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    p.aLegID,
					BLeg:      legs[i].bleg,
					Cand:      legs[i].cand,
					Outcome:   lipapi.AttemptCancelled,
					Reason:    "parallel race loser",
					DetailErr: context.Canceled,
				}, diag.AttrOpts{CallID: p.traceID, BLegID: legs[i].bleg.BLegID})
			}
			losers = append(losers, &legs[i])
		}
	}

	var losersDone <-chan error
	if len(losers) > 0 {
		done := make(chan error, 1)
		losersDone = done
		go func() {
			var cleanupErr error
			defer func() {
				if r := recover(); r != nil {
					pe := safety.Capture(safety.BoundaryBackend, "parallel_cancel_losers", r)
					e.logParallelRacePanic(ctx, pe, "executor: isolated panic in parallel race loser cleanup", diag.AttrOpts{CallID: p.traceID})
					cleanupErr = errors.Join(cleanupErr, fmt.Errorf("parallel race loser cleanup panic"))
				}
				done <- cleanupErr
				close(done)
			}()
			cancelCtx, cancel := detachedCleanupContext(ctx, cancelLosersTimeout)
			defer cancel()
			cleanupErr = cancelLosers(cancelCtx, losers)
			releaseBLegs(p.aScope, losers)
		}()
	}

	return attemptOpenResult{
		opened:     true,
		registered: p.aScope != nil,
		stream: &parallelBridgeStream{
			winner:           &legs[winner],
			buf:              winnerBuf,
			losersDone:       losersDone,
			loserCleanupWait: cancelLosersTimeout,
		},
		bleg:        legs[winner].bleg,
		cand:        legs[winner].cand,
		interleaved: legs[winner].interleaved,
	}, nil
}

func isParallelFatalErr(err error) bool {
	return errors.Is(err, lipapi.ErrMaxRouteAttempts) ||
		errors.Is(err, lipapi.ErrTTFTTimeout)
}

func isWinningEvent(ev lipapi.Event) bool {
	switch ev.Kind {
	case lipapi.EventTextDelta:
		return strings.TrimSpace(ev.Delta) != ""
	case lipapi.EventReasoningDelta:
		return strings.TrimSpace(ev.Delta) != ""
	default:
		return false
	}
}

func detachedCleanupContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	base := parent
	if base == nil {
		base = context.Background()
	} else {
		base = context.WithoutCancel(base)
	}
	if timeout <= 0 {
		return base, func() {}
	}
	return context.WithTimeout(base, timeout)
}

func cancelLosers(ctx context.Context, losers []*parallelLeg) error {
	var cleanupErr error
	for _, l := range losers {
		if l.stream == nil {
			continue
		}
		res := l.stream.Cancel(ctx, leglifecycle.CancelCause{
			Kind:   leglifecycle.CancelRaceLoser,
			Detail: "parallel race loser",
		})
		if res.Err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("candidate %q cancel loser stream: %w", l.cand.Key, res.Err))
		}
		if err := l.stream.Close(); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("candidate %q close loser stream: %w", l.cand.Key, err))
		}
	}
	return cleanupErr
}

// parallelBridgeStream bridges a winning B-leg stream to the A-leg after the race.
type parallelBridgeStream struct {
	winner           *parallelLeg
	buf              []lipapi.Event
	bufIdx           int
	finished         atomic.Bool
	losersDone       <-chan error
	loserCleanupWait time.Duration
}

func (s *parallelBridgeStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.finished.Load() {
		return lipapi.Event{}, io.EOF
	}
	idx := s.bufIdx
	if idx < len(s.buf) {
		ev := s.buf[idx]
		s.bufIdx++
		if ev.Kind == lipapi.EventResponseFinished {
			s.finished.Store(true)
		}
		return ev, nil
	}
	if s.winner.stream == nil {
		s.finished.Store(true)
		return lipapi.Event{}, io.EOF
	}
	ev, err := s.winner.stream.Recv(ctx)
	if err != nil {
		s.finished.Store(true)
		return lipapi.Event{}, err
	}
	if ev.Kind == lipapi.EventResponseFinished {
		s.finished.Store(true)
	}
	return ev, nil
}

func (s *parallelBridgeStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	if s.winner != nil && s.winner.stream != nil {
		s.finished.Store(true)
		return s.winner.stream.Cancel(ctx, cause)
	}
	return lipapi.CancelResult{}
}

func (s *parallelBridgeStream) Close() error {
	var closeErr error
	// Wait for loser cancellation to finish before returning, bounded by loserCleanupWait.
	if s.losersDone != nil {
		wait := s.loserCleanupWait
		if wait <= 0 {
			wait = cancelLosersTimeout
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case err, ok := <-s.losersDone:
			if ok && err != nil {
				closeErr = errors.Join(closeErr, err)
			}
		case <-timer.C:
			closeErr = errors.Join(closeErr, fmt.Errorf("parallel race loser cleanup timed out after %s", wait))
		}
	}
	if s.winner != nil && s.winner.stream != nil {
		if err := s.winner.stream.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return closeErr
}
