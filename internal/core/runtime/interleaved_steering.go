package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview/sdkadapter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/steering"
)

// The thinker memo reaches executor context exclusively as a persistent
// backend-only steering overlay in the conversation-view store (#391). The
// runtime projects it as a standalone synthetic user message at its fixed
// activation anchor; there is no per-attempt injection code. Publishing is
// best-effort with bounded diagnostics: when steering cannot be persisted, the
// memo is not linked for presentation and the turn continues without it,
// mirroring the tolerant missing-memo semantics of the shaping contract.

const (
	// interleavedMemoOverlayID is the stable conversation-view overlay ID that
	// carries the latest thinker memo for an A-leg. Each capture replaces the
	// payload and re-resolves the anchor to the current ingress tail.
	interleavedMemoOverlayID = "interleaved-thinking-memo"
	// interleavedMemoSteeringReason is the bounded content-free reason recorded
	// on the memo steering overlay and its diagnostics.
	interleavedMemoSteeringReason = "interleaved_thinking_memo"
)

// memoSteeringPayload renders the model-visible user-message text persisted for
// a captured memo. The header keeps prior injections unambiguous without any
// protocol-specific markers.
func memoSteeringPayload(memo string) string {
	return interleavedthinking.SessionSteeringGuidanceHeader + "\n" + strings.TrimSpace(memo)
}

// publishMemoSteeringOverlay persists or replaces the thinker memo steering
// overlay for the A-leg. Placement resolves after_ingress_tail against the
// accepted ingress trajectory frozen at turn preparation; the anchor therefore
// stays fixed while later history appends behind it (cache-stable).
func (e *Executor) publishMemoSteeringOverlay(
	ctx context.Context,
	aLegID string,
	ingress lipapi.Call,
	snap conversationview.Snapshot,
	memo string,
) error {
	if e == nil || ctx == nil {
		return errors.New("executor: invalid memo steering publish arguments")
	}
	store := e.conversationViewSteeringStore()
	if store == nil {
		return errors.New("executor: conversation-view steering capability unavailable")
	}
	writer, err := sdkadapter.NewWriter(store, aLegID, func(_ context.Context) (lipapi.Call, conversationview.Snapshot, error) {
		return ingress, snap, nil
	})
	if err != nil {
		return err
	}
	_, err = writer.Put(ctx, steering.PutRequest{
		OverlayID: steering.OverlayID(interleavedMemoOverlayID),
		Message: steering.Message{
			Role: lipapi.RoleUser,
			Text: memoSteeringPayload(memo),
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              steering.ReasonCode(interleavedMemoSteeringReason),
	})
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
	store := e.conversationViewSteeringStore()
	if store == nil {
		return errors.New("executor: conversation-view steering capability unavailable")
	}
	_, err := store.DeactivateSteering(ctx, aLegID, interleavedMemoOverlayID)
	if err != nil && !errors.Is(err, conversationview.ErrOverlayNotFound) && !errors.Is(err, conversationview.ErrALegNotFound) {
		e.logMemoSteeringDeactivateFailed(ctx, aLegID, err)
		return err
	}
	return nil
}

// restoreMemoSteeringOverlay restores the previously linked memo after a
// replacement transaction fails, or deactivates the newly published overlay
// when no prior memo existed.
func (e *Executor) restoreMemoSteeringOverlay(ctx context.Context, aLegID string, oldRef *interleavedstate.MemoRef, src capturedMemoSource) error {
	if oldRef == nil || oldRef.Key == "" || e.MemoStore == nil {
		return e.deactivateMemoSteeringOverlay(ctx, aLegID)
	}
	oldMemo, ok, err := e.MemoStore.Get(ctx, interleavedthinking.Scope(aLegID), *oldRef)
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(oldMemo.Memo) == "" {
		return e.deactivateMemoSteeringOverlay(ctx, aLegID)
	}
	return e.publishMemoSteeringOverlay(ctx, aLegID, src.Ingress, src.Snapshot, oldMemo.Memo)
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
		snap = stripMemoSteeringOverlay(snap)
	}
	if snap.StateRevision == conversationRevision(facts) && !memoVisibleSuppressed {
		return facts, true
	}
	ingress := memoProjectionIngress(facts)
	if len(ingress.Items) == 0 && len(ingress.Messages) == 0 {
		ingress = memoProjectionBaseline(facts)
	}
	projected, ev, err := conversationview.Project(ingress, snap)
	if err != nil {
		e.logMemoSteeringRefreshFailure(ctx, facts.traceID, "projection", err)
		return facts, false
	}
	filtered, err := conversationview.FilterNeverBackend(ingress, snap)
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

func projectRefreshedMemoContext(source recvTurnFacts, ctx context.Context, log *slog.Logger) context.Context {
	return source.projectContext(ctx, log)
}

// memoStateVisibleToClient reports whether the currently linked memo was
// surfaced to the client during this logical turn. A missing memo body yields
// false so hidden-mode behavior is unchanged.
func (e *Executor) memoStateVisibleToClient(ctx context.Context, aLegID string, state interleavedstate.State) bool {
	if e == nil || e.MemoStore == nil || state.MemoRef == nil || state.MemoRef.Key == "" {
		return false
	}
	memo, ok, err := e.MemoStore.Get(ctx, interleavedthinking.Scope(aLegID), *state.MemoRef)
	if err != nil || !ok {
		return false
	}
	return memo.VisibleToClient
}

// stripMemoSteeringOverlay returns a copy of snap without the memo steering
// overlay so the immediate visible-mode continuation does not duplicate
// reasoning the client already saw.
func stripMemoSteeringOverlay(snap conversationview.Snapshot) conversationview.Snapshot {
	kept := make([]conversationview.SteeringOverlay, 0, len(snap.Steering))
	for _, ov := range snap.Steering {
		if ov.OverlayID == interleavedMemoOverlayID {
			continue
		}
		kept = append(kept, ov)
	}
	snap.Steering = kept
	return snap
}
