package billing

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func f2bIntentObservation(storeID, accountID, callID, bLegID string) metering.Observation {
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: accountID,
		ALegID: "a-f2b", BillingCallID: callID, BLegID: bLegID,
	}
	amount := metering.DecimalFromNanoUnits(10)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "f2b-intent-obs", SourceEventKey: "f2b-intent-obs", Revision: 1,
		StreamID: "s", Sequence: 1, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: storeID, ALegID: "a-f2b", BillingCallID: callID, BLegID: bLegID},
		Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
		MappingRef: "f2b.intent",
		Charges:    []metering.ReportedCharge{{ChargeItemID: "c1", Kind: metering.ChargeKindAggregate, Amount: &amount, Currency: "USD", Payer: payer}},
	}
}

func f2bIntentWork(t *testing.T, queue EconomicQueue, kind EconomicWorkKind, storeID, accountID, callID, bLegID string) EconomicRevisionWork {
	t.Helper()
	obs := f2bIntentObservation(storeID, accountID, callID, bLegID)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: obs.Subject, Scope: "b_leg", Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator},
		Observations: []metering.Observation{obs},
	}
	work := EconomicRevisionWork{
		Queue: queue, Kind: kind, HeadKey: "f2b-head", Subject: obs.Subject,
		EvidenceRevision: 1, Input: input, CreatedAt: time.Unix(200, 0).UTC(),
	}
	normalized, err := work.Normalize()
	if err != nil {
		t.Fatalf("intent work normalize: %v", err)
	}
	return normalized
}

func TestF2BIsMonetaryRequiresProviderRatingLineage(t *testing.T) {
	t.Parallel()
	callID, err := NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	provider := f2bIntentWork(t, EconomicQueueProvider, EconomicWorkKindProviderRating, "s-f2b", "acct-f2b", callID.String(), "b-f2b")
	if !IsMonetaryEconomicRevisionWork(provider) {
		t.Fatalf("provider_rating provider work must be monetary")
	}
	if got := EffectiveEconomicPostingOwner(provider); got != PostingOwnerV1 {
		t.Fatalf("legacy effective owner = %q, want v1", got)
	}
	v2 := provider
	v2.PostingOwner = PostingOwnerV2
	if !IsMonetaryEconomicRevisionWork(v2) {
		t.Fatalf("explicit V2 provider work must remain monetary")
	}
	if got := EffectiveEconomicPostingOwner(v2); got != PostingOwnerV2 {
		t.Fatalf("V2 effective owner = %q, want v2", got)
	}
	evidence := provider
	evidence.EvidenceOnly = true
	if IsMonetaryEconomicRevisionWork(evidence) {
		t.Fatalf("explicit evidence-only provider work must not be monetary")
	}
	if got := EffectiveEconomicPostingOwner(evidence); got != "" {
		t.Fatalf("evidence effective owner = %q, want empty", got)
	}
	customerObs := f2bIntentObservation("s-f2b", "acct-f2b", callID.String(), "b-f2b")
	customer := EconomicRevisionWork{
		Queue: EconomicQueueCustomer, Kind: EconomicWorkKindCustomerRating,
		HeadKey: "f2b-head", Subject: customerObs.Subject, EvidenceRevision: 1,
		Input: economics.PostUsageRatingInput{
			Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
			Subject: customerObs.Subject, Scope: "b_leg",
			Payer:        metering.PaymentParty{Kind: metering.PaymentPartyOperator},
			Observations: []metering.Observation{customerObs},
		},
		CreatedAt: time.Unix(200, 0).UTC(),
	}
	if IsMonetaryEconomicRevisionWork(customer) {
		t.Fatalf("customer rating must never be monetary")
	}
	recon := provider
	recon.Kind = EconomicWorkKindReconciliation
	// Reconciliation requires dependencies; construct minimal valid recon work
	// via helper is complex here, so assert kind gate directly: a provider-queue
	// reconciliation envelope is evidence-only by kind.
	recon.Dependencies = nil // Normalize would fail without deps; check helper pre-normalize shape
	if IsMonetaryEconomicRevisionWork(recon) {
		// recon Kind is reconciliation, queue provider -> helper must be false
		t.Fatalf("provider-queue reconciliation must never be monetary")
	}
	if _, _, _, err := MonetaryEconomicPostingKey("s-f2b", customer); err == nil {
		t.Fatalf("evidence-only posting key derivation must fail closed")
	}
}
