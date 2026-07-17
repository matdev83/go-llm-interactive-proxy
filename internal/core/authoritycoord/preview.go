package authoritycoord

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

// PreviewClamps invokes optional AttemptClampPreviewer slots without holds and
// merges non-widening known clamps (design Clamp Preview). Preview reservations,
// compensation handles, and unknown clamp kinds are rejected.
func (c *AttemptCoordinator) PreviewClamps(ctx context.Context, in authority.AttemptAdmission) ([]authority.Clamp, error) {
	if c == nil {
		return nil, nil
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if err := validateAttemptSlots(c.Slots); err != nil {
		return nil, err
	}
	var merged []authority.Clamp
	slots := append([]AttemptSlot(nil), c.Slots...)
	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].Class != slots[j].Class {
			return slots[i].Class < slots[j].Class
		}
		return slots[i].ID < slots[j].ID
	})
	for _, slot := range slots {
		previewer, ok := slot.Provider.(authority.AttemptClampPreviewer)
		if !ok || previewer == nil {
			continue
		}
		id := strings.TrimSpace(slot.ID)
		d, err := invokePreviewAttempt(ctx, previewer, in)
		if err != nil {
			strength, _ := resolveAttemptPosture(slot)
			if strength == authority.StrengthRequired {
				return nil, fmt.Errorf("authoritycoord: preview %s: %w", id, err)
			}
			continue
		}
		if len(d.Reservations) > 0 || strings.TrimSpace(d.CompensationHandle) != "" {
			return nil, fmt.Errorf("authoritycoord: preview %s returned holds (side-effect free preview forbids reservations)", id)
		}
		for _, clamp := range d.Clamps {
			if err := validatePreviewClamp(clamp); err != nil {
				return nil, fmt.Errorf("authoritycoord: preview %s: %w", id, err)
			}
		}
		var merr error
		merged, merr = mergeClampsNonWidening(merged, d.Clamps)
		if merr != nil {
			return nil, fmt.Errorf("authoritycoord: preview %s: %w", id, merr)
		}
	}
	return merged, nil
}
