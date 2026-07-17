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
		if id == "" {
			id = fmt.Sprintf("attempt-class-%d", slot.Class)
		}
		d, err := invokePreviewAttempt(ctx, previewer, in)
		if err != nil {
			strength := slot.Strength
			if strength == "" {
				if slot.Class == AttemptPriorityAdvisory {
					strength = authority.StrengthAdvisory
				} else {
					strength = authority.StrengthRequired
				}
			}
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
		merged = mergeClampsNonWidening(merged, d.Clamps)
	}
	return merged, nil
}

func validatePreviewClamp(c authority.Clamp) error {
	switch c.Kind {
	case authority.ClampMaxOutputTokens:
		if c.Value < 0 {
			return fmt.Errorf("max_output_tokens clamp value must be non-negative")
		}
		return nil
	case authority.ClampMaxSpend:
		if !c.Money.Present {
			return fmt.Errorf("max_spend clamp requires present money")
		}
		if c.Money.NanoUnits < 0 {
			return fmt.Errorf("max_spend clamp money must be non-negative")
		}
		if strings.TrimSpace(c.Money.Currency) == "" {
			return fmt.Errorf("max_spend clamp requires currency")
		}
		return nil
	default:
		return fmt.Errorf("unknown or unapplicable clamp kind %q", c.Kind)
	}
}
