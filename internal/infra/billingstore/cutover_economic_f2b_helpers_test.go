package billingstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 17.3 F2B GREEN: monetary economic revision inventory/fence.
//
// Covers the second F2 counterexample (payable provider revision queued/leased
// before its first head/pin) without blocking evidence-only jobs:
// - Pending + leased monetary provider work classified/pinned with canonical
//   provider identity, counted, claimable with metadata, completable via
//   production EconomicRevisionWorker (no direct SQL completion).
// - Nonpayable exclusion (BYOK/customer payer) still pins/completes.
// - Evidence-only customer rating, reconciliation, shadow, and no-adapter
//   queues never classified/counted/fenced and remain operable.
// - New V1 monetary after drain fenced (F1 lock); V2 active allows V2 only via
//   explicit V2 auth/owner; old V1 leases wake fenced; stale epoch fenced with
//   renewable fresh metadata.
// - Restart/reopen durable; bounded multi-batch classification.

func f2bNewStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	base := newSQLiteTestStore(t)
	if base.StoreID() == storeID {
		return base
	}
	s, err := NewDurableStore(context.Background(), base.DB(), Config{StoreID: storeID})
	if err != nil {
		t.Fatalf("NewDurableStore %q: %v", storeID, err)
	}
	return s
}

func f2bSetupShadowAccount(t *testing.T, store *DurableStore, accountID string) context.Context {
	t.Helper()
	ctx := context.Background()
	acct := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f2b-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}

func f2bMustCallID(t *testing.T) billing.BillingCallID {
	t.Helper()
	id, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func f2bIsFenceErr(err error) bool {
	return errors.Is(err, billing.ErrPostingOwnershipFence) ||
		errors.Is(err, billing.ErrPostingOwnershipConflict) ||
		errors.Is(err, billing.ErrAccountingCutoverFence) ||
		errors.Is(err, billing.ErrCutoverV1Fenced) ||
		errors.Is(err, billing.ErrCutoverV2NotAuthorized) ||
		errors.Is(err, ErrOperationConflict)
}

func f2bObservation(storeID, accountID, callID, bLegID, obsID string, revision uint64, payer metering.PaymentParty) metering.Observation {
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, AccountID: accountID,
		ALegID: "a-f2b", BillingCallID: callID, BLegID: bLegID,
	}
	amount := metering.DecimalFromNanoUnits(200)
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: obsID, SourceEventKey: obsID, Revision: revision,
		StreamID: "f2b-stream", Sequence: revision, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: storeID, ALegID: "a-f2b", BillingCallID: callID, BLegID: bLegID},
		Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
		MappingRef: "f2b.provider.cost",
		Charges:    []metering.ReportedCharge{{ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate, Amount: &amount, Currency: "USD", Payer: payer}},
	}
}

func f2bProviderWork(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID, headKey string, revision uint64, payable bool) billing.EconomicRevisionWork {
	t.Helper()
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	if !payable {
		payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	}
	obs := f2bObservation(store.StoreID(), accountID, callID.String(), bLegID, fmt.Sprintf("f2b-obs-%s-%d", bLegID, revision), revision, payer)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: obs.Subject, Scope: "b_leg", Payer: payer, Observations: []metering.Observation{obs},
	}
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: headKey, Subject: obs.Subject,
		EvidenceRevision: revision, Input: input, CreatedAt: time.Unix(200+int64(revision), 0).UTC(),
	}
	normalized, err := work.Normalize()
	if err != nil {
		t.Fatalf("provider work normalize: %v", err)
	}
	return normalized
}

func f2bCustomerWork(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID, headKey string, revision uint64) billing.EconomicRevisionWork {
	t.Helper()
	// Evidence-only: customer queue never posts provider money, even with
	// provider-basis input (Kind gate excludes it).
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	obs := f2bObservation(store.StoreID(), accountID, callID.String(), bLegID, fmt.Sprintf("f2b-cust-obs-%s-%d", bLegID, revision), revision, payer)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: obs.Subject, Scope: "b_leg", Payer: payer, Observations: []metering.Observation{obs},
	}
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueCustomer, Kind: billing.EconomicWorkKindCustomerRating,
		HeadKey: headKey, Subject: obs.Subject,
		EvidenceRevision: revision, Input: input, CreatedAt: time.Unix(210+int64(revision), 0).UTC(),
	}
	normalized, err := work.Normalize()
	if err != nil {
		t.Fatalf("customer work normalize: %v", err)
	}
	return normalized
}

func f2bReconciliationWork(t *testing.T, rating billing.EconomicRevisionWork) billing.EconomicRevisionWork {
	t.Helper()
	normalized, err := rating.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := normalized.Identity()
	if err != nil {
		t.Fatal(err)
	}
	dep, err := billing.NewEconomicJobDependency(billing.EconomicWorkKindProviderRating, identity)
	if err != nil {
		t.Fatal(err)
	}
	// Reconciliation on provider queue is evidence-only by Kind.
	obs := normalized.Input.Observations[0].Clone()
	obs.ID = obs.ID + "-recon"
	obs.SourceEventKey = obs.ID
	input := normalized.Input.Clone()
	input.Observations = []metering.Observation{obs}
	input.InputSetHash = ""
	input.ObservationRefs = nil
	work := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, Kind: billing.EconomicWorkKindReconciliation,
		HeadKey: normalized.HeadKey + "-recon", Subject: normalized.Subject,
		EvidenceRevision: normalized.EvidenceRevision, Input: input,
		Dependencies: []billing.EconomicJobDependency{dep}, CreatedAt: time.Unix(220, 0).UTC(),
	}
	out, err := work.Normalize()
	if err != nil {
		t.Fatalf("reconciliation work normalize: %v", err)
	}
	return out
}

type f2bRater struct{}

func (f2bRater) Rate(_ context.Context, in economics.PostUsageRatingInput) (economics.Valuation, error) {
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

func f2bProviderWorker(t *testing.T, store *DurableStore) *billing.EconomicRevisionWorker {
	t.Helper()
	w, err := billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithClaim(
		store, store, f2bRater{}, nil, store, store, billing.EconomicQueueProvider, 8)
	if err != nil {
		t.Fatalf("provider worker compose: %v", err)
	}
	return w
}

func f2bPureProviderWorker(t *testing.T, store *DurableStore) *billing.EconomicRevisionWorker {
	t.Helper()
	w, err := billing.NewEconomicRevisionWorkerWithReconciler(
		store, store, f2bRater{}, nil, billing.EconomicQueueProvider, 8)
	if err != nil {
		t.Fatalf("pure worker compose: %v", err)
	}
	return w
}

func f2bProviderJournals(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	txs, err := store.JournalTransactions(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tx := range txs {
		if tx.OperationKind == "provider_call_cogs" {
			n++
		}
	}
	return n
}

func f2bRevisionInputFor(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, bLegID, headKey string, revision uint64, amount int64, payable bool) billing.ProviderCostRevisionInput {
	t.Helper()
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: store.StoreID(), AccountID: accountID,
		ALegID: "a-f2b", BillingCallID: callID.String(), BLegID: bLegID,
	}
	// Align subject BLeg with economic work under test when headKey matches the
	// f2bProviderWork B-leg convention; callers pass explicit B-leg via headKey
	// mapping below by overriding subject when needed.
	_ = headKey
	amountDecimal := metering.DecimalFromNanoUnits(amount)
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	if !payable {
		payer = metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: "customer"}
	}
	evidence := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: subject, Scope: "b2b2-provider-cost", Payer: payer,
		Observations: []metering.Observation{{
			Version: metering.ObservationVersionV2, ID: "f2b-rev-obs", SourceEventKey: "f2b-rev-obs", Revision: revision,
			StreamID: "f2b-rev-stream", Sequence: revision, Origin: metering.OriginProvider,
			Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
			Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
			Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
			Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID},
			Semantics:   metering.SemanticsCumulative, ObservedAt: time.Unix(100, 0).UTC(), ReceivedAt: time.Unix(100, 0).UTC(),
			MappingRef: "f2b.rev.cost",
			Charges:    []metering.ReportedCharge{{ChargeItemID: "provider-charge", Kind: metering.ChargeKindAggregate, Amount: &amountDecimal, Currency: "USD", Payer: payer}},
		}},
		Rater: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "f2b-rater", Version: "v1"}, RaterID: "reference"},
	}
	cost := billing.OperatorCOGSResult{
		KnownSubtotalByCurrency: map[string]billing.Money{"USD": {Nano: amount, Currency: "USD"}},
		KnownSubtotal:           billing.Money{Nano: amount, Currency: "USD"},
		Completeness:            billing.CostCompletenessKnown,
		Payable:                 payable,
		IncludedLegKeys:         []string{bLegID},
	}
	if !payable {
		cost.IncludedLegKeys = nil
	}
	return billing.ProviderCostRevisionInput{
		AccountID: accountID, CallID: callID, Subject: subject, HeadKey: headKey,
		EvidenceRevision: revision, InputSetHash: "0000000000000000000000000000000000000000000000000000000000000001",
		ValuationID: "f2b-rev-valuation", Cost: cost, Authoritative: payable, Evidence: evidence,
	}
}
