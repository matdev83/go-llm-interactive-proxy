package billingstore

// f356 downstream settlement proof (PR #666 adversarial re-review at head
// f356b681, TEST-ONLY).
//
// The in-core rating review (billing_review_f356_required_taint_test.go) proves
// the component rating seams classify the two remaining P1 gaps and the P2 gap.
// This file proves the downstream consequence that matters for money: the
// actual CallRatingResult produced by the production RateCall seam for those
// cases can never reach a customer monetary effect under the legal V2 lifecycle,
// on either supported dialect. The valid controls, rated by the same seam,
// settle exactly once under V2 ownership and replay without a second posting.
//
// The runner is dialect-shared: TestDBParity_SQLite drives it over SQLite and
// TestDBParity_PostgresDirect drives the identical body over the configured
// PostgreSQL DSN, so the durable no-effect proof is not a single-dialect claim.
// Expected values come from the review handoff arithmetic, never from
// production helper booleans.

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
	// f356SchemaID matches ref83intKey's frozen component schema so the reused
	// ref83intObservation / ref83intLeg / ref83intCall fixtures and the custom
	// component graph share one schema identity.
	f356SchemaID = "refinement83.cert.v1"
	// f356BalanceNano funds every case (5000 USD).
	f356BalanceNano int64 = 5_000_000_000_000
	// f356MaxNano bounds the admitted exposure (1000 USD).
	f356MaxNano int64 = 1_000_000_000_000
	// f356ValidChargeNano is the nested conserved control X30 + Y30 + C40 at
	// USD1, settled once.
	f356ValidChargeNano int64 = 100_000_000_000
)

func f356Key(component string) metering.ComponentKey {
	return ref83intKey(metering.DirectionInput, component, metering.UnitToken)
}

// f62aKey is the shared-schema component factory for the 62a follow-up cases, so
// they read as role names (zero_parent, cached_subset, ...) instead of hashes.
func f62aKey(role string) metering.ComponentKey {
	return f356Key("vendor:reviewf356_62a_" + role)
}

// f63Key is the same factory for the 63c transitive cases, kept separate so the
// two rounds' graphs cannot collide on a component identity.
func f63Key(role string) metering.ComponentKey {
	return f356Key("vendor:reviewf356_63c_" + role)
}

func f356LinearRule(t *testing.T, id string, key metering.ComponentKey, price string) economics.RatingRule {
	t.Helper()
	return economics.RatingRule{ID: id, Kind: economics.RatingRuleLinear, Component: &key, Currency: "USD", UnitPrice: ref83intDecimal(t, price)}
}

// f356Tariff freezes the exact component graph against the same pricing
// reference ref83intWinnerPolicy/ref83intCall bind, with no fees.
func f356Tariff(t *testing.T, rules []economics.RatingRule, relationships []metering.ComponentRelationship) economics.TariffSnapshot {
	t.Helper()
	tariff, err := economics.BuildTariffSnapshotWithSchemas(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "retail-pricing-83", Version: "v3"}, RaterID: "reference"},
		"USD", rules,
		[]metering.ComponentSchema{{ID: f356SchemaID, Version: "1", Relationships: relationships}},
	)
	if err != nil {
		t.Fatalf("BuildTariffSnapshotWithSchemas: %v", err)
	}
	return tariff
}

// f356Measure is one (component, quantity) pair for the shared observation
// fixture. It is an alias of the anonymous shape ref83intObservation accepts, so
// the production fixture stays the only measure builder.
type f356Measure = struct {
	key      metering.ComponentKey
	quantity string
}

func f356M(key metering.ComponentKey, quantity string) f356Measure {
	return f356Measure{key: key, quantity: quantity}
}

// f356NormalizedCharge returns the actual RateCall outcome with only the
// customer charge normalized to a valid USD zero. The
// noncomplete/incomparable CustomerValuation, CallID, Fingerprint and
// RouteTariffs are left untouched, so the settlement fence must reject on the
// valuation completeness rather than on a malformed empty-currency charge.
func f356NormalizedCharge(rated billing.CallRatingResult) billing.CallRatingResult {
	clone := rated
	clone.CustomerCharge = billing.Money{Nano: 0, Currency: "USD"}
	return clone
}

// f356RequireCompletenessFence proves the error is the typed incomplete fence
// whose diagnostic is the valuation-completeness classification.
func f356RequireCompletenessFence(t *testing.T, err error, wantCompleteness string) {
	t.Helper()
	if !errors.Is(err, billing.ErrRetailRateIncomplete) {
		t.Fatalf("fence err=%v, want ErrRetailRateIncomplete", err)
	}
	if !strings.Contains(err.Error(), "completeness") || !strings.Contains(err.Error(), wantCompleteness) {
		t.Fatalf("fence err=%v, want completeness diagnostic mentioning %q", err, wantCompleteness)
	}
}

// f356Setup performs the legal empty V2 activation before creating the
// account, admits the exposure with explicit V2 ownership, rates through the
// production RateCall seam, and appends the durable closure/leg under V2
// ownership. It returns the exact rating outcome; RateCall's error and its
// possibly noncomplete CustomerValuation are never replaced with a fabricated
// complete result.
func f356Setup(t *testing.T, store *DurableStore, accountID, bLegID string, tariff economics.TariffSnapshot, measures ...f356Measure) (billing.CallUsageRecord, billing.CallExposure, billing.CallRatingResult, error) {
	t.Helper()
	ctx := context.Background()
	// Legal empty activation before any account/exposure/usage exists.
	f3ActivateEmpty(t, store)
	if err := store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: f356BalanceNano, State: billing.AccountReady, Version: 1,
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
		MaxCustomerCharge: billing.Money{Nano: f356MaxNano, Currency: "USD"},
		CustomerPricing:   billing.PricingSnapshot{Ref: policy.PricingRef, Currency: "USD"},
		CustomerPolicy:    policy, CustomerTariff: tariff,
	})
	exposure, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:        billing.Money{Nano: f356MaxNano, Currency: "USD"},
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

// f356Snapshot records the full durable money, exposure and pin state so
// equality proves zero effect. Pin presence is tracked separately from pin
// content so an absent pin can never be confused with a newly created
// incomplete one.
type f356Snapshot struct {
	balanceNano    int64
	accountVersion uint64
	journals       int
	exposure       billing.CallExposure
	pinPresent     bool
	pin            billing.PostingPin
}

func f356SnapshotOf(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID) f356Snapshot {
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
	snapshot := f356Snapshot{
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

func f356SameSnapshot(a, b f356Snapshot) bool {
	return a.balanceNano == b.balanceNano &&
		a.accountVersion == b.accountVersion &&
		a.journals == b.journals &&
		a.pinPresent == b.pinPresent &&
		reflect.DeepEqual(a.exposure, b.exposure) &&
		reflect.DeepEqual(a.pin, b.pin)
}

// f356RequirePinnedOpenV2 proves the pre-apply durable state is the legal V2
// starting point: an open exposure and a pinned V2 customer-settlement pin.
// A rejected result that never started from this state would not prove
// anything.
func f356RequirePinnedOpenV2(t *testing.T, before f356Snapshot) {
	t.Helper()
	if !before.exposure.IsOpen() || !before.pinPresent ||
		before.pin.Owner != billing.PostingOwnerV2 || before.pin.Status != billing.PostingPinPinned {
		t.Fatalf("unexpected pre-apply V2 state: exposureOpen=%v pinPresent=%v pin=%+v",
			before.exposure.IsOpen(), before.pinPresent, before.pin)
	}
}

// f356RequireSettlesOnce drives the full happy-path durable proof for one valid
// control: the V2 fence accepts it, the first apply posts exactly once, the
// exposure closes, the pin completes, and an exact replay is a durable no-op.
// Every expected value is the literal nano amount the handoff arithmetic
// produces, never a value echoed back by the store.
func f356RequireSettlesOnce(
	t *testing.T,
	store *DurableStore,
	call billing.CallUsageRecord,
	exposure billing.CallExposure,
	rated billing.CallRatingResult,
	chargeNano int64,
	label string,
) {
	t.Helper()
	ctx := context.Background()
	if rateErr := billing.ValidateCallRatingResultForSettlement(rated, call, exposure, billing.PostingOwnerV2); rateErr != nil {
		t.Fatalf("%s must pass the V2 fence, got %v", label, rateErr)
	}
	before := f356SnapshotOf(t, store, call.AccountID, call.CallID)
	f356RequirePinnedOpenV2(t, before)
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure, Result: rated, PostingOwner: billing.PostingOwnerV2,
	})
	if err != nil {
		t.Fatalf("%s ApplyCallBillingResult V2: %v", label, err)
	}
	if settled.Replayed {
		t.Fatalf("%s first valid V2 settlement reported Replayed: %+v", label, settled)
	}
	after := f356SnapshotOf(t, store, call.AccountID, call.CallID)
	if after.balanceNano != f356BalanceNano-chargeNano {
		t.Fatalf("%s balance=%d, want %d", label, after.balanceNano, f356BalanceNano-chargeNano)
	}
	if after.accountVersion != before.accountVersion+1 || after.journals != before.journals+1 {
		t.Fatalf("%s account/journal effect = %+v (before %+v)", label, after, before)
	}
	if after.exposure.IsOpen() {
		t.Fatalf("%s must close the exposure: %+v", label, after.exposure)
	}
	if !after.pinPresent || after.pin.Owner != billing.PostingOwnerV2 || !after.pin.IsCompleted() {
		t.Fatalf("%s must complete the V2 pin: %+v", label, after.pin)
	}
	t.Logf("%s settled Replayed=%v charge=%+v completeness=%q before=%+v after=%+v pin=%+v",
		label, settled.Replayed, rated.CustomerCharge, rated.CustomerValuation.Completeness, before, after, after.pin)

	replay, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure, Result: rated, PostingOwner: billing.PostingOwnerV2,
	})
	if err != nil {
		t.Fatalf("%s replay ApplyCallBillingResult V2: %v", label, err)
	}
	if !replay.Replayed {
		t.Fatalf("%s exact replay must report Replayed: %+v", label, replay)
	}
	afterReplay := f356SnapshotOf(t, store, call.AccountID, call.CallID)
	if !f356SameSnapshot(after, afterReplay) {
		t.Fatalf("%s exact replay changed settlement effects:\n after=%+v\n  replay=%+v", label, after, afterReplay)
	}
}

// runReviewF356SettlementFence is the dialect-shared body, invoked from
// TestDBParity_SQLite and TestDBParity_PostgresDirect so one body proves the
// money behaviour on both supported dialects. open must hand out one freshly
// activated-capable store per subtest so the legal empty V2 activation is
// genuinely empty.
func runReviewF356SettlementFence(t *testing.T, open func(t *testing.T) *DurableStore) {
	t.Helper()
	ctx := context.Background()

	// f356RejectCase is one adversarial (scope, parent) graph whose rating must
	// be noncomplete and must never post money.
	type f356RejectCase struct {
		name          string
		wantRating    error
		accountID     string
		bLegID        string
		relationships []metering.ComponentRelationship
		rules         []economics.RatingRule
		measures      []f356Measure
	}

	// P1-1: the INFORMATIONAL input_token_total declares the complete partition
	// {B, C} with REQUIRED C absent, so a false complete USD60 must be impossible.
	total := f356Key(metering.ComponentInputTokenTotal)
	p1B := f356Key("vendor:reviewf356_p1_b")
	p1C := f356Key("vendor:reviewf356_p1_c")

	// P1-2: A --partition--> {B, C}; Z --partition--> B (B shared, so A/Z are
	// tainted); A --subset--> D with A and Z ABSENT, so a false complete USD120
	// must be impossible.
	p2A := f356Key("vendor:reviewf356_p2_a")
	p2Z := f356Key("vendor:reviewf356_p2_z")
	p2B := f356Key("vendor:reviewf356_p2_b")
	p2C := f356Key("vendor:reviewf356_p2_c")
	p2D := f356Key("vendor:reviewf356_p2_d")

	rejects := []f356RejectCase{
		{
			name: "missing_required_partition_member_cannot_settle",
			// The absent REQUIRED member is missing evidence, not an arithmetic
			// contradiction and not a shared-child ambiguity.
			wantRating: billing.ErrSchemaPartitionIncomplete,
			accountID:  "reviewf356-p1-missing",
			bLegID:     "b-f356-p1-missing",
			relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: total, Child: p1B},
				{Kind: metering.RelationshipPartition, Parent: total, Child: p1C},
			},
			rules:    []economics.RatingRule{f356LinearRule(t, "reviewf356-p1-b", p1B, "1")},
			measures: []f356Measure{f356M(total, "100"), f356M(p1B, "60")},
		},
		{
			name: "tainted_absent_parent_cannot_settle",
			// The unprovable shared-child cover is ambiguous, not contradictory.
			wantRating: billing.ErrSchemaPartitionIncomparable,
			accountID:  "reviewf356-p2-tainted",
			bLegID:     "b-f356-p2-tainted",
			relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: p2A, Child: p2B},
				{Kind: metering.RelationshipPartition, Parent: p2A, Child: p2C},
				{Kind: metering.RelationshipPartition, Parent: p2Z, Child: p2B},
				{Kind: metering.RelationshipSubset, Parent: p2A, Child: p2D},
			},
			rules: []economics.RatingRule{
				f356LinearRule(t, "reviewf356-p2-b", p2B, "1"),
				f356LinearRule(t, "reviewf356-p2-c", p2C, "1"),
				f356LinearRule(t, "reviewf356-p2-d", p2D, "1"),
			},
			measures: []f356Measure{f356M(p2B, "60"), f356M(p2C, "40"), f356M(p2D, "20")},
		},
		{
			// 62a P1-A: a resolving parent rule is not a commercial basis. The
			// parent's own quantity is zero, so its rated line is worth nothing
			// and the children are the only money in the scope, yet the missing
			// REQUIRED member must still fail closed.
			name:       "zero_charge_parent_missing_required_member_cannot_settle",
			wantRating: billing.ErrSchemaPartitionIncomplete,
			accountID:  "reviewf356-62a-zero-parent",
			bLegID:     "b-f356-62a-zero-parent",
			relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: f62aKey("zero_parent"), Child: f62aKey("b")},
				{Kind: metering.RelationshipPartition, Parent: f62aKey("zero_parent"), Child: f62aKey("c")},
			},
			rules: []economics.RatingRule{
				f356LinearRule(t, "reviewf356-62a-parent", f62aKey("zero_parent"), "1"),
				f356LinearRule(t, "reviewf356-62a-b", f62aKey("b"), "1"),
				f356LinearRule(t, "reviewf356-62a-c", f62aKey("c"), "1"),
			},
			measures: []f356Measure{
				f356M(f62aKey("zero_parent"), "0"),
				f356M(f62aKey("b"), "60"),
			},
		},
		{
			// 62a P1-B: a declared subset quantity may never exceed its parent's.
			// The OpenAI cache-read shape is the live stock instance, so the
			// generic contract is pinned on the provider-family key.
			name:       "subset_quantity_exceeding_parent_cannot_settle",
			wantRating: billing.ErrSchemaSubsetContradiction,
			accountID:  "reviewf356-62a-subset",
			bLegID:     "b-f356-62a-subset",
			relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipSubset, Parent: f62aKey("subset_parent"), Child: f62aKey("cached_subset")},
			},
			rules: []economics.RatingRule{
				f356LinearRule(t, "reviewf356-62a-subset-parent", f62aKey("subset_parent"), "1"),
				f356LinearRule(t, "reviewf356-62a-cached", f62aKey("cached_subset"), "1"),
			},
			measures: []f356Measure{
				f356M(f62aKey("subset_parent"), "0"),
				f356M(f62aKey("cached_subset"), "40"),
			},
		},
		{
			// 62a defensive matrix: an UNOBSERVED parent with an unprovable
			// complete partition and a payable subset descendant. Nothing else
			// classifies the shape, so without the fail-closed denial B + D
			// settles as complete additive money.
			name:       "unobserved_parent_unplaceable_subset_cannot_settle",
			wantRating: billing.ErrSchemaPartitionIncomplete,
			accountID:  "reviewf356-62a-unobserved",
			bLegID:     "b-f356-62a-unobserved",
			relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: f62aKey("unobserved"), Child: f62aKey("ub")},
				{Kind: metering.RelationshipPartition, Parent: f62aKey("unobserved"), Child: f62aKey("uc")},
				{Kind: metering.RelationshipSubset, Parent: f62aKey("unobserved"), Child: f62aKey("ud")},
			},
			rules: []economics.RatingRule{
				f356LinearRule(t, "reviewf356-62a-ub", f62aKey("ub"), "1"),
				f356LinearRule(t, "reviewf356-62a-ud", f62aKey("ud"), "1"),
			},
			measures: []f356Measure{
				f356M(f62aKey("ub"), "60"),
				f356M(f62aKey("ud"), "20"),
			},
		},
		{
			// 63c P1-1: the partition-side money is represented NESTED below an
			// unobserved parent, so a direct-child relevance test finds nothing.
			// A -partition-> {B, C}, B -partition-> {X, Y}, A -subset-> D with
			// A, B, C absent must never certify X + D as complete.
			name:       "nested_payable_descendant_unplaceable_subset_cannot_settle",
			wantRating: billing.ErrSchemaPartitionIncomplete,
			accountID:  "reviewf356-63c-nested",
			bLegID:     "b-f356-63c-nested",
			relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipPartition, Parent: f63Key("a"), Child: f63Key("b")},
				{Kind: metering.RelationshipPartition, Parent: f63Key("a"), Child: f63Key("c")},
				{Kind: metering.RelationshipPartition, Parent: f63Key("b"), Child: f63Key("x")},
				{Kind: metering.RelationshipPartition, Parent: f63Key("b"), Child: f63Key("y")},
				{Kind: metering.RelationshipSubset, Parent: f63Key("a"), Child: f63Key("d")},
			},
			rules: []economics.RatingRule{
				f356LinearRule(t, "reviewf356-63c-x", f63Key("x"), "1"),
				f356LinearRule(t, "reviewf356-63c-d", f63Key("d"), "1"),
			},
			measures: []f356Measure{
				f356M(f63Key("x"), "60"),
				f356M(f63Key("d"), "20"),
			},
		},
		{
			// 63c P1-2: subset containment is transitive, so C is bounded by A
			// across the two-level chain A -subset-> B -subset-> C even with B
			// unobserved, and 20 > 10 is inconsistent evidence.
			name:       "transitive_subset_quantity_exceeding_ancestor_cannot_settle",
			wantRating: billing.ErrSchemaSubsetContradiction,
			accountID:  "reviewf356-63c-subset-chain",
			bLegID:     "b-f356-63c-subset-chain",
			relationships: []metering.ComponentRelationship{
				{Kind: metering.RelationshipSubset, Parent: f63Key("chain_a"), Child: f63Key("chain_b")},
				{Kind: metering.RelationshipSubset, Parent: f63Key("chain_b"), Child: f63Key("chain_c")},
			},
			rules: []economics.RatingRule{
				f356LinearRule(t, "reviewf356-63c-chain-a", f63Key("chain_a"), "0"),
				f356LinearRule(t, "reviewf356-63c-chain-c", f63Key("chain_c"), "1"),
			},
			measures: []f356Measure{
				f356M(f63Key("chain_a"), "10"),
				f356M(f63Key("chain_c"), "20"),
			},
		},
	}
	for _, testCase := range rejects {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			store := open(t)
			call, exposure, rated, rateErr := f356Setup(t, store, testCase.accountID, testCase.bLegID,
				f356Tariff(t, testCase.rules, testCase.relationships), testCase.measures...)

			if !errors.Is(rateErr, testCase.wantRating) {
				t.Fatalf("RateCall err=%v, want %v; completeness=%q charge=%+v valuation=%+v",
					rateErr, testCase.wantRating, rated.CustomerValuation.Completeness, rated.CustomerCharge, rated.CustomerValuation)
			}
			if rated.CustomerValuation.Completeness == economics.CompletenessComplete {
				t.Fatalf("RateCall certified complete money from an incomplete or ambiguous partition; charge=%+v valuation=%+v",
					rated.CustomerCharge, rated.CustomerValuation)
			}
			wantCompleteness := string(rated.CustomerValuation.Completeness)

			adversarial := f356NormalizedCharge(rated)
			f356RequireCompletenessFence(t, billing.ValidateCallRatingResultForSettlement(
				adversarial, call, exposure, billing.PostingOwnerV2,
			), wantCompleteness)

			before := f356SnapshotOf(t, store, call.AccountID, call.CallID)
			f356RequirePinnedOpenV2(t, before)
			_, applyErr := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
				Call: call, Exposure: exposure, Result: adversarial, PostingOwner: billing.PostingOwnerV2,
			})
			f356RequireCompletenessFence(t, applyErr, wantCompleteness)
			after := f356SnapshotOf(t, store, call.AccountID, call.CallID)
			t.Logf("%s RateCall err=%v completeness=%q charge=%+v | apply=%v",
				testCase.name, rateErr, rated.CustomerValuation.Completeness, rated.CustomerCharge, applyErr)
			t.Logf("%s before=%+v pin=%+v after=%+v pin=%+v", testCase.name, before, before.pin, after, after.pin)
			if !f356SameSnapshot(before, after) {
				t.Fatalf("rejected %s V2 Apply caused effects:\n before=%+v\n  after=%+v", testCase.name, before, after)
			}

			// A second identical rejected apply must remain a no-op, so a
			// repeated terminal decision cannot leak a durable effect.
			if _, replayErr := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
				Call: call, Exposure: exposure, Result: adversarial, PostingOwner: billing.PostingOwnerV2,
			}); replayErr == nil {
				t.Fatalf("rejected %s must stay rejected on replay", testCase.name)
			}
			afterReplay := f356SnapshotOf(t, store, call.AccountID, call.CallID)
			if !f356SameSnapshot(after, afterReplay) {
				t.Fatalf("rejected %s replay changed durable state:\n after=%+v\n  replay=%+v", testCase.name, after, afterReplay)
			}
		})
	}

	// P2 control: the nested conserved child-only partition
	// A -> {B, C}, B -> {X, Y} with A, B, C, X, Y observed and only X, Y, C
	// priced is complete USD100 and must settle exactly once, then replay as a
	// no-op.
	t.Run("nested_conserved_cover_settles_once", func(t *testing.T) {
		store := open(t)
		a := f356Key("vendor:reviewf356_p3_a")
		b := f356Key("vendor:reviewf356_p3_b")
		c := f356Key("vendor:reviewf356_p3_c")
		x := f356Key("vendor:reviewf356_p3_x")
		y := f356Key("vendor:reviewf356_p3_y")
		relationships := []metering.ComponentRelationship{
			{Kind: metering.RelationshipPartition, Parent: a, Child: b},
			{Kind: metering.RelationshipPartition, Parent: a, Child: c},
			{Kind: metering.RelationshipPartition, Parent: b, Child: x},
			{Kind: metering.RelationshipPartition, Parent: b, Child: y},
		}
		rules := []economics.RatingRule{
			f356LinearRule(t, "reviewf356-p3-x", x, "1"),
			f356LinearRule(t, "reviewf356-p3-y", y, "1"),
			f356LinearRule(t, "reviewf356-p3-c", c, "1"),
		}
		call, exposure, rated, rateErr := f356Setup(t, store, "reviewf356-p3-nested", "b-f356-p3-nested",
			f356Tariff(t, rules, relationships),
			f356M(a, "100"), f356M(b, "60"), f356M(x, "30"), f356M(y, "30"), f356M(c, "40"),
		)
		if rateErr != nil {
			t.Fatalf("RateCall: %v; valuation=%+v", rateErr, rated.CustomerValuation)
		}
		if rated.CustomerValuation.Completeness != economics.CompletenessComplete {
			t.Fatalf("completeness=%q, want complete; valuation=%+v", rated.CustomerValuation.Completeness, rated.CustomerValuation)
		}
		if rated.CustomerCharge != (billing.Money{Nano: f356ValidChargeNano, Currency: "USD"}) {
			t.Fatalf("customer charge=%+v, want 100 USD (X30 + Y30 + C40 at USD1)", rated.CustomerCharge)
		}
		if err := billing.ValidateCallRatingResultForSettlement(rated, call, exposure, billing.PostingOwnerV2); err != nil {
			t.Fatalf("nested conserved cover must pass the V2 fence, got %v", err)
		}
		f356RequireSettlesOnce(t, store, call, exposure, rated, f356ValidChargeNano, "nested conserved control")
	})

	// 62a P1-B control: a CONSISTENT subset relationship. The parent bills its
	// own complete USD100 at USD1 and the provider reported no subset quantity,
	// so 0 <= subset <= parent holds vacuously, no subset contradiction may be
	// raised, and the ordinary aggregate-only settlement must post exactly once
	// and replay as a no-op.
	t.Run("consistent_subset_settles_once", func(t *testing.T) {
		store := open(t)
		parent := f62aKey("valid_subset_parent")
		cached := f62aKey("valid_cached_subset")
		call, exposure, rated, rateErr := f356Setup(t, store, "reviewf356-62a-valid-subset", "b-f356-62a-valid-subset",
			f356Tariff(t,
				[]economics.RatingRule{f356LinearRule(t, "reviewf356-62a-valid-parent", parent, "1")},
				[]metering.ComponentRelationship{
					{Kind: metering.RelationshipSubset, Parent: parent, Child: cached},
				}),
			f356M(parent, "100"),
		)
		if rateErr != nil {
			t.Fatalf("RateCall: %v; valuation=%+v", rateErr, rated.CustomerValuation)
		}
		if errors.Is(rateErr, billing.ErrSchemaSubsetContradiction) {
			t.Fatalf("a consistent subset relationship must never report a contradiction: %v", rateErr)
		}
		if rated.CustomerValuation.Completeness != economics.CompletenessComplete {
			t.Fatalf("completeness=%q, want complete; valuation=%+v",
				rated.CustomerValuation.Completeness, rated.CustomerValuation)
		}
		if rated.CustomerCharge != (billing.Money{Nano: f356ValidChargeNano, Currency: "USD"}) {
			t.Fatalf("customer charge=%+v, want 100 USD (parent 100 at USD1)", rated.CustomerCharge)
		}
		f356RequireSettlesOnce(t, store, call, exposure, rated, f356ValidChargeNano, "consistent subset control")
	})
}
