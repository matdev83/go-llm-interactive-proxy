package runtime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
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
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

const cancelLosersTimeout = 5 * time.Second

func (e *Executor) logParallelRacePanic(ctx context.Context, pe *safety.PanicError, msg string, o diag.AttrOpts) {
	if e != nil && e.Log != nil && pe != nil {
		attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{AttrOpts: o})
		e.Log.LogAttrs(ctx, slog.LevelError, msg, diag.AppendIsolatedCrashStack(attrs, pe)...)
	}
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

func (e *Executor) releaseLosers(ctx context.Context, aScope *leglifecycle.ALeg, legs []*parallelLeg) error {
	var cleanupErr error
	for _, leg := range legs {
		usage, _ := leg.observedUsage.Load().(lipapi.Event)
		_outcome := lipapi.AttemptCancelled
		_reason := "parallel race loser"
		_detailErr := error(context.Canceled)
		if leg.recvErr != nil && !errors.Is(leg.recvErr, context.Canceled) && !errors.Is(leg.recvErr, context.DeadlineExceeded) {
			_outcome = lipapi.AttemptSwallowedFailure
			_reason = attemptReasonDetail(leg.recvErr)
			_detailErr = leg.recvErr
		}

		evidence := attemptEvidence{
			Command:       sdkterminal.CommandParallelLoser,
			ReleaseKind:   authorityapp.ReleaseKindLosing,
			LegOutcome:    billing.LegOutcomeFailed,
			Usage:         usage,
			Err:           _detailErr,
			RecordOutcome: _outcome,
			RecordReason:  _reason,
			CancelCause: &lipapi.CancelCause{
				Kind:   lipapi.CancelRaceLoser,
				Detail: "parallel race loser",
			},
		}

		if leg.tx != nil {
			tx := leg.tx
			if !tx.completed {
				evidence.TraceID = tx.reqFacts.traceID
				evidence.ALegID = tx.reqFacts.aLegID
				evidence.StartedAt = tx.openStartedAt
				res := tx.rollback(ctx, sdkterminal.CommandParallelLoser, evidence)
				if res.Result.Err != nil && !errors.Is(res.Result.Err, context.Canceled) {
					cleanupErr = errors.Join(cleanupErr, res.Result.Err)
				}
			}
		} else if leg.stream != nil || leg.authority.control != nil {
			sess := e.createSessionForParallelLeg(leg, aScope)
			res := sess.TerminalizeAttempt(ctx, IntentParallelLoser, evidence)
			if res.Result.Err != nil && !errors.Is(res.Result.Err, context.Canceled) {
				cleanupErr = errors.Join(cleanupErr, res.Result.Err)
			}
		}
	}
	return cleanupErr
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
	var maxHandicap time.Duration
	for _, c := range candidates {
		maxHandicap = max(maxHandicap, c.Handicap)
	}
	entries := make([]legEntry, len(candidates))
	for i, c := range candidates {
		entries[i] = legEntry{cand: c, delay: maxHandicap - c.Handicap}
	}
	slices.SortStableFunc(entries, func(a, b legEntry) int { return cmp.Compare(a.delay, b.delay) })
	if req.progress.budget != nil {
		var limited []legEntry
		for _, e := range entries {
			if !req.progress.budget.tryAcquire() {
				break
			}
			limited = append(limited, e)
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

	winnerCh := make(chan struct{}, 1)
	raceCtx, raceCancel := context.WithCancel(ctx)
	defer raceCancel()
	fastForwardCh := make(chan struct{})
	var fastForwardOnce sync.Once
	broadcastFastForward := func() {
		fastForwardOnce.Do(func() { close(fastForwardCh) })
	}

	// Immutable facts snapshotted before the loop to achieve worker isolation
	frozenReqFacts := req.reqFacts
	frozenRouteFacts := req.routeFacts
	frozenInterleaved := req.interleaved

	var frozenTTFT *ttftBudget
	if req.progress.ttft != nil {
		req.progress.ttft.mu.Lock()
		frozenTTFT = &ttftBudget{
			start:         req.progress.ttft.start,
			global:        req.progress.ttft.global,
			done:          req.progress.ttft.done,
			leafDeadlines: maps.Clone(req.progress.ttft.leafDeadlines),
		}
		req.progress.ttft.mu.Unlock()
	}

	outcomeCh := make(chan parallelArmOutcome, len(entries))
	var wg sync.WaitGroup

	for _, entry := range entries {
		wg.Add(1)
		go func(entry legEntry) {
			defer wg.Done()

			localHist := candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
			if entry.delay > 0 {
				timer := time.NewTimer(entry.delay)
				select {
				case <-timer.C:
				case <-raceCtx.Done():
					timer.Stop()
				case <-fastForwardCh:
					timer.Stop()
				}
			}

			workerReqFacts := frozenReqFacts
			workerReqFacts.suppressThinker, workerReqFacts.suppressVisibleMemo = true, true

			plan := candidatePlan{cand: entry.cand, stickyBackendID: stickyBackendID, stickyBinding: stickyBinding}

			evalOutcome, err := e.evaluateCandidate(ctx, workerReqFacts, frozenRouteFacts, plan, frozenInterleaved)
			if err != nil {
				outcomeCh <- parallelArmOutcome{
					cand:    entry.cand,
					failErr: err,
					delta:   parallelFailureDeltaFromHistory(localHist),
				}
				return
			}
			if !evalOutcome.accepted {
				outcomeCh <- parallelArmOutcome{
					cand:      entry.cand,
					rejected:  true,
					rejection: cloneCandidateRejection(evalOutcome.rejection),
					delta:     parallelFailureDeltaFromHistory(localHist),
				}
				return
			}

			tx, err := e.startAttemptTx(ctx, workerReqFacts, frozenRouteFacts, entry.cand, nil, &localHist)
			if err != nil {
				outcomeCh <- parallelArmOutcome{
					cand:    entry.cand,
					failErr: err,
					delta:   parallelFailureDeltaFromHistory(localHist),
				}
				return
			}

			defer func() {
				if r := recover(); r != nil {
					pe := safety.Capture(safety.BoundaryBackend, "parallel_race_leg", r)
					e.logParallelRacePanic(ctx, pe, "executor: isolated panic in parallel race leg", diag.AttrOpts{CallID: frozenReqFacts.traceID})
					rollbackAttemptTxOpenFailure(ctx, tx, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeNeverStarted)
				}
			}()

			err = e.openAttemptTx(ctx, tx, evalOutcome, plan)
			if tx.completed {
				outcomeCh <- parallelArmOutcome{
					cand:      entry.cand,
					completed: true,
					bleg:      tx.bleg,
					delta:     parallelFailureDeltaFromHistory(localHist),
				}
				return
			}
			if err != nil {
				rollbackAttemptTxOpenFailure(ctx, tx, authorityapp.ReleaseKindAdmissionFailure, billing.LegOutcomeNeverStarted)
				outcomeCh <- parallelArmOutcome{
					cand:    entry.cand,
					failErr: err,
					bleg:    tx.bleg,
					delta:   parallelFailureDeltaFromHistory(localHist),
				}
				return
			}
			if workerReqFacts.aScope != nil {
				if err := workerReqFacts.aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{
					ID:      tx.bleg.BLegID,
					Attempt: lifecycleAttempt(tx.stream),
				}); err != nil {
					tx.stream = nil
					rollbackAttemptTxOpenFailure(ctx, tx, authorityapp.ReleaseKindLosing, billing.LegOutcomeFailed)
					outcomeCh <- parallelArmOutcome{
						cand:    entry.cand,
						failErr: err,
						bleg:    tx.bleg,
						delta:   parallelFailureDeltaFromHistory(localHist),
					}
					return
				}
				tx.registered = true
			}

			armLeg := &parallelLeg{
				billingCallState: frozenReqFacts.billingCallState,
				cand:             entry.cand,
				bleg:             tx.bleg,
				stream:           tx.stream,
				authority:        tx.authLifecycle,
				delay:            entry.delay,
				interleaved:      frozenInterleaved,
				memoUpdate:       evalOutcome.shapeRes.MemoUpdate,
				tx:               tx,
				startedAt:        e.now(),
			}

			legCtx, legCancel, ttftDeadline := raceCtx, context.CancelFunc(func() {}), ttftContextDeadline{}
			if frozenTTFT != nil {
				workerTTFT := &ttftBudget{
					start:         frozenTTFT.start,
					global:        frozenTTFT.global,
					done:          frozenTTFT.done,
					leafDeadlines: maps.Clone(frozenTTFT.leafDeadlines),
				}
				legCtx, legCancel, ttftDeadline = workerTTFT.scopedContext(raceCtx, e.now(), entry.cand.Key, entry.cand.Primary.TTFTTimeout)
			}
			defer legCancel()

			select {
			case <-raceCtx.Done():
				outcomeCh <- parallelArmOutcome{
					cand:     entry.cand,
					failErr:  raceCtx.Err(),
					bleg:     tx.bleg,
					observed: emptyOperatorUsageShell(),
					armLeg:   armLeg,
					delta:    parallelFailureDeltaFromHistory(localHist),
				}
				return
			default:
			}

			var preBuf []lipapi.Event
			for {
				ev, err := tx.stream.Recv(legCtx)
				if err != nil {
					observed := operatorUsageOrShell(preBuf)
					armLeg.observedUsage.Store(observed)
					var localErr error
					if ttftDeadline.expired(legCtx, err) {
						if ttftDeadline.scope == ttftTimeoutGlobal {
							localErr = lipapi.ErrTTFTTimeout
						} else {
							localErr = ttftFailure(ttftDeadline.scope, entry.cand.Key)
						}
					} else {
						localErr = err
					}

					outcomeCh <- parallelArmOutcome{
						cand:     entry.cand,
						failErr:  localErr,
						bleg:     tx.bleg,
						observed: observed,
						armLeg:   armLeg,
						delta:    parallelFailureDeltaFromHistory(localHist),
					}
					return
				}
				preBuf = append(preBuf, ev)
				if isWinningEvent(ev) {
					observed := operatorUsageOrShell(preBuf)
					armLeg.observedUsage.Store(observed)
					ready := tx.HandoffReady(pendingSelectionEffects{
						interleaved: frozenInterleaved,
						memoUpdate:  evalOutcome.shapeRes.MemoUpdate,
					})

					outcomeCh <- parallelArmOutcome{
						cand:  entry.cand,
						ready: ready,
						pending: pendingSelectionEffects{
							interleaved: frozenInterleaved,
							memoUpdate:  evalOutcome.shapeRes.MemoUpdate,
						},
						bleg:      tx.bleg,
						observed:  observed,
						winnerBuf: preBuf,
						armLeg:    armLeg,
						delta:     parallelFailureDeltaFromHistory(localHist),
					}
					return
				}
			}
		}(entry)
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				e.logParallelRacePanic(ctx, safety.Capture(safety.BoundaryWorker, "parallel_race_waiter", r), "executor: isolated panic in parallel race waiter", diag.AttrOpts{CallID: req.reqFacts.traceID})
			}
		}()
		wg.Wait()
		close(outcomeCh)
		select {
		case winnerCh <- struct{}{}:
		default:
		}
	}()

	var (
		outcomesMu sync.Mutex
		outcomes   []parallelArmOutcome
	)
	coordinatorDoneCh := make(chan struct{})

	go func() {
		defer close(coordinatorDoneCh)
		defer func() {
			if r := recover(); r != nil {
				e.logParallelRacePanic(ctx, safety.Capture(safety.BoundaryWorker, "parallel_race_coordinator", r), "executor: isolated panic in parallel race coordinator", diag.AttrOpts{CallID: req.reqFacts.traceID})
			}
		}()
		var arrivalSeq uint64
		for o := range outcomeCh {
			arrivalSeq++
			o.arrival = arrivalSeq
			outcomesMu.Lock()
			outcomes = append(outcomes, o)
			outcomesMu.Unlock()

			if o.rejected || o.failErr != nil || o.completed {
				if o.cand.Handicap > 0 {
					broadcastFastForward()
				}
			}
			if isParallelFatalErr(o.failErr) {
				raceCancel()
				select {
				case winnerCh <- struct{}{}:
				default:
				}
			}
			if o.ready != nil {
				raceCancel()
				select {
				case winnerCh <- struct{}{}:
				default:
				}
			}
		}
	}()

	select {
	case <-winnerCh:
		<-coordinatorDoneCh
	case <-ctx.Done():
		raceCancel()
		select {
		case <-coordinatorDoneCh:
		case <-time.After(50 * time.Millisecond):
		}
	}

	outcomesMu.Lock()
	collectedOutcomes := make([]parallelArmOutcome, len(outcomes))
	copy(collectedOutcomes, outcomes)
	outcomesMu.Unlock()

	// Parallel round reducer owns shared progress mutation: excluded/failures/budget/TTFT/[first]/interleaved/affinity/slot — workers already isolated in 5.1
	return e.reduceParallelOutcomes(ctx, req, entries, candidates, cycleAdvance, collectedOutcomes)
}

// Parallel round reducer owns shared progress mutation: excluded/failures/budget/TTFT/[first]/interleaved/affinity/slot — workers already isolated in 5.1
func (e *Executor) reduceParallelOutcomes(
	ctx context.Context,
	req openNextRequest,
	entries []legEntry,
	candidates []routing.AttemptCandidate,
	cycleAdvance *interleavedstate.CycleState,
	collectedOutcomes []parallelArmOutcome,
) (openedAttempt, error) {
	reducer := newParallelRoundReducer(e, req, entries, candidates, cycleAdvance)
	return reducer.Reduce(ctx, collectedOutcomes)
}

type legEntry struct {
	cand  routing.AttemptCandidate
	delay time.Duration
}

// parallelRoundReducer owns serial shared progress mutation, winner selection,
// stable failure merging, and loser terminalization for a parallel execution round.
type parallelRoundReducer struct {
	e            *Executor
	req          openNextRequest
	entries      []legEntry
	candidates   []routing.AttemptCandidate
	cycleAdvance *interleavedstate.CycleState
}

func newParallelRoundReducer(
	e *Executor,
	req openNextRequest,
	entries []legEntry,
	candidates []routing.AttemptCandidate,
	cycleAdvance *interleavedstate.CycleState,
) *parallelRoundReducer {
	return &parallelRoundReducer{
		e:            e,
		req:          req,
		entries:      entries,
		candidates:   candidates,
		cycleAdvance: cycleAdvance,
	}
}

func (r *parallelRoundReducer) Reduce(
	ctx context.Context,
	collectedOutcomes []parallelArmOutcome,
) (openedAttempt, error) {
	var zero openedAttempt

	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()

	var (
		winnerOut *parallelArmOutcome
		winnerBuf []lipapi.Event
		fatalErr  error
	)

	// 1. Winner Selection: Deterministic for same controlled arrival schedule.
	// Earliest arrival sequence number among ready outcomes wins.
	for i := range collectedOutcomes {
		o := &collectedOutcomes[i]
		if isParallelFatalErr(o.failErr) {
			fatalErr = errors.Join(fatalErr, o.failErr)
		}
		if o.ready != nil && o.failErr == nil {
			if winnerOut == nil || o.arrival < winnerOut.arrival {
				winnerOut = o
			}
		}
	}

	if winnerOut != nil {
		winnerBuf = winnerOut.winnerBuf
	}

	// Terminalize all loser / late ready arm attempts exactly once.
	for i := range collectedOutcomes {
		o := &collectedOutcomes[i]
		if o.ready != nil && o != winnerOut {
			o.ready.Dispose(cleanupCtx, errors.New("parallel race loser"))
		}
	}

	// 2. Build legs in stable entries order.
	legs := make([]parallelLeg, len(r.entries))
	for i, entry := range r.entries {
		legs[i] = parallelLeg{
			billingCallState: r.req.reqFacts.billingCallState,
			cand:             entry.cand,
			delay:            entry.delay,
		}
		for _, o := range collectedOutcomes {
			if o.cand.Key == entry.cand.Key {
				if o.armLeg != nil {
					legs[i].stream = o.armLeg.stream
					legs[i].bleg = o.armLeg.bleg
					legs[i].authority = o.armLeg.authority
					legs[i].tx = o.armLeg.tx
					legs[i].interleaved = o.armLeg.interleaved
					legs[i].memoUpdate = o.armLeg.memoUpdate
					legs[i].startedAt = o.armLeg.startedAt
				}
				legs[i].recvErr = o.failErr
				if o.observed.Kind != "" {
					legs[i].observedUsage.Store(o.observed)
				}
				break
			}
		}
	}

	// 3. Handle Context Cancellation.
	if ctx.Err() != nil {
		if winnerOut != nil && winnerOut.ready != nil {
			winnerOut.ready.Dispose(cleanupCtx, ctx.Err())
		}
		r.releaseOpenedLegs(cleanupCtx, legs)
		return zero, ctx.Err()
	}

	// 4. Handle Fatal Errors (only when no ready winner exists).
	if fatalErr != nil && winnerOut == nil {
		if winnerOut != nil && winnerOut.ready != nil {
			winnerOut.ready.Dispose(cleanupCtx, fatalErr)
		}
		r.releaseOpenedLegs(cleanupCtx, legs)
		return zero, fmt.Errorf("executor: parallel race aborted: %w", fatalErr)
	}

	// 5. Merge Exclusions and Candidate Failures in Stable Entries Order.
	if r.req.progress.excluded == nil {
		r.req.progress.excluded = make(map[string]struct{})
	}
	sh := r.req.progress.getFailures()

	for _, entry := range r.entries {
		var o *parallelArmOutcome
		for j := range collectedOutcomes {
			if collectedOutcomes[j].cand.Key == entry.cand.Key {
				o = &collectedOutcomes[j]
				break
			}
		}
		if o == nil {
			continue
		}

		if o.rejected {
			r.e.applyRejection(sh, o.cand, o.rejection)
			r.req.progress.excluded[o.cand.Key] = struct{}{}
		} else if o.completed {
			r.req.progress.excluded[o.cand.Key] = struct{}{}
		}

		if sh != nil {
			o.delta.applyToShared(sh)
		}
	}

	// 6. Winner Outcome: Commit effects, terminalize losers, install bridge stream, return openedAttempt.
	if winnerOut != nil && winnerOut.ready != nil && winnerOut.failErr == nil {
		winnerIdx := -1
		for i := range legs {
			if legs[i].cand.Key == winnerOut.cand.Key {
				winnerIdx = i
				break
			}
		}

		pending := winnerOut.ready.Pending()
		committedInterleaved, err := r.e.commitMemoInjection(ctx, r.req.reqFacts.aLegID, pending.interleaved, pending.memoUpdate)
		if err != nil {
			return zero, r.e.cleanUpParallelFailure(ctx, r.req, legs, winnerIdx, winnerOut, err)
		}
		legs[winnerIdx].interleaved = committedInterleaved
		if pending.memoUpdate == nil && r.cycleAdvance != nil {
			if err := r.e.persistInterleavedState(ctx, r.req.reqFacts.aLegID, legs[winnerIdx].interleaved); err != nil {
				return zero, r.e.cleanUpParallelFailure(ctx, r.req, legs, winnerIdx, winnerOut, err)
			}
		}

		var losers []*parallelLeg
		for i := range legs {
			if i == winnerIdx {
				continue
			}
			if legs[i].stream != nil {
				legs[i].managedByMain = true
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
					if rcv := recover(); rcv != nil {
						pe := safety.Capture(safety.BoundaryBackend, "parallel_cancel_losers", rcv)
						r.e.logParallelRacePanic(ctx, pe, "executor: isolated panic in parallel race loser cleanup", diag.AttrOpts{CallID: r.req.reqFacts.traceID})
						cleanupErr = errors.Join(cleanupErr, fmt.Errorf("parallel race loser cleanup panic"))
					}
					done <- cleanupErr
					close(done)
				}()
				cancelCtx, cancel := detachedCleanupContext(ctx, cancelLosersTimeout)
				defer cancel()
				cleanupErr = r.e.releaseLosers(cancelCtx, r.req.reqFacts.aScope, losers)
			}()
		}

		if err := winnerOut.ready.InstallBridgeStream(&parallelBridgeStream{
			winner:           &legs[winnerIdx],
			buf:              winnerBuf,
			losersDone:       losersDone,
			loserCleanupWait: cancelLosersTimeout,
		}); err != nil {
			return zero, r.e.cleanUpParallelFailure(ctx, r.req, legs, winnerIdx, winnerOut, err)
		}
		return openedAttempt{
			ready:       winnerOut.ready,
			interleaved: legs[winnerIdx].interleaved,
			memoUpdate:  nil,
		}, nil
	}

	// 7. No Winner (All Arms Failed): Terminalize failed legs, record ParallelFailure in stable order.
	var parallelFailure error
	var failedLegs []*parallelLeg
	for i := range legs {
		if legs[i].stream == nil {
			parallelFailure = errors.Join(parallelFailure, fmt.Errorf("candidate %q did not open a stream", legs[i].cand.Key))
			continue
		}
		failure := legs[i].recvErr
		if failure == nil {
			failure = errors.New("parallel leg ended before winner")
		}
		parallelFailure = errors.Join(parallelFailure, fmt.Errorf("candidate %q failed before winner: %w", legs[i].cand.Key, failure))
		failedLegs = append(failedLegs, &legs[i])
	}
	if len(failedLegs) > 0 {
		if cerr := r.e.releaseLosers(cleanupCtx, r.req.reqFacts.aScope, failedLegs); cerr != nil {
			parallelFailure = errors.Join(parallelFailure, cerr)
		}
	}
	if skipped := len(r.candidates) - len(legs); skipped > 0 {
		parallelFailure = errors.Join(parallelFailure, fmt.Errorf("parallel race skipped %d leg(s) due max-attempt budget", skipped))
	}
	if parallelFailure == nil {
		parallelFailure = errors.New("parallel race failed without winner")
	}
	failures := r.req.progress.getFailures()
	failures.ParallelFailure = fmt.Errorf("executor: parallel race arm failed: %w", parallelFailure)
	if r.req.progress.excluded == nil {
		r.req.progress.excluded = map[string]struct{}{}
	}
	for _, c := range r.candidates {
		r.req.progress.excluded[c.Key] = struct{}{}
	}
	return openedAttempt{interleaved: r.req.interleaved}, nil
}

func isParallelFatalErr(err error) bool {
	return errors.Is(err, lipapi.ErrMaxRouteAttempts) ||
		errors.Is(err, lipapi.ErrTTFTTimeout) ||
		errors.Is(err, leglifecycle.ErrALegCanceled)
}

func isWinningEvent(ev lipapi.Event) bool {
	return (ev.Kind == lipapi.EventTextDelta || ev.Kind == lipapi.EventReasoningDelta) && strings.TrimSpace(ev.Delta) != ""
}

func detachedCleanupContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	} else {
		parent = context.WithoutCancel(parent)
	}
	if timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, timeout)
}

func (r *parallelRoundReducer) releaseOpenedLegs(ctx context.Context, legs []parallelLeg) {
	var opened []*parallelLeg
	for i := range legs {
		if legs[i].stream != nil {
			legs[i].managedByMain = true
			opened = append(opened, &legs[i])
		}
	}
	if len(opened) > 0 {
		_ = r.e.releaseLosers(ctx, r.req.reqFacts.aScope, opened)
	}
}

func rollbackAttemptTxOpenFailure(ctx context.Context, tx *attemptTx, rel authorityapp.ReleaseKind, outcome billing.LegOutcome) {
	if tx == nil || tx.completed {
		return
	}
	tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, rel, outcome, nil, "")
}

func (e *Executor) cleanUpParallelFailure(ctx context.Context, req openNextRequest, legs []parallelLeg, winner int, wo *parallelArmOutcome, err error) error {
	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()
	if wo != nil && wo.ready != nil {
		wo.ready.Dispose(cleanupCtx, err)
	}
	var toClean []*parallelLeg
	for i := range legs {
		if i != winner && legs[i].stream != nil {
			toClean = append(toClean, &legs[i])
		}
	}
	cleanupErr := e.releaseLosers(cleanupCtx, req.reqFacts.aScope, toClean)
	return errors.Join(err, cleanupErr)
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

type parallelArmOutcome struct {
	cand      routing.AttemptCandidate
	ready     *readyAttempt
	failErr   error
	pending   pendingSelectionEffects
	rejected  bool
	rejection candidateRejection
	completed bool
	delta     parallelFailureDelta
	arrival   uint64
	observed  lipapi.Event
	bleg      b2bua.BLegRecord
	armLeg    *parallelLeg
	winnerBuf []lipapi.Event
}

type parallelFailureDelta struct {
	transformCount   int
	unrepresentable  int
	nonTransform     bool
	capabilityReject lipapi.NegotiationResult
	transportReject  lipapi.TransportNegotiationResult
	admissionErr     error
	contextLimit     bool
	parallelFailure  error
}

func cloneCandidateRejection(r candidateRejection) candidateRejection {
	res := candidateRejection{kind: r.kind}
	if admit, ok := r.detail.(lipapi.CandidateAdmissionResult); ok {
		cp := admit
		if len(admit.Capability.Missing) > 0 {
			cp.Capability.Missing = slices.Clone(admit.Capability.Missing)
		}
		res.detail = cp
	} else {
		res.detail = r.detail
	}
	return res
}

func parallelFailureDeltaFromHistory(h candidateFailureHistory) parallelFailureDelta {
	d := parallelFailureDelta{
		capabilityReject: h.CapabilityReject,
		transportReject:  h.TransportReject,
		admissionErr:     h.AdmissionErr,
		contextLimit:     h.ContextLimit,
		parallelFailure:  h.ParallelFailure,
	}
	if h.TransformExcludes != nil {
		h.TransformExcludes.mu.Lock()
		d.transformCount = h.TransformExcludes.count
		d.unrepresentable = h.TransformExcludes.unrepresentable
		d.nonTransform = h.TransformExcludes.nonTransform
		h.TransformExcludes.mu.Unlock()
	}
	if len(d.capabilityReject.Missing) > 0 {
		d.capabilityReject.Missing = slices.Clone(d.capabilityReject.Missing)
	}
	return d
}

func (d parallelFailureDelta) applyToShared(target *candidateFailureHistory) {
	if target == nil {
		return
	}
	if target.TransformExcludes == nil {
		target.TransformExcludes = &transformExcludeTracker{}
	}
	target.TransformExcludes.mu.Lock()
	target.TransformExcludes.count += d.transformCount
	target.TransformExcludes.unrepresentable += d.unrepresentable
	if d.nonTransform {
		target.TransformExcludes.nonTransform = true
	}
	target.TransformExcludes.mu.Unlock()

	if d.capabilityReject.Kind != "" {
		cp := d.capabilityReject
		if len(cp.Missing) > 0 {
			cp.Missing = slices.Clone(cp.Missing)
		}
		target.CapabilityReject = cp
	}
	if d.transportReject.Kind != "" {
		target.TransportReject = d.transportReject
	}
	if d.admissionErr != nil {
		target.AdmissionErr = d.admissionErr
	}
	if d.contextLimit {
		target.ContextLimit = true
	}
	if d.parallelFailure != nil {
		target.ParallelFailure = d.parallelFailure
	}
}
