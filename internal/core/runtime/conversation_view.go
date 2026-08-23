package runtime

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
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
		return conversationview.Snapshot{}, nil, lipapi.Call{}, fmt.Errorf("executor: conversation view snapshot: %w", err)
	}
	// Fast path: empty snapshot must remain identity-preserving (no clone)
	// to keep no-op evidence EffectNone and avoid spurious canonical diff.
	if len(snap.NeverBackend) == 0 && len(snap.Steering) == 0 {
		return snap, &conversationview.ProjectionEvidence{}, call, nil
	}
	out, ev, err := conversationview.Project(call, snap)
	if err != nil {
		return conversationview.Snapshot{}, nil, lipapi.Call{}, fmt.Errorf("executor: conversation view projection: %w", err)
	}
	// Evidence already bounded: counts, revisions, placement classes, no plaintext
	return snap, ev, out, nil
}
