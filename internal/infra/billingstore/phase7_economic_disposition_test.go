package billingstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestSQLiteCallLegEconomicDispositionRoundTripAndConflict(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	partial := testIndependentCallLegFor(callID, "b-partial")
	partialObservation := phase7BillingStoreObservation(callID, "b-partial", "obs-partial")
	partialDisposition, err := billing.NewEconomicEvidenceDisposition(partialObservation, billing.EconomicEvidenceCoveragePartial, "legacy V1 token-only evidence")
	if err != nil {
		t.Fatal(err)
	}
	partial.EvidenceVersion = billing.EvidenceFormatVersionV2
	partial.EvidenceProjection = billing.EvidenceProjectionV1
	partial.Observations = []metering.Observation{partialObservation}
	partial.EconomicEvidenceVersion = billing.EconomicEvidenceDispositionVersionV1
	partial.EconomicDispositions = []billing.EconomicEvidenceDisposition{partialDisposition}
	if err := store.AppendCallLegUsage(ctx, partial); err != nil {
		t.Fatalf("append partial: %v", err)
	}
	gotPartial, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, "b-partial"))
	if err != nil {
		t.Fatalf("get partial: %v", err)
	}
	if len(gotPartial.EconomicDispositions) != 1 || gotPartial.EconomicDispositions[0].Coverage != billing.EconomicEvidenceCoveragePartial || gotPartial.EconomicDispositions[0].CoverageReason != "legacy V1 token-only evidence" {
		t.Fatalf("partial disposition was not durable: %+v", gotPartial.EconomicDispositions)
	}
	if err := store.AppendCallLegUsage(ctx, partial); err != nil {
		t.Fatalf("identical partial replay: %v", err)
	}

	complete := testIndependentCallLegFor(callID, "b-complete")
	complete.AttemptSeq = 2
	completeObservation := phase7BillingStoreObservation(callID, "b-complete", "obs-complete")
	completeDisposition, err := billing.NewEconomicEvidenceDisposition(completeObservation, billing.EconomicEvidenceCoverageComplete, "")
	if err != nil {
		t.Fatal(err)
	}
	complete.EvidenceVersion = billing.EvidenceFormatVersionV2
	complete.EvidenceProjection = billing.EvidenceProjectionV1
	complete.Observations = []metering.Observation{completeObservation}
	complete.EconomicEvidenceVersion = billing.EconomicEvidenceDispositionVersionV1
	complete.EconomicDispositions = []billing.EconomicEvidenceDisposition{completeDisposition}
	if err := store.AppendCallLegUsage(ctx, complete); err != nil {
		t.Fatalf("append complete: %v", err)
	}
	gotComplete, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, "b-complete"))
	if err != nil {
		t.Fatalf("get complete: %v", err)
	}
	if len(gotComplete.EconomicDispositions) != 1 || gotComplete.EconomicDispositions[0].Coverage != billing.EconomicEvidenceCoverageComplete {
		t.Fatalf("complete disposition was not durable: %+v", gotComplete.EconomicDispositions)
	}

	unsupported := testIndependentCallLegFor(callID, "b-unsupported")
	unsupported.AttemptSeq = 3
	unsupportedObservation := phase7BillingStoreObservation(callID, "b-unsupported", "obs-unsupported")
	unsupportedDisposition, err := billing.NewEconomicEvidenceDisposition(unsupportedObservation, billing.EconomicEvidenceCoverageUnsupported, "connector does not expose provider economics")
	if err != nil {
		t.Fatal(err)
	}
	unsupported.EvidenceVersion = billing.EvidenceFormatVersionV2
	unsupported.EvidenceProjection = billing.EvidenceProjectionV1
	unsupported.Observations = []metering.Observation{unsupportedObservation}
	unsupported.EconomicEvidenceVersion = billing.EconomicEvidenceDispositionVersionV1
	unsupported.EconomicDispositions = []billing.EconomicEvidenceDisposition{unsupportedDisposition}
	if err := store.AppendCallLegUsage(ctx, unsupported); err != nil {
		t.Fatalf("append unsupported: %v", err)
	}
	gotUnsupported, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, "b-unsupported"))
	if err != nil {
		t.Fatalf("get unsupported: %v", err)
	}
	if len(gotUnsupported.EconomicDispositions) != 1 || gotUnsupported.EconomicDispositions[0].Coverage != billing.EconomicEvidenceCoverageUnsupported || gotUnsupported.EconomicDispositions[0].CoverageReason != "connector does not expose provider economics" {
		t.Fatalf("unsupported disposition was not durable: %+v", gotUnsupported.EconomicDispositions)
	}

	legacy := testIndependentCallLegFor(callID, "b-legacy")
	legacy.AttemptSeq = 4
	if err := store.AppendCallLegUsage(ctx, legacy); err != nil {
		t.Fatalf("append legacy V1 row: %v", err)
	}
	gotLegacy, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, "b-legacy"))
	if err != nil {
		t.Fatalf("get legacy V1 row: %v", err)
	}
	if gotLegacy.EconomicEvidenceVersion != 0 || len(gotLegacy.EconomicDispositions) != 0 {
		t.Fatalf("legacy V1 row was rewritten as economic V2: %+v", gotLegacy)
	}

	conflict := partial
	conflict.EconomicDispositions = append([]billing.EconomicEvidenceDisposition(nil), partial.EconomicDispositions...)
	conflict.EconomicDispositions[0].Coverage = billing.EconomicEvidenceCoverageComplete
	conflict.EconomicDispositions[0].CoverageReason = ""
	if err := store.AppendCallLegUsage(ctx, conflict); !errors.Is(err, billing.ErrReplayConflict) {
		t.Fatalf("mismatched disposition replay = %v, want ErrReplayConflict", err)
	}
}

func phase7BillingStoreObservation(callID billing.BillingCallID, bLegID, id string) metering.Observation {
	now := time.Unix(1700000000, 0).UTC()
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-source", Revision: 1,
		StreamID: id + "-stream", Sequence: 1, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-phase7", BillingCallID: callID.String(), BLegID: bLegID},
		Correlation: metering.CorrelationV2{StoreID: "store-phase7", BillingCallID: callID.String(), BLegID: bLegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "provider.phase7",
		Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken}, Value: &value, Quality: metering.QualityObserved}},
	}
}
