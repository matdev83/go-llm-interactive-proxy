package conversationview

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Bounds from requirements 3.4 and 9.17.
const (
	MaxNeverBackendTags   = 4096
	MaxActiveOverlays     = 64
	MaxSteeringTextBytes  = 64 * 1024
	MaxTotalSteeringBytes = 256 * 1024
	MaxReasonCodeBytes    = 64
	MaxOverlayIDBytes     = 128
	MaxALegIDBytes        = 256
)

// ReasonCode is a bounded non-secret identifier for diagnostics.
type ReasonCode string

func (r ReasonCode) Validate() error {
	s := string(r)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%w: reason code is required", ErrInvalidReasonCode)
	}
	if len(s) > MaxReasonCodeBytes {
		return fmt.Errorf("%w: reason code exceeds %d bytes", ErrInvalidReasonCode, MaxReasonCodeBytes)
	}
	for _, ch := range s {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("%w: reason code must be ascii", ErrInvalidReasonCode)
		}
		if !(ch == '_' || ch == '-' || ch == '.' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
			return fmt.Errorf("%w: invalid character %q in reason code", ErrInvalidReasonCode, ch)
		}
	}
	return nil
}

// OverlayID alias for documentation; validated as bounded identifier.
type OverlayID = string

func validateOverlayID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: overlay id is required", ErrInvalidOverlayID)
	}
	if len(id) > MaxOverlayIDBytes {
		return fmt.Errorf("%w: overlay id exceeds %d bytes", ErrInvalidOverlayID, MaxOverlayIDBytes)
	}
	for _, ch := range id {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("%w: overlay id must be ascii", ErrInvalidOverlayID)
		}
		if !(ch == '_' || ch == '-' || ch == '.' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
			return fmt.Errorf("%w: invalid character %q in overlay id", ErrInvalidOverlayID, ch)
		}
	}
	return nil
}

func validateALegID(id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("%w: a-leg id is required", ErrInvalidALegID)
	}
	if len(trimmed) > MaxALegIDBytes {
		return fmt.Errorf("%w: a-leg id exceeds %d bytes", ErrInvalidALegID, MaxALegIDBytes)
	}
	return nil
}

// PlacementKind enumerates durable steering placement.
type PlacementKind string

const (
	PlacementStablePrefix PlacementKind = "stable_prefix"
	PlacementAfterMessage PlacementKind = "after_message"
)

func (k PlacementKind) Validate() error {
	switch k {
	case PlacementStablePrefix, PlacementAfterMessage:
		return nil
	default:
		return fmt.Errorf("%w: unknown placement %q", ErrInvalidPlacement, k)
	}
}

// AnchorMissingPolicy controls behavior when a fixed anchor disappears.
type AnchorMissingPolicy string

const (
	AnchorStablePrefixFallback AnchorMissingPolicy = "stable_prefix_fallback"
	AnchorFailClosed           AnchorMissingPolicy = "fail_closed"
)

func (p AnchorMissingPolicy) Validate() error {
	switch p {
	case AnchorStablePrefixFallback, AnchorFailClosed:
		return nil
	default:
		return fmt.Errorf("%w: unknown policy %q", ErrInvalidAnchorMissingPolicy, p)
	}
}

// StoredPlacement is the persisted placement for a steering overlay.
type StoredPlacement struct {
	Kind   PlacementKind  `json:"kind"`
	Anchor *MessageAnchor `json:"anchor,omitempty"`
}

func (sp StoredPlacement) Validate() error {
	if err := sp.Kind.Validate(); err != nil {
		return err
	}
	if sp.Kind == PlacementAfterMessage {
		if sp.Anchor == nil {
			return fmt.Errorf("%w: after_message placement requires anchor", ErrInvalidPlacement)
		}
		if err := sp.Anchor.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPlacement, err)
		}
	} else {
		if sp.Anchor != nil {
			return fmt.Errorf("%w: stable_prefix must not have anchor", ErrInvalidPlacement)
		}
	}
	return nil
}

// StoredMessageV1 is the persisted model-visible steering payload.
type StoredMessageV1 struct {
	Role lipapi.Role `json:"role"`
	Text string      `json:"text"`
}

func (m StoredMessageV1) Validate() error {
	role := lipapi.Role(strings.TrimSpace(string(m.Role)))
	if role == "" {
		return fmt.Errorf("%w: steering role is required", ErrInvalidSteeringMessage)
	}
	switch role {
	case lipapi.RoleSystem, lipapi.RoleDeveloper, lipapi.RoleUser, lipapi.RoleAssistant, lipapi.RoleTool:
	default:
		return fmt.Errorf("%w: invalid steering role %q", ErrInvalidSteeringMessage, role)
	}
	if len(m.Text) > MaxSteeringTextBytes {
		return fmt.Errorf("%w: steering text exceeds %d bytes", ErrInvalidSteeringMessage, MaxSteeringTextBytes)
	}
	if strings.TrimSpace(m.Text) == "" {
		return fmt.Errorf("%w: steering text is required", ErrInvalidSteeringMessage)
	}
	return nil
}

func (m StoredMessageV1) equal(other StoredMessageV1) bool {
	return m.Role == other.Role && m.Text == other.Text
}

// Tag is an A-leg never_backend exclusion record.
type Tag struct {
	Identity  MessageIdentity `json:"identity"`
	Reason    ReasonCode      `json:"reason"`
	CreatedAt time.Time       `json:"created_at"`
}

func (t Tag) Validate() error {
	if err := t.Identity.Validate(); err != nil {
		return err
	}
	if err := t.Reason.Validate(); err != nil {
		return err
	}
	return nil
}

// TagRequest is one element of a TagNeverBackend batch.
type TagRequest struct {
	Identity MessageIdentity `json:"identity"`
	Reason   ReasonCode      `json:"reason"`
}

func (r TagRequest) Validate() error {
	if err := r.Identity.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTagRequest, err)
	}
	if err := r.Reason.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTagRequest, err)
	}
	return nil
}

// TagResult is returned from a successful TagNeverBackend call.
type TagResult struct {
	StateRevision uint64 `json:"state_revision"`
	Tags          []Tag  `json:"tags"`
}

// SteeringOverlay is a persisted steering record.
type SteeringOverlay struct {
	OverlayID           string              `json:"overlay_id"`
	Revision            uint64              `json:"revision"`
	SlotOrdinal         uint64              `json:"slot_ordinal"`
	Active              bool                `json:"active"`
	Message             StoredMessageV1     `json:"message"`
	Placement           StoredPlacement     `json:"placement"`
	AnchorMissingPolicy AnchorMissingPolicy `json:"anchor_missing_policy"`
	Reason              ReasonCode          `json:"reason"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

func (o SteeringOverlay) Validate() error {
	if err := validateOverlayID(o.OverlayID); err != nil {
		return err
	}
	if err := o.Message.Validate(); err != nil {
		return err
	}
	if err := o.Placement.Validate(); err != nil {
		return err
	}
	if err := o.AnchorMissingPolicy.Validate(); err != nil {
		return err
	}
	if err := o.Reason.Validate(); err != nil {
		return err
	}
	return nil
}

func (o SteeringOverlay) clone() SteeringOverlay {
	out := o
	if o.Placement.Anchor != nil {
		cp := *o.Placement.Anchor
		out.Placement.Anchor = &cp
	}
	return out
}

func overlaysEqual(a, b SteeringOverlay) bool {
	if a.OverlayID != b.OverlayID || a.Active != b.Active || a.SlotOrdinal != b.SlotOrdinal {
		return false
	}
	if !a.Message.equal(b.Message) {
		return false
	}
	if a.Placement.Kind != b.Placement.Kind {
		return false
	}
	if a.Placement.Kind == PlacementAfterMessage {
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

// PutSteeringRequest is the writer-facing steering mutation.
type PutSteeringRequest struct {
	OverlayID           string              `json:"overlay_id"`
	Message             StoredMessageV1     `json:"message"`
	Placement           StoredPlacement     `json:"placement"`
	AnchorMissingPolicy AnchorMissingPolicy `json:"anchor_missing_policy"`
	Reason              ReasonCode          `json:"reason"`
}

func (r PutSteeringRequest) Validate() error {
	if err := validateOverlayID(r.OverlayID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	if err := r.Message.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	if err := r.Placement.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	if err := r.AnchorMissingPolicy.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	if err := r.Reason.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	return nil
}

// SteeringState is the post-mutation steering summary.
type SteeringState struct {
	OverlayID     string `json:"overlay_id"`
	Revision      uint64 `json:"revision"`
	SlotOrdinal   uint64 `json:"slot_ordinal"`
	Active        bool   `json:"active"`
	StateRevision uint64 `json:"state_revision"`
}

// Snapshot is the coherent per-turn conversation view.
type Snapshot struct {
	StateRevision uint64            `json:"state_revision"`
	NeverBackend  []Tag             `json:"never_backend"`
	Steering      []SteeringOverlay `json:"steering"`
}

// Narrow ports.

type Reader interface {
	Snapshot(ctx context.Context, aLegID string) (Snapshot, error)
}

type Tagger interface {
	TagNeverBackend(ctx context.Context, aLegID string, tags []TagRequest) (TagResult, error)
}

type SteeringStore interface {
	PutSteering(ctx context.Context, aLegID string, req PutSteeringRequest) (SteeringState, error)
	DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (SteeringState, error)
}

// Store is the combined port for implementations.
type Store interface {
	Reader
	Tagger
	SteeringStore
}

// ReferenceStore is an in-memory Store used to pin contract semantics.
type ReferenceStore struct {
	mu   sync.RWMutex
	legs map[string]*legView
	now  func() time.Time
}

type legView struct {
	revision  uint64
	tags      map[MessageIdentity]Tag
	steering  map[string]*SteeringOverlay
	nextSlot  uint64
	createdAt time.Time
}

// NewReferenceStore creates an empty store.
func NewReferenceStore() *ReferenceStore {
	return &ReferenceStore{
		legs: make(map[string]*legView),
		now:  time.Now,
	}
}

// NewReferenceStoreWithClock creates a store with a deterministic clock (test use).
func NewReferenceStoreWithClock(now func() time.Time) *ReferenceStore {
	if now == nil {
		now = time.Now
	}
	return &ReferenceStore{
		legs: make(map[string]*legView),
		now:  now,
	}
}

// CreateALeg registers an A-leg for conversation-view state.
func (s *ReferenceStore) CreateALeg(ctx context.Context, aLegID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateALegID(aLegID); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(aLegID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.legs[trimmed]; exists {
		return nil
	}
	s.legs[trimmed] = &legView{
		tags:     make(map[MessageIdentity]Tag),
		steering: make(map[string]*SteeringOverlay),
		nextSlot: 1,
	}
	return nil
}

// DeleteALeg removes all conversation-view state for an A-leg.
func (s *ReferenceStore) DeleteALeg(ctx context.Context, aLegID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateALegID(aLegID); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(aLegID)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.legs, trimmed)
	return nil
}

func (s *ReferenceStore) getLeg(aLegID string) (*legView, error) {
	trimmed := strings.TrimSpace(aLegID)
	if err := validateALegID(trimmed); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrALegNotFound, err)
	}
	lv, ok := s.legs[trimmed]
	if !ok {
		return nil, ErrALegNotFound
	}
	return lv, nil
}

// Snapshot returns a deep-owned coherent snapshot.
func (s *ReferenceStore) Snapshot(ctx context.Context, aLegID string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	lv, err := s.getLeg(aLegID)
	if err != nil {
		return Snapshot{}, err
	}
	// Collect tags sorted for determinism.
	tags := make([]Tag, 0, len(lv.tags))
	for _, t := range lv.tags {
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool {
		return string(tags[i].Identity) < string(tags[j].Identity)
	})
	// Collect active overlays sorted by SlotOrdinal.
	var steering []SteeringOverlay
	for _, ov := range lv.steering {
		if ov.Active {
			steering = append(steering, ov.clone())
		}
	}
	sort.Slice(steering, func(i, j int) bool {
		return steering[i].SlotOrdinal < steering[j].SlotOrdinal
	})
	// Deep copy tags (value copy suffices, but ensure slice ownership).
	tagsCopy := make([]Tag, len(tags))
	copy(tagsCopy, tags)
	steeringCopy := make([]SteeringOverlay, len(steering))
	for i := range steering {
		steeringCopy[i] = steering[i].clone()
	}
	return Snapshot{
		StateRevision: lv.revision,
		NeverBackend:  tagsCopy,
		Steering:      steeringCopy,
	}, nil
}

// TagNeverBackend atomically tags a batch of identities.
func (s *ReferenceStore) TagNeverBackend(ctx context.Context, aLegID string, tags []TagRequest) (TagResult, error) {
	if err := ctx.Err(); err != nil {
		return TagResult{}, err
	}
	if err := validateALegID(aLegID); err != nil {
		return TagResult{}, fmt.Errorf("%w: %v", ErrALegNotFound, err)
	}
	// Validate batch first (no mutation).
	seenInBatch := make(map[MessageIdentity]struct{}, len(tags))
	newIdentities := make(map[MessageIdentity]TagRequest)
	for i, req := range tags {
		if err := req.Validate(); err != nil {
			return TagResult{}, fmt.Errorf("%w: index %d: %v", ErrInvalidTagRequest, i, err)
		}
		if _, dup := seenInBatch[req.Identity]; dup {
			// Duplicate within batch - treat as idempotent de-dupe, not error; keep first.
			continue
		}
		seenInBatch[req.Identity] = struct{}{}
		// Will check existing later under lock, but track for cap calculation.
		newIdentities[req.Identity] = req
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lv, err := s.getLeg(aLegID)
	if err != nil {
		return TagResult{}, err
	}
	// Determine truly new identities not already stored.
	trulyNew := 0
	for id := range newIdentities {
		if _, exists := lv.tags[id]; !exists {
			trulyNew++
		}
	}
	if len(lv.tags)+trulyNew > MaxNeverBackendTags {
		return TagResult{}, ErrTagLimitExceeded
	}
	// Check if batch is semantic no-op (all identities already present).
	isNoOp := trulyNew == 0
	// Even if no-op, we still validate that batch didn't contain invalid duplicates? Already done.
	if !isNoOp {
		now := s.now().UTC()
		for id, req := range newIdentities {
			if _, exists := lv.tags[id]; exists {
				continue
			}
			lv.tags[id] = Tag{
				Identity:  req.Identity,
				Reason:    req.Reason,
				CreatedAt: now,
			}
		}
		if lv.revision == 1<<64-1 {
			return TagResult{}, ErrRevisionExhausted
		}
		lv.revision++
	}
	// Build result tags sorted.
	outTags := make([]Tag, 0, len(lv.tags))
	for _, t := range lv.tags {
		outTags = append(outTags, t)
	}
	sort.Slice(outTags, func(i, j int) bool {
		return string(outTags[i].Identity) < string(outTags[j].Identity)
	})
	return TagResult{
		StateRevision: lv.revision,
		Tags:          outTags,
	}, nil
}

// PutSteering creates or replaces a steering overlay.
func (s *ReferenceStore) PutSteering(ctx context.Context, aLegID string, req PutSteeringRequest) (SteeringState, error) {
	if err := ctx.Err(); err != nil {
		return SteeringState{}, err
	}
	if err := req.Validate(); err != nil {
		return SteeringState{}, err
	}
	if err := validateALegID(aLegID); err != nil {
		return SteeringState{}, fmt.Errorf("%w: %v", ErrALegNotFound, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lv, err := s.getLeg(aLegID)
	if err != nil {
		return SteeringState{}, err
	}
	// Check caps before mutation.
	// Count active overlays after operation and total bytes.
	existing, exists := lv.steering[req.OverlayID]
	now := s.now().UTC()

	if exists {
		// Replacement: check semantic no-op.
		candidate := SteeringOverlay{
			OverlayID:           req.OverlayID,
			Revision:            existing.Revision,
			SlotOrdinal:         existing.SlotOrdinal,
			Active:              true,
			Message:             req.Message,
			Placement:           req.Placement,
			AnchorMissingPolicy: req.AnchorMissingPolicy,
			Reason:              req.Reason,
			CreatedAt:           existing.CreatedAt,
			UpdatedAt:           now,
		}
		// Placement change requires new slot; adjust candidate slot for comparison?
		// For no-op detection, placement must be identical including anchor.
		// If placement differs, it's not a no-op.
		if overlaysEqual(*existing, candidate) {
			// No-op: do not bump revision or StateRevision.
			return SteeringState{
				OverlayID:     existing.OverlayID,
				Revision:      existing.Revision,
				SlotOrdinal:   existing.SlotOrdinal,
				Active:        true,
				StateRevision: lv.revision,
			}, nil
		}
		// Determine if placement change.
		placementChanged := existing.Placement.Kind != req.Placement.Kind
		if !placementChanged && req.Placement.Kind == PlacementAfterMessage {
			if (existing.Placement.Anchor == nil) != (req.Placement.Anchor == nil) {
				placementChanged = true
			} else if existing.Placement.Anchor != nil && *existing.Placement.Anchor != *req.Placement.Anchor {
				placementChanged = true
			}
		}
		// Compute active count/bytes after replacement.
		activeCount := 0
		totalBytes := 0
		for _, ov := range lv.steering {
			if ov.Active && ov.OverlayID != req.OverlayID {
				activeCount++
				totalBytes += len(ov.Message.Text)
			}
		}
		// The replacement will be active.
		activeCount++
		totalBytes += len(req.Message.Text)
		if activeCount > MaxActiveOverlays {
			return SteeringState{}, ErrSteeringLimitExceeded
		}
		if totalBytes > MaxTotalSteeringBytes {
			return SteeringState{}, ErrSteeringLimitExceeded
		}
		if len(req.Message.Text) > MaxSteeringTextBytes {
			return SteeringState{}, ErrSteeringLimitExceeded
		}
		// Apply.
		newSlot := existing.SlotOrdinal
		if placementChanged {
			if lv.nextSlot == 1<<64-1 {
				return SteeringState{}, ErrRevisionExhausted
			}
			newSlot = lv.nextSlot
			lv.nextSlot++
		}
		if existing.Revision == 1<<64-1 || lv.revision == 1<<64-1 {
			return SteeringState{}, ErrRevisionExhausted
		}
		updated := &SteeringOverlay{
			OverlayID:           req.OverlayID,
			Revision:            existing.Revision + 1,
			SlotOrdinal:         newSlot,
			Active:              true,
			Message:             req.Message,
			Placement:           req.Placement,
			AnchorMissingPolicy: req.AnchorMissingPolicy,
			Reason:              req.Reason,
			CreatedAt:           existing.CreatedAt,
			UpdatedAt:           now,
		}
		if updated.Placement.Anchor != nil {
			cp := *updated.Placement.Anchor
			updated.Placement.Anchor = &cp
		}
		lv.steering[req.OverlayID] = updated
		lv.revision++
		return SteeringState{
			OverlayID:     updated.OverlayID,
			Revision:      updated.Revision,
			SlotOrdinal:   updated.SlotOrdinal,
			Active:        true,
			StateRevision: lv.revision,
		}, nil
	}
	// New overlay creation.
	activeCount := 0
	totalBytes := 0
	for _, ov := range lv.steering {
		if ov.Active {
			activeCount++
			totalBytes += len(ov.Message.Text)
		}
	}
	activeCount++
	totalBytes += len(req.Message.Text)
	if activeCount > MaxActiveOverlays {
		return SteeringState{}, ErrSteeringLimitExceeded
	}
	if totalBytes > MaxTotalSteeringBytes {
		return SteeringState{}, ErrSteeringLimitExceeded
	}
	if len(req.Message.Text) > MaxSteeringTextBytes {
		return SteeringState{}, ErrSteeringLimitExceeded
	}
	if lv.nextSlot == 1<<64-1 || lv.revision == 1<<64-1 {
		return SteeringState{}, ErrRevisionExhausted
	}
	slot := lv.nextSlot
	lv.nextSlot++
	ov := &SteeringOverlay{
		OverlayID:           req.OverlayID,
		Revision:            1,
		SlotOrdinal:         slot,
		Active:              true,
		Message:             req.Message,
		Placement:           req.Placement,
		AnchorMissingPolicy: req.AnchorMissingPolicy,
		Reason:              req.Reason,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if ov.Placement.Anchor != nil {
		cp := *ov.Placement.Anchor
		ov.Placement.Anchor = &cp
	}
	lv.steering[req.OverlayID] = ov
	lv.revision++
	return SteeringState{
		OverlayID:     ov.OverlayID,
		Revision:      ov.Revision,
		SlotOrdinal:   ov.SlotOrdinal,
		Active:        true,
		StateRevision: lv.revision,
	}, nil
}

// DeactivateSteering marks an overlay inactive.
func (s *ReferenceStore) DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (SteeringState, error) {
	if err := ctx.Err(); err != nil {
		return SteeringState{}, err
	}
	if err := validateOverlayID(overlayID); err != nil {
		return SteeringState{}, fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	if err := validateALegID(aLegID); err != nil {
		return SteeringState{}, fmt.Errorf("%w: %v", ErrALegNotFound, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lv, err := s.getLeg(aLegID)
	if err != nil {
		return SteeringState{}, err
	}
	existing, exists := lv.steering[overlayID]
	if !exists {
		return SteeringState{}, ErrOverlayNotFound
	}
	if !existing.Active {
		// No-op deactivation: stable revision.
		return SteeringState{
			OverlayID:     existing.OverlayID,
			Revision:      existing.Revision,
			SlotOrdinal:   existing.SlotOrdinal,
			Active:        false,
			StateRevision: lv.revision,
		}, nil
	}
	if existing.Revision == 1<<64-1 || lv.revision == 1<<64-1 {
		return SteeringState{}, ErrRevisionExhausted
	}
	now := s.now().UTC()
	updated := &SteeringOverlay{
		OverlayID:           existing.OverlayID,
		Revision:            existing.Revision + 1,
		SlotOrdinal:         existing.SlotOrdinal,
		Active:              false,
		Message:             existing.Message,
		Placement:           existing.Placement,
		AnchorMissingPolicy: existing.AnchorMissingPolicy,
		Reason:              existing.Reason,
		CreatedAt:           existing.CreatedAt,
		UpdatedAt:           now,
	}
	if updated.Placement.Anchor != nil {
		cp := *updated.Placement.Anchor
		updated.Placement.Anchor = &cp
	}
	lv.steering[overlayID] = updated
	lv.revision++
	return SteeringState{
		OverlayID:     updated.OverlayID,
		Revision:      updated.Revision,
		SlotOrdinal:   updated.SlotOrdinal,
		Active:        false,
		StateRevision: lv.revision,
	}, nil
}

// GetOverlay is a test/debugging helper to inspect an overlay regardless of active state.
func (s *ReferenceStore) GetOverlay(ctx context.Context, aLegID string, overlayID string) (SteeringOverlay, error) {
	if err := ctx.Err(); err != nil {
		return SteeringOverlay{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	lv, err := s.getLeg(aLegID)
	if err != nil {
		return SteeringOverlay{}, err
	}
	ov, ok := lv.steering[overlayID]
	if !ok {
		return SteeringOverlay{}, ErrOverlayNotFound
	}
	return ov.clone(), nil
}

var (
	_ Store         = (*ReferenceStore)(nil)
	_ Reader        = (*ReferenceStore)(nil)
	_ Tagger        = (*ReferenceStore)(nil)
	_ SteeringStore = (*ReferenceStore)(nil)
)
