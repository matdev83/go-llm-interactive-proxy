package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

func phase171BaselineLeg(t *testing.T) CallLegUsageRecord {
	t.Helper()
	fixture := readPhase1V1CompatibilityFixture(t)
	var leg CallLegUsageRecord
	if err := json.Unmarshal(fixture.Leg.Payload, &leg); err != nil {
		t.Fatalf("decode baseline leg payload: %v", err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatalf("seal baseline leg payload: %v", err)
	}
	return sealed
}

func phase171BaselineCall(t *testing.T) CallUsageRecord {
	t.Helper()
	fixture := readPhase1V1CompatibilityFixture(t)
	var call CallUsageRecord
	if err := json.Unmarshal(fixture.Call.Payload, &call); err != nil {
		t.Fatalf("decode baseline call payload: %v", err)
	}
	sealed, err := call.Seal()
	if err != nil {
		t.Fatalf("seal baseline call payload: %v", err)
	}
	return sealed
}

func TestPhase171HistoricalV1LegRoundTripsBaselineHashes(t *testing.T) {
	t.Parallel()
	fixture := readPhase1V1CompatibilityFixture(t)
	leg := phase171BaselineLeg(t)

	version, err := WriterVersionForLeg(leg)
	if err != nil {
		t.Fatalf("WriterVersionForLeg: %v", err)
	}
	if version != HistoricalV1WriterVersion {
		t.Fatalf("writer version = %q, want %q", version, HistoricalV1WriterVersion)
	}

	view, err := ProjectHistoricalV1Leg(leg)
	if err != nil {
		t.Fatalf("ProjectHistoricalV1Leg: %v", err)
	}
	if view.Key != fixture.Leg.Key || view.Fingerprint != fixture.Leg.SemanticFingerprint {
		t.Fatalf("view identity = %q/%q, want %q/%q", view.Key, view.Fingerprint, fixture.Leg.Key, fixture.Leg.SemanticFingerprint)
	}
	raw, err := json.Marshal(leg)
	if err != nil {
		t.Fatalf("marshal sealed leg: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != fixture.Leg.PayloadSHA256 {
		t.Fatalf("sealed payload SHA = %s, want %s", got, fixture.Leg.PayloadSHA256)
	}
	if view.PayloadSHA256 != fixture.Leg.PayloadSHA256 {
		t.Fatalf("view payload SHA = %q, want %q", view.PayloadSHA256, fixture.Leg.PayloadSHA256)
	}
	if view.WriterVersion != HistoricalV1WriterVersion {
		t.Fatalf("view writer = %q, want %q", view.WriterVersion, HistoricalV1WriterVersion)
	}
}

func TestPhase171HistoricalV1LeavesBreakdownUnavailable(t *testing.T) {
	t.Parallel()
	leg := phase171BaselineLeg(t)
	view, err := ProjectHistoricalV1Leg(leg)
	if err != nil {
		t.Fatalf("ProjectHistoricalV1Leg: %v", err)
	}
	if view.BreakdownAvailable || view.SourceSeparationAvailable {
		t.Fatalf("V1 view must leave breakdown/source separation unavailable: %+v", view)
	}
	if view.LegacySemantics != LegacyScalarSemantics {
		t.Fatalf("legacy semantics = %q, want %q", view.LegacySemantics, LegacyScalarSemantics)
	}
	if err := view.Validate(); err != nil {
		t.Fatalf("view Validate: %v", err)
	}
}

func TestPhase171HistoricalV1WriterOwnershipV2CannotClaim(t *testing.T) {
	t.Parallel()
	leg := phase171BaselineLeg(t)
	if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{leg}, HistoricalV1WriterVersion); err != nil {
		t.Fatalf("V1 claim on V1-owned work: %v", err)
	}
	if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{leg}, V2WriterVersion); !errors.Is(err, ErrHistoricalV1WriterConflict) {
		t.Fatalf("V2 claim on V1-owned work = %v, want ErrHistoricalV1WriterConflict", err)
	}
}

func TestPhase171HistoricalV1MalformedFailsClosed(t *testing.T) {
	t.Parallel()
	leg := phase171BaselineLeg(t)

	ambiguous := leg
	ambiguous.EvidenceVersion = 1
	if _, err := WriterVersionForLeg(ambiguous); !errors.Is(err, ErrHistoricalV1Ambiguous) && !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("ambiguous evidence version = %v, want version failure", err)
	}
	if _, err := ProjectHistoricalV1Leg(ambiguous); err == nil {
		t.Fatal("ambiguous V1 projection must fail closed")
	}

	mutated := leg
	mutated.Fingerprint = "deadbeef"
	if _, err := ProjectHistoricalV1Leg(mutated); err == nil {
		t.Fatal("mutated fingerprint must fail closed")
	}

	if err := CheckHistoricalV1WriterClaim([]CallLegUsageRecord{leg}, "bogus"); err == nil {
		t.Fatal("bogus claimant must fail closed")
	}
	if err := CheckHistoricalV1WriterClaim(nil, HistoricalV1WriterVersion); err == nil {
		t.Fatal("empty leg set must fail closed")
	}
}

func TestPhase171HistoricalV1CallRoundTripsBaselineIdentity(t *testing.T) {
	t.Parallel()
	fixture := readPhase1V1CompatibilityFixture(t)
	call := phase171BaselineCall(t)
	view, err := ProjectHistoricalV1Call(call)
	if err != nil {
		t.Fatalf("ProjectHistoricalV1Call: %v", err)
	}
	if view.Key != fixture.Identity.CallKey {
		t.Fatalf("call key = %q, want %q", view.Key, fixture.Identity.CallKey)
	}
	if view.Fingerprint != fixture.Call.SemanticFingerprint {
		t.Fatalf("call fingerprint = %q, want %q", view.Fingerprint, fixture.Call.SemanticFingerprint)
	}
	if view.WriterVersion != HistoricalV1WriterVersion {
		t.Fatalf("call writer = %q, want v1", view.WriterVersion)
	}
	if err := view.Validate(); err != nil {
		t.Fatalf("call view Validate: %v", err)
	}
}
