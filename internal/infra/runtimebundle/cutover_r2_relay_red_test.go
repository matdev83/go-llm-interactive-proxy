package runtimebundle

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 17.3 R2 RED A: fresh V2 admission -> production observation relay ->
// economic worker posts once as V2.
//
// The relay must select/preserve the admitted durable owner for the call/work.
// Hardcoding PostingOwnerV1 fences fresh V2 observations after activation.
func TestR2RelayFreshV2PostsOnceAsV2(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeID := "r2-relay-a"
	meteringStore := newBridgeMeteringStore(t, storeID)
	billingStore := newBridgeBillingStore(t, storeID)
	accountID := "acct-r2-relay-a"
	if err := billingStore.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	m, err := billingStore.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := billingStore.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "r2-relay-shadow",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := billingStore.BeginCutoverDraining(ctx, "r2-relay-drain"); err != nil {
		t.Fatal(err)
	}
	if _, err := billingStore.ActivateCutoverV2(ctx, "r2-relay-activate"); err != nil {
		t.Fatalf("legal empty activation: %v", err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	// Admit the fresh V2 call through the durable V2 path so a call-scoped
	// V2 owner exists before any observation arrives.
	if _, err := billingStore.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:             billing.Money{Nano: 50000, Currency: "USD"},
		PricingRef:      billing.VersionRef{ID: "pricing", Version: "v1"},
		ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	}, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 admission: %v", err)
	}
	observation := r2RelayObservation(t, storeID, accountID, callID.String(), "b-r2-relay")
	sink := journalstore.NewObservationSinkWithOutbox(meteringStore)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	if !ok {
		t.Fatalf("metering sink must be atomic")
	}
	if err := atomicSink.AppendObservations(ctx, []metering.Observation{observation}); err != nil {
		t.Fatal(err)
	}
	builder, err := billing.NewObservationEconomicWorkBuilder(billing.ObservationEconomicWorkBuilderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	relay := newObservationEconomicRelay(meteringStore, billingStore, builder)
	if err := relay.ProcessOnce(ctx); err != nil {
		t.Fatalf("RED R2-A: production relay for fresh V2 err = %v (V1 hardcode fences V2)", err)
	}
	pending, err := billingStore.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("RED R2-A: pending provider = %d, want 1", len(pending))
	}
	if pending[0].PostingOwner != billing.PostingOwnerV2 {
		t.Fatalf("RED R2-A: relayed owner = %q, want v2 (admitted owner preserved)", pending[0].PostingOwner)
	}
	if pending[0].EvidenceOnly {
		t.Fatalf("RED R2-A: relayed work must be monetary, not evidence-only")
	}
	worker, err := billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithClaim(
		billingStore, billingStore, r2RelayRater{}, nil, billingStore, billingStore, billing.EconomicQueueProvider, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("economic worker: %v", err)
	}
	txs, err := billingStore.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tx := range txs {
		if tx.OperationKind == "provider_call_cogs" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("RED R2-A: provider journals = %d, want 1 (V2 posted once)", n)
	}
}

func r2RelayObservation(t *testing.T, storeID, accountID, callID, bLegID string) metering.Observation {
	t.Helper()
	now := time.Unix(1_700_002_000, 0).UTC()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: accountID,
		ALegID: "a-r2-relay", BillingCallID: callID, BLegID: bLegID,
	}
	amount := metering.DecimalFromNanoUnits(200)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	obs := metering.Observation{
		Version: metering.ObservationVersionV2, ID: "r2-relay-obs-1", SourceEventKey: "r2-relay-obs-1", Revision: 1,
		StreamID: "r2-relay-stream", Sequence: 1, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: storeID, ALegID: "a-r2-relay", BillingCallID: callID, BLegID: bLegID},
		Semantics:   metering.SemanticsCumulative, ObservedAt: now, ReceivedAt: now,
		MappingRef: "r2.relay.cost",
		Charges:    []metering.ReportedCharge{{ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate, Amount: &amount, Currency: "USD", Payer: payer}},
	}
	if err := obs.Validate(); err != nil {
		t.Fatalf("relay observation validate: %v", err)
	}
	return obs
}

type r2RelayRater struct{}

func (r2RelayRater) Rate(_ context.Context, in economics.PostUsageRatingInput) (economics.Valuation, error) {
	refs := append([]metering.ObservationRef(nil), in.ObservationRefs...)
	if len(refs) == 0 {
		for _, observation := range in.Observations {
			ref, err := observation.Ref(in.Subject.StoreID)
			if err != nil {
				return economics.Valuation{}, err
			}
			refs = append(refs, ref)
		}
	}
	return economics.Valuation{
		Version: economics.ValuationVersionV2, Perspective: in.Perspective, Basis: in.Basis,
		Subject: in.Subject, Scope: in.Scope, InputObservations: refs,
		Completeness: economics.CompletenessPartial, CreatedAt: time.Unix(300, 0).UTC(),
	}, nil
}
