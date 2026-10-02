package conversationprojection

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

// ---------------------------------------------------------------------------
// Transform-stable reassertion: request-local trajectory lineage.
//
// A backend-only shaping stage may change the payload bytes of complete messages
// after early projection already resolved the persisted after-message anchors
// against A-leg/client truth. Message identity is a hash of normalized semantic
// content, so such a rewrite can change the identity a frozen anchor names even
// though the complete-message trajectory is unchanged. Re-resolving that frozen
// anchor against the backend-shaped content would then report ErrAnchorMissing
// and deny an otherwise ordinary turn.
//
// This file decides whether the trajectory really was preserved. It establishes a
// one-to-one structural correspondence between the FROZEN filtered baseline and
// the cleaned, backend-shaped trajectory, using only request-local evidence: no
// conversation-view store read, no awareness of which stage rewrote what, and no
// dependency on any concrete feature.
//
// IDENTITY EQUALITY IS NOT THE PROOF, in either direction.
//
//   - It is not a usable substitute: ANY rewrite of the anchored payload changes that
//     message's content-derived identity, so an identity-equality proof would refuse
//     EVERY legitimate rewrite and the carry-forward this file implements could never
//     fire. A legacy PartToolResult structured-Content rewrite is the extreme case:
//     it changes model-visible bytes with ZERO identity drift.
//   - It is also strictly WEAKER than the structural proof: a legacy PartToolResult
//     projects from its Text field only, and a legacy PartJSON atom likewise omits the
//     part's ToolName, ToolCallID, and reference fields. Two messages differing only
//     there share one identity, so identity cannot see those differences at all.
//
// The structural comparison below is therefore the authority. It compares the
// canonical authority form, the trajectory cardinality and order, the
// instruction/message or item partition, the roles and kinds, the ordered part
// kinds, and the stable canonical identities/references/tool identities. Payload
// bytes - part text, JSON documents, opaque blobs, reasoning text and signatures -
// are deliberately absent, because those are exactly what a backend-only content
// rewrite is allowed to change. MessageIdentityOf is never consulted to decide
// compatibility; it is used only to describe a position the structural proof has
// already accepted.
//
// Every ambiguous shape fails closed: an insertion, a deletion, a reorder, a
// role change, a kind change, a stable-ID or reference change, a partition
// change, or a cardinality change all fail the proof, so the existing
// exact-resolution / anchor-missing-policy outcome stays authoritative and no
// placement is ever relocated by ordinal.
// ---------------------------------------------------------------------------

// carriedAnchors maps an overlay ID to the request-local anchor that a proven
// trajectory lineage derived for that overlay's already-resolved after-message
// placement. It is consulted ONLY after exact identity resolution has failed, so
// exact identity-based reassertion remains the normal path.
type carriedAnchors map[string]MessageAnchor

// trajectoryLineage records that a cleaned backend-shaped trajectory is a
// one-to-one, structure-preserving rewrite of the frozen filtered baseline.
//
// The correspondence is the identity on the canonical trajectory slot order:
// slot i of the cleaned trajectory corresponds to slot i of the frozen baseline,
// and the lineage exists only because every slot's structural descriptor
// matched. Slots are enumerated instructions-then-messages for legacy authority
// and by index for item authority, which keeps the instruction/message partition
// and the item partition part of the proof rather than something inferred from
// the content.
type trajectoryLineage struct {
	itemAuthority bool
	slots         int
}

// proven reports whether the lineage was established.
func (l trajectoryLineage) proven() bool { return l.slots > 0 }

// proveTrajectoryLineage establishes the one-to-one structural correspondence
// between the frozen filtered baseline and the cleaned backend-shaped
// trajectory, or reports that it cannot be established.
//
// The comparison is deliberately all-or-nothing: a single incompatible slot
// invalidates the whole proof, because a partial proof is exactly what would
// degrade into relocating the remaining placements by ordinal.
func proveTrajectoryLineage(frozen, cleaned lipapi.Call) (trajectoryLineage, bool) {
	if frozen.HasItemAuthority() != cleaned.HasItemAuthority() {
		return trajectoryLineage{}, false
	}
	if cleaned.HasItemAuthority() {
		if len(frozen.Items) != len(cleaned.Items) {
			return trajectoryLineage{}, false
		}
		for i := range frozen.Items {
			if !itemLineageCompatible(frozen.Items[i], cleaned.Items[i]) {
				return trajectoryLineage{}, false
			}
		}
		return trajectoryLineage{itemAuthority: true, slots: len(frozen.Items)}, true
	}
	if len(frozen.Instructions) != len(cleaned.Instructions) || len(frozen.Messages) != len(cleaned.Messages) {
		return trajectoryLineage{}, false
	}
	for i := range frozen.Instructions {
		if !messageLineageCompatible(frozen.Instructions[i], cleaned.Instructions[i]) {
			return trajectoryLineage{}, false
		}
	}
	for i := range frozen.Messages {
		if !messageLineageCompatible(frozen.Messages[i], cleaned.Messages[i]) {
			return trajectoryLineage{}, false
		}
	}
	return trajectoryLineage{slots: len(frozen.Instructions) + len(frozen.Messages)}, true
}

// frozenSlot resolves a frozen after-message anchor to its slot in the frozen
// filtered baseline. An anchor the baseline does not resolve has no frozen
// position, so there is nothing to carry forward.
func (l trajectoryLineage) frozenSlot(frozen lipapi.Call, anchor MessageAnchor) (int, bool) {
	if l.itemAuthority {
		idx, found, err := resolveAnchorInItems(frozen.Items, anchor)
		if err != nil || !found {
			return 0, false
		}
		return idx, true
	}
	isInstr, idx, found, err := resolveAnchorLegacy(frozen.Instructions, frozen.Messages, anchor)
	if err != nil || !found {
		return 0, false
	}
	if isInstr {
		return idx, true
	}
	return len(frozen.Instructions) + idx, true
}

// currentAnchorAt derives the anchor that names the cleaned trajectory message
// occupying the same slot. Because the lineage is the identity on slot order, the
// derived occurrence is the count of that message's own identity over the cleaned
// prefix, which is precisely what exact resolution needs to land on the same
// slot again.
func (l trajectoryLineage) currentAnchorAt(cleaned lipapi.Call, slot int) (MessageAnchor, bool) {
	if l.itemAuthority {
		if slot < 0 || slot >= len(cleaned.Items) {
			return MessageAnchor{}, false
		}
		id, err := ItemIdentityOf(cleaned.Items[slot])
		if err != nil {
			return MessageAnchor{}, false
		}
		occ := uint32(0)
		for i := 0; i <= slot; i++ {
			if cleaned.Items[i].Kind != lipapi.ItemKindMessage {
				continue
			}
			cid, err := ItemIdentityOf(cleaned.Items[i])
			if err != nil {
				return MessageAnchor{}, false
			}
			if cid == id {
				occ++
			}
		}
		return validatedAnchor(id, occ)
	}
	ordered := cleaned.Instructions
	if slot >= len(cleaned.Instructions) {
		ordered = append(append([]lipapi.Message(nil), cleaned.Instructions...), cleaned.Messages...)
	}
	if slot < 0 || slot >= len(ordered) {
		return MessageAnchor{}, false
	}
	prefix := ordered[:slot+1]
	id, err := MessageIdentityOf(prefix[len(prefix)-1])
	if err != nil {
		return MessageAnchor{}, false
	}
	occ := uint32(0)
	for _, m := range prefix {
		cid, err := MessageIdentityOf(m)
		if err != nil {
			return MessageAnchor{}, false
		}
		if cid == id {
			occ++
		}
	}
	return validatedAnchor(id, occ)
}

func validatedAnchor(id MessageIdentity, occ uint32) (MessageAnchor, bool) {
	anchor := MessageAnchor{Identity: id, Occurrence: occ}
	return anchor, anchor.Validate() == nil
}

// deriveCarriedAnchors returns the request-local anchors a proven trajectory
// lineage yields for the after-message placements early projection already
// resolved, or nil when no placement may be carried forward.
//
// The only admissible basis is the frozen filtered baseline plus the early
// projection evidence. Nothing here reads the conversation-view store, and
// nothing here is aware of which stage rewrote which payload. Every failure to
// prove a placement - no frozen evidence, an anchor the baseline cannot resolve,
// no structural bijection, an authority or partition change - yields nil for that
// overlay so the existing exact-resolution / anchor-missing-policy outcome stands.
func deriveCarriedAnchors(snap Snapshot, provenance []OverlayProvenance, frozen, cleaned lipapi.Call) carriedAnchors {
	if len(provenance) == 0 {
		return nil
	}
	lineage, ok := proveTrajectoryLineage(frozen, cleaned)
	if !ok || !lineage.proven() {
		return nil
	}
	early := make(map[string]MessageAnchor, len(provenance))
	for _, p := range provenance {
		if p.ResolvedKind != PlacementAfterMessage || p.ResolvedAnchor == nil {
			continue
		}
		early[p.OverlayID] = *p.ResolvedAnchor
	}
	out := carriedAnchors{}
	for _, ov := range snap.Steering {
		if !ov.Active || ov.Placement.Kind != PlacementAfterMessage || ov.Placement.Anchor == nil {
			continue
		}
		stored := *ov.Placement.Anchor
		// The early projection is the only authority allowed to have resolved this
		// placement. Without its frozen evidence there is nothing to carry forward.
		resolved, ok := early[ov.OverlayID]
		if !ok || resolved != stored {
			continue
		}
		slot, ok := lineage.frozenSlot(frozen, stored)
		if !ok {
			continue
		}
		carried, ok := lineage.currentAnchorAt(cleaned, slot)
		if !ok || carried == stored {
			// A carried anchor identical to the stored one is already covered by
			// exact resolution, which stays the normal path.
			continue
		}
		out[ov.OverlayID] = carried
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---------------------------------------------------------------------------
// Structural compatibility. Payload bytes are absent from every comparison below.
// ---------------------------------------------------------------------------

// messageLineageCompatible reports whether two legacy complete messages occupy the
// same logical trajectory slot.
func messageLineageCompatible(frozen, current lipapi.Message) bool {
	if frozen.Role != current.Role || len(frozen.Parts) != len(current.Parts) {
		return false
	}
	for i := range frozen.Parts {
		if !partLineageCompatible(frozen.Parts[i], current.Parts[i]) {
			return false
		}
	}
	return true
}

// partLineageCompatible compares one ordered part's kind, cardinality-compatible
// reference fields, and tool identities. Text and the JSON Content document are
// payload and are not compared.
func partLineageCompatible(frozen, current lipapi.Part) bool {
	return frozen.Kind == current.Kind &&
		frozen.ToolCallID == current.ToolCallID &&
		frozen.ToolName == current.ToolName &&
		frozen.ImageRef == current.ImageRef &&
		frozen.ImageMIME == current.ImageMIME &&
		frozen.FileRef == current.FileRef &&
		frozen.FileMIME == current.FileMIME &&
		frozen.FileName == current.FileName &&
		reasoningLineageCompatible(frozen.Reasoning, current.Reasoning)
}

// itemLineageCompatible reports whether two items occupy the same logical
// trajectory slot, across every canonical item kind.
func itemLineageCompatible(frozen, current lipapi.Item) bool {
	if frozen.Kind != current.Kind ||
		frozen.ID != current.ID ||
		frozen.Status != current.Status ||
		frozen.Phase != current.Phase ||
		frozen.Role != current.Role {
		return false
	}
	switch frozen.Kind {
	case lipapi.ItemKindMessage:
		return contentPartsLineageCompatible(frozen.Content, current.Content)
	case lipapi.ItemKindItemReference:
		return frozen.Reference != nil && current.Reference != nil && *frozen.Reference == *current.Reference
	case lipapi.ItemKindToolCall:
		if frozen.ToolCall == nil || current.ToolCall == nil {
			return frozen.ToolCall == nil && current.ToolCall == nil
		}
		return frozen.ToolCall.CallID == current.ToolCall.CallID &&
			frozen.ToolCall.Name == current.ToolCall.Name
	case lipapi.ItemKindToolResult:
		if frozen.ToolResult == nil || current.ToolResult == nil {
			return frozen.ToolResult == nil && current.ToolResult == nil
		}
		return frozen.ToolResult.CallID == current.ToolResult.CallID &&
			frozen.ToolResult.Name == current.ToolResult.Name &&
			contentPartsLineageCompatible(frozen.ToolResult.Parts, current.ToolResult.Parts)
	case lipapi.ItemKindReasoning:
		return reasoningLineageCompatible(reasoningItemPart(frozen.Reasoning), reasoningItemPart(current.Reasoning))
	case lipapi.ItemKindCompaction:
		if frozen.Compaction == nil || current.Compaction == nil {
			return frozen.Compaction == nil && current.Compaction == nil
		}
		return frozen.Compaction.EncapsulatedID == current.Compaction.EncapsulatedID &&
			frozen.Compaction.Dialect == current.Compaction.Dialect
	default:
		return itemExtensionLineageCompatible(frozen.Extension, current.Extension)
	}
}

// contentPartsLineageCompatible compares an ordered content-part sequence by kind,
// reference fields, stable references, and reasoning/extension shape. Text, file
// data, refusal, summary, annotation payload, and JSON documents are payload.
func contentPartsLineageCompatible(frozen, current []lipapi.ContentPart) bool {
	if len(frozen) != len(current) {
		return false
	}
	for i := range frozen {
		f, c := frozen[i], current[i]
		if f.Kind != c.Kind ||
			f.ImageRef != c.ImageRef ||
			f.ImageMIME != c.ImageMIME ||
			f.FileRef != c.FileRef ||
			f.FileMIME != c.FileMIME ||
			f.FileName != c.FileName ||
			f.VideoRef != c.VideoRef ||
			f.VideoMIME != c.VideoMIME ||
			f.AssistantRef != c.AssistantRef {
			return false
		}
		if !reasoningLineageCompatible(f.Reasoning, c.Reasoning) {
			return false
		}
		if (f.Extension == nil) != (c.Extension == nil) {
			return false
		}
		if f.Extension != nil &&
			(f.Extension.Namespace != c.Extension.Namespace ||
				f.Extension.Type != c.Extension.Type ||
				f.Extension.Implementor != c.Extension.Implementor) {
			return false
		}
	}
	return true
}

// reasoningLineageCompatible compares only the shape of a historical reasoning
// record: presence, dialect, and encrypted-content presence. Text, signature, and
// opaque metadata are payload.
func reasoningLineageCompatible(frozen, current *lipapi.ReasoningPart) bool {
	if (frozen == nil) != (current == nil) {
		return false
	}
	if frozen == nil {
		return true
	}
	return lipapi.NormalizeReasoningDialect(frozen.Dialect) == lipapi.NormalizeReasoningDialect(current.Dialect) &&
		frozen.EncryptedContentPresent == current.EncryptedContentPresent
}

func reasoningItemPart(item *lipapi.ReasoningItem) *lipapi.ReasoningPart {
	if item == nil {
		return nil
	}
	return item.Reasoning
}

func itemExtensionLineageCompatible(frozen, current *lipapi.OpaqueExtension) bool {
	if (frozen == nil) != (current == nil) {
		return false
	}
	if frozen == nil {
		return true
	}
	return frozen.Namespace == current.Namespace &&
		frozen.Type == current.Type &&
		frozen.Implementor == current.Implementor &&
		frozen.Direction == current.Direction
}
