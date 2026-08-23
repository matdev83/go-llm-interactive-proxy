package bunstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Compile-time assertions keep the optional capability discoverable via
// conversationview.AsStore without widening the base b2bua.Store.
var (
	_ conversationview.Store         = (*conversationViewStore)(nil)
	_ conversationview.Reader        = (*conversationViewStore)(nil)
	_ conversationview.Tagger        = (*conversationViewStore)(nil)
	_ conversationview.SteeringStore = (*conversationViewStore)(nil)
)

// conversationViewStore is the A-leg conversation-view capability owned by Store.
// All operations run under the existing A-leg row lock so snapshot and mutations
// are linearizable per A-leg and follow A-leg deletion atomically. The wrapper
// exists because Store already implements routeoverride.Store.Snapshot with a
// different return type.
type conversationViewStore struct {
	s *Store
}

// ConversationViewStore returns the optional conversation-view capability for
// this Store. It is process/continuity-owned, not generation-owned.
func (s *Store) ConversationViewStore() conversationview.Store {
	return &conversationViewStore{s: s}
}

// Snapshot returns a deep-owned coherent snapshot for the A-leg. Legacy legs
// with no state row read as empty revision 0.
func (m *conversationViewStore) Snapshot(ctx context.Context, aLegID string) (conversationview.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return conversationview.Snapshot{}, err
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return conversationview.Snapshot{}, conversationview.ErrALegNotFound
	}
	var out conversationview.Snapshot
	err := m.s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := m.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		snap, err := m.loadSnapshotTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		out = snap
		return nil
	})
	if err != nil {
		return conversationview.Snapshot{}, err
	}
	return out, nil
}

// TagNeverBackend atomically tags a batch of identities.
func (m *conversationViewStore) TagNeverBackend(ctx context.Context, aLegID string, tags []conversationview.TagRequest) (conversationview.TagResult, error) {
	if err := ctx.Err(); err != nil {
		return conversationview.TagResult{}, err
	}
	// Validate and de-duplicate batch before taking lock.
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
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return conversationview.TagResult{}, conversationview.ErrALegNotFound
	}
	var result conversationview.TagResult
	err := m.s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := m.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		rev, nextSlot, err := m.loadStateTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		existingSet, err := m.loadTagSetTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		trulyNew := 0
		for id := range newIdentities {
			if _, exists := existingSet[id]; !exists {
				trulyNew++
			}
		}
		if len(existingSet)+trulyNew > conversationview.MaxNeverBackendTags {
			return conversationview.ErrTagLimitExceeded
		}
		isNoOp := trulyNew == 0
		if !isNoOp {
			if rev == math.MaxInt64 {
				return conversationview.ErrRevisionExhausted
			}
			rev++
			now := time.Now().UTC().UnixNano()
			for id, req := range newIdentities {
				if _, exists := existingSet[id]; exists {
					continue
				}
				version, digest, err := splitIdentity(id)
				if err != nil {
					return err
				}
				if _, err := tx.NewRaw(`
					INSERT INTO a_leg_never_backend_messages(a_leg_id, identity_version, identity_digest, reason, created_at_unix)
					VALUES(?,?,?,?,?)
				`, aLegID, version, digest, string(req.Reason), now).Exec(ctx); err != nil {
					return opErr("insert never backend tag", err)
				}
			}
			if err := m.upsertStateTx(ctx, tx, aLegID, rev, nextSlot); err != nil {
				return err
			}
		}
		if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		// Build result tags sorted by identity.
		tagsSlice, err := m.loadTagsTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		result = conversationview.TagResult{
			StateRevision: uint64(rev),
			Tags:          tagsSlice,
		}
		return nil
	})
	if err != nil {
		return conversationview.TagResult{}, err
	}
	return result, nil
}

// PutSteering creates or replaces a steering overlay.
func (m *conversationViewStore) PutSteering(ctx context.Context, aLegID string, req conversationview.PutSteeringRequest) (conversationview.SteeringState, error) {
	if err := ctx.Err(); err != nil {
		return conversationview.SteeringState{}, err
	}
	if err := req.Validate(); err != nil {
		return conversationview.SteeringState{}, err
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return conversationview.SteeringState{}, conversationview.ErrALegNotFound
	}
	var out conversationview.SteeringState
	err := m.s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := m.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		rev, nextSlot, err := m.loadStateTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		existing, exists, err := m.loadOverlayTx(ctx, tx, aLegID, req.OverlayID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		nowUnix := now.UnixNano()

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
				UpdatedAt:           now,
			}
			if overlaysEqualForBun(existing, candidate) {
				if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
					return err
				}
				out = conversationview.SteeringState{
					OverlayID:                   existing.OverlayID,
					Revision:                    existing.Revision,
					SlotOrdinal:                 existing.SlotOrdinal,
					Active:                      true,
					StateRevision:               uint64(rev),
					CacheDiscontinuityKind:      conversationview.CacheDiscontinuityNone,
					CacheDiscontinuityPlacement: "",
				}
				return nil
			}
			placementChanged := existing.Placement.Kind != req.Placement.Kind
			if !placementChanged && req.Placement.Kind == conversationview.PlacementAfterMessage {
				if (existing.Placement.Anchor == nil) != (req.Placement.Anchor == nil) {
					placementChanged = true
				} else if existing.Placement.Anchor != nil && *existing.Placement.Anchor != *req.Placement.Anchor {
					placementChanged = true
				}
			}
			// Compute caps excluding current overlay if active.
			activeCount, totalBytes, err := m.loadActiveStatsTx(ctx, tx, aLegID)
			if err != nil {
				return err
			}
			if existing.Active {
				activeCount--
				totalBytes -= len(existing.Message.Text)
			}
			activeCount++
			totalBytes += len(req.Message.Text)
			if activeCount > conversationview.MaxActiveOverlays {
				return conversationview.ErrSteeringLimitExceeded
			}
			if totalBytes > conversationview.MaxTotalSteeringBytes {
				return conversationview.ErrSteeringLimitExceeded
			}
			if len(req.Message.Text) > conversationview.MaxSteeringTextBytes {
				return conversationview.ErrSteeringLimitExceeded
			}
			newSlot := existing.SlotOrdinal
			if placementChanged {
				if nextSlot == math.MaxInt64 {
					return conversationview.ErrRevisionExhausted
				}
				newSlot = uint64(nextSlot)
				nextSlot++
			}
			if existing.Revision == math.MaxInt64 || rev == math.MaxInt64 {
				return conversationview.ErrRevisionExhausted
			}
			newRev := existing.Revision + 1
			rev++
			if err := m.upsertOverlayTx(ctx, tx, aLegID, conversationview.SteeringOverlay{
				OverlayID:           req.OverlayID,
				Revision:            newRev,
				SlotOrdinal:         newSlot,
				Active:              true,
				Message:             req.Message,
				Placement:           req.Placement,
				AnchorMissingPolicy: req.AnchorMissingPolicy,
				Reason:              req.Reason,
				CreatedAt:           existing.CreatedAt,
				UpdatedAt:           now,
			}); err != nil {
				return err
			}
			if err := m.upsertStateTx(ctx, tx, aLegID, rev, nextSlot); err != nil {
				return err
			}
			if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
				return err
			}
			kind := conversationview.CacheDiscontinuityReplace
			if placementChanged {
				kind = conversationview.CacheDiscontinuityMove
			}
			out = conversationview.SteeringState{
				OverlayID:                   req.OverlayID,
				Revision:                    newRev,
				SlotOrdinal:                 newSlot,
				Active:                      true,
				StateRevision:               uint64(rev),
				CacheDiscontinuityKind:      kind,
				CacheDiscontinuityPlacement: req.Placement.Kind,
			}
			_ = nowUnix
			return nil
		}
		// New overlay creation.
		activeCount, totalBytes, err := m.loadActiveStatsTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		activeCount++
		totalBytes += len(req.Message.Text)
		if activeCount > conversationview.MaxActiveOverlays {
			return conversationview.ErrSteeringLimitExceeded
		}
		if totalBytes > conversationview.MaxTotalSteeringBytes {
			return conversationview.ErrSteeringLimitExceeded
		}
		if len(req.Message.Text) > conversationview.MaxSteeringTextBytes {
			return conversationview.ErrSteeringLimitExceeded
		}
		if nextSlot == math.MaxInt64 || rev == math.MaxInt64 {
			return conversationview.ErrRevisionExhausted
		}
		slot := uint64(nextSlot)
		nextSlot++
		rev++
		ov := conversationview.SteeringOverlay{
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
		if err := m.upsertOverlayTx(ctx, tx, aLegID, ov); err != nil {
			return err
		}
		if err := m.upsertStateTx(ctx, tx, aLegID, rev, nextSlot); err != nil {
			return err
		}
		if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		out = conversationview.SteeringState{
			OverlayID:                   ov.OverlayID,
			Revision:                    ov.Revision,
			SlotOrdinal:                 ov.SlotOrdinal,
			Active:                      true,
			StateRevision:               uint64(rev),
			CacheDiscontinuityKind:      conversationview.CacheDiscontinuityCreate,
			CacheDiscontinuityPlacement: req.Placement.Kind,
		}
		_ = nowUnix
		return nil
	})
	if err != nil {
		return conversationview.SteeringState{}, err
	}
	return out, nil
}

// DeactivateSteering marks an overlay inactive.
func (m *conversationViewStore) DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (conversationview.SteeringState, error) {
	if err := ctx.Err(); err != nil {
		return conversationview.SteeringState{}, err
	}
	// Validate overlayID via minimal request.
	tmpReq := conversationview.PutSteeringRequest{
		OverlayID:           overlayID,
		Message:             conversationview.StoredMessageV1{Role: "user", Text: "x"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	}
	if err := tmpReq.Validate(); err != nil {
		return conversationview.SteeringState{}, fmt.Errorf("%w: %v", conversationview.ErrInvalidSteeringRequest, err)
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return conversationview.SteeringState{}, conversationview.ErrALegNotFound
	}
	var out conversationview.SteeringState
	err := m.s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := m.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		rev, nextSlot, err := m.loadStateTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		existing, exists, err := m.loadOverlayTx(ctx, tx, aLegID, overlayID)
		if err != nil {
			return err
		}
		if !exists {
			return conversationview.ErrOverlayNotFound
		}
		if !existing.Active {
			if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
				return err
			}
			out = conversationview.SteeringState{
				OverlayID:                   existing.OverlayID,
				Revision:                    existing.Revision,
				SlotOrdinal:                 existing.SlotOrdinal,
				Active:                      false,
				StateRevision:               uint64(rev),
				CacheDiscontinuityKind:      conversationview.CacheDiscontinuityNone,
				CacheDiscontinuityPlacement: "",
			}
			return nil
		}
		if existing.Revision == math.MaxInt64 || rev == math.MaxInt64 {
			return conversationview.ErrRevisionExhausted
		}
		now := time.Now().UTC()
		newRev := existing.Revision + 1
		rev++
		updated := conversationview.SteeringOverlay{
			OverlayID:           existing.OverlayID,
			Revision:            newRev,
			SlotOrdinal:         existing.SlotOrdinal,
			Active:              false,
			Message:             existing.Message,
			Placement:           existing.Placement,
			AnchorMissingPolicy: existing.AnchorMissingPolicy,
			Reason:              existing.Reason,
			CreatedAt:           existing.CreatedAt,
			UpdatedAt:           now,
		}
		if err := m.upsertOverlayTx(ctx, tx, aLegID, updated); err != nil {
			return err
		}
		if err := m.upsertStateTx(ctx, tx, aLegID, rev, nextSlot); err != nil {
			return err
		}
		if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		out = conversationview.SteeringState{
			OverlayID:                   updated.OverlayID,
			Revision:                    updated.Revision,
			SlotOrdinal:                 updated.SlotOrdinal,
			Active:                      false,
			StateRevision:               uint64(rev),
			CacheDiscontinuityKind:      conversationview.CacheDiscontinuityDeactivate,
			CacheDiscontinuityPlacement: existing.Placement.Kind,
		}
		return nil
	})
	if err != nil {
		return conversationview.SteeringState{}, err
	}
	return out, nil
}

// GetOverlay is a test/debugging seam to inspect an overlay regardless of active state.
func (m *conversationViewStore) GetOverlay(ctx context.Context, aLegID string, overlayID string) (conversationview.SteeringOverlay, error) {
	if err := ctx.Err(); err != nil {
		return conversationview.SteeringOverlay{}, err
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return conversationview.SteeringOverlay{}, conversationview.ErrALegNotFound
	}
	var out conversationview.SteeringOverlay
	err := m.s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := m.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		ov, exists, err := m.loadOverlayTx(ctx, tx, aLegID, overlayID)
		if err != nil {
			return err
		}
		if !exists {
			return conversationview.ErrOverlayNotFound
		}
		if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		out = ov
		return nil
	})
	if err != nil {
		return conversationview.SteeringOverlay{}, err
	}
	return out, nil
}

func (m *conversationViewStore) lockALegTx(ctx context.Context, tx bun.Tx, aLegID string) error {
	q := `SELECT a_leg_id FROM a_legs WHERE a_leg_id = ?`
	if m.s.db.Dialect().Name() == dialect.PG {
		q = `SELECT a_leg_id FROM a_legs WHERE a_leg_id = ? FOR UPDATE`
	}
	var id string
	err := tx.NewRaw(q, aLegID).Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return conversationview.ErrALegNotFound
	}
	if err != nil {
		return opErr("lock a leg for conversation view", err)
	}
	return nil
}

func (m *conversationViewStore) touchALegTx(ctx context.Context, tx bun.Tx, aLegID string) error {
	_, err := tx.NewRaw(`UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?`, time.Now().UnixNano(), aLegID).Exec(ctx)
	if err != nil {
		return opErr("touch a leg last seen", err)
	}
	return nil
}

func (m *conversationViewStore) loadStateTx(ctx context.Context, tx bun.Tx, aLegID string) (rev int64, nextSlot int64, err error) {
	err = tx.NewRaw(`SELECT state_revision, next_slot_ordinal FROM a_leg_conversation_view_state WHERE a_leg_id = ?`, aLegID).Scan(ctx, &rev, &nextSlot)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 1, nil
	}
	if err != nil {
		return 0, 0, opErr("select conversation view state", err)
	}
	return rev, nextSlot, nil
}

func (m *conversationViewStore) upsertStateTx(ctx context.Context, tx bun.Tx, aLegID string, rev, nextSlot int64) error {
	_, err := tx.NewRaw(`
		INSERT INTO a_leg_conversation_view_state(a_leg_id, state_revision, next_slot_ordinal)
		VALUES(?,?,?)
		ON CONFLICT(a_leg_id) DO UPDATE SET
			state_revision=excluded.state_revision,
			next_slot_ordinal=excluded.next_slot_ordinal
	`, aLegID, rev, nextSlot).Exec(ctx)
	if err != nil {
		return opErr("upsert conversation view state", err)
	}
	return nil
}

func (m *conversationViewStore) loadSnapshotTx(ctx context.Context, tx bun.Tx, aLegID string) (conversationview.Snapshot, error) {
	rev, _, err := m.loadStateTx(ctx, tx, aLegID)
	if err != nil {
		return conversationview.Snapshot{}, err
	}
	tags, err := m.loadTagsTx(ctx, tx, aLegID)
	if err != nil {
		return conversationview.Snapshot{}, err
	}
	steering, err := m.loadActiveOverlaysTx(ctx, tx, aLegID)
	if err != nil {
		return conversationview.Snapshot{}, err
	}
	return conversationview.Snapshot{
		StateRevision: uint64(rev),
		NeverBackend:  tags,
		Steering:      steering,
	}, nil
}

func (m *conversationViewStore) loadTagsTx(ctx context.Context, tx bun.Tx, aLegID string) ([]conversationview.Tag, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT identity_version, identity_digest, reason, created_at_unix
		FROM a_leg_never_backend_messages
		WHERE a_leg_id = ?
		ORDER BY identity_digest ASC, identity_version ASC
	`, aLegID)
	if err != nil {
		return nil, opErr("select never backend tags", err)
	}
	defer func() { _ = rows.Close() }()
	var out []conversationview.Tag
	for rows.Next() {
		var version, digest, reason string
		var createdAt int64
		if err := rows.Scan(&version, &digest, &reason, &createdAt); err != nil {
			return nil, opErr("scan never backend tag", err)
		}
		id := conversationview.MessageIdentity(version + ":" + digest)
		if err := id.Validate(); err != nil {
			return nil, opErr("stored tag identity invalid", err)
		}
		out = append(out, conversationview.Tag{
			Identity:  id,
			Reason:    conversationview.ReasonCode(reason),
			CreatedAt: time.Unix(0, createdAt).UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("iterate never backend tags", err)
	}
	if out == nil {
		out = []conversationview.Tag{}
	}
	// Ensure deterministic ordering by full identity string (already sorted by digest).
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].Identity) < string(out[j].Identity)
	})
	return out, nil
}

func (m *conversationViewStore) loadTagSetTx(ctx context.Context, tx bun.Tx, aLegID string) (map[conversationview.MessageIdentity]struct{}, error) {
	tags, err := m.loadTagsTx(ctx, tx, aLegID)
	if err != nil {
		return nil, err
	}
	set := make(map[conversationview.MessageIdentity]struct{}, len(tags))
	for _, t := range tags {
		set[t.Identity] = struct{}{}
	}
	return set, nil
}

func (m *conversationViewStore) loadActiveOverlaysTx(ctx context.Context, tx bun.Tx, aLegID string) ([]conversationview.SteeringOverlay, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT overlay_id, overlay_revision, slot_ordinal, active, message_version, message_role, message_text,
		       placement_kind, anchor_identity_version, anchor_identity_digest, anchor_occurrence, anchor_missing_policy, reason, created_at_unix, updated_at_unix
		FROM a_leg_steering_overlays
		WHERE a_leg_id = ? AND active = 1
		ORDER BY slot_ordinal ASC
	`, aLegID)
	if err != nil {
		return nil, opErr("select active overlays", err)
	}
	defer func() { _ = rows.Close() }()
	var out []conversationview.SteeringOverlay
	for rows.Next() {
		var overlayID string
		var overlayRevision, slotOrdinal int64
		var active int
		var messageVersion, messageRole, messageText, placementKind, anchorVersion, anchorDigest, anchorMissingPolicy, reason string
		var anchorOccurrence int64
		var createdAt, updatedAt int64
		if err := rows.Scan(&overlayID, &overlayRevision, &slotOrdinal, &active, &messageVersion, &messageRole, &messageText, &placementKind, &anchorVersion, &anchorDigest, &anchorOccurrence, &anchorMissingPolicy, &reason, &createdAt, &updatedAt); err != nil {
			return nil, opErr("scan overlay", err)
		}
		ov, err := rowToOverlay(overlayID, overlayRevision, slotOrdinal, active, messageRole, messageText, placementKind, anchorVersion, anchorDigest, anchorOccurrence, anchorMissingPolicy, reason, createdAt, updatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, ov)
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("iterate overlays", err)
	}
	if out == nil {
		out = []conversationview.SteeringOverlay{}
	}
	return out, nil
}

func (m *conversationViewStore) loadActiveStatsTx(ctx context.Context, tx bun.Tx, aLegID string) (count int, totalBytes int, err error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT message_text FROM a_leg_steering_overlays WHERE a_leg_id = ? AND active = 1
	`, aLegID)
	if err != nil {
		return 0, 0, opErr("select active stats", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return 0, 0, opErr("scan active stats", err)
		}
		count++
		totalBytes += len(text)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, opErr("iterate active stats", err)
	}
	return count, totalBytes, nil
}

func (m *conversationViewStore) loadOverlayTx(ctx context.Context, tx bun.Tx, aLegID, overlayID string) (conversationview.SteeringOverlay, bool, error) {
	var overlayRevision, slotOrdinal int64
	var active int
	var messageVersion, messageRole, messageText, placementKind, anchorVersion, anchorDigest, anchorMissingPolicy, reason string
	var anchorOccurrence int64
	var createdAt, updatedAt int64
	err := tx.NewRaw(`
		SELECT overlay_id, overlay_revision, slot_ordinal, active, message_version, message_role, message_text,
		       placement_kind, anchor_identity_version, anchor_identity_digest, anchor_occurrence, anchor_missing_policy, reason, created_at_unix, updated_at_unix
		FROM a_leg_steering_overlays WHERE a_leg_id = ? AND overlay_id = ?
	`, aLegID, overlayID).Scan(ctx, &overlayID, &overlayRevision, &slotOrdinal, &active, &messageVersion, &messageRole, &messageText, &placementKind, &anchorVersion, &anchorDigest, &anchorOccurrence, &anchorMissingPolicy, &reason, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return conversationview.SteeringOverlay{}, false, nil
	}
	if err != nil {
		return conversationview.SteeringOverlay{}, false, opErr("select overlay", err)
	}
	ov, err := rowToOverlay(overlayID, overlayRevision, slotOrdinal, active, messageRole, messageText, placementKind, anchorVersion, anchorDigest, anchorOccurrence, anchorMissingPolicy, reason, createdAt, updatedAt)
	if err != nil {
		return conversationview.SteeringOverlay{}, false, err
	}
	return ov, true, nil
}

func (m *conversationViewStore) upsertOverlayTx(ctx context.Context, tx bun.Tx, aLegID string, ov conversationview.SteeringOverlay) error {
	active := 0
	if ov.Active {
		active = 1
	}
	anchorVersion, anchorDigest, anchorOcc := "", "", int64(0)
	if ov.Placement.Kind == conversationview.PlacementAfterMessage && ov.Placement.Anchor != nil {
		anchorVersion = ov.Placement.Anchor.Identity.Version()
		anchorDigest = ov.Placement.Anchor.Identity.Digest()
		anchorOcc = int64(ov.Placement.Anchor.Occurrence)
	}
	_, err := tx.NewRaw(`
		INSERT INTO a_leg_steering_overlays(
			a_leg_id, overlay_id, overlay_revision, slot_ordinal, active, message_version, message_role, message_text,
			placement_kind, anchor_identity_version, anchor_identity_digest, anchor_occurrence, anchor_missing_policy, reason, created_at_unix, updated_at_unix
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(a_leg_id, overlay_id) DO UPDATE SET
			overlay_revision=excluded.overlay_revision,
			slot_ordinal=excluded.slot_ordinal,
			active=excluded.active,
			message_version=excluded.message_version,
			message_role=excluded.message_role,
			message_text=excluded.message_text,
			placement_kind=excluded.placement_kind,
			anchor_identity_version=excluded.anchor_identity_version,
			anchor_identity_digest=excluded.anchor_identity_digest,
			anchor_occurrence=excluded.anchor_occurrence,
			anchor_missing_policy=excluded.anchor_missing_policy,
			reason=excluded.reason,
			created_at_unix=excluded.created_at_unix,
			updated_at_unix=excluded.updated_at_unix
	`, aLegID, ov.OverlayID, int64(ov.Revision), int64(ov.SlotOrdinal), active, "v1", string(ov.Message.Role), ov.Message.Text, string(ov.Placement.Kind), anchorVersion, anchorDigest, anchorOcc, string(ov.AnchorMissingPolicy), string(ov.Reason), ov.CreatedAt.UnixNano(), ov.UpdatedAt.UnixNano()).Exec(ctx)
	if err != nil {
		return opErr("upsert overlay", err)
	}
	return nil
}

func rowToOverlay(overlayID string, overlayRevision, slotOrdinal int64, active int, messageRole, messageText, placementKind, anchorVersion, anchorDigest string, anchorOccurrence int64, anchorMissingPolicy, reason string, createdAt, updatedAt int64) (conversationview.SteeringOverlay, error) {
	placement := conversationview.StoredPlacement{Kind: conversationview.PlacementKind(placementKind)}
	if placement.Kind == conversationview.PlacementAfterMessage {
		if anchorVersion == "" || anchorDigest == "" {
			return conversationview.SteeringOverlay{}, fmt.Errorf("bunstore: stored overlay %q has after_message placement without anchor", overlayID)
		}
		id := conversationview.MessageIdentity(anchorVersion + ":" + anchorDigest)
		if err := id.Validate(); err != nil {
			return conversationview.SteeringOverlay{}, opErr("stored anchor identity invalid", err)
		}
		placement.Anchor = &conversationview.MessageAnchor{
			Identity:   id,
			Occurrence: uint32(anchorOccurrence),
		}
	}
	ov := conversationview.SteeringOverlay{
		OverlayID:           overlayID,
		Revision:            uint64(overlayRevision),
		SlotOrdinal:         uint64(slotOrdinal),
		Active:              active != 0,
		Message:             conversationview.StoredMessageV1{Role: lipapi.Role(messageRole), Text: messageText},
		Placement:           placement,
		AnchorMissingPolicy: conversationview.AnchorMissingPolicy(anchorMissingPolicy),
		Reason:              conversationview.ReasonCode(reason),
		CreatedAt:           time.Unix(0, createdAt).UTC(),
		UpdatedAt:           time.Unix(0, updatedAt).UTC(),
	}
	if err := ov.Validate(); err != nil {
		return conversationview.SteeringOverlay{}, fmt.Errorf("bunstore: stored overlay invalid: %w", err)
	}
	return ov, nil
}

func splitIdentity(id conversationview.MessageIdentity) (version, digest string, err error) {
	s := string(id)
	idx := strings.IndexByte(s, ':')
	if idx < 0 {
		return "", "", fmt.Errorf("%w: missing colon in identity %q", conversationview.ErrInvalidMessageIdentity, s)
	}
	return s[:idx], s[idx+1:], nil
}

func overlaysEqualForBun(a, b conversationview.SteeringOverlay) bool {
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
