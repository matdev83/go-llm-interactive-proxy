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
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	metering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
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

type parallelLeg struct {
	billingCallState *billingCallState
	cand             routing.AttemptCandidate
	bleg             b2bua.BLegRecord
	stream           lipapi.ManagedEventStream
	authority        authorityLifecycle
	delay            time.Duration
	startedAt        time.Time
	recvErr          error
	observedUsage    atomic.Value // lipapi.Event
	interleaved      interleavedstate.State
	memoUpdate       *interleavedthinking.PendingMemoUpdate
	tx               *attemptTx
	managedByMain    bool
	mainDone         bool
}

func releaseBLegs(scope *leglifecycle.ALeg, legs []*parallelLeg) {
	if scope == nil {
		return
	}
	for _, leg := range legs {
		scope.ReleaseBLeg(leg.bleg.BLegID)
	}
}

func (e *Executor) releaseLosers(ctx context.Context, aScope *leglifecycle.ALeg, legs []*parallelLeg) error {
	err := cancelLosers(ctx, legs)
	for _, leg := range legs {
		usage, _ := leg.observedUsage.Load().(lipapi.Event)
		if leg.tx != nil {
			leg.tx.Rollback(ctx, sdkterminal.CommandParallelLoser, authorityapp.ReleaseKindLosing, billing.LegOutcomeFailed, usage)
		} else if leg.authority.control != nil {
			committed := leg.authority.outputCommitted != nil && leg.authority.outputCommitted.Load()
			e.recordParallelBillingLeg(ctx, leg, usage, sdkterminal.CommandParallelLoser, committed)
			_ = terminalizeAttemptEphemeral(ctx, sdkterminal.CommandParallelLoser, committed, func(cctx context.Context) error {
				leg.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindLosing, usage)
				if leg.authority.backendAttempted != nil && leg.authority.backendAttempted.Load() {
					e.emitBackendEgressMeteringFact(cctx, leg.bleg.BLegID, metering.AttemptOutcomeLoser, metering.SurfacedNo, usage)
				}
				return nil
			})
		}
	}
	releaseBLegs(aScope, legs)
	return err
}

func (e *Executor) tryOpenParallelGroup(
	ctx context.Context,
	req openNextRequest,
	candidates []routing.AttemptCandidate,
	nextCycle *interleavedstate.CycleState,
	stickyBackendID string,
	stickyBinding bool,
) (openedAttempt, error) {
	var zero openedAttempt
	interleaved := req.interleaved
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
		entries[i] = legEntry{cand: c, startDelay: maxHandicap - c.Handicap}
	}
	slices.SortStableFunc(entries, func(a, b legEntry) int {
		return cmp.Compare(a.startDelay, b.startDelay)
	})
	if req.progress.budget != nil {
		limited := make([]legEntry, 0, len(entries))
		for _, entry := range entries {
			if !req.progress.budget.tryAcquire() {
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
		req.interleaved = interleaved
	}
	var (
		mu        sync.Mutex
		winnerIdx = -1
		winnerBuf []lipapi.Event
		legs      = make([]parallelLeg, len(entries))
		wg        sync.WaitGroup
		fatalErr  error
	)
	defer func() {
		mu.Lock()
		for i := range legs {
			legs[i].mainDone = true
		}
		mu.Unlock()
	}()
	winnerCh := make(chan struct{}, 1)
	raceCtx, raceCancel := context.WithCancel(ctx)
	defer raceCancel()
	fastForwardCh := make(chan struct{})
	var fastForwardOnce sync.Once
	broadcastFastForward := func() {
		fastForwardOnce.Do(func() { close(fastForwardCh) })
	}
	for i, entry := range entries {
		legs[i] = parallelLeg{billingCallState: req.reqFacts.billingCallState, cand: entry.cand, delay: entry.startDelay}
	}
	for idx, entry := range entries {
		wg.Add(1)
		go func(idx int, entry legEntry) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					pe := safety.Capture(safety.BoundaryBackend, "parallel_race_leg", r)
					e.logParallelRacePanic(ctx, pe, "executor: isolated panic in parallel race leg", diag.AttrOpts{CallID: req.reqFacts.traceID})
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
			legReq := req
			legReq.reqFacts.suppressThinker = true
			legReq.reqFacts.suppressVisibleMemo = true
			legReq.interleaved = interleaved
			legProgress := *req.progress
			legProgress.budget = nil
			legReq.progress = &legProgress
			plan := candidatePlan{
				cand:            entry.cand,
				nextCycle:       nil,
				stickyBackendID: stickyBackendID,
				stickyBinding:   stickyBinding,
			}
			evalOutcome, err := e.evaluateCandidate(ctx, legReq.reqFacts, legReq.routeFacts, plan, legReq.interleaved)
			if err != nil {
				mu.Lock()
				if winnerIdx < 0 {
					fatalErr = errors.Join(fatalErr, err)
				}
				mu.Unlock()
				raceCancel()
				return
			}
			if !evalOutcome.accepted {
				mu.Lock()
				e.applyRejection(req.progress.getFailures(), entry.cand, evalOutcome.rejection)
				req.progress.excluded[entry.cand.Key] = struct{}{}
				mu.Unlock()
				if entry.cand.Handicap > 0 {
					broadcastFastForward()
				}
				return
			}
			tx, err := e.startAttemptTx(ctx, legReq.reqFacts, legReq.routeFacts, entry.cand, nil, legReq.progress.getFailures())
			if err != nil {
				mu.Lock()
				if winnerIdx < 0 {
					fatalErr = errors.Join(fatalErr, err)
				}
				mu.Unlock()
				raceCancel()
				return
			}
			defer func() {
				mu.Lock()
				published := legs[idx].tx != nil
				managed := legs[idx].managedByMain || winnerIdx >= 0
				mainDone := legs[idx].mainDone
				mu.Unlock()
				if published && !managed && mainDone && !tx.completed {
					tx.Rollback(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeNeverStarted, emptyOperatorUsageShell())
				} else if !published && !tx.completed {
					tx.Rollback(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeNeverStarted, emptyOperatorUsageShell())
				}
			}()
			err = e.openAttemptTx(ctx, tx, evalOutcome, plan)
			if tx.completed {
				mu.Lock()
				req.progress.excluded[entry.cand.Key] = struct{}{}
				mu.Unlock()
				if entry.cand.Handicap > 0 {
					broadcastFastForward()
				}
				return
			}
			if err != nil {
				mu.Lock()
				if winnerIdx < 0 {
					fatalErr = errors.Join(fatalErr, err)
				}
				mu.Unlock()
				raceCancel()
				if entry.cand.Handicap > 0 {
					broadcastFastForward()
				}
				return
			}
			if legReq.reqFacts.aScope != nil {
				if err := legReq.reqFacts.aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{
					ID:      tx.bleg.BLegID,
					Attempt: lifecycleAttempt(tx.stream),
				}); err != nil {
					tx.stream = nil
					tx.Rollback(ctx, sdkterminal.CommandBackendOpenFailure, authorityapp.ReleaseKindLosing, billing.LegOutcomeFailed, emptyOperatorUsageShell())
					mu.Lock()
					if winnerIdx < 0 {
						fatalErr = errors.Join(fatalErr, err)
					}
					mu.Unlock()
					raceCancel()
					return
				}
				tx.registered = true
			}
			legCtx := raceCtx
			var legCancel context.CancelFunc = func() {}
			ttftDeadline := ttftContextDeadline{}
			if req.progress.ttft != nil {
				legCtx, legCancel, ttftDeadline = req.progress.ttft.scopedContext(raceCtx, e.now(), entry.cand.Key, entry.cand.Primary.TTFTTimeout)
			}
			defer legCancel()
			mu.Lock()
			legs[idx].stream = tx.stream
			legs[idx].bleg = tx.bleg
			legs[idx].authority = tx.authLifecycle
			legs[idx].tx = tx
			legs[idx].interleaved = interleaved
			legs[idx].memoUpdate = evalOutcome.shapeRes.MemoUpdate
			legs[idx].startedAt = e.now()
			if winnerIdx >= 0 {
				mu.Unlock()
				return
			}
			mu.Unlock()
			var preBuf []lipapi.Event
			for {
				ev, err := tx.stream.Recv(legCtx)
				if err != nil {
					observed := operatorUsageOrShell(preBuf)
					mu.Lock()
					legs[idx].observedUsage.Store(observed)
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
					observed := operatorUsageOrShell(preBuf)
					mu.Lock()
					legs[idx].observedUsage.Store(observed)
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
		}(idx, entry)
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				pe := safety.Capture(safety.BoundaryWorker, "parallel_race_waiter", r)
				e.logParallelRacePanic(ctx, pe, "executor: isolated panic in parallel race waiter", diag.AttrOpts{CallID: req.reqFacts.traceID})
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
		cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
		defer cleanupCancel()
		mu.Lock()
		openedLegs := make([]*parallelLeg, 0, len(legs))
		for i := range legs {
			if legs[i].stream != nil {
				legs[i].managedByMain = true
				openedLegs = append(openedLegs, &legs[i])
			}
		}
		mu.Unlock()
		if len(openedLegs) > 0 {
			_ = e.releaseLosers(cleanupCtx, req.reqFacts.aScope, openedLegs)
		}
		return zero, ctx.Err()
	}
	wg.Wait()
	mu.Lock()
	fatal := fatalErr
	winner := winnerIdx
	mu.Unlock()
	if fatal != nil {
		cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
		defer cleanupCancel()
		mu.Lock()
		for i := range legs {
			if legs[i].stream != nil {
				legs[i].managedByMain = true
				usage, _ := legs[i].observedUsage.Load().(lipapi.Event)
				if usage.Kind == "" {
					usage = emptyOperatorUsageShell()
				}
				legs[i].tx.Rollback(cleanupCtx, sdkterminal.CommandParallelLoser, authorityapp.ReleaseKindLosing, billing.LegOutcomeFailed, usage)
			}
		}
		mu.Unlock()
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
				ALegID:    req.reqFacts.aLegID,
				BLeg:      legs[i].bleg,
				Cand:      legs[i].cand,
				Outcome:   lipapi.AttemptSwallowedFailure,
				Reason:    attemptReasonDetail(failure),
				DetailErr: failure,
			}, diag.AttrOpts{CallID: req.reqFacts.traceID, BLegID: legs[i].bleg.BLegID})
			failedLegs = append(failedLegs, &legs[i])
		}
		if len(failedLegs) > 0 {
			cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
			defer cleanupCancel()
			if cerr := e.releaseLosers(cleanupCtx, req.reqFacts.aScope, failedLegs); cerr != nil {
				parallelFailure = errors.Join(parallelFailure, cerr)
			}
		}
		if skipped := len(candidates) - len(legs); skipped > 0 {
			parallelFailure = errors.Join(parallelFailure,
				fmt.Errorf("parallel race skipped %d leg(s) due max-attempt budget", skipped))
		}
		if parallelFailure == nil {
			parallelFailure = errors.New("parallel race failed without winner")
		}
		parallelFailure = fmt.Errorf("executor: parallel race arm failed: %w", parallelFailure)
		failures := req.progress.getFailures()
		failures.ParallelFailure = parallelFailure
		if req.progress.excluded == nil {
			req.progress.excluded = map[string]struct{}{}
		}
		for _, c := range candidates {
			req.progress.excluded[c.Key] = struct{}{}
		}
		return openedAttempt{interleaved: interleaved}, nil
	}
	committedInterleaved, err := e.commitMemoInjection(ctx, req.reqFacts.aLegID, legs[winner].interleaved, legs[winner].memoUpdate)
	if err != nil {
		cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
		defer cleanupCancel()
		var toClean []*parallelLeg
		for i := range legs {
			if legs[i].stream != nil {
				toClean = append(toClean, &legs[i])
			}
		}
		cleanupErr := e.releaseLosers(cleanupCtx, req.reqFacts.aScope, toClean)
		return zero, errors.Join(err, cleanupErr)
	}
	legs[winner].interleaved = committedInterleaved
	if legs[winner].memoUpdate == nil && cycleAdvance != nil {
		if err := e.persistInterleavedState(ctx, req.reqFacts.aLegID, legs[winner].interleaved); err != nil {
			cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
			defer cleanupCancel()
			var toClean []*parallelLeg
			for i := range legs {
				if legs[i].stream != nil {
					toClean = append(toClean, &legs[i])
				}
			}
			cleanupErr := e.releaseLosers(cleanupCtx, req.reqFacts.aScope, toClean)
			return zero, errors.Join(err, cleanupErr)
		}
	}
	mu.Lock()
	var losers []*parallelLeg
	for i := range legs {
		if i == winner {
			continue
		}
		if legs[i].stream != nil {
			legs[i].managedByMain = true
			if legs[i].recvErr != nil && !errors.Is(legs[i].recvErr, context.Canceled) &&
				!errors.Is(legs[i].recvErr, context.DeadlineExceeded) {
				e.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    req.reqFacts.aLegID,
					BLeg:      legs[i].bleg,
					Cand:      legs[i].cand,
					Outcome:   lipapi.AttemptSwallowedFailure,
					Reason:    attemptReasonDetail(legs[i].recvErr),
					DetailErr: legs[i].recvErr,
				}, diag.AttrOpts{CallID: req.reqFacts.traceID, BLegID: legs[i].bleg.BLegID})
			} else {
				e.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    req.reqFacts.aLegID,
					BLeg:      legs[i].bleg,
					Cand:      legs[i].cand,
					Outcome:   lipapi.AttemptCancelled,
					Reason:    "parallel race loser",
					DetailErr: context.Canceled,
				}, diag.AttrOpts{CallID: req.reqFacts.traceID, BLegID: legs[i].bleg.BLegID})
			}
			losers = append(losers, &legs[i])
		}
	}
	mu.Unlock()
	var losersDone <-chan error
	if len(losers) > 0 {
		done := make(chan error, 1)
		losersDone = done
		go func() {
			var cleanupErr error
			defer func() {
				if r := recover(); r != nil {
					pe := safety.Capture(safety.BoundaryBackend, "parallel_cancel_losers", r)
					e.logParallelRacePanic(ctx, pe, "executor: isolated panic in parallel race loser cleanup", diag.AttrOpts{CallID: req.reqFacts.traceID})
					cleanupErr = errors.Join(cleanupErr, fmt.Errorf("parallel race loser cleanup panic"))
				}
				done <- cleanupErr
				close(done)
			}()
			cancelCtx, cancel := detachedCleanupContext(ctx, cancelLosersTimeout)
			defer cancel()
			cleanupErr = e.releaseLosers(cancelCtx, req.reqFacts.aScope, losers)
		}()
	}
	winnerSession := legs[winner].tx.Handoff()
	winnerSession.storeInner(&parallelBridgeStream{
		winner:           &legs[winner],
		buf:              winnerBuf,
		losersDone:       losersDone,
		loserCleanupWait: cancelLosersTimeout,
	})
	return openedAttempt{
		session:     winnerSession,
		interleaved: legs[winner].interleaved,
		memoUpdate:  nil,
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
		if res.Err != nil && !errors.Is(res.Err, context.Canceled) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("candidate %q cancel loser stream: %w", l.cand.Key, res.Err))
		}
		if l.tx == nil {
			if err := l.stream.Close(); err != nil && !errors.Is(err, context.Canceled) {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("candidate %q close loser stream: %w", l.cand.Key, err))
			}
		}
	}
	return cleanupErr
}

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
	if s.losersDone != nil {
		wait := cmp.Or(s.loserCleanupWait, cancelLosersTimeout)
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
