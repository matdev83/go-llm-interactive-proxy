package economics

import (
	"fmt"
	"strings"
)

// ValidateFor checks an untrusted RatingResult against the RatingRequest that
// produced it (design External Result Validation; requirements 4.6–4.8, 4.10).
func (r RatingResult) ValidateFor(req RatingRequest) error {
	if err := req.Perspective.Validate(); err != nil {
		return fmt.Errorf("economics: rating request perspective: %w", err)
	}
	if r.Perspective != req.Perspective {
		return fmt.Errorf("economics: rating perspective %q does not match request %q", r.Perspective, req.Perspective)
	}
	if err := r.Perspective.Validate(); err != nil {
		return fmt.Errorf("economics: rating result perspective: %w", err)
	}
	if err := ValidateSafeRef("rater_id", r.RaterID); err != nil {
		return err
	}
	if err := validateVersionRef(r.Version); err != nil {
		return err
	}
	if !r.Money.Present {
		return fmt.Errorf("economics: rating money must be present (absent is not authoritative zero)")
	}
	if err := ValidatePresentMoney(r.Money); err != nil {
		return fmt.Errorf("economics: rating money: %w", err)
	}
	if !r.RoundingPolicy.IsKnown() {
		return fmt.Errorf("economics: unknown rounding policy %q", r.RoundingPolicy)
	}
	if strings.TrimSpace(r.LineID) != "" {
		if err := ValidateSafeRef("line_id", r.LineID); err != nil {
			return err
		}
	}
	for i, id := range req.FactIDs {
		if err := ValidateSafeRef("fact_ids", id); err != nil {
			return fmt.Errorf("economics: rating request fact_ids[%d]: %w", i, err)
		}
	}
	for i, ref := range req.FactRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("economics: rating request fact_refs[%d]: %w", i, err)
		}
	}
	return nil
}

func validateVersionRef(v VersionRef) error {
	if err := ValidateSafeRef("version.id", v.ID); err != nil {
		return err
	}
	if err := ValidateSafeRef("version.version", v.Version); err != nil {
		return err
	}
	return nil
}
