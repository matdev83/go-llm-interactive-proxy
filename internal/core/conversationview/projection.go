package conversationview

import (
	"fmt"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// FallbackEvidence records a deterministic stable-prefix fallback for a missing anchor.
type FallbackEvidence struct {
	OverlayID string        `json:"overlay_id"`
	Anchor    MessageAnchor `json:"anchor"`
}

// ProjectionEvidence captures bounded diagnostics for a successful projection.
type ProjectionEvidence struct {
	FilteredCount int                `json:"filtered_count"`
	InjectedCount int                `json:"injected_count"`
	Fallbacks     []FallbackEvidence `json:"fallbacks,omitempty"`
}

// Project derives the backend-effective call from call and snap.
// It is pure, deterministic and never mutates the input call.
// Exclusion is applied first, then steering injection; result is validated.
func Project(call lipapi.Call, snap Snapshot) (lipapi.Call, *ProjectionEvidence, error) {
	if call.HasItemAuthority() {
		return projectItems(call, snap)
	}
	return projectLegacy(call, snap)
}

// ResolveAfterIngressTailAnchor resolves the terminal forwardable user message
// in call (filtered by snap exclusions) to a MessageAnchor.
// It rejects if the terminal message is absent, not user, or not safe.
func ResolveAfterIngressTailAnchor(call lipapi.Call, snap Snapshot) (MessageAnchor, error) {
	exclusion := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		exclusion[t.Identity] = struct{}{}
	}
	if call.HasItemAuthority() {
		// Build forwardable message items.
		var fwd []lipapi.Item
		for _, it := range call.Items {
			if it.Kind != lipapi.ItemKindMessage {
				continue
			}
			id, err := ItemIdentityOf(it)
			if err != nil {
				return MessageAnchor{}, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
			}
			if _, excluded := exclusion[id]; excluded {
				continue
			}
			fwd = append(fwd, it)
		}
		if len(fwd) == 0 {
			return MessageAnchor{}, fmt.Errorf("%w: no forwardable message", ErrTerminalUserNotFound)
		}
		hasUser := false
		for _, it := range fwd {
			if it.Role == lipapi.RoleUser {
				hasUser = true
				break
			}
		}
		if !hasUser {
			return MessageAnchor{}, fmt.Errorf("%w: no forwardable user message", ErrTerminalUserNotFound)
		}
		last := fwd[len(fwd)-1]
		if last.Role != lipapi.RoleUser {
			return MessageAnchor{}, fmt.Errorf("%w: terminal is %q", ErrTerminalNotUser, last.Role)
		}
		id, err := ItemIdentityOf(last)
		if err != nil {
			return MessageAnchor{}, err
		}
		var occ uint32
		for _, it := range fwd {
			cid, _ := ItemIdentityOf(it)
			if cid == id {
				occ++
			}
		}
		anchor := MessageAnchor{Identity: id, Occurrence: occ}
		if err := anchor.Validate(); err != nil {
			return MessageAnchor{}, err
		}
		return anchor, nil
	}
	// Legacy authority.
	var fwdInstr []lipapi.Message
	var fwdMsgs []lipapi.Message
	for _, m := range call.Instructions {
		id, err := MessageIdentityOf(m)
		if err != nil {
			return MessageAnchor{}, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[id]; excluded {
			continue
		}
		fwdInstr = append(fwdInstr, m)
	}
	for _, m := range call.Messages {
		id, err := MessageIdentityOf(m)
		if err != nil {
			return MessageAnchor{}, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[id]; excluded {
			continue
		}
		fwdMsgs = append(fwdMsgs, m)
	}
	// Combined trajectory for occurrence counting.
	combined := append(append([]lipapi.Message(nil), fwdInstr...), fwdMsgs...)
	if len(combined) == 0 {
		return MessageAnchor{}, fmt.Errorf("%w: empty forwardable trajectory", ErrTerminalUserNotFound)
	}
	// Check if any forwardable user exists.
	hasUser := false
	for _, m := range combined {
		if m.Role == lipapi.RoleUser {
			hasUser = true
			break
		}
	}
	if !hasUser {
		return MessageAnchor{}, fmt.Errorf("%w: no forwardable user message", ErrTerminalUserNotFound)
	}
	last := combined[len(combined)-1]
	if last.Role != lipapi.RoleUser {
		return MessageAnchor{}, fmt.Errorf("%w: terminal is %q", ErrTerminalNotUser, last.Role)
	}
	id, err := MessageIdentityOf(last)
	if err != nil {
		return MessageAnchor{}, err
	}
	var occ uint32
	for _, m := range combined {
		cid, _ := MessageIdentityOf(m)
		if cid == id {
			occ++
		}
	}
	anchor := MessageAnchor{Identity: id, Occurrence: occ}
	if err := anchor.Validate(); err != nil {
		return MessageAnchor{}, err
	}
	return anchor, nil
}

// ---------------------------------------------------------------------------
// item authority projection
// ---------------------------------------------------------------------------

func projectItems(call lipapi.Call, snap Snapshot) (lipapi.Call, *ProjectionEvidence, error) {
	exclusion := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		exclusion[t.Identity] = struct{}{}
	}
	// Phase 1: exclusion of complete messages.
	var removedIDs map[string]struct{}
	filteredCount := 0
	var filtered []lipapi.Item
	filtered = make([]lipapi.Item, 0, len(call.Items))
	removedIDs = make(map[string]struct{})
	for _, it := range call.Items {
		if it.Kind == lipapi.ItemKindMessage {
			id, err := ItemIdentityOf(it)
			if err != nil {
				return lipapi.Call{}, nil, fmt.Errorf("%w: invalid message item: %v", ErrProjectionFailed, err)
			}
			if _, excluded := exclusion[id]; excluded {
				filteredCount++
				if it.ID != "" {
					removedIDs[it.ID] = struct{}{}
				}
				continue
			}
		}
		filtered = append(filtered, it)
	}
	// Phase 2: dependency cleanup – drop item_reference targeting removed concrete IDs.
	var afterRef []lipapi.Item
	afterRef = make([]lipapi.Item, 0, len(filtered))
	for _, it := range filtered {
		if it.Kind == lipapi.ItemKindItemReference && it.Reference != nil {
			if _, removed := removedIDs[it.Reference.ID]; removed {
				continue
			}
		}
		afterRef = append(afterRef, it)
	}
	filtered = afterRef

	// Phase 3: placements
	var stable []SteeringOverlay
	var after []SteeringOverlay
	for _, ov := range snap.Steering {
		if !ov.Active {
			continue
		}
		switch ov.Placement.Kind {
		case PlacementStablePrefix:
			stable = append(stable, ov)
		case PlacementAfterMessage:
			after = append(after, ov)
		default:
			return lipapi.Call{}, nil, fmt.Errorf("%w: unknown placement %q", ErrProjectionFailed, ov.Placement.Kind)
		}
	}
	sort.Slice(stable, func(i, j int) bool { return stable[i].SlotOrdinal < stable[j].SlotOrdinal })
	// Resolve after anchors
	type resolved struct {
		ov  SteeringOverlay
		idx int
	}
	var resolvedAnchors []resolved
	var fallbackOverlays []SteeringOverlay
	var fallbacks []FallbackEvidence
	for _, ov := range after {
		if ov.Placement.Anchor == nil {
			return lipapi.Call{}, nil, fmt.Errorf("%w: overlay %s missing anchor", ErrProjectionFailed, ov.OverlayID)
		}
		idx, found, err := resolveAnchorInItems(filtered, *ov.Placement.Anchor)
		if err != nil {
			return lipapi.Call{}, nil, fmt.Errorf("%w: overlay %s: %v", ErrProjectionFailed, ov.OverlayID, err)
		}
		if !found {
			switch ov.AnchorMissingPolicy {
			case AnchorStablePrefixFallback:
				fallbackOverlays = append(fallbackOverlays, ov)
				fallbacks = append(fallbacks, FallbackEvidence{OverlayID: ov.OverlayID, Anchor: *ov.Placement.Anchor})
			case AnchorFailClosed:
				return lipapi.Call{}, nil, fmt.Errorf("%w: overlay %s anchor %s missing: %v", ErrAnchorMissing, ov.OverlayID, ov.Placement.Anchor.String(), ErrAnchorNotFound)
			default:
				return lipapi.Call{}, nil, fmt.Errorf("%w: unknown policy for overlay %s", ErrProjectionFailed, ov.OverlayID)
			}
		} else {
			resolvedAnchors = append(resolvedAnchors, resolved{ov: ov, idx: idx})
		}
	}
	// Merge fallbacks into stable and re-sort.
	if len(fallbackOverlays) > 0 {
		stable = append(stable, fallbackOverlays...)
		sort.Slice(stable, func(i, j int) bool { return stable[i].SlotOrdinal < stable[j].SlotOrdinal })
	}
	sort.Slice(resolvedAnchors, func(i, j int) bool {
		if resolvedAnchors[i].idx != resolvedAnchors[j].idx {
			return resolvedAnchors[i].idx < resolvedAnchors[j].idx
		}
		return resolvedAnchors[i].ov.SlotOrdinal < resolvedAnchors[j].ov.SlotOrdinal
	})

	leading := leadingInstructionItemCount(filtered)
	stableItems := make([]lipapi.Item, 0, len(stable))
	for _, ov := range stable {
		stableItems = append(stableItems, steeringOverlayToItem(ov))
	}
	// Assemble final items: prefix + stable + history with after injections.
	final := make([]lipapi.Item, 0, len(filtered)+len(stable)+len(resolvedAnchors))
	// prefix
	final = append(final, filtered[:leading]...)
	// stable
	final = append(final, stableItems...)
	// history with after injections
	// Build map from idx to list of overlays (in order)
	afterByIdx := make(map[int][]SteeringOverlay)
	for _, r := range resolvedAnchors {
		afterByIdx[r.idx] = append(afterByIdx[r.idx], r.ov)
	}
	for i := leading; i < len(filtered); i++ {
		final = append(final, filtered[i])
		if ovs, ok := afterByIdx[i]; ok {
			for _, ov := range ovs {
				final = append(final, steeringOverlayToItem(ov))
			}
		}
	}
	// Handle anchors that were inside prefix region (rare): they would be before stable insertion.
	// We already handled afterByIdx for i < leading if any: those would not be injected above.
	// Fix: inject prefix anchors immediately after their prefix item before stable.
	// For simplicity, handle prefix anchors separately: rebuild prefix with interleaved after.
	prefixAnchors := 0
	for _, r := range resolvedAnchors {
		if r.idx < leading {
			prefixAnchors++
		}
	}
	if prefixAnchors > 0 {
		// Rebuild to interleave correctly.
		fixed := make([]lipapi.Item, 0, len(filtered)+len(stable)+len(resolvedAnchors))
		for i := 0; i < leading; i++ {
			fixed = append(fixed, filtered[i])
			if ovs, ok := afterByIdx[i]; ok {
				for _, ov := range ovs {
					fixed = append(fixed, steeringOverlayToItem(ov))
				}
			}
		}
		fixed = append(fixed, stableItems...)
		for i := leading; i < len(filtered); i++ {
			fixed = append(fixed, filtered[i])
			if ovs, ok := afterByIdx[i]; ok {
				for _, ov := range ovs {
					fixed = append(fixed, steeringOverlayToItem(ov))
				}
			}
		}
		final = fixed
	}

	out := lipapi.CloneCall(call)
	out.Items = final

	if err := out.Validate(); err != nil {
		return lipapi.Call{}, nil, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
	}
	evidence := &ProjectionEvidence{
		FilteredCount: filteredCount,
		InjectedCount: len(stable) + len(resolvedAnchors),
		Fallbacks:     fallbacks,
	}
	return out, evidence, nil
}

// ---------------------------------------------------------------------------
// legacy authority projection
// ---------------------------------------------------------------------------

func projectLegacy(call lipapi.Call, snap Snapshot) (lipapi.Call, *ProjectionEvidence, error) {
	exclusion := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		exclusion[t.Identity] = struct{}{}
	}
	var filteredInstr []lipapi.Message
	var filteredMsgs []lipapi.Message
	filteredCount := 0
	for _, m := range call.Instructions {
		id, err := MessageIdentityOf(m)
		if err != nil {
			return lipapi.Call{}, nil, fmt.Errorf("%w: invalid instruction message: %v", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[id]; excluded {
			filteredCount++
			continue
		}
		filteredInstr = append(filteredInstr, m)
	}
	for _, m := range call.Messages {
		id, err := MessageIdentityOf(m)
		if err != nil {
			return lipapi.Call{}, nil, fmt.Errorf("%w: invalid message: %v", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[id]; excluded {
			filteredCount++
			continue
		}
		filteredMsgs = append(filteredMsgs, m)
	}

	var stable []SteeringOverlay
	var after []SteeringOverlay
	for _, ov := range snap.Steering {
		if !ov.Active {
			continue
		}
		switch ov.Placement.Kind {
		case PlacementStablePrefix:
			stable = append(stable, ov)
		case PlacementAfterMessage:
			after = append(after, ov)
		default:
			return lipapi.Call{}, nil, fmt.Errorf("%w: unknown placement", ErrProjectionFailed)
		}
	}
	sort.Slice(stable, func(i, j int) bool { return stable[i].SlotOrdinal < stable[j].SlotOrdinal })

	type resolved struct {
		ov      SteeringOverlay
		isInstr bool
		idx     int
	}
	var resolvedAnchors []resolved
	var fallbackOverlays []SteeringOverlay
	var fallbacks []FallbackEvidence
	for _, ov := range after {
		if ov.Placement.Anchor == nil {
			return lipapi.Call{}, nil, fmt.Errorf("%w: overlay %s missing anchor", ErrProjectionFailed, ov.OverlayID)
		}
		isInstr, idx, found, err := resolveAnchorLegacy(filteredInstr, filteredMsgs, *ov.Placement.Anchor)
		if err != nil {
			return lipapi.Call{}, nil, fmt.Errorf("%w: overlay %s: %v", ErrProjectionFailed, ov.OverlayID, err)
		}
		if !found {
			switch ov.AnchorMissingPolicy {
			case AnchorStablePrefixFallback:
				fallbackOverlays = append(fallbackOverlays, ov)
				fallbacks = append(fallbacks, FallbackEvidence{OverlayID: ov.OverlayID, Anchor: *ov.Placement.Anchor})
			case AnchorFailClosed:
				return lipapi.Call{}, nil, fmt.Errorf("%w: overlay %s anchor %s missing", ErrAnchorMissing, ov.OverlayID, ov.Placement.Anchor.String())
			default:
				return lipapi.Call{}, nil, fmt.Errorf("%w: unknown policy", ErrProjectionFailed)
			}
		} else {
			resolvedAnchors = append(resolvedAnchors, resolved{ov: ov, isInstr: isInstr, idx: idx})
		}
	}
	if len(fallbackOverlays) > 0 {
		stable = append(stable, fallbackOverlays...)
		sort.Slice(stable, func(i, j int) bool { return stable[i].SlotOrdinal < stable[j].SlotOrdinal })
	}
	// Sort resolved: instructions first, then messages by idx, then slot.
	sort.Slice(resolvedAnchors, func(i, j int) bool {
		if resolvedAnchors[i].isInstr != resolvedAnchors[j].isInstr {
			return resolvedAnchors[i].isInstr && !resolvedAnchors[j].isInstr
		}
		if resolvedAnchors[i].idx != resolvedAnchors[j].idx {
			return resolvedAnchors[i].idx < resolvedAnchors[j].idx
		}
		return resolvedAnchors[i].ov.SlotOrdinal < resolvedAnchors[j].ov.SlotOrdinal
	})

	// Build stable messages for instructions region.
	stableMsgs := make([]lipapi.Message, 0, len(stable))
	for _, ov := range stable {
		stableMsgs = append(stableMsgs, steeringOverlayToMessage(ov))
	}

	// Assemble instructions: filteredInstr + stable (appended) + after anchored within instructions interleaved.
	// For simplicity, build instructions final with after injections.
	finalInstr := make([]lipapi.Message, 0, len(filteredInstr)+len(stableMsgs)+len(resolvedAnchors))
	afterInstrByIdx := make(map[int][]SteeringOverlay)
	for _, r := range resolvedAnchors {
		if r.isInstr {
			afterInstrByIdx[r.idx] = append(afterInstrByIdx[r.idx], r.ov)
		}
	}
	for i, m := range filteredInstr {
		finalInstr = append(finalInstr, m)
		if ovs, ok := afterInstrByIdx[i]; ok {
			for _, ov := range ovs {
				finalInstr = append(finalInstr, steeringOverlayToMessage(ov))
			}
		}
	}
	// Append stable overlays at end of instruction region (after all instruction messages and their after injections).
	finalInstr = append(finalInstr, stableMsgs...)

	// Assemble messages final with after injections.
	finalMsgs := make([]lipapi.Message, 0, len(filteredMsgs)+len(resolvedAnchors))
	afterMsgByIdx := make(map[int][]SteeringOverlay)
	for _, r := range resolvedAnchors {
		if !r.isInstr {
			afterMsgByIdx[r.idx] = append(afterMsgByIdx[r.idx], r.ov)
		}
	}
	for i, m := range filteredMsgs {
		finalMsgs = append(finalMsgs, m)
		if ovs, ok := afterMsgByIdx[i]; ok {
			for _, ov := range ovs {
				finalMsgs = append(finalMsgs, steeringOverlayToMessage(ov))
			}
		}
	}

	out := lipapi.CloneCall(call)
	out.Instructions = finalInstr
	out.Messages = finalMsgs
	// Ensure we don't leave Items nil vs empty confusion: CloneCall already handled.

	if err := out.Validate(); err != nil {
		return lipapi.Call{}, nil, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
	}
	evidence := &ProjectionEvidence{
		FilteredCount: filteredCount,
		InjectedCount: len(stable) + len(resolvedAnchors),
		Fallbacks:     fallbacks,
	}
	return out, evidence, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func leadingInstructionItemCount(items []lipapi.Item) int {
	count := 0
	for _, it := range items {
		if it.Kind != lipapi.ItemKindMessage {
			break
		}
		if it.Role == lipapi.RoleSystem || it.Role == lipapi.RoleDeveloper {
			count++
		} else {
			break
		}
	}
	return count
}

func steeringOverlayToItem(ov SteeringOverlay) lipapi.Item {
	text := ov.Message.Text
	role := ov.Message.Role
	if role == "" {
		role = lipapi.RoleSystem
	}
	return lipapi.Item{
		Kind:   lipapi.ItemKindMessage,
		ID:     "lip-steering-" + ov.OverlayID,
		Status: lipapi.ItemStatusCompleted,
		Role:   role,
		Content: []lipapi.ContentPart{
			{Kind: lipapi.ContentPartText, Text: text},
		},
	}
}

func steeringOverlayToMessage(ov SteeringOverlay) lipapi.Message {
	text := ov.Message.Text
	role := ov.Message.Role
	if role == "" {
		role = lipapi.RoleSystem
	}
	return lipapi.Message{
		Role:  role,
		Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: text}},
	}
}

func resolveAnchorInItems(items []lipapi.Item, anchor MessageAnchor) (int, bool, error) {
	if err := anchor.Validate(); err != nil {
		return -1, false, err
	}
	counts := make(map[MessageIdentity]uint32)
	for i, it := range items {
		if it.Kind != lipapi.ItemKindMessage {
			continue
		}
		id, err := ItemIdentityOf(it)
		if err != nil {
			return -1, false, err
		}
		counts[id]++
		if id == anchor.Identity && counts[id] == anchor.Occurrence {
			return i, true, nil
		}
	}
	return -1, false, nil
}

func resolveAnchorLegacy(instr []lipapi.Message, msgs []lipapi.Message, anchor MessageAnchor) (bool, int, bool, error) {
	if err := anchor.Validate(); err != nil {
		return false, -1, false, err
	}
	counts := make(map[MessageIdentity]uint32)
	for i, m := range instr {
		id, err := MessageIdentityOf(m)
		if err != nil {
			return false, -1, false, err
		}
		counts[id]++
		if id == anchor.Identity && counts[id] == anchor.Occurrence {
			return true, i, true, nil
		}
	}
	for i, m := range msgs {
		id, err := MessageIdentityOf(m)
		if err != nil {
			return false, -1, false, err
		}
		counts[id]++
		if id == anchor.Identity && counts[id] == anchor.Occurrence {
			return false, i, true, nil
		}
	}
	return false, -1, false, nil
}
