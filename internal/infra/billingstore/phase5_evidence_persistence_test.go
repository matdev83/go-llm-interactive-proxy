package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestSQLiteAppendCallLegUsagePersistsV2EvidenceAndKeepsReplayStable(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	record := testIndependentCallLegFor(callID, "b-v2-persist")
	now := time.Unix(200, 0).UTC()
	value := metering.Decimal{Coefficient: "11"}
	record.EvidenceVersion = billing.EvidenceFormatVersionV2
	record.EvidenceProjection = billing.EvidenceProjectionV1
	record.Observations = []metering.Observation{{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-v2-persist",
		SourceEventKey: "source-v2-persist",
		Revision:       1,
		StreamID:       "b-v2-persist:stream",
		Sequence:       1,
		Origin:         metering.OriginProvider,
		Acquisition:    metering.AcquisitionProviderResponse,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: record.ALegID,
			BillingCallID: callID.String(), BLegID: record.BLegID, AttemptSeq: uint64(record.AttemptSeq),
		},
		Correlation: metering.CorrelationV2{
			StoreID: "store-1", CallID: callID.String(), BillingCallID: callID.String(),
			ALegID: record.ALegID, BLegID: record.BLegID, AttemptSeq: uint64(record.AttemptSeq),
		},
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now,
		MappingRef: "phase5:persistence:v1",
		Measures: []metering.Measure{{
			Key: metering.ComponentKey{
				Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
				Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
			},
			Value: &value, Quality: metering.QualityObserved, MethodRef: "phase5:persistence:v1",
		}},
	}}

	if err := store.AppendCallLegUsage(ctx, record); err != nil {
		t.Fatalf("AppendCallLegUsage: %v", err)
	}
	// The existing append transaction seals and fingerprints the additive JSON
	// payload; a byte-for-byte replay remains idempotent without new SQL columns.
	if err := store.AppendCallLegUsage(ctx, record); err != nil {
		t.Fatalf("replay AppendCallLegUsage: %v", err)
	}
	key := mustCallLegKey(t, callID, record.BLegID)
	got, err := store.GetCallLegUsage(ctx, key)
	if err != nil {
		t.Fatalf("GetCallLegUsage: %v", err)
	}
	if got.EvidenceVersion != billing.EvidenceFormatVersionV2 || got.EvidenceProjection != billing.EvidenceProjectionV1 {
		t.Fatalf("restored evidence envelope = version %d projection %q", got.EvidenceVersion, got.EvidenceProjection)
	}
	if len(got.Observations) != 1 || got.Observations[0].SourceEventKey != "source-v2-persist" {
		t.Fatalf("restored observations = %#v, want one source observation", got.Observations)
	}
	if got.Observations[0].Subject.BLegID != record.BLegID || got.Observations[0].Correlation.BillingCallID != callID.String() {
		t.Fatalf("restored observation lineage = subject=%+v correlation=%+v", got.Observations[0].Subject, got.Observations[0].Correlation)
	}
}
