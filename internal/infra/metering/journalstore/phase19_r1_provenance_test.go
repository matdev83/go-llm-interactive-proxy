package journalstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

var phase19R1ProvenanceDSN atomic.Int64

func phase19R1ProvenanceStore(t *testing.T) *journalstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:phase19r1-e2e-%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)", phase19R1ProvenanceDSN.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: "store-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func phase19R1Decimal(t *testing.T, value string) *metering.Decimal {
	t.Helper()
	d, err := metering.ParseDecimal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func phase19R1Observation(t *testing.T, callID billing.BillingCallID, bLegID, id, origin string, measures ...metering.Measure) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionProviderResponse
	if origin == metering.OriginLocal {
		acquisition = metering.AcquisitionLocalTransport
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-source", Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: "a-1", BillingCallID: callID.String(), BLegID: bLegID},
		Correlation: metering.CorrelationV2{StoreID: "store-1", CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-1", BLegID: bLegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "phase19r1:retail:v1", Measures: measures,
	}
}

func phase19R1Measure(t *testing.T, key metering.ComponentKey, quantity string) metering.Measure {
	t.Helper()
	return metering.Measure{Key: key, Value: phase19R1Decimal(t, quantity), Quality: metering.QualityObserved}
}

func phase19R1Policy() billing.ChargePolicy {
	return billing.ChargePolicy{
		Ref:                billing.VersionRef{ID: "retail-policy", Version: "v10"},
		PricingRef:         billing.VersionRef{ID: "retail-pricing", Version: "v3"},
		Scope:              billing.ChargeSurfacedTurn,
		IncludeInputTokens: true,
		Retail:             &billing.RetailSelectionPolicy{Mode: billing.RetailSelectionSurfacedWinner, Basis: billing.RetailBasisIndependent},
	}
}

func phase19R1Call(t *testing.T, policy billing.ChargePolicy, callID billing.BillingCallID, legs ...string) billing.CallUsageRecord {
	t.Helper()
	return billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             callID,
		AccountID:          "acct-1",
		ALegID:             "a-1",
		SessionID:          "sess-shared",
		StartedAt:          time.Unix(100, 0).UTC(),
		FinishedAt:         time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: policy.PricingRef,
		ChargePolicyRef:    policy.Ref,
		ExpectedBLegIDs:    append([]string(nil), legs...),
	}
}

func phase19R1Leg(callID billing.BillingCallID, id string, observations ...metering.Observation) billing.CallLegUsageRecord {
	return billing.CallLegUsageRecord{
		CallID:             callID,
		ALegID:             "a-1",
		BLegID:             id,
		BackendID:          "backend-a",
		ProviderID:         "provider-a",
		ModelID:            "model-a",
		AttemptSeq:         1,
		StartedAt:          time.Unix(100, 0).UTC(),
		FinishedAt:         time.Unix(100, 500000000).UTC(),
		Outcome:            billing.LegOutcomeWinner,
		Surfaced:           billing.SurfacedYes,
		Evidence:           billing.FinalBillingEvidence{Source: billing.EvidenceSourceUnavailable, Authority: billing.EvidenceAuthorityUnavailable},
		EvidenceVersion:    billing.EvidenceFormatVersionV2,
		EvidenceProjection: billing.EvidenceProjectionV1,
		Observations:       observations,
	}
}

func phase19R1Tariff(t *testing.T, rules []economics.RatingRule) economics.TariffSnapshot {
	t.Helper()
	tariff, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "retail-pricing", Version: "v3"}, RaterID: "reference"},
		"USD", rules)
	if err != nil {
		t.Fatal(err)
	}
	return tariff
}

func phase19R1ResolveAll(t *testing.T, ctx context.Context, store *journalstore.DurableStore, valuation economics.Valuation, scope string) {
	t.Helper()
	for _, ref := range valuation.InputObservations {
		if _, err := store.GetObservationRef(ctx, ref); err != nil {
			t.Fatalf("%s input ref %+v does not resolve to the persisted journal row: %v", scope, ref, err)
		}
	}
	for _, ref := range valuation.MissingObservations {
		if _, err := store.GetObservationRef(ctx, ref); err != nil {
			t.Fatalf("%s missing ref %+v does not resolve: %v", scope, ref, err)
		}
	}
	for _, line := range valuation.Lines {
		for _, ref := range line.SourceObservationRefs {
			if _, err := store.GetObservationRef(ctx, ref); err != nil {
				t.Fatalf("%s line %q ref %+v does not resolve: %v", scope, line.ID, ref, err)
			}
		}
	}
}

// A. Mixed provider observation (output token) plus local observation (same
// output plus a local-only image): both originals persist, output rates once,
// the image rates once, and every emitted valuation input/line ref resolves
// via journalstore.GetObservationRef to the original immutable journal row.
func TestPhase19R1ProvenanceMixedPartialSelectionResolvesJournalRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	policy := phase19R1Policy()
	call := phase19R1Call(t, policy, callID, "b-winner")
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	imageKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImageToken, Unit: metering.UnitToken, SchemaID: "openai.usage.v2"}
	providerObs := phase19R1Observation(t, callID, "b-winner", "provider-output", metering.OriginProvider,
		phase19R1Measure(t, outputKey, "200"))
	localObs := phase19R1Observation(t, callID, "b-winner", "local-output-image", metering.OriginLocal,
		phase19R1Measure(t, outputKey, "200"),
		phase19R1Measure(t, imageKey, "2"))
	leg := phase19R1Leg(callID, "b-winner", providerObs, localObs)

	store := phase19R1ProvenanceStore(t)
	if err := store.AppendObservation(ctx, providerObs); err != nil {
		t.Fatalf("append provider original: %v", err)
	}
	if err := store.AppendObservation(ctx, localObs); err != nil {
		t.Fatalf("append local original: %v", err)
	}

	selection, err := billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{Call: call, Legs: []billing.CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.ObservationRefs) != 2 {
		t.Fatalf("selection refs = %d, want both originals frozen for audit", len(selection.ObservationRefs))
	}
	tariff := phase19R1Tariff(t, []economics.RatingRule{
		{ID: "output-token", Kind: economics.RatingRuleLinear, Component: &outputKey, Currency: "USD", UnitPrice: phase19R1Decimal(t, "0.03")},
		{ID: "image-input", Kind: economics.RatingRuleLinear, Component: &imageKey, Currency: "USD", UnitPrice: phase19R1Decimal(t, "0.05")},
	})
	result, err := billing.RateSelectedRetailBLegs(ctx, billing.RetailRatingInput{
		Call: call, Legs: []billing.CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if err != nil {
		t.Fatalf("RateSelectedRetailBLegs: %v", err)
	}
	// Provider output 200*0.03 = 6.00 once plus local-only image 2*0.05 = 0.10.
	if got, want := result.CustomerCharge.Nano, int64(6_100_000_000); got != want {
		t.Fatalf("R customer charge = %d nano, want %d (single output basis plus additive image)", got, want)
	}
	if len(result.InferenceValuation.Lines) != 2 {
		t.Fatalf("inference lines = %d, want output once plus image once: %+v", len(result.InferenceValuation.Lines), result.InferenceValuation.Lines)
	}

	phase19R1ResolveAll(t, ctx, store, result.InferenceValuation, "inference")
	phase19R1ResolveAll(t, ctx, store, result.Valuation, "composite")

	// The local line must carry the original full two-measure observation ref,
	// not a rewritten one-measure payload under the same identity.
	for _, line := range result.InferenceValuation.Lines {
		for _, ref := range line.SourceObservationRefs {
			if ref.ObservationID != "local-output-image" {
				continue
			}
			stored, err := store.GetObservationRef(ctx, ref)
			if err != nil {
				t.Fatalf("local line ref: %v", err)
			}
			if len(stored.Measures) != 2 {
				t.Fatalf("local line resolves to %d measures, want original full 2-measure observation", len(stored.Measures))
			}
		}
	}
	if len(leg.Observations[1].Measures) != 2 {
		t.Fatalf("rating mutated local observation measures: %+v", leg.Observations[1].Measures)
	}
}

// B. Linked correction/supersedes variant: a local delta correction of the
// image field still rates once per component, every emitted ref resolves, and
// the correction lineage still names the original predecessor hash.
func TestPhase19R1ProvenanceCorrectionLineageResolvesJournalRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	policy := phase19R1Policy()
	call := phase19R1Call(t, policy, callID, "b-winner")
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	imageKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImageToken, Unit: metering.UnitToken, SchemaID: "openai.usage.v2"}
	providerObs := phase19R1Observation(t, callID, "b-winner", "provider-output", metering.OriginProvider,
		phase19R1Measure(t, outputKey, "200"))
	localBase := phase19R1Observation(t, callID, "b-winner", "local-base", metering.OriginLocal,
		phase19R1Measure(t, outputKey, "200"),
		phase19R1Measure(t, imageKey, "1"))
	baseRef, err := localBase.Ref(localBase.Subject.StoreID)
	if err != nil {
		t.Fatalf("local base ref: %v", err)
	}
	correction := localBase.Clone()
	correction.ID = "local-correction"
	correction.SourceEventKey = "local-correction-source"
	correction.Sequence = localBase.Sequence + 1
	correction.Semantics = metering.SemanticsCorrection
	correction.Measures = []metering.Measure{phase19R1Measure(t, imageKey, "1")}
	correction.Supersedes = []metering.ObservationRef{baseRef}
	leg := phase19R1Leg(callID, "b-winner", providerObs, localBase, correction)

	store := phase19R1ProvenanceStore(t)
	for _, observation := range []metering.Observation{providerObs, localBase, correction} {
		if err := store.AppendObservation(ctx, observation); err != nil {
			t.Fatalf("append %q: %v", observation.ID, err)
		}
	}

	selection, err := billing.SelectRetailBLegEvidence(billing.RetailSelectionInput{Call: call, Legs: []billing.CallLegUsageRecord{leg}, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.ObservationRefs) != 3 {
		t.Fatalf("selection refs = %d, want all three originals frozen for audit", len(selection.ObservationRefs))
	}
	tariff := phase19R1Tariff(t, []economics.RatingRule{
		{ID: "output-token", Kind: economics.RatingRuleLinear, Component: &outputKey, Currency: "USD", UnitPrice: phase19R1Decimal(t, "0.03")},
		{ID: "image-input", Kind: economics.RatingRuleLinear, Component: &imageKey, Currency: "USD", UnitPrice: phase19R1Decimal(t, "0.05")},
	})
	result, err := billing.RateSelectedRetailBLegs(ctx, billing.RetailRatingInput{
		Call: call, Legs: []billing.CallLegUsageRecord{leg}, Selection: selection, Policy: policy,
		Tariff: tariff, Payer: metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
	})
	if err != nil {
		t.Fatalf("RateSelectedRetailBLegs: %v", err)
	}
	// Provider output 200*0.03 = 6.00 once; local image 1+1 = 2 at 0.05 = 0.10.
	if got, want := result.CustomerCharge.Nano, int64(6_100_000_000); got != want {
		t.Fatalf("R customer charge = %d nano, want %d (single output basis plus corrected image)", got, want)
	}

	phase19R1ResolveAll(t, ctx, store, result.InferenceValuation, "inference")
	phase19R1ResolveAll(t, ctx, store, result.Valuation, "composite")

	storedCorrection, err := store.GetObservation(ctx, correction.ID, correction.Revision)
	if err != nil {
		t.Fatalf("journal correction: %v", err)
	}
	if len(storedCorrection.Supersedes) != 1 || !storedCorrection.Supersedes[0].Equal(baseRef) {
		t.Fatalf("correction lineage = %+v, want original base ref %+v", storedCorrection.Supersedes, baseRef)
	}
	if len(leg.Observations[2].Supersedes) != 1 || !leg.Observations[2].Supersedes[0].Equal(baseRef) {
		t.Fatalf("rating mutated correction lineage: %+v", leg.Observations[2].Supersedes)
	}
	if len(leg.Observations[1].Measures) != 2 {
		t.Fatalf("rating mutated local base measures: %+v", leg.Observations[1].Measures)
	}
}
