package billing

import (
	"testing"
)

// Stripped V2 projection retaining old sealed identity must fail closed in
// the claim path, even though Seal would restore the missing label.
func TestPhase171ProjectionStrippedV2ClaimFailsClosed(t *testing.T) {
	t.Parallel()
	base := phase171BaselineLeg(t)

	validV2 := base
	validV2.Key = ""
	validV2.Fingerprint = ""
	validV2.EvidenceVersion = EvidenceFormatVersionV2
	validV2.EvidenceProjection = EvidenceProjectionV1
	sealedV2, err := validV2.Seal()
	if err != nil {
		t.Fatalf("seal valid V2 leg: %v", err)
	}

	stripped := sealedV2
	stripped.EvidenceProjection = ""

	if _, err := WriterVersionForLeg(stripped); err == nil {
		t.Fatal("stripped V2 projection must fail version resolution")
	}

	if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{stripped}, V2WriterVersion); err == nil {
		t.Fatal("stripped V2 projection retaining sealed identity must fail closed")
	}
}

func TestPhase171ProjectionValidV1AndV2Controls(t *testing.T) {
	t.Parallel()
	base := phase171BaselineLeg(t)
	if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{base}, HistoricalV1WriterVersion); err != nil {
		t.Fatalf("valid V1 claim: %v", err)
	}

	validV2 := base
	validV2.Key = ""
	validV2.Fingerprint = ""
	validV2.EvidenceVersion = EvidenceFormatVersionV2
	validV2.EvidenceProjection = EvidenceProjectionV1
	sealedV2, err := validV2.Seal()
	if err != nil {
		t.Fatalf("seal valid V2 leg: %v", err)
	}
	if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{sealedV2}, V2WriterVersion); err != nil {
		t.Fatalf("valid V2 claim: %v", err)
	}
}
