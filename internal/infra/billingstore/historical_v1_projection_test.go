package billingstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Stripped V2 projection in the raw stored payload must fail closed at the
// storage seam, even though Seal would restore the missing label.
func TestPhase171ProjectionStoreStrippedV2ClaimFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	fixture := readPhase171V1Fixture(t)
	var call billing.CallUsageRecord
	if err := json.Unmarshal(fixture.Call.Payload, &call); err != nil {
		t.Fatalf("decode call payload: %v", err)
	}
	sealedCall, err := call.Seal()
	if err != nil {
		t.Fatalf("seal call: %v", err)
	}
	if err := store.AppendCallUsage(ctx, sealedCall); err != nil {
		t.Fatalf("AppendCallUsage: %v", err)
	}

	var leg billing.CallLegUsageRecord
	if err := json.Unmarshal(fixture.Leg.Payload, &leg); err != nil {
		t.Fatalf("decode leg payload: %v", err)
	}
	leg.BLegID = "b-stripped"
	leg.Key = ""
	leg.Fingerprint = ""
	leg.EvidenceVersion = billing.EvidenceFormatVersionV2
	leg.EvidenceProjection = billing.EvidenceProjectionV1
	sealedV2, err := leg.Seal()
	if err != nil {
		t.Fatalf("seal valid V2 leg: %v", err)
	}

	stripped := sealedV2
	stripped.EvidenceProjection = ""
	if _, err := billing.WriterVersionForLeg(stripped); err == nil {
		t.Fatal("stripped V2 projection must fail version resolution")
	}
	phase171InsertRawLegRow(t, store, sealedV2.Key, sealedV2.Fingerprint, sealedCall.CallID.String(), stripped)

	if err := store.CheckHistoricalV1WriterClaim(ctx, sealedCall.CallID, billing.V2WriterVersion); err == nil {
		t.Fatal("stripped V2 projection in stored payload must fail closed")
	}
}

func TestPhase171ProjectionStoreValidV1AndV2Controls(t *testing.T) {
	t.Parallel()

	t.Run("valid V1", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		sealedCall, _ := phase171SeedBaselineV1(t, store)
		if err := store.CheckHistoricalV1WriterClaim(ctx, sealedCall.CallID, billing.HistoricalV1WriterVersion); err != nil {
			t.Fatalf("valid V1 claim: %v", err)
		}
	})

	t.Run("valid V2", func(t *testing.T) {
		t.Parallel()
		store := newSQLiteTestStore(t)
		ctx := context.Background()
		fixture := readPhase171V1Fixture(t)
		var call billing.CallUsageRecord
		if err := json.Unmarshal(fixture.Call.Payload, &call); err != nil {
			t.Fatalf("decode call payload: %v", err)
		}
		sealedCall, err := call.Seal()
		if err != nil {
			t.Fatalf("seal call: %v", err)
		}
		if err := store.AppendCallUsage(ctx, sealedCall); err != nil {
			t.Fatalf("AppendCallUsage: %v", err)
		}
		var leg billing.CallLegUsageRecord
		if err := json.Unmarshal(fixture.Leg.Payload, &leg); err != nil {
			t.Fatalf("decode leg payload: %v", err)
		}
		leg.BLegID = "b-v2"
		leg.Key = ""
		leg.Fingerprint = ""
		leg.EvidenceVersion = billing.EvidenceFormatVersionV2
		leg.EvidenceProjection = billing.EvidenceProjectionV1
		sealedV2, err := leg.Seal()
		if err != nil {
			t.Fatalf("seal valid V2 leg: %v", err)
		}
		if err := store.AppendCallLegUsage(ctx, sealedV2); err != nil {
			t.Fatalf("AppendCallLegUsage: %v", err)
		}
		if err := store.CheckHistoricalV1WriterClaim(ctx, sealedCall.CallID, billing.V2WriterVersion); err != nil {
			t.Fatalf("valid V2 claim: %v", err)
		}
	})
}
