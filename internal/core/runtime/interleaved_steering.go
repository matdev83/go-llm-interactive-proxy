package runtime

import (
	"context"
	"errors"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationprojection"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// The thinker memo reaches executor context exclusively as a persistent
// backend-only steering overlay in the conversation-view store (#391). The
// runtime projects it as a standalone synthetic user message at its fixed
// activation anchor; there is no per-attempt injection code. Publishing is
// best-effort with bounded diagnostics: when steering cannot be persisted, the
// memo is not linked for presentation and the turn continues without it,
// mirroring the tolerant missing-memo semantics of the shaping contract.
//
// Memo steering policy (rendering, overlay identity, placement/fallback
// selection, memo filtering) is feature-owned by
// internal/plugins/features/interleavedthinking and reaches this orchestration
// only through the runtime.InterleavedProcessor port. This file keeps the
// authoritative output commitment and B-leg continuation sequencing.

// publishMemoSteeringOverlay persists or replaces the thinker memo steering
// overlay for the A-leg. The mutation itself is feature-owned (rendering,
// overlay identity, placement, fallback, reason); this orchestration only
// resolves the writer and commits it authoritatively.
func (e *Executor) publishMemoSteeringOverlay(
	ctx context.Context,
	aLegID string,
	ingress lipapi.Call,
	snap conversationprojection.Snapshot,
	memo string,
) error {
	if e == nil || ctx == nil {
		return errors.New("executor: invalid memo steering publish arguments")
	}
	if e.Processor == nil {
		return errors.New("executor: interleaved processor unavailable")
	}
	if e.SteeringWriterFactory == nil {
		return errors.New("executor: conversation-view steering capability unavailable")
	}
	writer, err := e.SteeringWriterFactory(ctx, aLegID, func(_ context.Context) (lipapi.Call, conversationprojection.Snapshot, error) {
		return ingress, snap, nil
	})
	if err != nil {
		return err
	}
	_, err = writer.Put(ctx, e.Processor.MemoSteeringPutRequest(memo))
	return err
}

// deactivateMemoSteeringOverlay removes the memo steering overlay once the memo
// budget is exhausted or the memo bookkeeping can no longer guarantee bounded
// presentation. Best-effort and idempotent: a missing overlay or A-leg is not
// an error.
func (e *Executor) deactivateMemoSteeringOverlay(ctx context.Context, aLegID string) error {
	if e == nil || ctx == nil || aLegID == "" {
		return nil
	}
	if e.Processor == nil {
		return nil
	}
	if e.SteeringWriterFactory == nil {
		return errors.New("executor: conversation-view steering capability unavailable")
	}
	writer, err := e.SteeringWriterFactory(ctx, aLegID, nil)
	if err != nil {
		return err
	}
	_, err = writer.Deactivate(ctx, e.Processor.MemoSteeringOverlayID())
	if err != nil && !errors.Is(err, conversationprojection.ErrOverlayNotFound) && !errors.Is(err, conversationprojection.ErrALegNotFound) {
		e.logMemoSteeringDeactivateFailed(ctx, aLegID, err)
		return err
	}
	return nil
}

// refreshMemoSteeringFacts rebuilds the request facts for the same-turn
// interleaved executor continuation so the just-published memo overlay is part
// of the projected baseline, snapshot, provenance, and filtered view handed to
// planning and final reassertion. When the visible-mode immediate-continuation
// suppression applies, the memo overlay is stripped from this turn's snapshot
// copy only; the persisted overlay stays active for later turns.
//
// Soft-fail: any reader/projector error keeps the pre-capture facts so the
// continuation proceeds on its coherent frozen view without the newest overlay;
// the next logical turn picks it up from the authoritative store.
func (e *Executor) refreshMemoSteeringFacts(
	ctx context.Context,
	facts recvTurnFacts,
	state interleavedstate.State,
	suppressVisibleMemo bool,
) (recvTurnFacts, bool) {
	if e == nil || ctx == nil {
		return facts, false
	}
	if e.Processor == nil {
		return facts, false
	}
	reader := e.conversationViewReader()
	if reader == nil {
		return facts, false
	}
	snap, err := reader.Snapshot(ctx, facts.aLegID)
	if err != nil {
		e.logMemoSteeringRefreshFailure(ctx, facts.traceID, "snapshot", err)
		return facts, false
	}
	memoVisibleSuppressed := suppressVisibleMemo && e.memoStateVisibleToClient(ctx, facts.aLegID, state)
	if memoVisibleSuppressed {
		snap = withoutSteeringOverlay(snap, e.Processor.IsMemoSteeringOverlay)
	}
	if snap.StateRevision == conversationRevision(facts) && !memoVisibleSuppressed {
		return facts, true
	}
	ingress := memoProjectionIngress(facts)
	if len(ingress.Items) == 0 && len(ingress.Messages) == 0 {
		ingress = memoProjectionBaseline(facts)
	}
	projected, ev, err := conversationprojection.Project(ingress, snap)
	if err != nil {
		e.logMemoSteeringRefreshFailure(ctx, facts.traceID, "projection", err)
		return facts, false
	}
	filtered, err := conversationprojection.FilterNeverBackend(ingress, snap)
	if err != nil {
		e.logMemoSteeringRefreshFailure(ctx, facts.traceID, "filter", err)
		return facts, false
	}
	updated := cloneRefreshedMemoFacts(facts)
	// Projection owns conversation authority only. Apply its trajectory to the
	// already-frozen baseline so route overrides and all other admission fields
	// remain identical across thinker/executor legs.
	baseline := memoProjectionBaseline(facts)
	baseline.Instructions, baseline.Messages, baseline.Items = projected.Instructions, projected.Messages, projected.Items
	updated.baseline = baseline
	updated.conversationSnapshot = snap
	if ev != nil {
		updated.conversationProvenance = ev.Provenance
	}
	updated.conversationFilteredBaseline = filtered
	return updated, true
}

func cloneRefreshedMemoFacts(source recvTurnFacts) recvTurnFacts {
	return source.clone()
}

func conversationRevision(source recvTurnFacts) uint64 {
	return source.conversationSnapshot.StateRevision
}

func memoProjectionIngress(source recvTurnFacts) lipapi.Call {
	return source.ingressCall
}

func memoProjectionBaseline(source recvTurnFacts) lipapi.Call {
	return source.baseline
}

func projectRefreshedMemoContext(ctx context.Context, source recvTurnFacts, log *slog.Logger) context.Context {
	return source.projectContext(ctx, log)
}

// memoStateVisibleToClient reports whether the currently linked memo was
// surfaced to the client during this logical turn.
func (e *Executor) memoStateVisibleToClient(ctx context.Context, aLegID string, state interleavedstate.State) bool {
	if e == nil || e.Processor == nil || aLegID == "" {
		return false
	}
	return e.Processor.IsMemoVisibleToClient(ctx, aLegID)
}

// withoutSteeringOverlay returns a copy of snap without overlays matching
// isTarget, so the immediate visible-mode continuation does not duplicate
// reasoning the client already saw. The match predicate is feature-owned;
// this sequencing helper knows no overlay identity.
func withoutSteeringOverlay(snap conversationprojection.Snapshot, isTarget func(string) bool) conversationprojection.Snapshot {
	if isTarget == nil {
		return snap
	}
	kept := make([]conversationprojection.Overlay, 0, len(snap.Steering))
	for _, ov := range snap.Steering {
		if isTarget(ov.OverlayID) {
			continue
		}
		kept = append(kept, ov)
	}
	snap.Steering = kept
	return snap
}
