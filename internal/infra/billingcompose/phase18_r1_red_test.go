package billingcompose_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Regression for Phase 18 blocker 1 (was RED before the fix, now GREEN):
// production JoinRatingResolver with the legacy-tag scalar tariff
// (PutPricing materialization) and fresh V2 boundary observations must NOT
// post using V1 InputTokens/OutputTokens. It rates from canonical V2
// component quantities (legacy tariff as explicitly mapped component
// material) and carries a component valuation; the V2 owner path is enforced
// via ResolveCallRatingForOwner and the posting boundary.
func TestPhase18R1RedV2OwnerLegacyTariffMustNotUseScalar(t *testing.T) {
	t.Parallel()
	c, pricing, policy, _ := seedCatalog(t)
	resolver, err := billingcompose.NewCallRatingResolver(c)
	if err != nil {
		t.Fatal(err)
	}
	callID := catalogCustomerCallID
	call := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             callID,
		AccountID:          "acct-1",
		ALegID:             "a-1",
		StartedAt:          time.Unix(100, 0).UTC(),
		FinishedAt:         time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: pricing.Ref,
		ChargePolicyRef:    policy.Ref,
		ExpectedBLegIDs:    []string{"b-1"},
	}
	inputKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	now := time.Unix(1_700_000_000, 0).UTC()
	obs := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "v2-boundary", SourceEventKey: "v2-boundary-source", Revision: 1, StreamID: "v2-boundary-stream", Sequence: 1,
		Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalTransport, Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: "a-1", BillingCallID: callID.String(), BLegID: "b-1"},
		Correlation: metering.CorrelationV2{StoreID: "store-1", CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-1", BLegID: "b-1"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "red:v2:v1",
		Measures: []metering.Measure{
			{Key: inputKey, Value: mustDecimal(t, "5"), Quality: metering.QualityObserved},
			{Key: outputKey, Value: mustDecimal(t, "2"), Quality: metering.QualityObserved},
		},
	}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-1", BLegID: "b-1", AttemptSeq: 1,
		BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 1_000_000, Present: true},
			OutputTokens: billing.Quantity{Value: 1_000_000, Present: true},
		},
		EvidenceVersion:    billing.EvidenceFormatVersionV2,
		EvidenceProjection: billing.EvidenceProjectionV1,
		Observations:       []metering.Observation{obs},
	}
	exposure := billing.CallExposure{
		AccountID: "acct-1", CallID: callID.String(),
		Max: billing.Money{Nano: 100000, Currency: "USD"}, Status: billing.ExposureOpen, CreatedAt: time.Unix(1, 0).UTC(),
		PricingRef: pricing.Ref, ChargePolicyRef: policy.Ref,
	}
	result, err := resolver.ResolveCallRating(context.Background(), billing.CompleteCall{Closure: call, Legs: []billing.CallLegUsageRecord{leg}}, exposure)
	if err != nil {
		t.Fatalf("resolver error = %v (want component valuation, not scalar fallback error)", err)
	}
	// Desired: component valuation from V2 quantities (5/2 tokens + fixed 3
	// nanos via legacy-mapped tariff), never the V1 scalar 303 nanos from
	// 1M/1M InputTokens/OutputTokens.
	if result.CustomerValuation.ID == "" {
		t.Fatalf("DEFECT: production resolver posted scalar without component valuation: charge=%+v fingerprint=%q", result.CustomerCharge, result.Fingerprint)
	}
	if result.CustomerCharge.Nano == 303 {
		t.Fatalf("DEFECT: charge 303 equals V1 scalar 1M/1M evidence; must rate from V2 5/2 quantities via component path")
	}
	// Explicit V2 owner path (production worker claim owner) enforces the
	// same component boundary before returning.
	aware, ok := resolver.(billing.OwnerAwareCallRatingResolver)
	if !ok {
		t.Fatalf("production resolver must implement OwnerAwareCallRatingResolver")
	}
	owned, err := aware.ResolveCallRatingForOwner(context.Background(), billing.CompleteCall{Closure: call, Legs: []billing.CallLegUsageRecord{leg}}, exposure, billing.PostingOwnerV2)
	if err != nil {
		t.Fatalf("owner-aware V2 rating: %v", err)
	}
	if owned.CustomerValuation.ID == "" {
		t.Fatalf("owner-aware V2 result must carry component valuation, got %+v", owned)
	}
	if err := billing.ValidateCallRatingResultForOwner(owned, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 posting boundary: %v", err)
	}
}

func mustDecimal(t *testing.T, s string) *metering.Decimal {
	t.Helper()
	d, err := metering.ParseDecimal(s)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}
