package billingstore

// 5B downstream settlement proof (PR #666 review, TEST-ONLY).
//
// The in-core rating review (billing_review5be_test.go) proves the component
// rating seams classify 5B contradictions/overlaps. This file proves the
// downstream consequence that matters for money: the actual CallRatingResult
// produced by the production RateCall seam for those cases can never reach a
// customer monetary effect under the legal V2 lifecycle. The valid
// same-schema disjoint control, rated by the same seam, settles exactly once
// under V2 ownership and replays without a second posting.
//
// The component graphs and quantities mirror the in-core 5B vectors; the
// expected values come from that handoff arithmetic, not from production
// helper booleans. The store seam is handed the actual RateCall outcome; for
// the adversarial rejection the only change is normalizing CustomerCharge to a
// valid USD zero so the fence cannot short-circuit on a malformed empty
// currency before it examines the unchanged noncomplete CustomerValuation.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

const (
	// review5beSchemaID matches ref83intKey's frozen component schema so the
	// reused ref83intObservation / ref83intLeg / ref83intCall fixtures and the
	// custom component graph share one schema identity.
	review5beSchemaID = "refinement83.cert.v1"
	// review5beBalanceNano funds every case (5000 USD) so a genuinely valid
	// 100 USD charge can post.
	review5beBalanceNano int64 = 5_000_000_000_000
	// review5beMaxNano bounds the admitted exposure (1000 USD).
	review5beMaxNano int64 = 1_000_000_000_000
	// review5beValidChargeNano is the disjoint X30 + Y30 + C40 control at USD1.
	review5beValidChargeNano int64 = 100_000_000_000
)

func review5beKey(component string) metering.ComponentKey {
	return ref83intKey(metering.DirectionInput, component, metering.UnitToken)
}

func review5beLinearRule(t *testing.T, id string, key metering.ComponentKey, price string) economics.RatingRule {
	t.Helper()
	return economics.RatingRule{ID: id, Component: &key, Currency: "USD", UnitPrice: ref83intDecimal(t, price)}
}

// review5beTariff freezes the exact component graph against the same pricing
// reference ref83intWinnerPolicy/ref83intCall bind, with no fees.
func review5beTariff(t *testing.T, rules []economics.RatingRule, schemas []metering.ComponentSchema) economics.TariffSnapshot {
	t.Helper()
	tariff, err := economics.BuildTariffSnapshotWithSchemas(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "retail-pricing-83", Version: "v3"}, RaterID: "reference"},
		"USD", rules, schemas,
	)
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas: %v", err)
	}
	return tariff
}

func review5beMeasure(key metering.ComponentKey, quantity string) struct {
	key      metering.ComponentKey
	quantity string
} {
	return struct {
		key      metering.ComponentKey
		quantity string
	}{key: key, quantity: quantity}
}

// review5beAdversarialCharge returns the actual RateCall outcome with only the
// customer charge normalized to a valid USD zero. The noncomplete/conflict
// CustomerValuation, CallID, Fingerprint and RouteTariffs are left untouched,
// so the settlement fence must reject on the valuation completeness rather
// than on a malformed empty-currency charge.
func review5beAdversarialCharge(rated billing.CallRatingResult) billing.CallRatingResult {
	clone := rated
	clone.CustomerCharge = billing.Money{Nano: 0, Currency: "USD"}
	return clone
}

// review5beRequireCompletenessFence proves the error is the typed incomplete
// fence whose diagnostic is the valuation-completeness classification.
func review5beRequireCompletenessFence(t *testing.T, err error, wantCompleteness string) {
	t.Helper()
	if !errors.Is(err, billing.ErrRetailRateIncomplete) {
		t.Fatalf("fence err=%v, want ErrRetailRateIncomplete", err)
	}
	if !strings.Contains(err.Error(), "completeness") || !strings.Contains(err.Error(), wantCompleteness) {
		t.Fatalf("fence err=%v, want completeness diagnostic mentioning %q", err, wantCompleteness)
	}
}

// review5beSetup creates the file-backed store, performs the legal empty V2
// activation (Ensure -> shadow -> drain -> v2_active) before creating the
// account, admits the exposure with explicit V2 ownership, rates through the
// production RateCall seam, and appends the durable closure/leg under V2
// ownership. It returns the exact rating outcome; RateCall's error and its
// possibly noncomplete CustomerValuation are never replaced with a fabricated
// complete result.
func review5beSetup(t *testing.T, accountID, bLegID string, tariff economics.TariffSnapshot, measures ...struct {
	key      metering.ComponentKey
	quantity string
},
) (*DurableStore, billing.CallUsageRecord, billing.CallExposure, billing.CallRatingResult, error) {
	t.Helper()
	ctx := context.Background()
	store, _, closeStore := ref83intStore(t)
	t.Cleanup(closeStore)
	// Legal empty activation before any account/exposure/usage exists.
	f3ActivateEmpty(t, store)
	if err := store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: review5beBalanceNano, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	policy := ref83intWinnerPolicy()
	call := ref83intCall(t, callID, accountID, policy, bLegID)
	obs := ref83intObservation(t, callID, bLegID, bLegID+"-obs", "0", measures...)
	leg := ref83intLeg(t, callID, bLegID, 1, billing.LegOutcomeWinner, billing.SurfacedYes, &obs)
	rated, rateErr := billing.RateCall(billing.CallRatingInput{
		Call: call, Legs: []billing.CallLegUsageRecord{leg},
		MaxCustomerCharge: billing.Money{Nano: review5beMaxNano, Currency: "USD"},
		CustomerPricing:   billing.PricingSnapshot{Ref: policy.PricingRef, Currency: "USD"},
		CustomerPolicy:    policy, CustomerTariff: tariff,
	})
	exposure, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:        billing.Money{Nano: review5beMaxNano, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
		RouteTariffs: rated.RouteTariffs,
	}, billing.PostingOwnerV2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, call, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, leg, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	return store, call, exposure, rated, rateErr
}

// review5beSnapshot records the full durable money, exposure and pin state so
// equality proves zero effect. Pin presence is tracked separately from pin
// content so an absent pin can never be confused with a newly created
// incomplete one.
type review5beSnapshot struct {
	balanceNano    int64
	accountVersion uint64
	journals       int
	exposure       billing.CallExposure
	pinPresent     bool
	pin            billing.PostingPin
}

func review5beSnapshotOf(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID) review5beSnapshot {
	t.Helper()
	ctx := context.Background()
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	exposure, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := review5beSnapshot{
		balanceNano:    account.BalanceNano,
		accountVersion: account.Version,
		journals:       ref83intJournalCount(t, ctx, store, accountID, "customer_call_settlement"),
		exposure:       exposure,
	}
	pin, pinErr := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	switch {
	case pinErr == nil:
		snapshot.pinPresent = true
		snapshot.pin = pin
	case errors.Is(pinErr, billing.ErrPostingOwnershipNotFound):
	default:
		t.Fatalf("GetPostingPin: %v", pinErr)
	}
	return snapshot
}

func review5beSameSnapshot(a, b review5beSnapshot) bool {
	return a.balanceNano == b.balanceNano &&
		a.accountVersion == b.accountVersion &&
		a.journals == b.journals &&
		a.pinPresent == b.pinPresent &&
		reflect.DeepEqual(a.exposure, b.exposure) &&
		reflect.DeepEqual(a.pin, b.pin)
}

// TestReview5beSettlementFence proves that the actual 5B rating outcomes cannot
// post customer money under V2 ownership while the valid disjoint control
// settles once and replays without double posting.
func TestReview5beSettlementFence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("contradicted_partition_cannot_settle", func(t *testing.T) {
		t.Parallel()
		parent := review5beKey("vendor:review5be_contradicted_total")
		childB := review5beKey("vendor:review5be_contradicted_b")
		childC := review5beKey("vendor:review5be_contradicted_c")
		tariff := review5beTariff(t,
			[]economics.RatingRule{review5beLinearRule(t, "review5be-contradicted-a", parent, "1")},
			[]metering.ComponentSchema{{
				ID: review5beSchemaID, Version: "1",
				Relationships: []metering.ComponentRelationship{
					{Kind: metering.RelationshipPartition, Parent: parent, Child: childB},
					{Kind: metering.RelationshipPartition, Parent: parent, Child: childC},
				},
			}})
		store, call, exposure, rated, rateErr := review5beSetup(t, "review5be-contradicted", "b-contradicted", tariff,
			review5beMeasure(parent, "100"), review5beMeasure(childB, "60"), review5beMeasure(childC, "60"))

		if !errors.Is(rateErr, billing.ErrSchemaPartitionContradiction) {
			t.Fatalf("RateCall err=%v, want ErrSchemaPartitionContradiction; completeness=%q valuation=%+v",
				rateErr, rated.CustomerValuation.Completeness, rated.CustomerValuation)
		}
		if rated.CustomerValuation.Completeness != economics.CompletenessPartial {
			t.Fatalf("contradicted partition completeness=%q, want partial: %+v",
				rated.CustomerValuation.Completeness, rated.CustomerValuation)
		}

		adversarial := review5beAdversarialCharge(rated)
		review5beRequireCompletenessFence(t, billing.ValidateCallRatingResultForSettlement(
			adversarial, call, exposure, billing.PostingOwnerV2,
		), string(economics.CompletenessPartial))

		before := review5beSnapshotOf(t, store, call.AccountID, call.CallID)
		if !before.exposure.IsOpen() || !before.pinPresent ||
			before.pin.Owner != billing.PostingOwnerV2 || before.pin.Status != billing.PostingPinPinned {
			t.Fatalf("unexpected pre-apply V2 state: exposureOpen=%v pinPresent=%v pin=%+v",
				before.exposure.IsOpen(), before.pinPresent, before.pin)
		}
		_, applyErr := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: call, Exposure: exposure, Result: adversarial, PostingOwner: billing.PostingOwnerV2,
		})
		review5beRequireCompletenessFence(t, applyErr, string(economics.CompletenessPartial))
		after := review5beSnapshotOf(t, store, call.AccountID, call.CallID)
		t.Logf("5B-1 RateCall err=%v completeness=%q | apply=%v", rateErr, rated.CustomerValuation.Completeness, applyErr)
		t.Logf("5B-1 before=%+v pin=%+v after=%+v pin=%+v", before, before.pin, after, after.pin)
		if !review5beSameSnapshot(before, after) {
			t.Fatalf("rejected 5B-1 V2 Apply caused effects:\n before=%+v\n  after=%+v", before, after)
		}
	})

	t.Run("recursive_overlap_cannot_settle", func(t *testing.T) {
		t.Parallel()
		parent := review5beKey("vendor:review5be_overlap_total")
		partB := review5beKey("vendor:review5be_overlap_b")
		partC := review5beKey("vendor:review5be_overlap_c")
		midX := review5beKey("vendor:review5be_overlap_x")
		midY := review5beKey("vendor:review5be_overlap_y")
		subsetD := review5beKey("vendor:review5be_overlap_d")
		schemas := []metering.ComponentSchema{{
			ID: review5beSchemaID, Version: "1",
			Relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
				{Kind: metering.RelationshipPartition, Parent: partB, Child: midX},
				{Kind: metering.RelationshipPartition, Parent: partB, Child: midY},
				{Kind: metering.RelationshipSubset, Parent: parent, Child: subsetD},
			},
		}}
		rules := []economics.RatingRule{
			review5beLinearRule(t, "review5be-overlap-x", midX, "1"),
			review5beLinearRule(t, "review5be-overlap-y", midY, "1"),
			review5beLinearRule(t, "review5be-overlap-c", partC, "1"),
			review5beLinearRule(t, "review5be-overlap-d", subsetD, "1"),
		}
		store, call, exposure, rated, rateErr := review5beSetup(t, "review5be-overlap", "b-overlap",
			review5beTariff(t, rules, schemas),
			review5beMeasure(midX, "30"), review5beMeasure(midY, "30"),
			review5beMeasure(partC, "40"), review5beMeasure(subsetD, "20"))

		if !errors.Is(rateErr, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("RateCall err=%v, want ErrSchemaOverlapConflict; completeness=%q valuation=%+v",
				rateErr, rated.CustomerValuation.Completeness, rated.CustomerValuation)
		}
		if rated.CustomerValuation.Completeness != economics.CompletenessConflict {
			t.Fatalf("recursive overlap completeness=%q, want conflict: %+v",
				rated.CustomerValuation.Completeness, rated.CustomerValuation)
		}

		adversarial := review5beAdversarialCharge(rated)
		review5beRequireCompletenessFence(t, billing.ValidateCallRatingResultForSettlement(
			adversarial, call, exposure, billing.PostingOwnerV2,
		), string(economics.CompletenessConflict))

		before := review5beSnapshotOf(t, store, call.AccountID, call.CallID)
		if !before.exposure.IsOpen() || !before.pinPresent ||
			before.pin.Owner != billing.PostingOwnerV2 || before.pin.Status != billing.PostingPinPinned {
			t.Fatalf("unexpected pre-apply V2 state: exposureOpen=%v pinPresent=%v pin=%+v",
				before.exposure.IsOpen(), before.pinPresent, before.pin)
		}
		_, applyErr := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: call, Exposure: exposure, Result: adversarial, PostingOwner: billing.PostingOwnerV2,
		})
		review5beRequireCompletenessFence(t, applyErr, string(economics.CompletenessConflict))
		after := review5beSnapshotOf(t, store, call.AccountID, call.CallID)
		t.Logf("5B-2 RateCall err=%v completeness=%q | apply=%v", rateErr, rated.CustomerValuation.Completeness, applyErr)
		t.Logf("5B-2 before=%+v pin=%+v after=%+v pin=%+v", before, before.pin, after, after.pin)
		if !review5beSameSnapshot(before, after) {
			t.Fatalf("rejected 5B-2 V2 Apply caused effects:\n before=%+v\n  after=%+v", before, after)
		}
	})

	t.Run("valid_disjoint_settles_once", func(t *testing.T) {
		t.Parallel()
		parent := review5beKey("vendor:review5be_valid_total")
		partB := review5beKey("vendor:review5be_valid_b")
		partC := review5beKey("vendor:review5be_valid_c")
		midX := review5beKey("vendor:review5be_valid_x")
		midY := review5beKey("vendor:review5be_valid_y")
		schemas := []metering.ComponentSchema{{
			ID: review5beSchemaID, Version: "1",
			Relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
				{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
				{Kind: metering.RelationshipPartition, Parent: partB, Child: midX},
				{Kind: metering.RelationshipPartition, Parent: partB, Child: midY},
			},
		}}
		rules := []economics.RatingRule{
			review5beLinearRule(t, "review5be-valid-x", midX, "1"),
			review5beLinearRule(t, "review5be-valid-y", midY, "1"),
			review5beLinearRule(t, "review5be-valid-c", partC, "1"),
		}
		store, call, exposure, rated, rateErr := review5beSetup(t, "review5be-valid", "b-valid",
			review5beTariff(t, rules, schemas),
			review5beMeasure(midX, "30"), review5beMeasure(midY, "30"), review5beMeasure(partC, "40"))

		if rateErr != nil {
			t.Fatalf("RateCall: %v; valuation=%+v", rateErr, rated.CustomerValuation)
		}
		if rated.CustomerValuation.Completeness != economics.CompletenessComplete {
			t.Fatalf("completeness=%q, want complete; valuation=%+v", rated.CustomerValuation.Completeness, rated.CustomerValuation)
		}
		if rated.CustomerCharge != (billing.Money{Nano: review5beValidChargeNano, Currency: "USD"}) {
			t.Fatalf("customer charge=%+v, want 100 USD (X30 + Y30 + C40 at USD1)", rated.CustomerCharge)
		}
		if err := billing.ValidateCallRatingResultForSettlement(rated, call, exposure, billing.PostingOwnerV2); err != nil {
			t.Fatalf("valid disjoint rating must pass the V2 fence, got %v", err)
		}

		before := review5beSnapshotOf(t, store, call.AccountID, call.CallID)
		if !before.exposure.IsOpen() || !before.pinPresent ||
			before.pin.Owner != billing.PostingOwnerV2 || before.pin.Status != billing.PostingPinPinned {
			t.Fatalf("unexpected pre-apply V2 state: exposureOpen=%v pinPresent=%v pin=%+v",
				before.exposure.IsOpen(), before.pinPresent, before.pin)
		}
		settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: call, Exposure: exposure, Result: rated, PostingOwner: billing.PostingOwnerV2,
		})
		if err != nil {
			t.Fatalf("ApplyCallBillingResult V2: %v", err)
		}
		if settled.Replayed {
			t.Fatalf("first valid V2 settlement reported Replayed: %+v", settled)
		}
		after := review5beSnapshotOf(t, store, call.AccountID, call.CallID)
		if after.balanceNano != review5beBalanceNano-review5beValidChargeNano {
			t.Fatalf("balance=%d, want %d", after.balanceNano, review5beBalanceNano-review5beValidChargeNano)
		}
		if after.accountVersion != before.accountVersion+1 || after.journals != before.journals+1 {
			t.Fatalf("valid settlement account/journal effect = %+v (before %+v)", after, before)
		}
		if after.exposure.IsOpen() {
			t.Fatalf("valid settlement must close the exposure: %+v", after.exposure)
		}
		if !after.pinPresent || after.pin.Owner != billing.PostingOwnerV2 || !after.pin.IsCompleted() {
			t.Fatalf("valid settlement must complete the V2 pin: %+v", after.pin)
		}
		t.Logf("control RateCall charge=%+v completeness=%q", rated.CustomerCharge, rated.CustomerValuation.Completeness)
		t.Logf("control settled Replayed=%v before=%+v after=%+v pin=%+v", settled.Replayed, before, after, after.pin)

		replay, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
			Call: call, Exposure: exposure, Result: rated, PostingOwner: billing.PostingOwnerV2,
		})
		if err != nil {
			t.Fatalf("replay ApplyCallBillingResult V2: %v", err)
		}
		if !replay.Replayed {
			t.Fatalf("exact replay must report Replayed: %+v", replay)
		}
		afterReplay := review5beSnapshotOf(t, store, call.AccountID, call.CallID)
		t.Logf("control exact replay Replayed=%v afterReplay=%+v pin=%+v", replay.Replayed, afterReplay, afterReplay.pin)
		if !review5beSameSnapshot(after, afterReplay) {
			t.Fatalf("exact replay changed settlement effects:\n after=%+v\n  replay=%+v", after, afterReplay)
		}
	})
}
