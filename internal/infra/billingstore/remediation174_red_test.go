package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// Phase 17.4 remediation RED (F2): optional raw-evidence absence.
//
// Optional privileged raw-transport/capture bytes (traffic.RawCaptureSink)
// are deliberately absent/unsupported in durable accounting: canonical
// metering observations carry bounded SafeEvidenceField lexemes only, and
// recovery identity uses canonical observation refs/payload hashes, never a
// hash of an entire upstream response. Compatible recovery must succeed
// with the raw artifact unavailable while canonical observations/evidence
// identity, balances, journal/adjustment references and pending work remain
// valid.
//
// This test must PASS both before and after the F2 godoc fix (recovery is
// already raw-independent); it is the bounded fixture proving raw loss is
// not queue-row expiry. The queue-retention test
// TestRecovery174RetentionPrunePreservesLinkageAndRecovery stays honestly
// named and is not relabelled as raw evidence.
func TestRemediation174OptionalRawAbsencePreservesRecovery(t *testing.T) {
	t.Parallel()
	store := f3NewStore(t, "rec174-rawabsent")
	ctx := context.Background()
	f3SetupAccount(t, store, "acct-rec174-raw", 100000)
	f3ActivateEmpty(t, store)

	// Optional raw capture is explicitly unavailable on this path.
	var rawSink traffic.RawCaptureSink = traffic.DisabledRawCapture{}
	if err := rawSink.WriteRaw(ctx, traffic.LegBTP, traffic.CaptureMeta{BLegID: "b-raw"}, []byte("upstream-bytes")); err == nil {
		t.Fatalf("optional raw capture must be unavailable (DisabledRawCapture must reject), got nil error")
	}

	// Canonical posted V2 call: provider + customer settle exactly once.
	postedID := f3MustCallID(t)
	posted := testIndependentCallUsageFor(postedID, []string{"b-raw"})
	posted.AccountID = "acct-rec174-raw"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-raw", CallID: postedID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: posted.CustomerPricingRef, ChargePolicyRef: posted.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, posted, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	postedLeg := testIndependentCallLegFor(postedID, "b-raw")
	if err := store.AppendCallLegUsageWithOwner(ctx, postedLeg, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3ProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f3RatingStub{charge: 120, fp: "rec174-raw-fp"}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// Canonical pending V2 call: provider evidence stays pending.
	pendingID := f3MustCallID(t)
	pending := testIndependentCallUsageFor(pendingID, []string{"b-rawp"})
	pending.AccountID = "acct-rec174-raw"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-rec174-raw", CallID: pendingID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: pending.CustomerPricingRef, ChargePolicyRef: pending.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, pending, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	pendingLeg := testIndependentCallLegFor(pendingID, "b-rawp")
	if err := store.AppendCallLegUsageWithOwner(ctx, pendingLeg, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	sealedPending, err := pendingLeg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	pendingState, err := store.GetProviderCostWorkState(ctx, sealedPending.Key)
	if err != nil {
		t.Fatalf("pending provider evidence must exist: %v", err)
	}
	if pendingState.Status != "pending" {
		t.Fatalf("pending provider work status = %q, want pending", pendingState.Status)
	}

	// Canonical observation/evidence identity without raw bodies: a provider
	// observation with allowlisted safe fields has a stable ref/payload hash
	// that never covers raw upstream bytes.
	payer := metering.PaymentParty{Kind: metering.PaymentPartyOperator}
	obs := f2bObservation(store.StoreID(), "acct-rec174-raw", pendingID.String(), "b-rawp", "rec174-raw-obs-1", 1, payer)
	obsRef, err := obs.Ref(store.StoreID())
	if err != nil {
		t.Fatalf("canonical observation ref: %v", err)
	}
	if err := obsRef.Validate(); err != nil {
		t.Fatalf("canonical observation ref must validate without raw bytes: %v", err)
	}
	fingerprint := obs.Fingerprint()
	if fingerprint == "" {
		t.Fatalf("canonical observation fingerprint must be non-empty without raw bytes")
	}

	balanceBefore := f3Balance(t, store, "acct-rec174-raw")
	journalsBefore := f3JournalCount(t, store, "acct-rec174-raw")
	opKey, err := billing.CustomerPostingOperationKey("acct-rec174-raw", postedID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("completed V2 pin must exist: %v", err)
	}
	if !pin.IsCompleted() || pin.Owner != billing.PostingOwnerV2 {
		t.Fatalf("pin must stay V2 completed, got %#v", pin)
	}

	// Compatible recovery succeeds with raw unavailable: snapshot verifies,
	// canonical records survive, pending work drains exactly once.
	snapshot, err := store.GetAccountingRecoverySnapshot(ctx)
	if err != nil {
		t.Fatalf("recovery snapshot with raw absent: %v", err)
	}
	if !snapshot.HasV2MonetaryPosting {
		t.Fatalf("snapshot must report V2 postings with raw absent")
	}
	if _, err := store.VerifyAccountingRecovery(ctx, billing.CurrentAccountingBinaryCapability()); err != nil {
		t.Fatalf("compatible recovery must succeed with raw absent: %v", err)
	}
	if _, err := store.GetCallUsage(ctx, postedID); err != nil {
		t.Fatalf("sealed posted call must survive raw absence: %v", err)
	}
	if got := f3Balance(t, store, "acct-rec174-raw"); got != balanceBefore {
		t.Fatalf("raw absence moved balance %d -> %d", balanceBefore, got)
	}
	if n := f3JournalCount(t, store, "acct-rec174-raw"); n != journalsBefore {
		t.Fatalf("raw absence moved journals %d -> %d", journalsBefore, n)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider drain with raw absent: %v", err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer drain with raw absent: %v", err)
	}
	if got := f3Balance(t, store, "acct-rec174-raw"); got != balanceBefore-120 {
		t.Fatalf("drain with raw absent balance = %d, want %d", got, balanceBefore-120)
	}
}
