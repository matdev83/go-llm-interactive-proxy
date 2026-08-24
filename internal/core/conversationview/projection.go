package conversationview

import (
	"fmt"
	"math"
	"sort"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func boolPtr(b bool) *bool { v := b; return &v }
func intPtr(i int) *int    { v := i; return &v }

func checkedCapacity(parts ...int) (int, error) {
	total := 0
	for _, part := range parts {
		if part < 0 || total > math.MaxInt-part {
			return 0, ErrProjectionFailed
		}
		total += part
	}
	return total, nil
}

// FallbackEvidence records a deterministic stable-prefix fallback for a missing anchor.
type FallbackEvidence struct {
	OverlayID string        `json:"overlay_id"`
	Anchor    MessageAnchor `json:"anchor"`
}

// OverlayProvenance is deterministic request-local provenance for one injected overlay.
// It is derived solely from Snapshot + input call and is used by a later final-reassertion
// stage to recognize/rebuild/remove projection-owned steering instances without string heuristics.
// It does not affect MessageIdentityOf values or model-visible content.
// Injected* fields record the exact trajectory position of the injected copy for
// placement-aware reassertion without broad identity sweeps.
type OverlayProvenance struct {
	OverlayID        string          `json:"overlay_id"`
	Revision         uint64          `json:"revision"`
	SlotOrdinal      uint64          `json:"slot_ordinal"`
	ResolvedKind     PlacementKind   `json:"resolved_kind"`
	ResolvedAnchor   *MessageAnchor  `json:"resolved_anchor,omitempty"`
	InjectedIdentity MessageIdentity `json:"injected_identity"`
	// Exact injected position for placement-aware reassertion.
	InjectedAuthority           string `json:"injected_authority"`            // "item" or "legacy"
	InjectedItemIndex           int    `json:"injected_item_index,omitempty"` // valid when authority == "item"
	InjectedLegacyIsInstruction *bool  `json:"injected_legacy_is_instruction,omitempty"`
	InjectedLegacyIndex         *int   `json:"injected_legacy_index,omitempty"`
	InjectedTrajectoryIndex     int    `json:"injected_trajectory_index"`
}

// MatchesMessage reports whether msg is the projection-owned copy for this provenance entry
// by semantic identity comparison. It is pure and uses no string-marker heuristics.
func (p OverlayProvenance) MatchesMessage(msg lipapi.Message) bool {
	id, err := MessageIdentityOf(msg)
	if err != nil {
		return false
	}
	return id == p.InjectedIdentity
}

// MatchesItem reports whether item is the projection-owned copy for this provenance entry.
func (p OverlayProvenance) MatchesItem(item lipapi.Item) bool {
	if item.Kind != lipapi.ItemKindMessage {
		return false
	}
	id, err := ItemIdentityOf(item)
	if err != nil {
		return false
	}
	return id == p.InjectedIdentity
}

// ProjectionEvidence captures bounded diagnostics for a successful projection.
type ProjectionEvidence struct {
	FilteredCount int                 `json:"filtered_count"`
	InjectedCount int                 `json:"injected_count"`
	Fallbacks     []FallbackEvidence  `json:"fallbacks,omitempty"`
	Provenance    []OverlayProvenance `json:"provenance,omitempty"`
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
// It first establishes the concrete original terminal complete message boundary
// and rejects if that terminal's identity is in snapshot exclusions (never falls
// back to an earlier forwardable user).
func ResolveAfterIngressTailAnchor(call lipapi.Call, snap Snapshot) (MessageAnchor, error) {
	exclusion := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		exclusion[t.Identity] = struct{}{}
	}
	if call.HasItemAuthority() {
		// Establish concrete terminal message boundary before filtering.
		termIdx := -1
		for i := len(call.Items) - 1; i >= 0; i-- {
			if call.Items[i].Kind == lipapi.ItemKindMessage {
				termIdx = i
				break
			}
		}
		if termIdx < 0 {
			return MessageAnchor{}, fmt.Errorf("%w: no forwardable message", ErrTerminalUserNotFound)
		}
		terminal := call.Items[termIdx]
		termID, err := ItemIdentityOf(terminal)
		if err != nil {
			return MessageAnchor{}, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[termID]; excluded {
			return MessageAnchor{}, fmt.Errorf("%w: terminal message is never_backend", ErrTerminalUserNotFound)
		}
		if terminal.Role != lipapi.RoleUser {
			return MessageAnchor{}, fmt.Errorf("%w: terminal is %q", ErrTerminalNotUser, terminal.Role)
		}
		// Derive the surviving (backend-effective) trajectory with the same
		// semantics as projection: excluded complete messages are removed and
		// item_reference entries targeting removed message IDs are dropped.
		// The FINAL surviving item must be the terminal complete user message;
		// any surviving non-message item after it makes the boundary unsafe.
		removedIDs := make(map[string]struct{})
		for _, it := range call.Items {
			if it.Kind != lipapi.ItemKindMessage {
				continue
			}
			id, err := ItemIdentityOf(it)
			if err != nil {
				return MessageAnchor{}, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
			}
			if _, excluded := exclusion[id]; excluded && it.ID != "" {
				removedIDs[it.ID] = struct{}{}
			}
		}
		lastSurvivorIdx := -1
		occ := uint32(0)
		for i, it := range call.Items {
			if it.Kind == lipapi.ItemKindMessage {
				id, err := ItemIdentityOf(it)
				if err != nil {
					return MessageAnchor{}, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
				}
				if _, excluded := exclusion[id]; excluded {
					continue
				}
				lastSurvivorIdx = i
				if id == termID {
					occ++
				}
				continue
			}
			if it.Kind == lipapi.ItemKindItemReference && it.Reference != nil {
				if _, removed := removedIDs[it.Reference.ID]; removed {
					continue
				}
			}
			lastSurvivorIdx = i
		}
		if lastSurvivorIdx < 0 {
			return MessageAnchor{}, fmt.Errorf("%w: no forwardable message", ErrTerminalUserNotFound)
		}
		if lastSurvivorIdx != termIdx {
			surviving := call.Items[lastSurvivorIdx]
			detail := string(surviving.Kind)
			if surviving.Kind == lipapi.ItemKindMessage {
				detail = string(surviving.Role)
			}
			return MessageAnchor{}, fmt.Errorf("%w: terminal %q item is not a safe user-message boundary", ErrTerminalNotUser, detail)
		}
		anchor := MessageAnchor{Identity: termID, Occurrence: occ}
		if err := anchor.Validate(); err != nil {
			return MessageAnchor{}, err
		}
		return anchor, nil
	}
	// Legacy authority: establish concrete terminal boundary over original trajectory.
	origCombined := append(append([]lipapi.Message(nil), call.Instructions...), call.Messages...)
	if len(origCombined) == 0 {
		return MessageAnchor{}, fmt.Errorf("%w: empty forwardable trajectory", ErrTerminalUserNotFound)
	}
	origLast := origCombined[len(origCombined)-1]
	origID, err := MessageIdentityOf(origLast)
	if err != nil {
		return MessageAnchor{}, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
	}
	if _, excluded := exclusion[origID]; excluded {
		return MessageAnchor{}, fmt.Errorf("%w: terminal message is never_backend", ErrTerminalUserNotFound)
	}
	if origLast.Role != lipapi.RoleUser {
		return MessageAnchor{}, fmt.Errorf("%w: terminal is %q", ErrTerminalNotUser, origLast.Role)
	}
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
	// Build map from idx to list of overlays (in order)
	afterByIdx := make(map[int][]SteeringOverlay)
	for _, r := range resolvedAnchors {
		afterByIdx[r.idx] = append(afterByIdx[r.idx], r.ov)
	}
	// Assemble final items deterministically and record exact injected positions
	// without relying on synthetic ID string markers. Provenance indices are
	// derived solely from placement logic (Snapshot + input) and are not
	// part of the model-visible content identity.
	finalCapacity, err := checkedCapacity(len(filtered), len(stable), len(resolvedAnchors))
	if err != nil {
		return lipapi.Call{}, nil, fmt.Errorf("%w: projected item capacity overflow", err)
	}
	provenanceCapacity, err := checkedCapacity(len(stable), len(resolvedAnchors))
	if err != nil {
		return lipapi.Call{}, nil, fmt.Errorf("%w: projection provenance capacity overflow", err)
	}
	final := make([]lipapi.Item, 0, finalCapacity)
	provIndex := make(map[string]int, provenanceCapacity)
	// prefix region with interleaved after anchors that fall inside prefix
	for i := range leading {
		final = append(final, filtered[i])
		if ovs, ok := afterByIdx[i]; ok {
			for _, ov := range ovs {
				provIndex[ov.OverlayID] = len(final)
				final = append(final, steeringOverlayToItem(ov))
			}
		}
	}
	// stable prefix region (deterministic SlotOrdinal order)
	for _, ov := range stable {
		provIndex[ov.OverlayID] = len(final)
		final = append(final, steeringOverlayToItem(ov))
	}
	// history region with after injections
	for i := leading; i < len(filtered); i++ {
		final = append(final, filtered[i])
		if ovs, ok := afterByIdx[i]; ok {
			for _, ov := range ovs {
				provIndex[ov.OverlayID] = len(final)
				final = append(final, steeringOverlayToItem(ov))
			}
		}
	}

	out := lipapi.CloneCall(call)
	out.Items = final

	if err := out.Validate(); err != nil {
		return lipapi.Call{}, nil, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
	}
	// Deterministic provenance: derived solely from Snapshot + input, with exact injected positions.
	provenance := make([]OverlayProvenance, 0, provenanceCapacity)
	for _, ov := range stable {
		idx := provIndex[ov.OverlayID]
		// provIndex always present for stable; fallback to -1 only on invariant breach
		if _, ok := provIndex[ov.OverlayID]; !ok {
			idx = -1
		}
		provenance = append(provenance, OverlayProvenance{
			OverlayID:               ov.OverlayID,
			Revision:                ov.Revision,
			SlotOrdinal:             ov.SlotOrdinal,
			ResolvedKind:            PlacementStablePrefix,
			ResolvedAnchor:          nil,
			InjectedIdentity:        overlayInjectedIdentity(ov),
			InjectedAuthority:       "item",
			InjectedItemIndex:       idx,
			InjectedTrajectoryIndex: idx,
		})
	}
	for _, r := range resolvedAnchors {
		idx := provIndex[r.ov.OverlayID]
		if _, ok := provIndex[r.ov.OverlayID]; !ok {
			idx = -1
		}
		anchorCopy := r.ov.Placement.Anchor
		var cp *MessageAnchor
		if anchorCopy != nil {
			tmp := *anchorCopy
			cp = &tmp
		}
		provenance = append(provenance, OverlayProvenance{
			OverlayID:               r.ov.OverlayID,
			Revision:                r.ov.Revision,
			SlotOrdinal:             r.ov.SlotOrdinal,
			ResolvedKind:            PlacementAfterMessage,
			ResolvedAnchor:          cp,
			InjectedIdentity:        overlayInjectedIdentity(r.ov),
			InjectedAuthority:       "item",
			InjectedItemIndex:       idx,
			InjectedTrajectoryIndex: idx,
		})
	}
	sort.Slice(provenance, func(i, j int) bool { return provenance[i].SlotOrdinal < provenance[j].SlotOrdinal })
	evidence := &ProjectionEvidence{
		FilteredCount: filteredCount,
		InjectedCount: provenanceCapacity,
		Fallbacks:     fallbacks,
		Provenance:    provenance,
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

	// Assemble instructions and messages with placement-aware provenance.
	provenanceCapacity, err := checkedCapacity(len(stable), len(resolvedAnchors))
	if err != nil {
		return lipapi.Call{}, nil, fmt.Errorf("%w: projection provenance capacity overflow", err)
	}
	provenanceLegacy := make([]OverlayProvenance, 0, provenanceCapacity)
	// Build finalInstr with after injections and stable, recording provenance indices.
	finalInstructionCapacity, err := checkedCapacity(len(filteredInstr), len(stable), len(resolvedAnchors))
	if err != nil {
		return lipapi.Call{}, nil, fmt.Errorf("%w: projected instruction capacity overflow", err)
	}
	finalInstr := make([]lipapi.Message, 0, finalInstructionCapacity)
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
				idx := len(finalInstr)
				finalInstr = append(finalInstr, steeringOverlayToMessage(ov))
				var cp *MessageAnchor
				if ov.Placement.Anchor != nil {
					tmp := *ov.Placement.Anchor
					cp = &tmp
				}
				provenanceLegacy = append(provenanceLegacy, OverlayProvenance{
					OverlayID:                   ov.OverlayID,
					Revision:                    ov.Revision,
					SlotOrdinal:                 ov.SlotOrdinal,
					ResolvedKind:                PlacementAfterMessage,
					ResolvedAnchor:              cp,
					InjectedIdentity:            overlayInjectedIdentity(ov),
					InjectedAuthority:           "legacy",
					InjectedLegacyIsInstruction: boolPtr(true),
					InjectedLegacyIndex:         intPtr(idx),
					InjectedTrajectoryIndex:     idx,
				})
			}
		}
	}
	// Append stable overlays at end of instruction region (after all instruction messages and their after injections).
	for pos, ov := range stable {
		idx := len(finalInstr)
		finalInstr = append(finalInstr, steeringOverlayToMessage(ov))
		_ = pos
		provenanceLegacy = append(provenanceLegacy, OverlayProvenance{
			OverlayID:                   ov.OverlayID,
			Revision:                    ov.Revision,
			SlotOrdinal:                 ov.SlotOrdinal,
			ResolvedKind:                PlacementStablePrefix,
			ResolvedAnchor:              nil,
			InjectedIdentity:            overlayInjectedIdentity(ov),
			InjectedAuthority:           "legacy",
			InjectedLegacyIsInstruction: boolPtr(true),
			InjectedLegacyIndex:         intPtr(idx),
			InjectedTrajectoryIndex:     idx,
		})
	}

	// Assemble messages final with after injections.
	finalMessageCapacity, err := checkedCapacity(len(filteredMsgs), len(resolvedAnchors))
	if err != nil {
		return lipapi.Call{}, nil, fmt.Errorf("%w: projected message capacity overflow", err)
	}
	finalMsgs := make([]lipapi.Message, 0, finalMessageCapacity)
	afterMsgByIdx := make(map[int][]SteeringOverlay)
	for _, r := range resolvedAnchors {
		if !r.isInstr {
			afterMsgByIdx[r.idx] = append(afterMsgByIdx[r.idx], r.ov)
		}
	}
	// We need base offset for trajectory index: instructions length.
	instrLen := len(finalInstr)
	for i, m := range filteredMsgs {
		finalMsgs = append(finalMsgs, m)
		if ovs, ok := afterMsgByIdx[i]; ok {
			for _, ov := range ovs {
				idxInMsgs := len(finalMsgs)
				finalMsgs = append(finalMsgs, steeringOverlayToMessage(ov))
				var cp *MessageAnchor
				if ov.Placement.Anchor != nil {
					tmp := *ov.Placement.Anchor
					cp = &tmp
				}
				provenanceLegacy = append(provenanceLegacy, OverlayProvenance{
					OverlayID:                   ov.OverlayID,
					Revision:                    ov.Revision,
					SlotOrdinal:                 ov.SlotOrdinal,
					ResolvedKind:                PlacementAfterMessage,
					ResolvedAnchor:              cp,
					InjectedIdentity:            overlayInjectedIdentity(ov),
					InjectedAuthority:           "legacy",
					InjectedLegacyIsInstruction: boolPtr(false),
					InjectedLegacyIndex:         intPtr(idxInMsgs),
					InjectedTrajectoryIndex:     instrLen + idxInMsgs,
				})
			}
		}
	}

	out := lipapi.CloneCall(call)
	out.Instructions = finalInstr
	out.Messages = finalMsgs

	if err := out.Validate(); err != nil {
		return lipapi.Call{}, nil, fmt.Errorf("%w: %v", ErrProjectionFailed, err)
	}
	// Stable already in provenanceLegacy, but we added stable above; ensure all stable added.
	// The above already added stable; no extra stable loop needed.
	sort.Slice(provenanceLegacy, func(i, j int) bool { return provenanceLegacy[i].SlotOrdinal < provenanceLegacy[j].SlotOrdinal })
	evidence := &ProjectionEvidence{
		FilteredCount: filteredCount,
		InjectedCount: provenanceCapacity,
		Fallbacks:     fallbacks,
		Provenance:    provenanceLegacy,
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

func overlayInjectedIdentity(ov SteeringOverlay) MessageIdentity {
	msg := steeringOverlayToMessage(ov)
	id, err := MessageIdentityOf(msg)
	if err != nil {
		// Overlay validation ensures valid role/text; panic on invariant breach.
		panic(fmt.Sprintf("conversationview: overlay %s identity: %v", ov.OverlayID, err))
	}
	return id
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

// FilterNeverBackend returns a deep clone of call with all never_backend messages removed
// (including dangling item_reference cleanup) but without injecting steering.
// It is used to derive the filtered baseline for placement-aware reassertion.
func FilterNeverBackend(call lipapi.Call, snap Snapshot) (lipapi.Call, error) {
	filteredSnap := Snapshot{
		StateRevision: snap.StateRevision,
		NeverBackend:  snap.NeverBackend,
		Steering:      nil,
	}
	out, _, err := Project(call, filteredSnap)
	return out, err
}
