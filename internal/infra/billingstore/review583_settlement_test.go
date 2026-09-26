package billingstore

// 583 downstream settlement proof (PR #666 review, TEST-ONLY).
//
// The in-core rating review (billing_review583_zero_ambiguity_test.go) proves
// the two P1 repairs classify the P1-A explicit-zero partition overlap and the
// P1-B local ambiguity taint. This file proves the downstream money
// consequence under the legal V2 lifecycle: the actual production RateCall
// outcome for the adversarial graphs can never reach a customer monetary
// effect, while the same graphs' valid controls settle exactly once and replay
// unchanged.
//
// The graphs and quantities mirror the in-core P1-A/P1-B vectors; every
// expected amount comes from that graph arithmetic, not from production helper
// booleans. The store seam is handed the actual RateCall result; for the
// rejected case the only change is normalizing CustomerCharge to a valid USD
// zero (review5beAdversarialCharge) so the fence cannot short-circuit on a
// malformed empty currency before it examines the unchanged noncomplete
// CustomerValuation.
//
// The same runner is reused verbatim by the SQLite test and the
// //go:build integration PostgreSQL test; only the store factory differs.

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// review583Measure is the exact anonymous measure shape review5beMeasure
// returns; the alias keeps the shared helper signature identical so no
// copy of the measure constructor is needed.
type review583Measure = struct {
	key      metering.ComponentKey
	quantity string
}

// review583Case is one P1 scenario: a frozen component graph, its observation
// evidence, and whether the production rating must fail closed.
type review583Case struct {
	name         string
	accountID    string
	bLegID       string
	tariff       economics.TariffSnapshot
	measures     []review583Measure
	wantConflict bool
}

// review583RunSettlementSuite runs every P1 scenario against a freshly opened
// store from newStore. newStore owns the store's lifetime (registration and
// cleanup); the runner only exercises it.
func review583RunSettlementSuite(t *testing.T, newStore func(t *testing.T) *DurableStore) {
	t.Helper()
	for _, tc := range review583Cases(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newStore(t)
			review583RunCase(t, store, tc)
		})
	}
}

// review583RunCase drives one scenario through the legal V2 lifecycle: empty
// activation before any account, admission with explicit V2 ownership, terminal
// usage under V2 ownership, then either the typed refusal with zero durable
// effect or a single settlement plus exact replay.
func review583RunCase(t *testing.T, store *DurableStore, tc review583Case) {
	t.Helper()
	ctx := context.Background()
	call, exposure, rated, rateErr := review583Setup(t, store, tc.accountID, tc.bLegID, tc.tariff, tc.measures...)

	if tc.wantConflict {
		if !errors.Is(rateErr, billing.ErrSchemaOverlapConflict) {
			t.Fatalf("RateCall err=%v, want ErrSchemaOverlapConflict; completeness=%q valuation=%+v",
				rateErr, rated.CustomerValuation.Completeness, rated.CustomerValuation)
		}
		if rated.CustomerValuation.Completeness != economics.CompletenessConflict {
			t.Fatalf("conflict completeness=%q, want conflict: %+v",
				rated.CustomerValuation.Completeness, rated.CustomerValuation)
		}
		review583RequireNoPayableLines(t, rated.CustomerValuation)

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
		t.Logf("583-%s rejected: RateCall err=%v completeness=%q fence=%v lines=%d totals=%d",
			tc.name, rateErr, rated.CustomerValuation.Completeness, applyErr,
			len(rated.CustomerValuation.Lines), len(rated.CustomerValuation.Totals))
		t.Logf("583-%s before balance=%d journals=%d exposureOpen=%v pin=%v/%v unchanged=%v",
			tc.name, before.balanceNano, before.journals, before.exposure.IsOpen(),
			before.pin.Owner, before.pin.Status, review5beSameSnapshot(before, after))
		if !review5beSameSnapshot(before, after) {
			t.Fatalf("rejected 583-%s V2 Apply caused effects:\n before=%+v\n  after=%+v", tc.name, before, after)
		}
		return
	}

	if rateErr != nil {
		t.Fatalf("RateCall: %v; valuation=%+v", rateErr, rated.CustomerValuation)
	}
	if rated.CustomerValuation.Completeness != economics.CompletenessComplete {
		t.Fatalf("completeness=%q, want complete; valuation=%+v",
			rated.CustomerValuation.Completeness, rated.CustomerValuation)
	}
	if rated.CustomerCharge != (billing.Money{Nano: review5beValidChargeNano, Currency: "USD"}) {
		t.Fatalf("customer charge=%+v, want 100 USD", rated.CustomerCharge)
	}
	if err := billing.ValidateCallRatingResultForSettlement(rated, call, exposure, billing.PostingOwnerV2); err != nil {
		t.Fatalf("valid control rating must pass the V2 fence, got %v", err)
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
	t.Logf("583-%s control charge=%+v completeness=%q settled Replayed=%v", tc.name, rated.CustomerCharge, rated.CustomerValuation.Completeness, settled.Replayed)

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
	t.Logf("583-%s after balance=%d journals=%d exposureOpen=%v pin=%v | replayReplayed=%v stable=%v",
		tc.name, after.balanceNano, after.journals, after.exposure.IsOpen(),
		after.pin.Status, replay.Replayed, review5beSameSnapshot(after, afterReplay))
	if !review5beSameSnapshot(after, afterReplay) {
		t.Fatalf("exact replay changed settlement effects:\n after=%+v\n  replay=%+v", after, afterReplay)
	}
}

// review583Setup performs the small legal V2 sequence on an already-open store:
// empty activation before any account/exposure/usage, account creation,
// production RateCall, explicit V2 admission carrying the rated route tariffs,
// and V2 terminal usage. It returns the actual rating outcome; RateCall's error
// and possibly noncomplete CustomerValuation are never replaced.
func review583Setup(
	t *testing.T,
	store *DurableStore,
	accountID, bLegID string,
	tariff economics.TariffSnapshot,
	measures ...review583Measure,
) (billing.CallUsageRecord, billing.CallExposure, billing.CallRatingResult, error) {
	t.Helper()
	ctx := context.Background()
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
	return call, exposure, rated, rateErr
}

// review583RequireNoPayableLines proves a rejected overlap emits no payable
// line and no payable currency total.
func review583RequireNoPayableLines(t *testing.T, valuation economics.Valuation) {
	t.Helper()
	for _, line := range valuation.Lines {
		if line.Amount != nil || line.Status == economics.RatingLineRated || line.Status == economics.RatingLineExplicitFree {
			t.Fatalf("overlap rejection must not emit payable lines, got %+v", line)
		}
	}
	if len(valuation.Totals) != 0 && valuation.Totals[0].Amount != nil {
		t.Fatalf("overlap rejection must not emit a payable total, got %+v", valuation.Totals[0])
	}
}

// review583PartitionSchema declares A --partition--> {B, C} with the A --subset
// --> D included subset for the P1-A graph.
func review583PartitionSchema(parent, partB, partC, subsetD metering.ComponentKey) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: review5beSchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
			{Kind: metering.RelationshipSubset, Parent: parent, Child: subsetD},
		},
	}}
}

// review583PartitionOnlySchema declares A --partition--> {B, C} without any
// subset child for the P1-A valid control.
func review583PartitionOnlySchema(parent, partB, partC metering.ComponentKey) []metering.ComponentSchema {
	return []metering.ComponentSchema{{
		ID: review5beSchemaID, Version: "1",
		Relationships: []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partB},
			{Kind: metering.RelationshipPartition, Parent: parent, Child: partC},
		},
	}}
}

// review583Cases builds the four P1 scenarios with keys, rules, schemas and
// evidence. Each scenario keeps its keys local to its graph; every expected
// amount is the graph arithmetic (P1-A: C100; P1-B: X30 + Y30 + C40 and
// B60 + C40).
func review583Cases(t *testing.T) []review583Case {
	t.Helper()
	key := func(component string) metering.ComponentKey { return review5beKey("vendor:583:" + component) }
	measure := review5beMeasure
	rule := func(id string, k metering.ComponentKey) economics.RatingRule {
		return review5beLinearRule(t, id, k, "1")
	}

	// P1-A keys.
	aParent, aB, aC, aD := key("p1a_parent"), key("p1a_b"), key("p1a_c"), key("p1a_d")

	// P1-A valid-control keys.
	ctrlA, ctrlB, ctrlC := key("p1a_ctrl_parent"), key("p1a_ctrl_b"), key("p1a_ctrl_c")

	// P1-B keys.
	bParent, bB, bC := key("p1b_parent"), key("p1b_b"), key("p1b_c")
	bX, bY, bD := key("p1b_x"), key("p1b_y"), key("p1b_d")
	z1, z2, sharedS := key("p1b_z1"), key("p1b_z2"), key("p1b_s")
	zAmbiguity := []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: z1, Child: sharedS},
		{Kind: metering.RelationshipPartition, Parent: z2, Child: sharedS},
	}
	p1bOverlapSchema := []metering.ComponentSchema{{
		ID: review5beSchemaID, Version: "1",
		Relationships: append([]metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: bParent, Child: bB},
			{Kind: metering.RelationshipPartition, Parent: bParent, Child: bC},
			{Kind: metering.RelationshipPartition, Parent: bB, Child: bX},
			{Kind: metering.RelationshipPartition, Parent: bB, Child: bY},
			{Kind: metering.RelationshipSubset, Parent: bParent, Child: bD},
		}, zAmbiguity...),
	}}

	// P1-B valid-control keys.
	indParent, indB, indC := key("p1b_ind_parent"), key("p1b_ind_b"), key("p1b_ind_c")
	indZ1, indZ2, indS := key("p1b_ind_z1"), key("p1b_ind_z2"), key("p1b_ind_s")
	indRelationships := []metering.ComponentRelationship{
		{Kind: metering.RelationshipPartition, Parent: indParent, Child: indB},
		{Kind: metering.RelationshipPartition, Parent: indParent, Child: indC},
		{Kind: metering.RelationshipPartition, Parent: indZ1, Child: indS},
		{Kind: metering.RelationshipPartition, Parent: indZ2, Child: indS},
	}
	indSchema := []metering.ComponentSchema{{
		ID: review5beSchemaID, Version: "1", Relationships: indRelationships,
	}}

	return []review583Case{
		{
			name:      "p1a_explicit_zero_rejects_priced_subset",
			accountID: "review583-p1a-reject", bLegID: "b-p1a-reject",
			tariff: review5beTariff(t,
				[]economics.RatingRule{rule("583-p1a-c", aC), rule("583-p1a-d", aD)},
				review583PartitionSchema(aParent, aB, aC, aD)),
			measures: []review583Measure{
				measure(aB, "0"), measure(aC, "100"), measure(aD, "20"),
			},
			wantConflict: true,
		},
		{
			name:      "p1a_zero_share_partition_complete_100",
			accountID: "review583-p1a-control", bLegID: "b-p1a-control",
			tariff: review5beTariff(t,
				[]economics.RatingRule{rule("583-p1a-ctrl-c", ctrlC)},
				review583PartitionOnlySchema(ctrlA, ctrlB, ctrlC)),
			measures: []review583Measure{measure(ctrlB, "0"), measure(ctrlC, "100")},
		},
		{
			name:      "p1b_unrelated_ambiguity_rejects_overlap",
			accountID: "review583-p1b-reject", bLegID: "b-p1b-reject",
			tariff: review5beTariff(t,
				[]economics.RatingRule{
					rule("583-p1b-x", bX), rule("583-p1b-y", bY),
					rule("583-p1b-c", bC), rule("583-p1b-d", bD),
				},
				p1bOverlapSchema),
			measures: []review583Measure{
				measure(bX, "30"), measure(bY, "30"), measure(bC, "40"), measure(bD, "20"),
			},
			wantConflict: true,
		},
		{
			name:      "p1b_independent_cover_unaffected_by_ambiguity",
			accountID: "review583-p1b-control", bLegID: "b-p1b-control",
			tariff: review5beTariff(t,
				[]economics.RatingRule{rule("583-p1b-ind-b", indB), rule("583-p1b-ind-c", indC)},
				indSchema),
			measures: []review583Measure{
				measure(indParent, "100"), measure(indB, "60"), measure(indC, "40"),
			},
		},
	}
}

// TestReview583SettlementSQLite proves the P1-A/P1-B settlement consequence on
// the file-backed SQLite dialect.
func TestReview583SettlementSQLite(t *testing.T) {
	t.Parallel()
	review583RunSettlementSuite(t, func(t *testing.T) *DurableStore {
		store, _, closeStore := ref83intStore(t)
		t.Cleanup(closeStore)
		return store
	})
}
