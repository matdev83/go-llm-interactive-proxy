package b2bua

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
)

// Compile-time assertions keep the optional capability discoverable via
// conversationview.AsStore without widening the base b2bua.Store.
var (
	_ conversationview.Store         = (*memoryConversationViewStore)(nil)
	_ conversationview.Reader        = (*memoryConversationViewStore)(nil)
	_ conversationview.Tagger        = (*memoryConversationViewStore)(nil)
	_ conversationview.SteeringStore = (*memoryConversationViewStore)(nil)
)

// memoryConversationViewStore is the A-leg conversation-view capability
// owned by MemoryStore. All operations run under the existing A-leg lock
// (via MemoryStore.lockForOperation) so snapshot and mutations are
// linearizable per A-leg and follow A-leg deletion/eviction atomically.
// The wrapper exists because MemoryStore already implements
// routeoverride.Store.Snapshot with a different return type; a direct
// method set would collide on Snapshot. Callers discover the capability via
// AsConversationViewStore or conversationview.AsStore on the accessor.
type memoryConversationViewStore struct {
	s *MemoryStore
}

// ConversationViewStore returns the optional conversation-view capability for
// this MemoryStore. It is process/continuity-owned, not generation-owned.
func (s *MemoryStore) ConversationViewStore() conversationview.Store {
	return &memoryConversationViewStore{s: s}
}

// AsConversationViewStore reports whether v implements the optional
// conversation-view Store capability. It supports both the accessor
// (*memoryConversationViewStore) and any direct implementation.
func AsConversationViewStore(v any) (conversationview.Store, bool) {
	if s, ok := v.(conversationview.Store); ok {
		return s, true
	}
	if ms, ok := v.(*MemoryStore); ok {
		return ms.ConversationViewStore(), true
	}
	return nil, false
}

// Snapshot returns a deep-owned coherent snapshot for the A-leg.
// Old/no-state legs (A-leg exists but never mutated) read as an empty
// snapshot with StateRevision 0, satisfying backward compatibility for
// pre-feature legs.
func (m *memoryConversationViewStore) Snapshot(ctx context.Context, aLegID string) (conversationview.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return conversationview.Snapshot{}, err
	}
	retired := m.s.lockForOperation()
	defer func() { m.s.unlockForOperation(retired) }()
	now := m.s.nowTime()
	st, retiredID, err := m.legForViewLocked(aLegID, now)
	if retiredID != "" {
		retired = append(retired, retiredID)
	}
	if err != nil {
		return conversationview.Snapshot{}, err
	}
	snap := m.snapshotLocked(st)
	st.record.LastSeenAt = now
	return snap, nil
}

// TagNeverBackend atomically tags a batch of identities.
func (m *memoryConversationViewStore) TagNeverBackend(ctx context.Context, aLegID string, tags []conversationview.TagRequest) (conversationview.TagResult, error) {
	if err := ctx.Err(); err != nil {
		return conversationview.TagResult{}, err
	}
	// Validate batch first without holding lock (no mutation).
	seenInBatch := make(map[conversationview.MessageIdentity]struct{}, len(tags))
	newIdentities := make(map[conversationview.MessageIdentity]conversationview.TagRequest)
	for i, req := range tags {
		if err := req.Validate(); err != nil {
			return conversationview.TagResult{}, fmt.Errorf("%w: index %d: %v", conversationview.ErrInvalidTagRequest, i, err)
		}
		if _, dup := seenInBatch[req.Identity]; dup {
			continue
		}
		seenInBatch[req.Identity] = struct{}{}
		newIdentities[req.Identity] = req
	}

	retired := m.s.lockForOperation()
	defer func() { m.s.unlockForOperation(retired) }()
	now := m.s.nowTime()
	st, retiredID, err := m.legForViewLocked(aLegID, now)
	if retiredID != "" {
		retired = append(retired, retiredID)
	}
	if err != nil {
		return conversationview.TagResult{}, err
	}
	view := m.ensureViewLocked(st)
	trulyNew := 0
	for id := range newIdentities {
		if _, exists := view.NeverBackend[id]; !exists {
			trulyNew++
		}
	}
	if len(view.NeverBackend)+trulyNew > conversationview.MaxNeverBackendTags {
		return conversationview.TagResult{}, conversationview.ErrTagLimitExceeded
	}
	isNoOp := trulyNew == 0
	if !isNoOp {
		if view.Revision == 1<<64-1 {
			return conversationview.TagResult{}, conversationview.ErrRevisionExhausted
		}
		ts := now.UTC()
		for id, req := range newIdentities {
			if _, exists := view.NeverBackend[id]; exists {
				continue
			}
			view.NeverBackend[id] = conversationview.Tag{
				Identity:  req.Identity,
				Reason:    req.Reason,
				CreatedAt: ts,
			}
		}
		view.Revision++
	}
	st.record.LastSeenAt = now
	// Build result tags sorted.
	outTags := make([]conversationview.Tag, 0, len(view.NeverBackend))
	for _, t := range view.NeverBackend {
		outTags = append(outTags, t)
	}
	sort.Slice(outTags, func(i, j int) bool {
		return string(outTags[i].Identity) < string(outTags[j].Identity)
	})
	return conversationview.TagResult{
		StateRevision: view.Revision,
		Tags:          outTags,
	}, nil
}

// PutSteering creates or replaces a steering overlay.
func (m *memoryConversationViewStore) PutSteering(ctx context.Context, aLegID string, req conversationview.PutSteeringRequest) (conversationview.SteeringState, error) {
	if err := ctx.Err(); err != nil {
		return conversationview.SteeringState{}, err
	}
	if err := req.Validate(); err != nil {
		return conversationview.SteeringState{}, err
	}
	if err := validateALegIDForView(aLegID); err != nil {
		return conversationview.SteeringState{}, fmt.Errorf("%w: %v", conversationview.ErrALegNotFound, err)
	}
	retired := m.s.lockForOperation()
	defer func() { m.s.unlockForOperation(retired) }()
	now := m.s.nowTime()
	st, retiredID, err := m.legForViewLocked(aLegID, now)
	if retiredID != "" {
		retired = append(retired, retiredID)
	}
	if err != nil {
		return conversationview.SteeringState{}, err
	}
	view := m.ensureViewLocked(st)
	existing, exists := view.Steering[req.OverlayID]
	nowUTC := now.UTC()

	if exists {
		candidate := conversationview.SteeringOverlay{
			OverlayID:           req.OverlayID,
			Revision:            existing.Revision,
			SlotOrdinal:         existing.SlotOrdinal,
			Active:              true,
			Message:             req.Message,
			Placement:           req.Placement,
			AnchorMissingPolicy: req.AnchorMissingPolicy,
			Reason:              req.Reason,
			CreatedAt:           existing.CreatedAt,
			UpdatedAt:           nowUTC,
		}
		if overlaysEqualForMemory(existing, candidate) {
			st.record.LastSeenAt = now
			return conversationview.SteeringState{
				OverlayID:     existing.OverlayID,
				Revision:      existing.Revision,
				SlotOrdinal:   existing.SlotOrdinal,
				Active:        true,
				StateRevision: view.Revision,
			}, nil
		}
		placementChanged := existing.Placement.Kind != req.Placement.Kind
		if !placementChanged && req.Placement.Kind == conversationview.PlacementAfterMessage {
			if (existing.Placement.Anchor == nil) != (req.Placement.Anchor == nil) {
				placementChanged = true
			} else if existing.Placement.Anchor != nil && *existing.Placement.Anchor != *req.Placement.Anchor {
				placementChanged = true
			}
		}
		activeCount := 0
		totalBytes := 0
		for _, ov := range view.Steering {
			if ov.Active && ov.OverlayID != req.OverlayID {
				activeCount++
				totalBytes += len(ov.Message.Text)
			}
		}
		activeCount++
		totalBytes += len(req.Message.Text)
		if activeCount > conversationview.MaxActiveOverlays {
			return conversationview.SteeringState{}, conversationview.ErrSteeringLimitExceeded
		}
		if totalBytes > conversationview.MaxTotalSteeringBytes {
			return conversationview.SteeringState{}, conversationview.ErrSteeringLimitExceeded
		}
		if len(req.Message.Text) > conversationview.MaxSteeringTextBytes {
			return conversationview.SteeringState{}, conversationview.ErrSteeringLimitExceeded
		}
		newSlot := existing.SlotOrdinal
		if placementChanged {
			if view.NextSlot == 1<<64-1 {
				return conversationview.SteeringState{}, conversationview.ErrRevisionExhausted
			}
			newSlot = view.NextSlot
			view.NextSlot++
		}
		if existing.Revision == 1<<64-1 || view.Revision == 1<<64-1 {
			return conversationview.SteeringState{}, conversationview.ErrRevisionExhausted
		}
		updated := conversationview.SteeringOverlay{
			OverlayID:           req.OverlayID,
			Revision:            existing.Revision + 1,
			SlotOrdinal:         newSlot,
			Active:              true,
			Message:             req.Message,
			Placement:           req.Placement,
			AnchorMissingPolicy: req.AnchorMissingPolicy,
			Reason:              req.Reason,
			CreatedAt:           existing.CreatedAt,
			UpdatedAt:           nowUTC,
		}
		if updated.Placement.Anchor != nil {
			cp := *updated.Placement.Anchor
			updated.Placement.Anchor = &cp
		}
		view.Steering[req.OverlayID] = updated
		view.Revision++
		st.record.LastSeenAt = now
		return conversationview.SteeringState{
			OverlayID:     updated.OverlayID,
			Revision:      updated.Revision,
			SlotOrdinal:   updated.SlotOrdinal,
			Active:        true,
			StateRevision: view.Revision,
		}, nil
	}
	// New overlay creation.
	activeCount := 0
	totalBytes := 0
	for _, ov := range view.Steering {
		if ov.Active {
			activeCount++
			totalBytes += len(ov.Message.Text)
		}
	}
	activeCount++
	totalBytes += len(req.Message.Text)
	if activeCount > conversationview.MaxActiveOverlays {
		return conversationview.SteeringState{}, conversationview.ErrSteeringLimitExceeded
	}
	if totalBytes > conversationview.MaxTotalSteeringBytes {
		return conversationview.SteeringState{}, conversationview.ErrSteeringLimitExceeded
	}
	if len(req.Message.Text) > conversationview.MaxSteeringTextBytes {
		return conversationview.SteeringState{}, conversationview.ErrSteeringLimitExceeded
	}
	if view.NextSlot == 1<<64-1 || view.Revision == 1<<64-1 {
		return conversationview.SteeringState{}, conversationview.ErrRevisionExhausted
	}
	slot := view.NextSlot
	view.NextSlot++
	ov := conversationview.SteeringOverlay{
		OverlayID:           req.OverlayID,
		Revision:            1,
		SlotOrdinal:         slot,
		Active:              true,
		Message:             req.Message,
		Placement:           req.Placement,
		AnchorMissingPolicy: req.AnchorMissingPolicy,
		Reason:              req.Reason,
		CreatedAt:           nowUTC,
		UpdatedAt:           nowUTC,
	}
	if ov.Placement.Anchor != nil {
		cp := *ov.Placement.Anchor
		ov.Placement.Anchor = &cp
	}
	view.Steering[req.OverlayID] = ov
	view.Revision++
	st.record.LastSeenAt = now
	return conversationview.SteeringState{
		OverlayID:     ov.OverlayID,
		Revision:      ov.Revision,
		SlotOrdinal:   ov.SlotOrdinal,
		Active:        true,
		StateRevision: view.Revision,
	}, nil
}

// DeactivateSteering marks an overlay inactive.
func (m *memoryConversationViewStore) DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (conversationview.SteeringState, error) {
	if err := ctx.Err(); err != nil {
		return conversationview.SteeringState{}, err
	}
	// Validate overlayID via conversationview rules (use Put validation helper via temporary request).
	if strings.TrimSpace(overlayID) == "" {
		return conversationview.SteeringState{}, fmt.Errorf("%w: overlay id is required", conversationview.ErrInvalidSteeringRequest)
	}
	// Reuse conversationview validation by constructing minimal check.
	tmpReq := conversationview.PutSteeringRequest{
		OverlayID:           overlayID,
		Message:             conversationview.StoredMessageV1{Role: "user", Text: "x"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	}
	if err := tmpReq.Validate(); err != nil {
		// Surface as invalid steering request but map to ErrInvalidSteeringRequest.
		return conversationview.SteeringState{}, fmt.Errorf("%w: %v", conversationview.ErrInvalidSteeringRequest, err)
	}
	if err := validateALegIDForView(aLegID); err != nil {
		return conversationview.SteeringState{}, fmt.Errorf("%w: %v", conversationview.ErrALegNotFound, err)
	}
	retired := m.s.lockForOperation()
	defer func() { m.s.unlockForOperation(retired) }()
	now := m.s.nowTime()
	st, retiredID, err := m.legForViewLocked(aLegID, now)
	if retiredID != "" {
		retired = append(retired, retiredID)
	}
	if err != nil {
		return conversationview.SteeringState{}, err
	}
	view := m.ensureViewLocked(st)
	existing, exists := view.Steering[overlayID]
	if !exists {
		return conversationview.SteeringState{}, conversationview.ErrOverlayNotFound
	}
	if !existing.Active {
		st.record.LastSeenAt = now
		return conversationview.SteeringState{
			OverlayID:     existing.OverlayID,
			Revision:      existing.Revision,
			SlotOrdinal:   existing.SlotOrdinal,
			Active:        false,
			StateRevision: view.Revision,
		}, nil
	}
	if existing.Revision == 1<<64-1 || view.Revision == 1<<64-1 {
		return conversationview.SteeringState{}, conversationview.ErrRevisionExhausted
	}
	nowUTC2 := now.UTC()
	updated := conversationview.SteeringOverlay{
		OverlayID:           existing.OverlayID,
		Revision:            existing.Revision + 1,
		SlotOrdinal:         existing.SlotOrdinal,
		Active:              false,
		Message:             existing.Message,
		Placement:           existing.Placement,
		AnchorMissingPolicy: existing.AnchorMissingPolicy,
		Reason:              existing.Reason,
		CreatedAt:           existing.CreatedAt,
		UpdatedAt:           nowUTC2,
	}
	if updated.Placement.Anchor != nil {
		cp := *updated.Placement.Anchor
		updated.Placement.Anchor = &cp
	}
	view.Steering[overlayID] = updated
	view.Revision++
	st.record.LastSeenAt = now
	return conversationview.SteeringState{
		OverlayID:     updated.OverlayID,
		Revision:      updated.Revision,
		SlotOrdinal:   updated.SlotOrdinal,
		Active:        false,
		StateRevision: view.Revision,
	}, nil
}

// GetOverlay is a test/debugging seam to inspect an overlay regardless of active state.
// It is intentionally on the capability wrapper, not the base Store, to keep the narrow
// port minimal. Production code should use Snapshot.
func (m *memoryConversationViewStore) GetOverlay(ctx context.Context, aLegID string, overlayID string) (conversationview.SteeringOverlay, error) {
	if err := ctx.Err(); err != nil {
		return conversationview.SteeringOverlay{}, err
	}
	retired := m.s.lockForOperation()
	defer func() { m.s.unlockForOperation(retired) }()
	now := m.s.nowTime()
	st, retiredID, err := m.legForViewLocked(aLegID, now)
	if retiredID != "" {
		retired = append(retired, retiredID)
	}
	if err != nil {
		return conversationview.SteeringOverlay{}, err
	}
	view := st.conversationView
	if view == nil {
		return conversationview.SteeringOverlay{}, conversationview.ErrOverlayNotFound
	}
	ov, ok := view.Steering[overlayID]
	if !ok {
		return conversationview.SteeringOverlay{}, conversationview.ErrOverlayNotFound
	}
	// Return deep copy.
	cp := ov
	if ov.Placement.Anchor != nil {
		a := *ov.Placement.Anchor
		cp.Placement.Anchor = &a
	}
	st.record.LastSeenAt = now
	return cp, nil
}

func (m *memoryConversationViewStore) legForViewLocked(aLegID string, now time.Time) (*legState, string, error) {
	if err := validateALegIDForView(aLegID); err != nil {
		return nil, "", conversationview.ErrALegNotFound
	}
	st, ok := m.s.legs[aLegID]
	if !ok {
		return nil, "", conversationview.ErrALegNotFound
	}
	if m.s.evictIfStaleLocked(st, now) {
		return nil, st.record.ALegID, conversationview.ErrALegNotFound
	}
	return st, "", nil
}

func (m *memoryConversationViewStore) ensureViewLocked(st *legState) *legConversationView {
	if st.conversationView == nil {
		st.conversationView = &legConversationView{
			NeverBackend: make(map[conversationview.MessageIdentity]conversationview.Tag),
			Steering:     make(map[string]conversationview.SteeringOverlay),
			NextSlot:     1,
		}
	}
	return st.conversationView
}

func (m *memoryConversationViewStore) snapshotLocked(st *legState) conversationview.Snapshot {
	view := st.conversationView
	if view == nil {
		return conversationview.Snapshot{
			StateRevision: 0,
			NeverBackend:  nil,
			Steering:      nil,
		}
	}
	tags := make([]conversationview.Tag, 0, len(view.NeverBackend))
	for _, t := range view.NeverBackend {
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool {
		return string(tags[i].Identity) < string(tags[j].Identity)
	})
	var steering []conversationview.SteeringOverlay
	for _, ov := range view.Steering {
		if ov.Active {
			cp := ov
			if ov.Placement.Anchor != nil {
				a := *ov.Placement.Anchor
				cp.Placement.Anchor = &a
			}
			steering = append(steering, cp)
		}
	}
	sort.Slice(steering, func(i, j int) bool {
		return steering[i].SlotOrdinal < steering[j].SlotOrdinal
	})
	tagsCopy := make([]conversationview.Tag, len(tags))
	copy(tagsCopy, tags)
	steeringCopy := make([]conversationview.SteeringOverlay, len(steering))
	for i := range steering {
		steeringCopy[i] = steering[i]
		if steering[i].Placement.Anchor != nil {
			a := *steering[i].Placement.Anchor
			steeringCopy[i].Placement.Anchor = &a
		}
	}
	return conversationview.Snapshot{
		StateRevision: view.Revision,
		NeverBackend:  tagsCopy,
		Steering:      steeringCopy,
	}
}

func validateALegIDForView(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("a-leg id is required")
	}
	if len(strings.TrimSpace(id)) > conversationview.MaxALegIDBytes {
		return fmt.Errorf("a-leg id exceeds %d bytes", conversationview.MaxALegIDBytes)
	}
	return nil
}

func overlaysEqualForMemory(a, b conversationview.SteeringOverlay) bool {
	if a.OverlayID != b.OverlayID || a.Active != b.Active || a.SlotOrdinal != b.SlotOrdinal {
		return false
	}
	if a.Message.Role != b.Message.Role || a.Message.Text != b.Message.Text {
		return false
	}
	if a.Placement.Kind != b.Placement.Kind {
		return false
	}
	if a.Placement.Kind == conversationview.PlacementAfterMessage {
		if (a.Placement.Anchor == nil) != (b.Placement.Anchor == nil) {
			return false
		}
		if a.Placement.Anchor != nil && *a.Placement.Anchor != *b.Placement.Anchor {
			return false
		}
	}
	if a.AnchorMissingPolicy != b.AnchorMissingPolicy {
		return false
	}
	if a.Reason != b.Reason {
		return false
	}
	return true
}
