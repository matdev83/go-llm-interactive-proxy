package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	defaultInterleavedMaxMemoBytes = 16 * 1024
	defaultInterleavedRegularTurns = 2
)

// interleavedEnabled reports whether interleaved thinking is configured on the executor.
// When false, the attempt-open path skips shaping and state persistence entirely so
// behavior is identical to a deployment without the feature (Requirements 3.5, 10.2).
func (e *Executor) interleavedEnabled() bool {
	if e == nil {
		return false
	}
	return e.InterleavedConfig.Instructions != "" || e.MemoStore != nil
}

// loadInterleavedState fetches the persisted thinker cycle state and memo reference for
// the A-leg. A store that does not implement [b2bua.InterleavedStateStore] yields a
// zero state, which is the harmless new-session equivalent for cycle purposes.
// Memo bodies may be process-local; a persisted MemoRef without a memo is handled
// later by shaping as a missing memo, not as a continuity failure.
func (e *Executor) loadInterleavedState(ctx context.Context, aLegID string) (interleavedstate.State, error) {
	if !e.interleavedEnabled() {
		return interleavedstate.State{}, nil
	}
	is, ok := e.Store.(b2bua.InterleavedStateStore)
	if !ok || is == nil {
		return interleavedstate.State{}, nil
	}
	state, err := is.FetchInterleavedState(ctx, aLegID)
	if err != nil {
		if errors.Is(err, b2bua.ErrALegNotFound) {
			return interleavedstate.State{}, nil
		}
		return interleavedstate.State{}, fmt.Errorf("executor: load interleaved state: %w", err)
	}
	return state, nil
}

// persistInterleavedState stores the thinker cycle state and memo reference for the A-leg.
// It does not require durable memo bodies: the standard runtime persists the ref
// so in-process turns can consume it, while restart loss degrades to no memo.
// A store that does not implement [b2bua.InterleavedStateStore] rejects non-empty state so
// callers fail closed instead of silently dropping authoritative interleaved state.
func (e *Executor) persistInterleavedState(ctx context.Context, aLegID string, state interleavedstate.State) error {
	if !e.interleavedEnabled() {
		return nil
	}
	is, ok := e.Store.(b2bua.InterleavedStateStore)
	if !ok || is == nil {
		if state.IsEmpty() {
			return nil
		}
		return b2bua.ErrInterleavedStateUnsupported
	}
	return is.SetInterleavedState(ctx, aLegID, state)
}

// shapeAttemptCall applies candidate-specific interleaved shaping to a canonical call before
// capability negotiation and backend open. The A-leg ID is the authoritative memo scope.
// RoleNone candidates and disabled configurations return a deep clone unchanged.
func (e *Executor) shapeAttemptCall(
	ctx context.Context,
	call lipapi.Call,
	c routing.AttemptCandidate,
	aLegID string,
	state interleavedstate.State,
	suppressVisibleMemo bool,
) (interleavedthinking.ShapeResult, error) {
	if !e.interleavedEnabled() {
		return interleavedthinking.ShapeResult{Call: lipapi.CloneCall(call)}, nil
	}
	return interleavedthinking.ShapeCall(ctx, interleavedthinking.ShapeInput{
		Call:                call,
		Candidate:           c,
		Config:              e.InterleavedConfig,
		MemoStore:           e.MemoStore,
		Scope:               interleavedthinking.Scope(aLegID),
		MemoRef:             state.MemoRef,
		SuppressVisibleMemo: suppressVisibleMemo,
	})
}

func (e *Executor) interleavedHiddenMode() bool {
	if !e.interleavedEnabled() {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(e.InterleavedConfig.StreamToClient))
	return mode == "" || mode == "hidden"
}

func (e *Executor) interleavedVisibleMode() bool {
	if !e.interleavedEnabled() {
		return false
	}
	return strings.ToLower(strings.TrimSpace(e.InterleavedConfig.StreamToClient)) == "visible"
}

func (e *Executor) shouldWrapHiddenInterleavedThinker(c routing.AttemptCandidate) bool {
	return e.interleavedHiddenMode() && e.MemoStore != nil && c.InterleavedRole == interleavedstate.RoleThinker
}

func (e *Executor) shouldWrapVisibleInterleavedThinker(c routing.AttemptCandidate) bool {
	return e.interleavedVisibleMode() && e.MemoStore != nil && c.InterleavedRole == interleavedstate.RoleThinker
}

func (e *Executor) effectiveMaxMemoBytes() int {
	if e == nil || e.InterleavedConfig.MaxMemoBytes <= 0 {
		return defaultInterleavedMaxMemoBytes
	}
	return e.InterleavedConfig.MaxMemoBytes
}

func (e *Executor) effectiveRegularTurnsRemaining() int {
	if e == nil || e.InterleavedConfig.RegularTurnsRemaining <= 0 {
		return defaultInterleavedRegularTurns
	}
	return e.InterleavedConfig.RegularTurnsRemaining
}

func (e *Executor) newThinkerRecorder(c routing.AttemptCandidate, call lipapi.Call) *interleavedthinking.Recorder {
	return &interleavedthinking.Recorder{
		MaxMemoBytes:          e.effectiveMaxMemoBytes(),
		SourceSelector:        strings.TrimSpace(call.Route.Selector),
		Backend:               strings.TrimSpace(c.Primary.Backend),
		Model:                 strings.TrimSpace(c.Primary.Model),
		RequestID:             strings.TrimSpace(call.ID),
		RegularTurnsRemaining: e.effectiveRegularTurnsRemaining(),
	}
}

func (e *Executor) persistCapturedMemo(ctx context.Context, aLegID string, state interleavedstate.State, memo interleavedthinking.MemoState) (interleavedstate.State, error) {
	if e == nil || e.MemoStore == nil {
		return state, fmt.Errorf("executor: memo store required for interleaved capture")
	}
	oldRef := state.MemoRef
	ref, err := e.MemoStore.Put(ctx, interleavedthinking.Scope(aLegID), memo)
	if err != nil {
		return state, fmt.Errorf("executor: store thinker memo: %w", err)
	}
	state.MemoRef = &ref
	if err := e.persistInterleavedState(ctx, aLegID, state); err != nil {
		return state, fmt.Errorf("executor: persist memo reference: %w", err)
	}
	if oldRef != nil && oldRef.Key != "" && !oldRef.Equal(ref) {
		if err := e.MemoStore.Delete(ctx, interleavedthinking.Scope(aLegID), *oldRef); err != nil {
			return state, fmt.Errorf("executor: delete replaced memo: %w", err)
		}
	}
	return state, nil
}

func (e *Executor) commitMemoInjection(ctx context.Context, aLegID string, state interleavedstate.State, update *interleavedthinking.PendingMemoUpdate) (interleavedstate.State, error) {
	if update == nil {
		return state, nil
	}
	if e == nil || e.MemoStore == nil {
		return state, fmt.Errorf("executor: memo store required for interleaved injection")
	}
	ref, err := e.MemoStore.Update(ctx, interleavedthinking.Scope(aLegID), update.Ref, update.State)
	if err != nil {
		return state, fmt.Errorf("executor: update injected memo: %w", err)
	}
	state.MemoRef = &ref
	if err := e.persistInterleavedState(ctx, aLegID, state); err != nil {
		return state, fmt.Errorf("executor: persist interleaved memo reference: %w", err)
	}
	return state, nil
}

func (e *Executor) openInterleavedExecutorContinuation(ctx context.Context, from *retryRecvStream, state interleavedstate.State) (*retryRecvStream, error) {
	if e == nil || from == nil {
		return nil, fmt.Errorf("executor: invalid interleaved continuation arguments")
	}
	e.logInterleavedThinkerSuppressed(ctx, from.traceID)
	out, err := e.tryPlanOpenOnce(attemptOpenParams{
		ctx:                 ctx,
		bus:                 from.bus,
		traceID:             from.traceID,
		aLegID:              from.aLegID,
		aScope:              from.aScope,
		baseline:            from.baseline,
		sel:                 from.sel,
		requestSize:         from.requestSize,
		session:             from.session,
		excluded:            from.excluded,
		rng:                 from.rng,
		budget:              from.budget,
		ttft:                from.ttft,
		isRetryPath:         false,
		affinityKey:         from.affinityKey,
		affinitySet:         from.affinitySet,
		interleaved:         state,
		suppressThinker:     true,
		suppressVisibleMemo: true,
	})
	if err != nil {
		return nil, fmt.Errorf("executor: interleaved continuation plan/open: %w", err)
	}
	if !out.opened {
		return nil, fmt.Errorf("executor: interleaved continuation: %w", routing.ErrNoEligibleCandidate)
	}
	if from.aScope != nil && !out.registered {
		if err := from.aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{
			ID:      out.bleg.BLegID,
			Attempt: lifecycleAttempt(out.stream),
		}); err != nil {
			if out.stream != nil && !errors.Is(err, leglifecycle.ErrALegCanceled) {
				_ = out.stream.Close()
			}
			return nil, err
		}
	}
	rs := &retryRecvStream{
		executor:            e,
		bus:                 from.bus,
		baseline:            from.baseline,
		budget:              from.budget,
		ttft:                from.ttft,
		aLegID:              from.aLegID,
		traceID:             from.traceID,
		sel:                 from.sel,
		requestSize:         from.requestSize,
		session:             from.session,
		excluded:            from.excluded,
		rng:                 from.rng,
		affinityKey:         from.affinityKey,
		affinitySet:         from.affinitySet,
		recvViews:           from.recvViews,
		recvViewsOK:         from.recvViewsOK,
		routePrefs:          from.routePrefs,
		secureTurn:          from.secureTurn,
		secureTurnOK:        from.secureTurnOK,
		aScope:              from.aScope,
		interleaved:         out.interleaved,
		holdALegEnd:         true,
		suppressThinker:     true,
		suppressVisibleMemo: true,
		accounting:          newAttemptAccountingTracker(e.now()),
		recoverPolicy:       streamrecovery.NewPolicy(e.StreamRecovery, e.now()),
		bleg:                out.bleg,
		cand:                out.cand,
	}
	rs.storeInner(out.stream)
	return rs, nil
}
