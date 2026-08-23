package reasoningpreservation

import (
	"context"
	"fmt"
)

// CompressorInputSegment is a local index + text for one semantic placement.
type CompressorInputSegment struct {
	Index int
	Text  string
}

// PrepareCompressorInput applies egress decision => sanitization if required => then budgets.
// It enforces that redaction occurs before input-size accounting.
// Returns sanitized segments, outcome, error.
// On deny, returns nil segments, outcome "denied" or "missing-policy", and an error.
func PrepareCompressorInput(ctx context.Context, segments []CompressorInputSegment, decision CompressionEgressDecision, maxInputBytes int) ([]CompressorInputSegment, string, error) {
	if decision.Action == EgressDeny {
		vers := decision.PolicyVersion
		if vers == "" {
			vers = "denied"
		}
		if vers == "missing-policy" {
			return nil, "missing-policy", fmt.Errorf("%s: egress denied missing-policy", ID)
		}
		return nil, "denied", fmt.Errorf("%s: egress denied", ID)
	}
	var sanitized []CompressorInputSegment
	switch decision.Action {
	case EgressAllow:
		sanitized = make([]CompressorInputSegment, len(segments))
		copy(sanitized, segments)
	case EgressRedactThenAllow:
		if decision.Sanitizer == nil {
			return nil, "denied", fmt.Errorf("%s: redact requires sanitizer", ID)
		}
		sanitized = make([]CompressorInputSegment, 0, len(segments))
		for _, seg := range segments {
			out, err := decision.Sanitizer.SanitizeText(ctx, seg.Text)
			if err != nil {
				return nil, "denied", err
			}
			sanitized = append(sanitized, CompressorInputSegment{Index: seg.Index, Text: out})
		}
	default:
		return nil, "denied", fmt.Errorf("%s: unknown egress action", ID)
	}
	// budget after sanitization
	total := 0
	for _, s := range sanitized {
		total += len(s.Text)
	}
	if maxInputBytes > 0 && total > maxInputBytes {
		return nil, "input_oversize", fmt.Errorf("%s: input exceeds max_input_bytes %d > %d", ID, total, maxInputBytes)
	}
	return sanitized, "prepared", nil
}
