package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// conversationViewReader returns the optional narrow reader.
// Prefer explicit ConversationViewReader when set (test/narrow seam),
// otherwise resolve via conversationview.AsReader(Store) without widening
// b2bua.Store.
func (e *Executor) conversationViewReader() conversationview.Reader {
	if e == nil {
		return nil
	}
	if e.ConversationViewReader != nil {
		return e.ConversationViewReader
	}
	if r, ok := conversationview.AsReader(e.Store); ok {
		return r
	}
	return nil
}

// conversationViewTagger returns the optional narrow tagger.
// Prefer explicit ConversationViewTagger when set, otherwise resolve via
// conversationview.AsTagger(Store).
func (e *Executor) conversationViewTagger() conversationview.Tagger {
	if e == nil {
		return nil
	}
	if e.ConversationViewTagger != nil {
		return e.ConversationViewTagger
	}
	if t, ok := conversationview.AsTagger(e.Store); ok {
		return t
	}
	return nil
}

// conversationViewSteeringStore returns the optional narrow steering store.
// Resolves via conversationview.AsSteeringStore(Store).
func (e *Executor) conversationViewSteeringStore() conversationview.SteeringStore {
	if e == nil {
		return nil
	}
	if s, ok := conversationview.AsSteeringStore(e.Store); ok {
		return s
	}
	return nil
}

// conversationProjectionSummary is the bounded observable diagnostic for
// the 3.2 seam. It contains only counts/revisions/placement classes and
// StateRevision, never OverlayID, message identity/digest, or plaintext.
// Full ProjectionEvidence is retained internally for D14 final reassertion.
type conversationProjectionSummary struct {
	StateRevision uint64 `json:"state_revision"`
	FilteredCount int    `json:"filtered_count"`
	InjectedCount int    `json:"injected_count"`
	// Bounded placement/revision slot summaries (counts only, no IDs).
	StablePrefixCount  int    `json:"stable_prefix_count"`
	AfterMessageCount  int    `json:"after_message_count"`
	FallbackCount      int    `json:"fallback_count"`
	MaxOverlayRevision uint64 `json:"max_overlay_revision"`
	MaxSlotOrdinal     uint64 `json:"max_slot_ordinal"`
}

func newConversationProjectionSummary(snap conversationview.Snapshot, ev *conversationview.ProjectionEvidence) conversationProjectionSummary {
	if ev == nil {
		return conversationProjectionSummary{StateRevision: snap.StateRevision}
	}
	s := conversationProjectionSummary{
		StateRevision: snap.StateRevision,
		FilteredCount: ev.FilteredCount,
		InjectedCount: ev.InjectedCount,
		FallbackCount: len(ev.Fallbacks),
	}
	for _, p := range ev.Provenance {
		switch p.ResolvedKind {
		case conversationview.PlacementStablePrefix:
			s.StablePrefixCount++
		case conversationview.PlacementAfterMessage:
			s.AfterMessageCount++
		}
		if p.Revision > s.MaxOverlayRevision {
			s.MaxOverlayRevision = p.Revision
		}
		if p.SlotOrdinal > s.MaxSlotOrdinal {
			s.MaxSlotOrdinal = p.SlotOrdinal
		}
	}
	return s
}

// snapshotAndProject performs the single coherent per-turn snapshot read
// and pure projection. It is the 3.2 seam: after authoritative A-leg
// resolution (seal 3.1) and before backend request/pre-request transforms,
// context estimation, billing, routing/capability/baseline.
// Fail-closed on lookup or projection errors; evidence is bounded
// content-free (counts/revisions/placement only).
func (e *Executor) snapshotAndProject(ctx context.Context, aLegID string, call lipapi.Call) (conversationview.Snapshot, *conversationview.ProjectionEvidence, lipapi.Call, error) {
	reader := e.conversationViewReader()
	if reader == nil {
		// No capability: fast path preserves identity to avoid false mutate
		// detection in policy evidence (empty projection is a no-op).
		empty := conversationview.Snapshot{}
		return empty, &conversationview.ProjectionEvidence{}, call, nil
	}
	snap, err := reader.Snapshot(ctx, aLegID)
	if err != nil {
		if obs := e.conversationViewObserver(); obs != nil {
			conversationview.SafeObserver(obs).OnProjectionFailure(conversationview.StageEarly)
		}
		return conversationview.Snapshot{}, nil, lipapi.Call{}, fmt.Errorf("executor: conversation view snapshot: %w", err)
	}

	// External non-detached ingress stale cleanup (Finding 5 / Req 6.14, 12.14):
	// Inspect the snapshot for active "alg-rec" overlay. Deactivate ONLY when active.
	if !execctx.IsSuppressedPluginID(ctx, "agent_loop_guard") {
		hasActiveAlgRec := false
		for _, ov := range snap.Steering {
			if ov.OverlayID == "alg-rec" && ov.Active {
				hasActiveAlgRec = true
				break
			}
		}
		if hasActiveAlgRec {
			steeringStore := e.conversationViewSteeringStore()
			if steeringStore != nil {
				_, derr := steeringStore.DeactivateSteering(ctx, aLegID, "alg-rec")
				if derr != nil && !errors.Is(derr, conversationview.ErrOverlayNotFound) && !errors.Is(derr, conversationview.ErrALegNotFound) {
					if obs := e.conversationViewObserver(); obs != nil {
						conversationview.SafeObserver(obs).OnProjectionFailure(conversationview.StageEarly)
					}
					return conversationview.Snapshot{}, nil, lipapi.Call{}, fmt.Errorf("executor: deactivate stale recovery steering: %w", derr)
				}
				// Re-read snapshot after deactivation so projection uses clean snapshot
				snap, err = reader.Snapshot(ctx, aLegID)
				if err != nil {
					if obs := e.conversationViewObserver(); obs != nil {
						conversationview.SafeObserver(obs).OnProjectionFailure(conversationview.StageEarly)
					}
					return conversationview.Snapshot{}, nil, lipapi.Call{}, fmt.Errorf("executor: conversation view snapshot after stale cleanup: %w", err)
				}
			}
		}
	}
	// Fast path: empty snapshot must remain identity-preserving (no clone)
	// to keep no-op evidence EffectNone and avoid spurious canonical diff.
	if len(snap.NeverBackend) == 0 && len(snap.Steering) == 0 {
		return snap, &conversationview.ProjectionEvidence{}, call, nil
	}
	out, ev, err := conversationview.Project(call, snap)
	if err != nil {
		if obs := e.conversationViewObserver(); obs != nil {
			safe := conversationview.SafeObserver(obs)
			safe.OnProjectionFailure(conversationview.StageEarly)
			if errors.Is(err, conversationview.ErrAnchorMissing) || errors.Is(err, conversationview.ErrAnchorNotFound) {
				safe.OnAnchorFailure(conversationview.AnchorFailClosed)
			}
		}
		return conversationview.Snapshot{}, nil, lipapi.Call{}, fmt.Errorf("executor: conversation view projection: %w", err)
	}
	// Emit bounded diagnostics via narrow observer seam.
	if obs := e.conversationViewObserver(); obs != nil {
		safe := conversationview.SafeObserver(obs)
		summary := conversationview.NewProjectionSummary(snap, ev)
		safe.OnProjection(conversationview.StageEarly, summary)
		for range ev.Fallbacks {
			safe.OnAnchorFallback(conversationview.StageEarly, conversationview.AnchorStablePrefixFallback)
		}
	}
	// Evidence already bounded: counts, revisions, placement classes, no plaintext
	return snap, ev, out, nil
}

// conversationViewObserver returns the optional narrow observer (nil is no-op).
func (e *Executor) conversationViewObserver() conversationview.Observer {
	if e == nil {
		return nil
	}
	return e.ConversationViewObserver
}
