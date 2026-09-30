package billing_test

// Explicit rating-rule FORM matrix for the reference component rater (PR #666
// adversarial repair, TEST-ONLY).
//
// The consolidated acceptance-vector index (billing_acceptance_vectors_test.go)
// pins the frozen component-schema STATE MACHINE. What it deliberately does not
// do is enumerate the rating-rule FORMS that economics.RatingRule declares and
// prove that each one actually PARTICIPATES in the produced money. Those forms
// are exercised today only incidentally, through whichever vector happened to
// need them, so a form could resolve, be priced at zero, or even be dropped on
// the floor without any named test failing.
//
// This file is that matrix. It pins, for each declared form, the EFFECTIVE
// pre-round exact amount AND the rounded amount the rater produces, across a
// set of quantities deliberately placed on both sides of every threshold that
// form has:
//
//	explicit free, linear, block, minimum, all-units tier, graduated tier,
//	whole-context selection, period selection, conditional qualifier hit AND
//	miss, exact rational / non-terminating pre-round arithmetic, line and call
//	rounding, fixed call and submission fees as independent lines.
//
// Four cross-cutting properties are pinned across the forms rather than inside
// any single one, because they are what makes "a rule exists" different from
// "a rule participates":
//
//   - commercial relevance is EFFECTIVE, not EXISTENTIAL (TestRuleFormCommercialRelevanceIsEffective):
//     a member that resolves but can never bill money must not make a declared
//     required cover member load-bearing, and a member that CAN bill must.
//   - a fixed fee is outside the component graph (TestRuleFormFixedFeeIsIndependent):
//     it never participates in cover, containment, overlap or a whole-context
//     total, never repairs a component-level failure, and is charged once.
//   - exact arithmetic is exact (TestRuleFormExactRational, TestRuleFormRoundingPolicy):
//     a non-terminating pre-round rational stays a rational and the ROUNDING
//     POLICY, not the arithmetic, produces the integer money.
//   - a qualifier miss is not a default (TestRuleFormConditionalQualifier):
//     a non-matching condition behaves exactly like an absent rule, and an
//     equal-specificity tie is rejected at publication before any money exists.
//
// Every form is driven through BOTH review5beSeams (operator E /
// customer-policy R) except where a form is inherently single-perspective; the
// one such form (fixed fees) states the reason inline and still pins the other
// seam's documented omission.
//
// Expected amounts are written as the exact rational they denote, in the same
// forms the DTO uses: metering.Decimal.CanonicalString() ("505/1" is 50.5) and
// the LineItem rational pair for a non-terminating amount ("rat:1/3").

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// rfNone is the rendered form of an absent exact amount or rounded amount.
const rfNone = "<none>"

// rfKey is this file's component-identity factory. It reuses the r7 namespace
// so the key is schema-qualified, input-direction and token-unit, and no case
// can collide with the acceptance vectors on component-name heuristics.
func rfKey(role string) metering.ComponentKey { return r7Key("vendor:rf_" + role) }

// rfLineWant is the per-line expectation: the rendered exact amount, the
// rounded nano amount (-1 means the rounded amount must be ABSENT, which is the
// contract for a non-line rounding scope), the line status, and whether the
// line is strictly payable.
type rfLineWant struct {
	id      string
	amount  string
	rounded int64
	status  economics.RatingLineStatus
	payable bool
}

// rfExpect is the observable tuple a case pins.
type rfExpect struct {
	classes      []error
	notClasses   []error
	wantNoErr    bool
	completeness economics.Completeness
	// total is the rendered exact total: CanonicalString for a terminating
	// amount, "rat:num/den" for a non-terminating one, rfNone when the
	// valuation carries no total.
	total string
	// totalNano is the rounded total in nano units, or -1 when absent.
	totalNano int64
	lines     []rfLineWant
}

// rfCase is one fully built rating scenario for one rule form.
type rfCase struct {
	name       string
	rules      []economics.RatingRule
	schemas    []metering.ComponentSchema
	obs        []metering.Observation
	scope      string
	qualifiers []metering.Dimension
	expect     rfExpect
	// bySeam replaces the expectation for one named seam where the two rating
	// planes legitimately differ.
	bySeam map[string]rfExpect
}

// rfEmitted returns the sorted identity of every emitted line, so a case pins
// WHICH lines exist at all and not only what the present ones carry.
func rfEmitted(val economics.Valuation) []string {
	out := make([]string, 0, len(val.Lines))
	for _, line := range val.Lines {
		switch {
		case line.FixedFee != nil:
			out = append(out, "fixed:"+line.FixedFee.ID)
		case line.Component != nil:
			out = append(out, line.Component.Component)
		default:
			out = append(out, "unidentified:"+line.ID)
		}
	}
	slices.Sort(out)
	return out
}

// rfAmountText renders the line's exact pre-round amount. A terminating amount
// is a Decimal; a non-terminating one is the mutually-required exact rational
// pair, which is the ONLY representation LineItem offers for it.
func rfAmountText(line economics.LineItem) string {
	if line.Amount != nil {
		return line.Amount.CanonicalString()
	}
	if line.AmountNumerator == "" {
		return rfNone
	}
	return "rat:" + line.AmountNumerator + "/" + line.AmountDenominator
}

func rfRoundedNano(line economics.LineItem) int64 {
	if line.RoundedAmount == nil || !line.RoundedAmount.Present {
		return -1
	}
	return line.RoundedAmount.NanoUnits
}

// completenessFor is the completeness a case must report: clean money is
// complete, and any typed diagnostic degrades it to partial.
func completenessFor(clean bool) economics.Completeness {
	if clean {
		return economics.CompletenessComplete
	}
	return economics.CompletenessPartial
}

func rfTotalText(val economics.Valuation) string {
	if len(val.Totals) == 0 {
		return rfNone
	}
	total := val.Totals[0]
	if total.Amount != nil {
		return total.Amount.CanonicalString()
	}
	if total.AmountNumerator == "" {
		return rfNone
	}
	return "rat:" + total.AmountNumerator + "/" + total.AmountDenominator
}

func rfTotalNano(val economics.Valuation) int64 {
	if len(val.Totals) == 0 || !val.Totals[0].RoundedAmount.Present {
		return -1
	}
	return val.Totals[0].RoundedAmount.NanoUnits
}

func rfCheck(t *testing.T, label string, val economics.Valuation, err error, want rfExpect) {
	t.Helper()
	gotLines := rfEmitted(val)
	summary := func(format string, args ...any) string {
		return fmt.Sprintf("%s: "+format+" (completeness=%q exact total=%s rounded total=%d lines=%v)",
			append([]any{label}, append(args, val.Completeness, rfTotalText(val), rfTotalNano(val), gotLines)...)...)
	}
	if want.wantNoErr {
		if err != nil {
			t.Fatalf("%s", summary("err=%v, want no diagnostic", err))
		}
	} else {
		if err == nil {
			t.Fatalf("%s", summary("err=nil, want one of %v", want.classes))
		}
		for _, class := range want.classes {
			if !errors.Is(err, class) {
				t.Fatalf("%s", summary("err=%v, want errors.Is %v", class))
			}
		}
	}
	for _, class := range want.notClasses {
		if errors.Is(err, class) {
			t.Fatalf("%s", summary("err=%v must NOT be %v", class))
		}
	}
	if val.Completeness != want.completeness {
		t.Fatalf("%s", summary("completeness=%q, want %q", val.Completeness, want.completeness))
	}
	if total := rfTotalText(val); total != want.total {
		t.Fatalf("%s", summary("exact total=%s, want %s", total, want.total))
	}
	if nano := rfTotalNano(val); nano != want.totalNano {
		t.Fatalf("%s", summary("rounded total=%d nano, want %d", nano, want.totalNano))
	}
	emitted := make([]string, 0, len(want.lines))
	for _, line := range want.lines {
		emitted = append(emitted, line.id)
	}
	slices.Sort(emitted)
	if !slices.Equal(gotLines, emitted) {
		t.Fatalf("%s", summary("emitted lines=%v, want %v", gotLines, emitted))
	}
	byID := make(map[string]economics.LineItem, len(val.Lines))
	for _, line := range val.Lines {
		switch {
		case line.FixedFee != nil:
			byID["fixed:"+line.FixedFee.ID] = line
		case line.Component != nil:
			byID[line.Component.Component] = line
		}
	}
	for _, line := range want.lines {
		actual := byID[line.id]
		if amount := rfAmountText(actual); amount != line.amount {
			t.Fatalf("%s", summary("line %s exact amount=%s, want %s", line.id, amount, line.amount))
		}
		if rounded := rfRoundedNano(actual); rounded != line.rounded {
			t.Fatalf("%s", summary("line %s rounded=%d nano, want %d", line.id, rounded, line.rounded))
		}
		if actual.Status != line.status {
			t.Fatalf("%s", summary("line %s status=%q, want %q", line.id, actual.Status, line.status))
		}
		if smLinePositive(&actual) != line.payable {
			t.Fatalf("%s", summary("line %s payable=%v (amount %s), want %v", line.id,
				smLinePositive(&actual), rfAmountText(actual), line.payable))
		}
	}
}

// rfSeams returns the two established rating seams and asserts the pair is
// intact, so a future change to review5beSeams cannot silently reduce this
// matrix to one perspective.
func rfSeams(t *testing.T) []review5beSeam {
	t.Helper()
	seams := review5beSeams()
	if len(seams) != 2 {
		t.Fatalf("review5beSeams returned %d seams, want the operator E and customer-policy R pair", len(seams))
	}
	return seams
}

// rfRate drives one seam over one scenario. Scope and effective qualifiers are
// the two rating inputs a rule form can vary on, so both are applied on top of
// the shared b1 operator/retail input builders.
func rfRate(t *testing.T, resolved economics.TariffSnapshot, c rfCase, seam review5beSeam) (economics.Valuation, error) {
	t.Helper()
	switch seam.name {
	case "operator_E":
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("NewReferenceRater: %v", err)
		}
		in := b1OperatorInput(t, resolved, c.obs)
		if c.scope != "" {
			in.Scope = c.scope
		}
		in.EffectiveQualifiers = c.qualifiers
		return rater.Rate(context.Background(), in)
	case "customer_policy_R":
		in := b1RetailInput(t, resolved, c.obs)
		if c.scope != "" {
			in.Scope = c.scope
		}
		in.EffectiveQualifiers = c.qualifiers
		return billing.RateCustomerPolicyObservation(context.Background(), in, resolved)
	default:
		t.Fatalf("unknown seam %q", seam.name)
		return economics.Valuation{}, nil
	}
}

// rfTable runs every case of one rule form through both seams. Each case is its
// own named subtest, so a regression names the form AND the quantity.
func rfTable(t *testing.T, form string, cases []rfCase) {
	for _, testCase := range cases {
		testCase := testCase
		t.Run(form+"__"+testCase.name, func(t *testing.T) {
			t.Parallel()
			resolved := f3Resolved(t, "rf-"+form+"-"+testCase.name, testCase.rules, testCase.schemas)
			for _, seam := range rfSeams(t) {
				expect := testCase.expect
				if override, ok := testCase.bySeam[seam.name]; ok {
					expect = override
				}
				seam := seam
				t.Run(seam.name, func(t *testing.T) {
					t.Parallel()
					val, err := rfRate(t, resolved, testCase, seam)
					rfCheck(t, form+" "+testCase.name+" ["+seam.name+"] class="+smErrorClass(err), val, err, expect)
				})
			}
		})
	}
}

// rfRule is an explicitly-typed component rule. rfKey carries the namespace, so
// every case in this file states only the pricing shape under test.
func rfRule(t *testing.T, id string, kind economics.RatingRuleKind, key metering.ComponentKey) economics.RatingRule {
	t.Helper()
	return economics.RatingRule{ID: id, Kind: kind, Component: &key, Currency: "USD"}
}

func rfDec(t *testing.T, raw string) *metering.Decimal {
	t.Helper()
	d := b1Decimal(t, raw)
	return &d
}

// rfTiers builds an explicit tier ladder from (upTo, price) pairs, where an
// empty upTo marks the final unbounded tier.
func rfTiers(t *testing.T, ladder ...[2]string) []economics.RatingTier {
	t.Helper()
	out := make([]economics.RatingTier, 0, len(ladder))
	for _, entry := range ladder {
		tier := economics.RatingTier{UnitPrice: rfDec(t, entry[1])}
		if entry[0] != "" {
			tier.UpTo = rfDec(t, entry[0])
		}
		out = append(out, tier)
	}
	return out
}

// ---------------------------------------------------------------------------
// Form 1: explicit free
// ---------------------------------------------------------------------------

// TestRuleFormExplicitFree pins the declared free form. An explicit zero unit
// rate is NOT an absent rule: it resolves, emits a line, and reports
// RatingLineExplicitFree at every quantity, zero and positive alike. The line
// is never payable, and the rating stays complete and diagnostic-free, which is
// exactly what distinguishes "free" from "unknown".
func TestRuleFormExplicitFree(t *testing.T) {
	t.Parallel()
	key := rfKey("free")
	free := func(t *testing.T, id string, price string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, key)
		rule.UnitPrice = rfDec(t, price)
		return rule
	}
	cases := []rfCase{}
	for _, quantity := range []string{"0", "1", "7", "1000"} {
		cases = append(cases, rfCase{
			name:  "zero_rate_quantity_" + quantity,
			rules: []economics.RatingRule{free(t, "rf-free", "0")},
			obs:   []metering.Observation{f3Observation(t, "rf-free-"+quantity, b1Measure(t, key, quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "0/0",
				totalNano:    0,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  "0/0",
					rounded: 0,
					status:  economics.RatingLineExplicitFree,
					payable: false,
				}},
			},
		})
	}
	// The legacy empty-kind spelling of the same material must infer the same
	// linear operation, so the free verdict cannot depend on the declared kind.
	legacy := b1Rule(t, "rf-free-legacy", key, "0")
	cases = append(cases, rfCase{
		name:  "legacy_empty_kind_zero_rate_quantity_7",
		rules: []economics.RatingRule{legacy},
		obs:   []metering.Observation{f3Observation(t, "rf-free-legacy", b1Measure(t, key, "7"))},
		expect: rfExpect{
			wantNoErr:    true,
			completeness: economics.CompletenessComplete,
			total:        "0/0",
			totalNano:    0,
			lines: []rfLineWant{{
				id:      key.Component,
				amount:  "0/0",
				rounded: 0,
				status:  economics.RatingLineExplicitFree,
				payable: false,
			}},
		},
	})
	rfTable(t, "explicit_free", cases)
}

// ---------------------------------------------------------------------------
// Form 2: linear
// ---------------------------------------------------------------------------

// TestRuleFormLinear pins the base per-unit form on both sides of "no usage":
// a zero quantity at a POSITIVE rate is a rated zero (not explicit free, and
// not a missing rule), while any positive quantity bills proportionally. It
// also pins the PricePer divisor, which is the one way a linear rule states a
// per-million rate without changing the effective per-unit price.
func TestRuleFormLinear(t *testing.T) {
	t.Parallel()
	key := rfKey("linear")
	linear := func(t *testing.T, id, price, per string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, key)
		rule.UnitPrice = rfDec(t, price)
		if per != "" {
			rule.PricePer = rfDec(t, per)
		}
		return rule
	}
	type row struct {
		name     string
		quantity string
		price    string
		per      string
		amount   string
		nano     int64
		payable  bool
	}
	rows := []row{
		{"zero_quantity_positive_rate", "0", "2", "", "0/0", 0, false},
		{"quantity_1", "1", "2", "", "2/0", 2_000_000_000, true},
		{"quantity_3", "3", "2", "", "6/0", 6_000_000_000, true},
		{"quantity_1000", "1000", "2", "", "2000/0", 2_000_000_000_000, true},
		// PricePer 1000 with a 2000 price is the same effective 2/unit, so the
		// money is identical and only the stated unit price differs.
		{"quantity_1_price_per_1000", "1", "2000", "1000", "2/0", 2_000_000_000, true},
		{"quantity_1000_price_per_1000", "1000", "2000", "1000", "2000/0", 2_000_000_000_000, true},
	}
	cases := make([]rfCase, 0, len(rows))
	for _, row := range rows {
		row := row
		// A zero quantity under a positive rate is a RATED zero: it is neither
		// explicit free nor a missing rule.
		status := economics.RatingLineRated
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{linear(t, "rf-linear-"+row.name, row.price, row.per)},
			obs:   []metering.Observation{f3Observation(t, "rf-linear-"+row.name, b1Measure(t, key, row.quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.nano,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.nano,
					status:  status,
					payable: row.payable,
				}},
			},
		})
	}
	// The legacy empty-kind spelling resolves to the same linear operation.
	cases = append(cases, rfCase{
		name:  "legacy_empty_kind_quantity_3",
		rules: []economics.RatingRule{b1Rule(t, "rf-linear-legacy", key, "2")},
		obs:   []metering.Observation{f3Observation(t, "rf-linear-legacy", b1Measure(t, key, "3"))},
		expect: rfExpect{
			wantNoErr:    true,
			completeness: economics.CompletenessComplete,
			total:        "6/0",
			totalNano:    6_000_000_000,
			lines: []rfLineWant{{
				id:      key.Component,
				amount:  "6/0",
				rounded: 6_000_000_000,
				status:  economics.RatingLineRated,
				payable: true,
			}},
		},
	})
	rfTable(t, "linear", cases)
}

// ---------------------------------------------------------------------------
// Form 3: block
// ---------------------------------------------------------------------------

// TestRuleFormBlock pins the block form: ceil(quantity / block) whole blocks are
// billed at the per-unit rate, so every quantity in (0, block] costs one block
// and the money is a STEP function of the quantity, not a linear one. The table
// places cases on both sides of the block size and of the second block boundary.
func TestRuleFormBlock(t *testing.T) {
	t.Parallel()
	key := rfKey("block")
	block := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleBlock, key)
		rule.UnitPrice = rfDec(t, "0.01")
		rule.BlockSize = rfDec(t, "100")
		return rule
	}
	rows := []struct {
		name     string
		quantity string
		amount   string
		nano     int64
	}{
		// Zero usage bills zero blocks: a rated zero, never a missing rule.
		{"zero_quantity_zero_blocks", "0", "0/0", 0},
		{"quantity_1_rounds_up_to_one_block", "1", "1/0", 1_000_000_000},
		{"quantity_99_rounds_up_to_one_block", "99", "1/0", 1_000_000_000},
		{"quantity_100_exactly_one_block", "100", "1/0", 1_000_000_000},
		{"quantity_101_two_blocks", "101", "2/0", 2_000_000_000},
		{"quantity_200_exactly_two_blocks", "200", "2/0", 2_000_000_000},
		{"quantity_201_three_blocks", "201", "3/0", 3_000_000_000},
	}
	cases := make([]rfCase, 0, len(rows))
	for _, row := range rows {
		row := row
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{block(t, "rf-block-"+row.name)},
			obs:   []metering.Observation{f3Observation(t, "rf-block-"+row.name, b1Measure(t, key, row.quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.nano,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.nano,
					status:  economics.RatingLineRated,
					payable: row.amount != "0/0",
				}},
			},
		})
	}
	// The legacy empty-kind spelling of a block-only rule infers the block
	// operation (block size present, no minimum floor).
	cases = append(cases, rfCase{
		name: "legacy_empty_kind_block_quantity_101",
		rules: []economics.RatingRule{func() economics.RatingRule {
			rule := b1Rule(t, "rf-block-legacy", key, "0.01")
			rule.BlockSize = rfDec(t, "100")
			return rule
		}()},
		obs: []metering.Observation{f3Observation(t, "rf-block-legacy", b1Measure(t, key, "101"))},
		expect: rfExpect{
			wantNoErr:    true,
			completeness: economics.CompletenessComplete,
			total:        "2/0",
			totalNano:    2_000_000_000,
			lines: []rfLineWant{{
				id:      key.Component,
				amount:  "2/0",
				rounded: 2_000_000_000,
				status:  economics.RatingLineRated,
				payable: true,
			}},
		},
	})
	// Block and minimum composed on one legacy rule: the block ceiling applies
	// first, then the floor. The declared block kind may not carry a floor, so
	// the composition is only reachable through the legacy linear spelling, and
	// it is pinned here so the interaction cannot drift unnoticed.
	combined := func() economics.RatingRule {
		rule := b1Rule(t, "rf-block-min", key, "0.01")
		rule.BlockSize = rfDec(t, "100")
		rule.MinimumAmount = rfDec(t, "0.5")
		return rule
	}()
	for _, row := range []struct {
		name     string
		quantity string
		amount   string
		nano     int64
	}{
		// Zero usage bills zero blocks, so the FLOOR is what makes the line pay.
		// The floor is declared as 0.5, which normalises to coefficient 5 scale 1.
		{"combined_block_and_minimum_zero_quantity_pays_the_floor", "0", "5/1", 500_000_000},
		{"combined_block_and_minimum_quantity_1_one_block_clears_floor", "1", "1/0", 1_000_000_000},
		{"combined_block_and_minimum_quantity_101_two_blocks", "101", "2/0", 2_000_000_000},
	} {
		row := row
		rule := combined
		rule.ID = "rf-block-min-" + row.name
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{rule},
			obs:   []metering.Observation{f3Observation(t, "rf-block-min-"+row.name, b1Measure(t, key, row.quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.nano,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.nano,
					status:  economics.RatingLineRated,
					payable: true,
				}},
			},
		})
	}
	rfTable(t, "block", cases)
}

// ---------------------------------------------------------------------------
// Form 4: minimum
// ---------------------------------------------------------------------------

// TestRuleFormMinimum pins the floor form across the minimum boundary, and pins
// the two directions of requirement 2 at the same time: a ZERO quantity under
// a positive floor is a POSITIVE payable charge (the floor is what is owed, and
// it is distinguishable from an explicit free line), while a positive quantity
// under a zero rate with a zero floor is FREE.
func TestRuleFormMinimum(t *testing.T) {
	t.Parallel()
	key := rfKey("minimum")
	floor := func(t *testing.T, id, price, minimum string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleMinimum, key)
		rule.UnitPrice = rfDec(t, price)
		rule.MinimumAmount = rfDec(t, minimum)
		return rule
	}
	cases := []rfCase{}
	for _, row := range []struct {
		name     string
		quantity string
		amount   string
		nano     int64
		status   economics.RatingLineStatus
		payable  bool
	}{
		{"zero_quantity_pays_the_positive_floor", "0", "5/0", 5_000_000_000, economics.RatingLineRated, true},
		{"quantity_1_raises_to_the_floor", "1", "5/0", 5_000_000_000, economics.RatingLineRated, true},
		{"quantity_4_raises_to_the_floor", "4", "5/0", 5_000_000_000, economics.RatingLineRated, true},
		// Exactly on the boundary the amount already equals the floor, so the
		// floor is a no-op rather than a silent uplift.
		{"quantity_5_exactly_on_the_floor", "5", "5/0", 5_000_000_000, economics.RatingLineRated, true},
		{"quantity_6_above_the_floor", "6", "6/0", 6_000_000_000, economics.RatingLineRated, true},
		{"quantity_10_above_the_floor", "10", "10/0", 10_000_000_000, economics.RatingLineRated, true},
	} {
		row := row
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{floor(t, "rf-min-"+row.name, "1", "5")},
			obs:   []metering.Observation{f3Observation(t, "rf-min-"+row.name, b1Measure(t, key, row.quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.nano,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.nano,
					status:  row.status,
					payable: row.payable,
				}},
			},
		})
	}
	// A zero rate under a POSITIVE floor still pays: the floor, not the rate,
	// is the money. This is the case an existential "does this rule have money
	// in it" check gets wrong.
	cases = append(cases, rfCase{
		name:  "zero_rate_positive_floor_quantity_10_still_pays",
		rules: []economics.RatingRule{floor(t, "rf-min-zero-rate", "0", "5")},
		obs:   []metering.Observation{f3Observation(t, "rf-min-zero-rate", b1Measure(t, key, "10"))},
		expect: rfExpect{
			wantNoErr:    true,
			completeness: economics.CompletenessComplete,
			total:        "5/0",
			totalNano:    5_000_000_000,
			lines: []rfLineWant{{
				id:      key.Component,
				amount:  "5/0",
				rounded: 5_000_000_000,
				status:  economics.RatingLineRated,
				payable: true,
			}},
		},
	})
	// The mirror: a zero rate under a ZERO floor is free at every quantity.
	cases = append(cases, rfCase{
		name:  "zero_rate_zero_floor_quantity_10_is_free",
		rules: []economics.RatingRule{floor(t, "rf-min-zero-floor", "0", "0")},
		obs:   []metering.Observation{f3Observation(t, "rf-min-zero-floor", b1Measure(t, key, "10"))},
		expect: rfExpect{
			wantNoErr:    true,
			completeness: economics.CompletenessComplete,
			total:        "0/0",
			totalNano:    0,
			lines: []rfLineWant{{
				id:      key.Component,
				amount:  "0/0",
				rounded: 0,
				status:  economics.RatingLineExplicitFree,
				payable: false,
			}},
		},
	})
	rfTable(t, "minimum", cases)
}

// ---------------------------------------------------------------------------
// Form 5: all-units tier
// ---------------------------------------------------------------------------

// TestRuleFormAllUnitsTier pins the all-units form: ONE tier is selected by the
// quantity and its price is applied to EVERY unit, so the money jumps at each
// boundary. Cases sit on every declared boundary and one step past it. The
// second ladder uses a 5e-10 price so the same form also sits exactly on the
// sub-nano rounding tie, under every policy the SDK exposes.
func TestRuleFormAllUnitsTier(t *testing.T) {
	t.Parallel()
	key := rfKey("all_units")
	ladder := func(t *testing.T, id string, tiers [][2]string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleAllUnits, key)
		rule.TierMode = economics.TierAllUnits
		rule.Tiers = rfTiers(t, tiers...)
		return rule
	}
	coarse := func() [][2]string {
		return [][2]string{{"100", "1"}, {"1000", "0.5"}, {"", "0.1"}}
	}
	cases := []rfCase{}
	for _, row := range []struct {
		name     string
		quantity string
		amount   string
		nano     int64
	}{
		// Zero quantity still selects the first tier (0 <= 100) and bills zero.
		{"zero_quantity_selects_first_tier", "0", "0/0", 0},
		{"quantity_1_all_units_at_first_tier", "1", "1/0", 1_000_000_000},
		// 100 is INCLUSIVE: the all-units price still applies to every unit.
		{"quantity_100_boundary_still_all_units_at_first_tier", "100", "100/0", 100_000_000_000},
		// One unit past the boundary reprices ALL 101 units, not just the excess.
		{"quantity_101_all_units_reprice", "101", "505/1", 50_500_000_000},
		{"quantity_1000_boundary_all_units_at_second_tier", "1000", "500/0", 500_000_000_000},
		{"quantity_1001_all_units_at_final_tier", "1001", "1001/1", 100_100_000_000},
	} {
		row := row
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{ladder(t, "rf-au-"+row.name, coarse())},
			obs:   []metering.Observation{f3Observation(t, "rf-au-"+row.name, b1Measure(t, key, row.quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.nano,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.nano,
					status:  economics.RatingLineRated,
					payable: row.amount != "0/0",
				}},
			},
		})
	}
	// The sub-nano ladder. 5e-10 per unit puts quantity 1 at exactly half a nano
	// and quantity 3 at exactly one and a half nanos, so the same ladder pins
	// the tier boundary at 2 AND both rounding ties at once.
	nanoLadder := [][2]string{{"2", "0.0000000005"}, {"", "0.0000000005"}}
	for _, row := range []struct {
		policy  economics.RoundingPolicy
		name    string
		amount  string
		exact   string
		rounded int64
	}{
		{economics.RoundingHalfAwayFromZero, "quantity_1_tie_half_away_from_zero", "5/10", "0.5", 1},
		{economics.RoundingHalfEven, "quantity_1_tie_half_even", "5/10", "0.5", 0},
		{economics.RoundingTowardZero, "quantity_1_tie_toward_zero", "5/10", "0.5", 0},
		{economics.RoundingFloor, "quantity_1_tie_floor", "5/10", "0.5", 0},
		{economics.RoundingHalfAwayFromZero, "quantity_3_tie_half_away_from_zero", "15/10", "1.5", 2},
		{economics.RoundingHalfEven, "quantity_3_tie_half_even", "15/10", "1.5", 2},
		{economics.RoundingTowardZero, "quantity_3_tie_toward_zero", "15/10", "1.5", 1},
		{economics.RoundingFloor, "quantity_3_tie_floor", "15/10", "1.5", 1},
	} {
		row := row
		rule := ladder(t, "rf-au-"+row.name, nanoLadder)
		rule.RoundingScope = economics.RoundingScopeLine
		rule.RoundingPolicy = row.policy
		quantity := "1"
		if row.amount == "15/10" {
			quantity = "3"
		}
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{rule},
			obs:   []metering.Observation{f3Observation(t, "rf-au-"+row.name, b1Measure(t, key, quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.rounded,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.rounded,
					status:  economics.RatingLineRated,
					payable: true,
				}},
			},
		})
	}
	rfTable(t, "all_units_tier", cases)
}

// ---------------------------------------------------------------------------
// Form 6: graduated tier
// ---------------------------------------------------------------------------

// TestRuleFormGraduatedTier pins the graduated form: each tier prices only its
// OWN slice of the quantity, so the money is continuous where the all-units
// form jumps. The shared ladder makes the difference explicit at quantity 150:
// graduated charges 100*1 + 50*0.5 = 125 where all-units charges 150*0.5 = 75.
func TestRuleFormGraduatedTier(t *testing.T) {
	t.Parallel()
	key := rfKey("graduated")
	ladder := func(t *testing.T, id string, tiers [][2]string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleGraduated, key)
		rule.TierMode = economics.TierGraduated
		rule.Tiers = rfTiers(t, tiers...)
		return rule
	}
	coarse := [][2]string{{"100", "1"}, {"1000", "0.5"}, {"", "0.1"}}
	cases := []rfCase{}
	for _, row := range []struct {
		name     string
		quantity string
		amount   string
		nano     int64
	}{
		{"zero_quantity_bills_no_slice", "0", "0/0", 0},
		{"quantity_1_first_slice_only", "1", "1/0", 1_000_000_000},
		{"quantity_50_first_slice_only", "50", "50/0", 50_000_000_000},
		{"quantity_100_exactly_first_slice", "100", "100/0", 100_000_000_000},
		// Boundary + 1: 100 units at the first tier and 1 unit at the second.
		{"quantity_101_second_slice_starts", "101", "1005/1", 100_500_000_000},
		// The discriminating quantity. Graduated 125, all-units 75.
		{"quantity_150_mid_second_slice", "150", "125/0", 125_000_000_000},
		{"quantity_1000_exactly_two_slices", "1000", "550/0", 550_000_000_000},
		{"quantity_1001_final_slice_starts", "1001", "5501/1", 550_100_000_000},
		{"quantity_1500_final_slice", "1500", "600/0", 600_000_000_000},
	} {
		row := row
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{ladder(t, "rf-grad-"+row.name, coarse)},
			obs:   []metering.Observation{f3Observation(t, "rf-grad-"+row.name, b1Measure(t, key, row.quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.nano,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.nano,
					status:  economics.RatingLineRated,
					payable: row.amount != "0/0",
				}},
			},
		})
	}
	// The sub-nano graduated ladder, which makes each slice 5e-10 and therefore
	// puts quantity 1 exactly on the half-nano tie while quantity 2 lands on an
	// exact nano.
	nanoLadder := [][2]string{{"1", "0.0000000005"}, {"", "0.0000000005"}}
	for _, row := range []struct {
		policy   economics.RoundingPolicy
		name     string
		quantity string
		amount   string
		rounded  int64
	}{
		{economics.RoundingHalfAwayFromZero, "quantity_1_tie_half_away_from_zero", "1", "5/10", 1},
		{economics.RoundingHalfEven, "quantity_1_tie_half_even", "1", "5/10", 0},
		{economics.RoundingTowardZero, "quantity_1_tie_toward_zero", "1", "5/10", 0},
		{economics.RoundingFloor, "quantity_1_tie_floor", "1", "5/10", 0},
		{economics.RoundingHalfAwayFromZero, "quantity_2_two_slices_land_on_exact_nano", "2", "1/9", 1},
		{economics.RoundingHalfEven, "quantity_2_two_slices_half_even", "2", "1/9", 1},
	} {
		row := row
		rule := ladder(t, "rf-grad-"+row.name, nanoLadder)
		rule.RoundingScope = economics.RoundingScopeLine
		rule.RoundingPolicy = row.policy
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{rule},
			obs:   []metering.Observation{f3Observation(t, "rf-grad-"+row.name, b1Measure(t, key, row.quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.rounded,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.rounded,
					status:  economics.RatingLineRated,
					payable: true,
				}},
			},
		})
	}
	rfTable(t, "graduated_tier", cases)
}

// ---------------------------------------------------------------------------
// Form 7: whole-context selection
// ---------------------------------------------------------------------------

// TestRuleFormWholeContextSelection pins SelectionScope whole_context: the tier
// is selected from the AGGREGATE quantity of every component that shares the
// priced component's direction, unit and schema, while only the priced
// component's own quantity is charged. A sibling is added purely to move the
// context across the boundary, so the assertion that the priced amount jumped
// is proof that the sibling participated in selection.
//
// The block and minimum sub-tables pin the complementary half: those modifiers
// are applied to the BILLABLE quantity even on a whole-context rule, so the
// selection scope changes the rate and never the billed units.
func TestRuleFormWholeContextSelection(t *testing.T) {
	t.Parallel()
	priced := rfKey("wc_priced")
	sibling := rfKey("wc_sibling")
	ladder := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleAllUnits, priced)
		rule.TierMode = economics.TierAllUnits
		rule.SelectionScope = economics.SelectionWholeContext
		rule.Tiers = rfTiers(t, [2]string{"50", "1"}, [2]string{"", "0.1"})
		return rule
	}
	siblingRule := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, sibling)
		rule.UnitPrice = rfDec(t, "1000")
		return rule
	}
	cases := []rfCase{}
	for _, row := range []struct {
		name string
		// siblingQty is the only thing that moves the context across the
		// boundary; pricedQty is the amount actually charged.
		siblingQty string
		pricedAmt  string
		// pricedNano is stated by hand rather than derived, so the case where
		// the selected tier changes is pinned independently of the amount.
		pricedNano int64
		siblingAmt string
		total      string
	}{
		// Context 10 and 49 are at or below the boundary, so every unit of the
		// priced component bills at 1.
		{"context_10_below_boundary", "0", "10/0", 10_000_000_000, "0/0", "10/0"},
		{"context_49_one_below_boundary", "39", "10/0", 10_000_000_000, "39000/0", "39010/0"},
		// The boundary is inclusive at 50.
		{"context_50_exactly_on_boundary", "40", "10/0", 10_000_000_000, "40000/0", "40010/0"},
		// One context unit past the boundary reprices the priced component's
		// own 10 units from 1 to 0.1.
		{"context_51_one_past_boundary", "41", "1/0", 1_000_000_000, "41000/0", "41001/0"},
		{"context_100_well_past_boundary", "90", "1/0", 1_000_000_000, "90000/0", "90001/0"},
	} {
		row := row
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{ladder(t, "rf-wc-"+row.name), siblingRule(t, "rf-wc-sib-"+row.name)},
			obs: []metering.Observation{f3Observation(t, "rf-wc-"+row.name,
				b1Measure(t, priced, "10"), b1Measure(t, sibling, row.siblingQty))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.total,
				totalNano:    mustNano(t, row.total),
				lines: []rfLineWant{
					{id: priced.Component, amount: row.pricedAmt, rounded: row.pricedNano, status: economics.RatingLineRated, payable: true},
					{id: sibling.Component, amount: row.siblingAmt, rounded: mustNano(t, row.siblingAmt), status: economics.RatingLineRated, payable: row.siblingAmt != "0/0"},
				},
			},
		})
	}
	// The modifier half: a whole-context block rule still bills whole blocks of
	// its OWN quantity. The context here is 1002 (2 + 1000), far past any block
	// boundary, so a context-driven block ceiling would have billed 1100 units.
	blockKey := rfKey("wc_block")
	block := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleBlock, blockKey)
		rule.UnitPrice = rfDec(t, "1")
		rule.BlockSize = rfDec(t, "10")
		rule.SelectionScope = economics.SelectionWholeContext
		return rule
	}
	cases = append(cases, rfCase{
		name:  "block_modifier_applies_to_the_billable_quantity_not_the_context",
		rules: []economics.RatingRule{block(t, "rf-wc-block"), siblingRule(t, "rf-wc-block-sib")},
		obs: []metering.Observation{f3Observation(t, "rf-wc-block",
			b1Measure(t, blockKey, "2"), b1Measure(t, sibling, "1000"))},
		expect: rfExpect{
			wantNoErr:    true,
			completeness: economics.CompletenessComplete,
			total:        "1000010/0",
			totalNano:    mustNano(t, "1000010/0"),
			lines: []rfLineWant{
				// 2 units round up to one 10-unit block at 1/unit.
				{id: blockKey.Component, amount: "10/0", rounded: 10_000_000_000, status: economics.RatingLineRated, payable: true},
				{id: sibling.Component, amount: "1000000/0", rounded: mustNano(t, "1000000/0"), status: economics.RatingLineRated, payable: true},
			},
		},
	})
	// The same for the floor: the minimum is an absolute amount, never a
	// fraction of the context.
	floorKey := rfKey("wc_floor")
	floor := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleMinimum, floorKey)
		rule.UnitPrice = rfDec(t, "1")
		rule.MinimumAmount = rfDec(t, "5")
		rule.SelectionScope = economics.SelectionWholeContext
		return rule
	}
	cases = append(cases, rfCase{
		name:  "minimum_modifier_is_absolute_not_a_share_of_the_context",
		rules: []economics.RatingRule{floor(t, "rf-wc-floor"), siblingRule(t, "rf-wc-floor-sib")},
		obs: []metering.Observation{f3Observation(t, "rf-wc-floor",
			b1Measure(t, floorKey, "2"), b1Measure(t, sibling, "1000"))},
		expect: rfExpect{
			wantNoErr:    true,
			completeness: economics.CompletenessComplete,
			total:        "1000005/0",
			totalNano:    mustNano(t, "1000005/0"),
			lines: []rfLineWant{
				{id: floorKey.Component, amount: "5/0", rounded: 5_000_000_000, status: economics.RatingLineRated, payable: true},
				{id: sibling.Component, amount: "1000000/0", rounded: mustNano(t, "1000000/0"), status: economics.RatingLineRated, payable: true},
			},
		},
	})
	rfTable(t, "whole_context", cases)
}

// mustNano converts a rendered whole-number amount ("39000/0") into its nano
// value so a sibling's rounded expectation is derived from the same string the
// case declares, rather than restated by hand.
func mustNano(t *testing.T, amount string) int64 {
	t.Helper()
	d := b1Decimal(t, strings.TrimSuffix(amount, "/0"))
	rat, err := d.ToRat()
	if err != nil {
		t.Fatalf("ToRat(%q): %v", d.CanonicalString(), err)
	}
	nanos := new(big.Rat).Mul(rat, new(big.Rat).SetInt64(1_000_000_000))
	if !nanos.IsInt() {
		t.Fatalf("amount %q does not round to a whole nano amount", amount)
	}
	nano := nanos.Num().Int64()
	return nano
}

// ---------------------------------------------------------------------------
// Form 8: period selection
// ---------------------------------------------------------------------------

// TestRuleFormPeriodSelection pins SelectionScope period. It selects exactly
// like whole_context, and it additionally refuses to run outside a period
// valuation: the same rule in a call-scoped valuation is rejected with
// ErrPeriodScopeRequired and produces no money at all, rather than silently
// rating on the billable quantity. The period ROUNDING scope is pinned here too
// because the SDK binds the same period fence to it.
func TestRuleFormPeriodSelection(t *testing.T) {
	t.Parallel()
	priced := rfKey("pd_priced")
	sibling := rfKey("pd_sibling")
	ladder := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleAllUnits, priced)
		rule.TierMode = economics.TierAllUnits
		rule.SelectionScope = economics.SelectionPeriod
		rule.Tiers = rfTiers(t, [2]string{"50", "1"}, [2]string{"", "0.1"})
		return rule
	}
	siblingRule := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, sibling)
		rule.UnitPrice = rfDec(t, "1000")
		return rule
	}
	cases := []rfCase{}
	for _, row := range []struct {
		name       string
		siblingQty string
		pricedAmt  string
		pricedNano int64
		siblingAmt string
		total      string
	}{
		{"period_context_49_below_boundary", "39", "10/0", 10_000_000_000, "39000/0", "39010/0"},
		{"period_context_50_exactly_on_boundary", "40", "10/0", 10_000_000_000, "40000/0", "40010/0"},
		{"period_context_51_one_past_boundary", "41", "1/0", 1_000_000_000, "41000/0", "41001/0"},
	} {
		row := row
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{ladder(t, "rf-pd-"+row.name), siblingRule(t, "rf-pd-sib-"+row.name)},
			scope: "period:2026-09",
			obs: []metering.Observation{f3Observation(t, "rf-pd-"+row.name,
				b1Measure(t, priced, "10"), b1Measure(t, sibling, row.siblingQty))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.total,
				totalNano:    mustNano(t, row.total),
				lines: []rfLineWant{
					{id: priced.Component, amount: row.pricedAmt, rounded: row.pricedNano, status: economics.RatingLineRated, payable: true},
					{id: sibling.Component, amount: row.siblingAmt, rounded: mustNano(t, row.siblingAmt), status: economics.RatingLineRated, payable: true},
				},
			},
		})
	}
	// The period fence: the identical rule in a call-scoped valuation.
	cases = append(cases, rfCase{
		name:  "call_scoped_valuation_refuses_the_period_selection_rule",
		rules: []economics.RatingRule{ladder(t, "rf-pd-fenced"), siblingRule(t, "rf-pd-fenced-sib")},
		scope: "call:call-b1",
		obs: []metering.Observation{f3Observation(t, "rf-pd-fenced",
			b1Measure(t, priced, "10"), b1Measure(t, sibling, "41"))},
		expect: rfExpect{
			classes:      []error{billing.ErrPeriodScopeRequired},
			completeness: economics.CompletenessPartial,
			// The sibling still rates; only the period-selected line is refused.
			total:     "41000/0",
			totalNano: 41_000_000_000_000,
			lines: []rfLineWant{
				{id: priced.Component, amount: rfNone, rounded: -1, status: economics.RatingLineRateUnsupported},
				{id: sibling.Component, amount: "41000/0", rounded: 41_000_000_000_000, status: economics.RatingLineRated, payable: true},
			},
		},
	})
	// The period ROUNDING scope. A non-line scope means the line carries the
	// exact value ONLY and the period total is the rounding boundary, so the
	// line's rounded amount must be ABSENT and the total is what rounds.
	periodRounded := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, priced)
		rule.RateNumerator = rfDec(t, "1")
		rule.RateDenominator = rfDec(t, "2000000000")
		rule.RoundingScope = economics.RoundingScopePeriod
		rule.RoundingPolicy = economics.RoundingHalfAwayFromZero
		return rule
	}
	cases = append(
		cases,
		rfCase{
			name:  "period_rounding_scope_rounds_only_at_the_period_total",
			rules: []economics.RatingRule{periodRounded(t, "rf-pd-round")},
			scope: "period:2026-09",
			obs:   []metering.Observation{f3Observation(t, "rf-pd-round", b1Measure(t, priced, "1"))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "5/10",
				totalNano:    1,
				lines: []rfLineWant{{
					id:      priced.Component,
					amount:  "5/10",
					rounded: -1,
					status:  economics.RatingLineRated,
					payable: true,
				}},
			},
		},
		rfCase{
			name:  "call_scoped_valuation_refuses_the_period_rounding_scope",
			rules: []economics.RatingRule{periodRounded(t, "rf-pd-round-fenced")},
			scope: "call:call-b1",
			obs:   []metering.Observation{f3Observation(t, "rf-pd-round-fenced", b1Measure(t, priced, "1"))},
			expect: rfExpect{
				classes:      []error{billing.ErrPeriodScopeRequired},
				completeness: economics.CompletenessPartial,
				total:        rfNone,
				totalNano:    -1,
				lines: []rfLineWant{{
					id:      priced.Component,
					amount:  rfNone,
					rounded: -1,
					status:  economics.RatingLineRateUnsupported,
				}},
			},
		},
	)
	rfTable(t, "period_selection", cases)
}

// ---------------------------------------------------------------------------
// Form 9: conditional qualifier hit AND miss
// ---------------------------------------------------------------------------

// TestRuleFormConditionalQualifier pins both directions of the qualifier form
// and the selection rule the SDK defines for it.
//
//	resolveRule keeps every rule whose QualifierCondition set matches the
//	effective qualifiers and then picks the candidate with the greatest
//	specificity (component dimensions + conditions). A condition whose name is
//	present with a different value, or whose name is absent entirely, is simply
//	not a candidate.
//
// Two sub-tables, because the two are genuinely different:
//
//   - with a general rule present, a non-matching or absent condition falls
//     back to the general rule, which is itself a legitimate candidate rather
//     than a silent default. The money changes, so both numbers are pinned.
//   - with only the conditional rule declared, a non-matching condition behaves
//     exactly like an absent rule (ErrRateMissing, no line amount) and an
//     absent qualifier name is reported as the SDK's own fail-closed
//     ErrQualifierMissing instead.
//
// DOCUMENTED GAP (named, measured, left as-is): the SDK documents
// QualifierCondition as "a missing qualifier never matches the condition; the
// billing domain turns that miss into an explicit fail-closed diagnostic", but
// the fail-closed ErrQualifierMissing diagnostic is only raised when NO candidate
// remains. When a general rule also exists for the component, an ABSENT
// qualifier name silently prices at the general rate with no diagnostic at all,
// which is the one shape in this file where a missing condition does change the
// money without anything being reported. The cases below pin that measured
// behaviour; they do not assert the documented intent.
func TestRuleFormConditionalQualifier(t *testing.T) {
	t.Parallel()
	key := rfKey("cond")
	general := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, key)
		rule.UnitPrice = rfDec(t, "1")
		return rule
	}
	gold := func(t *testing.T, id string, price string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, key)
		rule.UnitPrice = rfDec(t, price)
		rule.Conditions = []economics.QualifierCondition{{Name: "plan", Value: "gold"}}
		return rule
	}
	cases := []rfCase{}
	for _, row := range []struct {
		name       string
		qualifiers []metering.Dimension
		amount     string
		nano       int64
	}{
		// HIT: the conditional rule is strictly more specific and wins.
		{"hit_conditional_rule_is_selected", []metering.Dimension{{Name: "plan", Value: "gold"}}, "30/0", 30_000_000_000},
		// MISS on value: the conditional rule is not a candidate, the general
		// rule is, and the general rate is billed.
		{"miss_on_value_falls_back_to_the_general_rule", []metering.Dimension{{Name: "plan", Value: "silver"}}, "10/0", 10_000_000_000},
		// MISS because the qualifier is absent entirely. See the DOCUMENTED GAP
		// note above: the same fallback, with no diagnostic at all.
		{"absent_qualifier_falls_back_to_the_general_rule_without_a_diagnostic", nil, "10/0", 10_000_000_000},
	} {
		row := row
		cases = append(cases, rfCase{
			name:       row.name,
			rules:      []economics.RatingRule{general(t, "rf-cond-gen-"+row.name), gold(t, "rf-cond-gold-"+row.name, "3")},
			qualifiers: row.qualifiers,
			obs:        []metering.Observation{f3Observation(t, "rf-cond-"+row.name, b1Measure(t, key, "10"))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.nano,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.nano,
					status:  economics.RatingLineRated,
					payable: true,
				}},
			},
		})
	}
	// Only the conditional rule exists, so a miss has nothing to fall back to.
	for _, row := range []struct {
		name       string
		qualifiers []metering.Dimension
		wantNoErr  bool
		classes    []error
		status     economics.RatingLineStatus
		amount     string
		total      string
		nano       int64
		rounded    int64
	}{
		{
			name: "hit_only_conditional_rule_prices_it", qualifiers: []metering.Dimension{{Name: "plan", Value: "gold"}}, wantNoErr: true,
			status: economics.RatingLineRated, amount: "30/0", total: "30/0", nano: 30_000_000_000, rounded: 30_000_000_000,
		},
		{
			name: "miss_on_value_behaves_exactly_as_an_absent_rule", qualifiers: []metering.Dimension{{Name: "plan", Value: "silver"}},
			classes: []error{billing.ErrRateMissing}, status: economics.RatingLineRateMissing, amount: rfNone, total: rfNone, nano: -1, rounded: -1,
		},
		{
			name:    "absent_qualifier_is_the_sdk_fail_closed_diagnostic",
			classes: []error{billing.ErrQualifierMissing}, status: economics.RatingLineRateUnsupported, amount: rfNone, total: rfNone, nano: -1, rounded: -1,
		},
	} {
		row := row
		cases = append(cases, rfCase{
			name:       row.name,
			rules:      []economics.RatingRule{gold(t, "rf-cond-only-"+row.name, "3")},
			qualifiers: row.qualifiers,
			obs:        []metering.Observation{f3Observation(t, "rf-cond-"+row.name, b1Measure(t, key, "10"))},
			expect: rfExpect{
				wantNoErr:    row.wantNoErr,
				classes:      row.classes,
				completeness: completenessFor(row.wantNoErr),
				total:        row.total,
				totalNano:    row.nano,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.rounded,
					status:  row.status,
					payable: row.nano > 0,
				}},
			},
		})
	}
	rfTable(t, "conditional_qualifier", cases)
}

// TestRuleFormConditionalQualifierSpecificityTieIsRejectedAtPublication pins the
// other half of requirement 6: two same-specificity conditional rules for one
// component would make selection depend on source order, so the reference rater
// refuses to construct at all. The money therefore cannot silently change at
// replay time - the tariff never becomes rateable in the first place.
//
// This case cannot be expressed in the table above because every rating form
// needs a constructed rater, and construction is exactly what fails.
func TestRuleFormConditionalQualifierSpecificityTieIsRejectedAtPublication(t *testing.T) {
	t.Parallel()
	key := rfKey("cond_tie")
	left := rfRule(t, "rf-tie-plan", economics.RatingRuleLinear, key)
	left.UnitPrice = rfDec(t, "3")
	left.Conditions = []economics.QualifierCondition{{Name: "plan", Value: "gold"}}
	right := rfRule(t, "rf-tie-region", economics.RatingRuleLinear, key)
	right.UnitPrice = rfDec(t, "4")
	right.Conditions = []economics.QualifierCondition{{Name: "region", Value: "eu"}}
	// Both conditions match the same effective qualifiers and both selectors
	// have specificity 1.
	snapshot, err := economics.BuildTariffSnapshot(
		economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "rf-cond-tie", Version: "v1"}, RaterID: "reference"},
		"USD", []economics.RatingRule{left, right},
	)
	if err != nil {
		t.Fatalf("BuildTariffSnapshot: %v", err)
	}
	if _, err := billing.NewReferenceRater(snapshot); !errors.Is(err, economics.ErrRatingRuleOverlap) {
		t.Fatalf("equal-specificity conditional rules must be rejected at publication, got err=%v", err)
	}

	// The strict ordering the SDK does define: one rule conditioned on two
	// qualifiers beats a rule conditioned on one, with no tie possible.
	strict := rfRule(t, "rf-strict-plan", economics.RatingRuleLinear, key)
	strict.UnitPrice = rfDec(t, "3")
	strict.Conditions = []economics.QualifierCondition{{Name: "plan", Value: "gold"}}
	refined := rfRule(t, "rf-strict-region", economics.RatingRuleLinear, key)
	refined.UnitPrice = rfDec(t, "4")
	refined.Conditions = []economics.QualifierCondition{{Name: "plan", Value: "gold"}, {Name: "region", Value: "eu"}}
	rfTable(t, "conditional_qualifier_specificity", []rfCase{{
		name:  "two_condition_rule_beats_one_condition_rule",
		rules: []economics.RatingRule{strict, refined},
		qualifiers: []metering.Dimension{
			{Name: "plan", Value: "gold"}, {Name: "region", Value: "eu"},
		},
		obs: []metering.Observation{f3Observation(t, "rf-cond-strict", b1Measure(t, key, "10"))},
		expect: rfExpect{
			wantNoErr:    true,
			completeness: economics.CompletenessComplete,
			total:        "40/0",
			totalNano:    40_000_000_000,
			lines: []rfLineWant{{
				id:      key.Component,
				amount:  "40/0",
				rounded: 40_000_000_000,
				status:  economics.RatingLineRated,
				payable: true,
			}},
		},
	}})
}

// ---------------------------------------------------------------------------
// Form 10: exact rational / non-terminating pre-round arithmetic
// ---------------------------------------------------------------------------

// TestRuleFormExactRational pins the exactness contract with a genuinely
// non-terminating pre-round value. A rate of 1/3 over an integer quantity is
// not representable as any bounded decimal, so the rater must keep it as the
// exact rational pair (1/3, 7/3) and must NOT emit a rounded Decimal in its
// place; the integer nano amount is produced afterwards, by the rounding policy.
//
// A float implementation of the same rate would land on 0.3333333333333333 and
// on a pre-round value whose rounded money differs, so pinning the numerator
// and denominator separately from the rounded amount is the assertion that the
// arithmetic was exact.
func TestRuleFormExactRational(t *testing.T) {
	t.Parallel()
	key := rfKey("exact")
	third := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, key)
		rule.RateNumerator = rfDec(t, "1")
		rule.RateDenominator = rfDec(t, "3")
		return rule
	}
	cases := []rfCase{}
	for _, row := range []struct {
		name     string
		quantity string
		amount   string
		nano     int64
	}{
		{"one_third_is_exact", "1", "rat:1/3", 333_333_333},
		// Three thirds is exactly 1, so the same rate terminates and the DTO
		// form switches back to a Decimal. Both forms are pinned.
		{"three_thirds_is_an_exact_whole", "3", "1/0", 1_000_000_000},
		{"seven_thirds_is_exact", "7", "rat:7/3", 2_333_333_333},
	} {
		row := row
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{third(t, "rf-exact-"+row.name)},
			obs:   []metering.Observation{f3Observation(t, "rf-exact-"+row.name, b1Measure(t, key, row.quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.nano,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.nano,
					status:  economics.RatingLineRated,
					payable: true,
				}},
			},
		})
	}
	// A non-terminating RATE under a tiered form, so the exactness contract is
	// pinned for tier selection as well and not only for a base rate.
	tiered := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleAllUnits, key)
		rule.TierMode = economics.TierAllUnits
		numerator, denominator := rfDec(t, "1"), rfDec(t, "3")
		rule.Tiers = []economics.RatingTier{
			{UpTo: rfDec(t, "10"), RateNumerator: numerator, RateDenominator: denominator},
			{RateNumerator: rfDec(t, "1"), RateDenominator: rfDec(t, "7")},
		}
		return rule
	}
	cases = append(
		cases,
		rfCase{
			name:  "tiered_rate_one_third_is_exact",
			rules: []economics.RatingRule{tiered(t, "rf-exact-tier-first")},
			obs:   []metering.Observation{f3Observation(t, "rf-exact-tier-first", b1Measure(t, key, "10"))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "rat:10/3",
				totalNano:    3_333_333_333,
				lines: []rfLineWant{{
					id: key.Component, amount: "rat:10/3", rounded: 3_333_333_333,
					status: economics.RatingLineRated, payable: true,
				}},
			},
		},
		rfCase{
			// All-units reprices EVERY unit once the context passes the
			// boundary, so 11 units at 1/7 is 11/7 and not 10/3 + 1/7. The
			// assertion that distinguishes the two forms is the graduated
			// table above; here it pins that the exact rational survives a tier
			// selection as well as a base rate.
			name:  "tiered_rate_one_seventh_is_exact_for_every_selected_unit",
			rules: []economics.RatingRule{tiered(t, "rf-exact-tier-final")},
			obs:   []metering.Observation{f3Observation(t, "rf-exact-tier-final", b1Measure(t, key, "11"))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "rat:11/7",
				totalNano:    1_571_428_571,
				lines: []rfLineWant{{
					id: key.Component, amount: "rat:11/7", rounded: 1_571_428_571,
					status: economics.RatingLineRated, payable: true,
				}},
			},
		},
	)
	rfTable(t, "exact_rational", cases)
}

// ---------------------------------------------------------------------------
// Form 11: line and call rounding
// ---------------------------------------------------------------------------

// TestRuleFormRoundingPolicy pins that the ROUNDING POLICY, not the arithmetic,
// produces the integer money, at the exact half-nano ties.
//
// The rate 1/2000000000 over one unit is exactly 5e-10, i.e. exactly half a nano,
// and over three units exactly 1.5 nanos. Both are true ties, so every policy
// the SDK exposes is pinned separately at both:
//
//	0.5 nano  half_away_from_zero -> 1   half_even -> 0   toward_zero -> 0   floor -> 0
//	1.5 nano  half_away_from_zero -> 2   half_even -> 2   toward_zero -> 1   floor -> 1
//
// floor and toward_zero coincide on every case, and that is a real property and
// not a copy/paste: every rated amount in this rater is non-negative (a negative
// quantity is rejected by evaluateQuantityRule), so floor can never differ from
// truncation. Pinning them separately is what proves the policy is actually
// being read rather than defaulted.
func TestRuleFormRoundingPolicy(t *testing.T) {
	t.Parallel()
	key := rfKey("rounding")
	halfNano := func(t *testing.T, id string, policy economics.RoundingPolicy) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, key)
		rule.RateNumerator = rfDec(t, "1")
		rule.RateDenominator = rfDec(t, "2000000000")
		rule.RoundingScope = economics.RoundingScopeLine
		rule.RoundingPolicy = policy
		return rule
	}
	cases := []rfCase{}
	for _, row := range []struct {
		policy  economics.RoundingPolicy
		name    string
		amount  string
		rounded int64
	}{
		// Exactly 0.5 nano.
		{economics.RoundingHalfAwayFromZero, "quantity_1_tie_half_away_from_zero_rounds_up", "5/10", 1},
		{economics.RoundingHalfEven, "quantity_1_tie_half_even_rounds_to_even", "5/10", 0},
		{economics.RoundingTowardZero, "quantity_1_tie_toward_zero_truncates", "5/10", 0},
		{economics.RoundingFloor, "quantity_1_tie_floor_matches_toward_zero", "5/10", 0},
		// Exactly 1.5 nanos, where half_even and half_away agree but truncation
		// does not, so it distinguishes the tie policies from the non-tie ones.
		{economics.RoundingHalfAwayFromZero, "quantity_3_tie_half_away_from_zero_rounds_up", "15/10", 2},
		{economics.RoundingHalfEven, "quantity_3_tie_half_even_rounds_to_even", "15/10", 2},
		{economics.RoundingTowardZero, "quantity_3_tie_toward_zero_truncates", "15/10", 1},
		{economics.RoundingFloor, "quantity_3_tie_floor_matches_toward_zero", "15/10", 1},
	} {
		row := row
		quantity := "1"
		if row.amount == "15/10" {
			quantity = "3"
		}
		cases = append(cases, rfCase{
			name:  row.name,
			rules: []economics.RatingRule{halfNano(t, "rf-round-"+row.name, row.policy)},
			obs:   []metering.Observation{f3Observation(t, "rf-round-"+row.name, b1Measure(t, key, quantity))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        row.amount,
				totalNano:    row.rounded,
				lines: []rfLineWant{{
					id:      key.Component,
					amount:  row.amount,
					rounded: row.rounded,
					status:  economics.RatingLineRated,
					payable: true,
				}},
			},
		})
	}
	rfTable(t, "rounding_policy", cases)
}

// TestRuleFormRoundingScopeLineVsCall pins the two rounding SCOPES on the same
// money. Three components each worth exactly half a nano:
//
//	line scope  every line rounds once   -> 1 + 1 + 1 = 3 nanos
//	call scope  only the call total rounds -> round(1.5)  = 2 nanos (half away)
//
// The exact total is identical in both (1.5e-9), so the pair isolates the
// rounding boundary from the arithmetic. At call scope the LINE rounded amount
// must be ABSENT: a second, non-authoritative rounded amount on the line is
// exactly what the DTO forbids, because downstream code could sum it.
func TestRuleFormRoundingScopeLineVsCall(t *testing.T) {
	t.Parallel()
	keys := []metering.ComponentKey{rfKey("round_a"), rfKey("round_b"), rfKey("round_c")}
	names := make([]string, 0, len(keys))
	measures := make([]metering.Measure, 0, len(keys))
	rules := func(t *testing.T, idPrefix string, scope economics.RoundingScope, policy economics.RoundingPolicy) []economics.RatingRule {
		out := make([]economics.RatingRule, 0, len(keys))
		for i, key := range keys {
			rule := rfRule(t, idPrefix+"-"+string(rune('a'+i)), economics.RatingRuleLinear, key)
			rule.RateNumerator = rfDec(t, "1")
			rule.RateDenominator = rfDec(t, "2000000000")
			rule.RoundingScope = scope
			rule.RoundingPolicy = policy
			out = append(out, rule)
		}
		return out
	}
	for _, key := range keys {
		names = append(names, key.Component)
		measures = append(measures, b1Measure(t, key, "1"))
	}
	halfNanoLines := func(rounded int64) []rfLineWant {
		out := make([]rfLineWant, 0, len(keys))
		for _, name := range names {
			out = append(out, rfLineWant{
				id: name, amount: "5/10", rounded: rounded,
				status: economics.RatingLineRated, payable: true,
			})
		}
		return out
	}
	cases := []rfCase{{
		name:  "line_scope_rounds_each_line_to_one_nano",
		rules: rules(t, "rf-line", economics.RoundingScopeLine, economics.RoundingHalfAwayFromZero),
		obs:   []metering.Observation{f3Observation(t, "rf-line-scope", measures...)},
		expect: rfExpect{
			wantNoErr:    true,
			completeness: economics.CompletenessComplete,
			total:        "15/10",
			totalNano:    3,
			lines:        halfNanoLines(1),
		},
	}}
	for _, row := range []struct {
		policy economics.RoundingPolicy
		name   string
		nano   int64
	}{
		{economics.RoundingHalfAwayFromZero, "call_scope_half_away_from_zero_rounds_the_total_to_two", 2},
		{economics.RoundingHalfEven, "call_scope_half_even_rounds_the_total_to_two", 2},
		{economics.RoundingTowardZero, "call_scope_toward_zero_truncates_the_total_to_one", 1},
		{economics.RoundingFloor, "call_scope_floor_truncates_the_total_to_one", 1},
	} {
		row := row
		cases = append(cases, rfCase{
			name:  row.name,
			rules: rules(t, "rf-call-"+row.name, economics.RoundingScopeCall, row.policy),
			obs:   []metering.Observation{f3Observation(t, "rf-call-scope-"+row.name, measures...)},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "15/10",
				totalNano:    row.nano,
				// No line carries a rounded amount at a non-line rounding scope.
				lines: halfNanoLines(-1),
			},
		})
	}
	rfTable(t, "rounding_scope", cases)
}

// ---------------------------------------------------------------------------
// Form 12: fixed call and submission fees as independent lines
// ---------------------------------------------------------------------------

// TestRuleFormFixedFeeIsIndependent pins that a fixed fee is a commercial line
// outside the component graph.
//
// It is inherently SINGLE-PERSPECTIVE on the review5beSeams pair, and that is
// a property of the product rather than a gap: RateCustomerPolicyObservation
// rates the incremental B-leg inference plane and is documented to omit call
// and submission fees, because evaluating them once per observation head would
// multiply a call-scoped fee across retries and selected legs. The fee is owned
// by terminal call settlement. The customer seam is therefore NOT skipped here;
// its documented omission is asserted instead, so the difference is pinned
// rather than papered over.
//
// What the operator seam pins:
//
//   - a fee is emitted only at its own trusted scope, and a fee declared at the
//     other scope is reported as ErrFixedFeeScopeMismatch rather than charged
//     into the wrong period;
//   - exactly one fee line is emitted regardless of how many components and
//     observations the valuation carries, so a per-observation loop can never
//     double-charge it;
//   - the fee does NOT participate in a whole-context total. The fee is 5 and
//     the sibling quantity 39, so a context of 49 stays below the boundary of
//     50; had the fee been counted, the context would have been 54 and the
//     priced component would have dropped from 10 to 1;
//   - the fee does NOT repair a component-level failure: alongside a missing
//     REQUIRED cover member the valuation is still partial with the typed
//     partition diagnostic;
//   - the fee survives an overlap conflict as payable while every quantity line
//     stays suppressed, which is the same independence seen from the other side;
//   - a zero fixed amount is an explicit free line, not a payable one.
func TestRuleFormFixedFeeIsIndependent(t *testing.T) {
	t.Parallel()
	first := rfKey("fix_a")
	second := rfKey("fix_b")
	firstRule := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, first)
		rule.UnitPrice = rfDec(t, "1")
		return rule
	}
	secondRule := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, second)
		rule.UnitPrice = rfDec(t, "2")
		return rule
	}
	// absentRule prices a component that is never observed, so its rule can only
	// matter for rule-derived relevance, never for an emitted line.
	absentRule := func(t *testing.T, id, price string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, rfKey("fix_c"))
		rule.UnitPrice = rfDec(t, price)
		return rule
	}
	bothMeasures := func() []metering.Measure {
		return []metering.Measure{b1Measure(t, first, "10"), b1Measure(t, second, "1")}
	}
	feeRules := func(t *testing.T, idPrefix string, includeSubmission bool) []economics.RatingRule {
		rules := []economics.RatingRule{
			firstRule(t, idPrefix+"-a"), secondRule(t, idPrefix+"-b"),
			b1FixedRule(t, idPrefix+"-call", economics.FixedFeeScopeCall, "5"),
		}
		if includeSubmission {
			rules = append(rules, b1FixedRule(t, idPrefix+"-sub", economics.FixedFeeScopeSubmission, "7"))
		}
		return rules
	}
	quantityLines := func() []rfLineWant {
		return []rfLineWant{
			{id: first.Component, amount: "10/0", rounded: 10_000_000_000, status: economics.RatingLineRated, payable: true},
			{id: second.Component, amount: "2/0", rounded: 2_000_000_000, status: economics.RatingLineRated, payable: true},
		}
	}
	callFeeLine := rfLineWant{
		id: "fixed:rf-fix-call", amount: "5/0", rounded: 5_000_000_000,
		status: economics.RatingLineRated, payable: true,
	}
	submissionFeeLine := rfLineWant{
		id: "fixed:rf-fix-sub", amount: "7/0", rounded: 7_000_000_000,
		status: economics.RatingLineRated, payable: true,
	}
	// The customer seam sees only the quantity plane.
	customerOnly := func(total string, nano int64, completeness economics.Completeness, classes []error) rfExpect {
		return rfExpect{
			wantNoErr:    classes == nil,
			classes:      classes,
			completeness: completeness,
			total:        total,
			totalNano:    nano,
			lines:        quantityLines(),
		}
	}

	cases := []rfCase{
		{
			name:  "call_scope_charges_the_call_fee_once",
			rules: feeRules(t, "rf-fix", false),
			obs:   []metering.Observation{f3Observation(t, "rf-fix-call", bothMeasures()...)},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "17/0",
				totalNano:    17_000_000_000,
				lines:        append(quantityLines(), callFeeLine),
			},
			bySeam: map[string]rfExpect{
				// Documented omission: no fixed-fee line, no scope diagnostic
				// either, because the fee is never evaluated on this plane.
				"customer_policy_R": customerOnly("12/0", 12_000_000_000, economics.CompletenessComplete, nil),
			},
		},
		{
			// The same fee material under a submission-scoped valuation: the
			// call fee must NOT be charged into the submission and must be
			// reported rather than silently dropped.
			name:  "submission_scope_charges_only_the_submission_fee",
			rules: feeRules(t, "rf-fix", true),
			obs:   []metering.Observation{f3Observation(t, "rf-fix-sub", bothMeasures()...)},
			scope: "submission:sub-1",
			expect: rfExpect{
				classes:      []error{billing.ErrFixedFeeScopeMismatch},
				completeness: economics.CompletenessPartial,
				total:        "19/0",
				totalNano:    19_000_000_000,
				lines:        append(quantityLines(), submissionFeeLine),
			},
			bySeam: map[string]rfExpect{
				"customer_policy_R": customerOnly("12/0", 12_000_000_000, economics.CompletenessComplete, nil),
			},
		},
		{
			// Two observations in the same B-leg: the fee is a call-scoped
			// commercial charge, so it must appear exactly once, not once per
			// observation head.
			name:  "two_observations_still_charge_the_fee_once",
			rules: feeRules(t, "rf-fix", false),
			obs: []metering.Observation{
				f3Observation(t, "rf-fix-two-a", b1Measure(t, first, "10")),
				f3Observation(t, "rf-fix-two-b", b1Measure(t, second, "1")),
			},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "17/0",
				totalNano:    17_000_000_000,
				lines:        append(quantityLines(), callFeeLine),
			},
			bySeam: map[string]rfExpect{
				"customer_policy_R": customerOnly("12/0", 12_000_000_000, economics.CompletenessComplete, nil),
			},
		},
		{
			// The fee is not a context component. Priced 10 with a sibling 39
			// gives a context of 49, one unit below the ladder boundary of 50,
			// so the whole-context tier stays at price 1 and the priced line is
			// 10. If the 5-unit fee were counted in the context the total would
			// be 54 and the priced line would fall to 1.
			name: "fee_is_not_counted_in_a_whole_context_total",
			rules: func() []economics.RatingRule {
				ladder := rfRule(t, "rf-fix-wc", economics.RatingRuleAllUnits, first)
				ladder.TierMode = economics.TierAllUnits
				ladder.SelectionScope = economics.SelectionWholeContext
				ladder.Tiers = rfTiers(t, [2]string{"50", "1"}, [2]string{"", "0.1"})
				sibling := secondRule(t, "rf-fix-wc-sib")
				sibling.UnitPrice = rfDec(t, "1000")
				return []economics.RatingRule{ladder, sibling, b1FixedRule(t, "rf-fix-call", economics.FixedFeeScopeCall, "5")}
			}(),
			obs: []metering.Observation{f3Observation(t, "rf-fix-wc",
				b1Measure(t, first, "10"), b1Measure(t, second, "39"))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "39015/0",
				totalNano:    39_015_000_000_000,
				lines: []rfLineWant{
					{id: first.Component, amount: "10/0", rounded: 10_000_000_000, status: economics.RatingLineRated, payable: true},
					{id: second.Component, amount: "39000/0", rounded: 39_000_000_000_000, status: economics.RatingLineRated, payable: true},
					callFeeLine,
				},
			},
			bySeam: map[string]rfExpect{
				"customer_policy_R": {
					wantNoErr:    true,
					completeness: economics.CompletenessComplete,
					total:        "39010/0",
					totalNano:    39_010_000_000_000,
					lines: []rfLineWant{
						{id: first.Component, amount: "10/0", rounded: 10_000_000_000, status: economics.RatingLineRated, payable: true},
						{id: second.Component, amount: "39000/0", rounded: 39_000_000_000_000, status: economics.RatingLineRated, payable: true},
					},
				},
			},
		},
		{
			// A missing REQUIRED cover member is a component-level failure the
			// fee must not paper over.
			name: "fee_does_not_repair_a_component_level_failure",
			rules: []economics.RatingRule{
				firstRule(t, "rf-fix-part-a"),
				absentRule(t, "rf-fix-part-c", "2"),
				b1FixedRule(t, "rf-fix-call", economics.FixedFeeScopeCall, "5"),
			},
			schemas: rfSchema(rfCover{parent: rfKey("fix_p"), children: []metering.ComponentKey{first, rfKey("fix_c")}}),
			obs:     []metering.Observation{f3Observation(t, "rf-fix-part", b1Measure(t, first, "10"))},
			expect: rfExpect{
				classes:      []error{billing.ErrSchemaPartitionIncomplete},
				completeness: economics.CompletenessPartial,
				total:        "15/0",
				totalNano:    15_000_000_000,
				lines: []rfLineWant{
					{id: first.Component, amount: "10/0", rounded: 10_000_000_000, status: economics.RatingLineRated, payable: true},
					callFeeLine,
				},
			},
			bySeam: map[string]rfExpect{
				"customer_policy_R": {
					classes:      []error{billing.ErrSchemaPartitionIncomplete},
					completeness: economics.CompletenessPartial,
					total:        "10/0",
					totalNano:    10_000_000_000,
					lines: []rfLineWant{
						{id: first.Component, amount: "10/0", rounded: 10_000_000_000, status: economics.RatingLineRated, payable: true},
					},
				},
			},
		},
		{
			// The other direction of independence: the overlap conflict
			// suppresses every quantity line, and the trusted-scope fee stays
			// payable as an explanatory line of its own.
			name: "fee_survives_an_overlap_conflict_without_quantity_money",
			rules: []economics.RatingRule{
				firstRule(t, "rf-fix-overlap-a"), secondRule(t, "rf-fix-overlap-b"),
				b1FixedRule(t, "rf-fix-call", economics.FixedFeeScopeCall, "5"),
			},
			schemas: rfSchema(rfCover{parent: first, children: []metering.ComponentKey{second}}),
			obs:     []metering.Observation{f3Observation(t, "rf-fix-overlap", bothMeasures()...)},
			expect: rfExpect{
				classes:      []error{billing.ErrSchemaOverlapConflict},
				completeness: economics.CompletenessConflict,
				total:        "5/0",
				totalNano:    5_000_000_000,
				lines:        []rfLineWant{callFeeLine},
			},
			bySeam: map[string]rfExpect{
				"customer_policy_R": {
					classes:      []error{billing.ErrSchemaOverlapConflict},
					completeness: economics.CompletenessConflict,
					total:        rfNone,
					totalNano:    -1,
					lines:        nil,
				},
			},
		},
		{
			// The free direction on the fee form.
			name: "zero_fixed_amount_is_an_explicit_free_line",
			rules: []economics.RatingRule{
				firstRule(t, "rf-fix-zero-a"),
				b1FixedRule(t, "rf-fix-free", economics.FixedFeeScopeCall, "0"),
			},
			obs: []metering.Observation{f3Observation(t, "rf-fix-zero", b1Measure(t, first, "10"))},
			expect: rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "10/0",
				totalNano:    10_000_000_000,
				lines: []rfLineWant{
					{id: first.Component, amount: "10/0", rounded: 10_000_000_000, status: economics.RatingLineRated, payable: true},
					{id: "fixed:rf-fix-free", amount: "0/0", rounded: 0, status: economics.RatingLineExplicitFree, payable: false},
				},
			},
			bySeam: map[string]rfExpect{
				"customer_policy_R": {
					wantNoErr:    true,
					completeness: economics.CompletenessComplete,
					total:        "10/0",
					totalNano:    10_000_000_000,
					lines: []rfLineWant{
						{id: first.Component, amount: "10/0", rounded: 10_000_000_000, status: economics.RatingLineRated, payable: true},
					},
				},
			},
		},
	}
	rfTable(t, "fixed_fee", cases)
}

// ---------------------------------------------------------------------------
// Cross-cutting: commercial relevance is effective, not existential
// ---------------------------------------------------------------------------

// TestRuleFormCommercialRelevanceIsEffective pins the load-bearing property of
// commercial relevance across every rule form, and it is the assertion that
// separates "a rule exists" from "a rule participates".
//
// The shape is fixed: parent P declares COMPLETE coverage of {B, C}, only B is
// observed and priced, and C's own rule is the form under test. The rating
// decision depends on whether C COULD EVER bill money:
//
//   - a form that resolves but can never bill - explicit free, a zero rate at a
//     positive quantity, a block rule whose rate is zero, an all-units or
//     graduated ladder with no positive tier, a zero floor, a fixed fee, or a
//     qualifier condition that does not match - must NOT make the missing
//     required member load-bearing. The cover stays complete and the rating is
//     clean, because nothing was owed.
//   - a form that CAN bill - a positive rate, a positive minimum even under a
//     zero rate, a positive tier, a matching qualifier - MUST make it
//     load-bearing, so an unobserved charge cannot certify a complete settlement.
//
// Both directions are asserted for each form, and the money is identical in
// both (the sibling's 60), so a failure can only come from the relevance
// predicate and never from the arithmetic.
func TestRuleFormCommercialRelevanceIsEffective(t *testing.T) {
	t.Parallel()
	parent := rfKey("rel_p")
	observed := rfKey("rel_b")
	absent := rfKey("rel_c")
	schemas := rfSchema(rfCover{parent: parent, children: []metering.ComponentKey{observed, absent}})
	base := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, observed)
		rule.UnitPrice = rfDec(t, "1")
		return rule
	}
	linear := func(t *testing.T, id, price string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleLinear, absent)
		rule.UnitPrice = rfDec(t, price)
		return rule
	}
	minimum := func(t *testing.T, id, price, floor string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleMinimum, absent)
		rule.UnitPrice = rfDec(t, price)
		rule.MinimumAmount = rfDec(t, floor)
		return rule
	}
	block := func(t *testing.T, id, price string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleBlock, absent)
		rule.UnitPrice = rfDec(t, price)
		rule.BlockSize = rfDec(t, "100")
		return rule
	}
	allUnits := func(t *testing.T, id string, tiers [][2]string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleAllUnits, absent)
		rule.TierMode = economics.TierAllUnits
		rule.Tiers = rfTiers(t, tiers...)
		return rule
	}
	graduated := func(t *testing.T, id string, tiers [][2]string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleGraduated, absent)
		rule.TierMode = economics.TierGraduated
		rule.Tiers = rfTiers(t, tiers...)
		return rule
	}
	zeroTiers := [][2]string{{"100", "0"}, {"", "0"}}
	positiveTiers := [][2]string{{"100", "0"}, {"", "2"}}
	conditional := func(t *testing.T, id, price string) economics.RatingRule {
		rule := linear(t, id, price)
		rule.Conditions = []economics.QualifierCondition{{Name: "plan", Value: "gold"}}
		return rule
	}
	conversion := func(t *testing.T, id string) economics.RatingRule {
		rule := rfRule(t, id, economics.RatingRuleConversion, absent)
		rule.ConversionSchema = "rf:conversion:v1"
		return rule
	}

	cases := []rfCase{}
	for _, row := range []struct {
		name        string
		loadBearing bool
		rules       func(t *testing.T, id string) []economics.RatingRule
		qualifiers  []metering.Dimension
		// override replaces the derived expectation for a case whose emitted
		// lines legitimately include more than the observed sibling.
		override *rfExpect
		// overrideBySeam does the same for one seam, where the customer-policy
		// plane deliberately evaluates no fixed fee at all.
		overrideBySeam map[string]*rfExpect
	}{
		{
			name:        "linear_positive_rate_is_load_bearing",
			loadBearing: true,
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{linear(t, id, "2")}
			},
		},
		{
			name: "linear_zero_rate_never_bills_so_is_not_load_bearing",
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{linear(t, id, "0")}
			},
		},
		{
			name:        "minimum_positive_rate_is_load_bearing",
			loadBearing: true,
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{minimum(t, id, "2", "0")}
			},
		},
		{
			// The sharp case: the rate is zero but the floor is money at every
			// quantity, including zero, so this member owes nothing only
			// because it was never observed.
			name:        "minimum_positive_floor_under_a_zero_rate_is_load_bearing",
			loadBearing: true,
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{minimum(t, id, "0", "5")}
			},
		},
		{
			name: "minimum_zero_rate_zero_floor_never_bills",
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{minimum(t, id, "0", "0")}
			},
		},
		{
			name:        "block_positive_rate_is_load_bearing",
			loadBearing: true,
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{block(t, id, "0.01")}
			},
		},
		{
			// A block bills at least one block but a zero rate prices every
			// block at nothing, so no quantity can ever make it pay.
			name:  "block_zero_rate_never_bills",
			rules: func(t *testing.T, id string) []economics.RatingRule { return []economics.RatingRule{block(t, id, "0")} },
		},
		{
			name: "all_units_ladder_without_a_positive_tier_never_bills",
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{allUnits(t, id, zeroTiers)}
			},
		},
		{
			name:        "all_units_ladder_with_a_positive_tier_is_load_bearing",
			loadBearing: true,
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{allUnits(t, id, positiveTiers)}
			},
		},
		{
			name: "graduated_ladder_without_a_positive_slice_never_bills",
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{graduated(t, id, zeroTiers)}
			},
		},
		{
			name:        "graduated_ladder_with_a_positive_slice_is_load_bearing",
			loadBearing: true,
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{graduated(t, id, positiveTiers)}
			},
		},
		{
			name:        "whole_context_ladder_with_a_positive_tier_is_load_bearing",
			loadBearing: true,
			rules: func(t *testing.T, id string) []economics.RatingRule {
				rule := allUnits(t, id, positiveTiers)
				rule.SelectionScope = economics.SelectionWholeContext
				return []economics.RatingRule{rule}
			},
		},
		{
			name: "whole_context_ladder_without_a_positive_tier_never_bills",
			rules: func(t *testing.T, id string) []economics.RatingRule {
				rule := allUnits(t, id, zeroTiers)
				rule.SelectionScope = economics.SelectionWholeContext
				return []economics.RatingRule{rule}
			},
		},
		{
			// The relevance question is "could this rule EVER bill", evaluated
			// with the period fence OPEN. A period-selected rule therefore makes
			// the member load-bearing even in a call-scoped valuation, where the
			// money can in fact never be charged. This asymmetry is deliberate
			// fail-closed behaviour, and it is pinned so it cannot change
			// silently.
			name:        "period_ladder_with_a_positive_tier_is_load_bearing_even_in_a_call_scope",
			loadBearing: true,
			rules: func(t *testing.T, id string) []economics.RatingRule {
				rule := allUnits(t, id, positiveTiers)
				rule.SelectionScope = economics.SelectionPeriod
				return []economics.RatingRule{rule}
			},
		},
		{
			// A condition that does not match resolves to nothing, which is
			// exactly the absent-rule case for money.
			name: "positive_rate_behind_an_unmatched_qualifier_never_bills",
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{conditional(t, id, "2")}
			},
		},
		{
			name:        "positive_rate_behind_a_matched_qualifier_is_load_bearing",
			loadBearing: true,
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{conditional(t, id, "2")}
			},
			qualifiers: []metering.Dimension{{Name: "plan", Value: "gold"}},
		},
		{
			// DOCUMENTED GAP: the conversion form is declared by the SDK and
			// validated by publication, but the reference rater refuses it at
			// evaluation with ErrRateUnsupported because it needs an explicit
			// transform adapter. ruleCanChargePositive skips every probe the
			// rule refuses, so an UNIMPLEMENTED rule is indistinguishable from a
			// free one for commercial relevance and the missing member is
			// excused. The measured behaviour is asserted below; the gap is
			// named here.
			name:  "conversion_rule_is_not_implemented_and_is_indistinguishable_from_free",
			rules: func(t *testing.T, id string) []economics.RatingRule { return []economics.RatingRule{conversion(t, id)} },
		},
		{
			// A fixed fee has no component identity, so resolveRule can never
			// select it for a declared cover member and the SDK offers no way to
			// declare one as a component child at all. The fee is still charged
			// at its own trusted scope, which is the second half of the
			// independence: it is real money and it is not a cover member.
			name: "fixed_fee_rule_never_covers_a_component_member",
			rules: func(t *testing.T, id string) []economics.RatingRule {
				return []economics.RatingRule{b1FixedRule(t, id, economics.FixedFeeScopeCall, "9")}
			},
			override: &rfExpect{
				wantNoErr:    true,
				completeness: economics.CompletenessComplete,
				total:        "69/0",
				totalNano:    69_000_000_000,
				lines: []rfLineWant{
					{
						id: observed.Component, amount: "60/0", rounded: 60_000_000_000,
						status: economics.RatingLineRated, payable: true,
					},
					{
						id: "fixed:rf-rel-fixed_fee_rule_never_covers_a_component_member", amount: "9/0", rounded: 9_000_000_000,
						status: economics.RatingLineRated, payable: true,
					},
				},
			},
			overrideBySeam: map[string]*rfExpect{
				// The customer-policy plane omits call fees entirely, so the
				// sibling's money is the whole valuation there.
				"customer_policy_R": {
					wantNoErr:    true,
					completeness: economics.CompletenessComplete,
					total:        "60/0",
					totalNano:    60_000_000_000,
					lines: []rfLineWant{
						{
							id: observed.Component, amount: "60/0", rounded: 60_000_000_000,
							status: economics.RatingLineRated, payable: true,
						},
					},
				},
			},
		},
		{
			name:  "no_rule_at_all_is_the_baseline_not_load_bearing",
			rules: func(*testing.T, string) []economics.RatingRule { return nil },
		},
	} {
		row := row
		expect := rfExpect{
			wantNoErr:    true,
			completeness: economics.CompletenessComplete,
			total:        "60/0",
			totalNano:    60_000_000_000,
			lines: []rfLineWant{{
				id: observed.Component, amount: "60/0", rounded: 60_000_000_000,
				status: economics.RatingLineRated, payable: true,
			}},
		}
		if row.loadBearing {
			expect = rfExpect{
				classes:      []error{billing.ErrSchemaPartitionIncomplete},
				completeness: economics.CompletenessPartial,
				total:        "60/0",
				totalNano:    60_000_000_000,
				lines: []rfLineWant{{
					id: observed.Component, amount: "60/0", rounded: 60_000_000_000,
					status: economics.RatingLineRated, payable: true,
				}},
			}
		}
		if row.override != nil {
			expect = *row.override
		}
		bySeam := map[string]rfExpect{}
		for name, seam := range row.overrideBySeam {
			bySeam[name] = *seam
		}
		cases = append(cases, rfCase{
			name:       row.name,
			rules:      append(row.rules(t, "rf-rel-"+row.name), base(t, "rf-rel-base-"+row.name)),
			schemas:    schemas,
			qualifiers: row.qualifiers,
			obs:        []metering.Observation{f3Observation(t, "rf-rel-"+row.name, b1Measure(t, observed, "60"))},
			expect:     expect,
			bySeam:     bySeam,
		})
	}
	rfTable(t, "commercial_relevance", cases)
}

// ---------------------------------------------------------------------------
// Cross-cutting: zero quantity, zero rate, in both directions
// ---------------------------------------------------------------------------

// TestRuleFormZeroQuantityAndZeroRate pins requirement 2 in isolation, in both
// directions, so neither side can become a silent "no rule":
//
//	positive rate, zero quantity  -> a RATED zero line (not explicit free, not
//	                                 rate_missing, not a missing-rule diagnostic)
//	positive rate, positive qty    -> payable
//	zero rate, zero quantity       -> EXPLICIT FREE
//	zero rate, positive qty        -> EXPLICIT FREE
//	positive minimum, zero qty     -> POSITIVE PAYABLE
//	positive minimum, positive qty -> POSITIVE PAYABLE
//	zero rate and zero floor       -> EXPLICIT FREE at both quantities
//
// The rateable/free distinction is carried by the line STATUS as well as by the
// amount: a zero-quantity line under a positive rate is `rated`, while only a
// genuinely free shape reports `explicit_free`. A caller that treats every zero
// as free, and a caller that treats every emitted line as a charge, both fail
// here.
func TestRuleFormZeroQuantityAndZeroRate(t *testing.T) {
	t.Parallel()
	key := rfKey("zero_dir")
	type shape struct {
		id     string
		price  string
		floor  string
		status economics.RatingLineStatus
		// zeroAmount is the exact amount at quantity 0 and positiveAmount the
		// amount at quantity 1000.
		zeroAmount     string
		positiveAmount string
	}
	shapes := []shape{
		// A positive rate is the only shape whose money moves with the quantity.
		{id: "positive_rate", price: "2", status: economics.RatingLineRated, zeroAmount: "0/0", positiveAmount: "2000/0"},
		{id: "zero_rate", price: "0", status: economics.RatingLineExplicitFree, zeroAmount: "0/0", positiveAmount: "0/0"},
		{id: "positive_minimum", price: "0", floor: "5", status: economics.RatingLineRated, zeroAmount: "5/0", positiveAmount: "5/0"},
		{id: "zero_rate_zero_minimum", price: "0", floor: "0", status: economics.RatingLineExplicitFree, zeroAmount: "0/0", positiveAmount: "0/0"},
	}
	cases := []rfCase{}
	for _, shape := range shapes {
		shape := shape
		for _, quantity := range []struct {
			value  string
			amount string
		}{{"0", shape.zeroAmount}, {"1000", shape.positiveAmount}} {
			quantity := quantity
			kind := economics.RatingRuleLinear
			if shape.floor != "" {
				kind = economics.RatingRuleMinimum
			}
			rule := rfRule(t, "rf-zdir-"+shape.id+"-"+quantity.value, kind, key)
			rule.UnitPrice = rfDec(t, shape.price)
			if shape.floor != "" {
				rule.MinimumAmount = rfDec(t, shape.floor)
			}
			nano := mustNano(t, quantity.amount)
			cases = append(cases, rfCase{
				name:  shape.id + "_quantity_" + quantity.value,
				rules: []economics.RatingRule{rule},
				obs:   []metering.Observation{f3Observation(t, "rf-zdir-"+shape.id+"-"+quantity.value, b1Measure(t, key, quantity.value))},
				expect: rfExpect{
					// No diagnostic in any of the eight cells: none of them is a
					// missing rule, so none of them may look like one.
					wantNoErr:    true,
					completeness: economics.CompletenessComplete,
					total:        quantity.amount,
					totalNano:    nano,
					lines: []rfLineWant{{
						id:      key.Component,
						amount:  quantity.amount,
						rounded: nano,
						status:  shape.status,
						payable: nano > 0,
					}},
				},
			})
		}
	}
	rfTable(t, "zero_quantity_and_zero_rate", cases)
}

// ---------------------------------------------------------------------------
// Shared schema helper
// ---------------------------------------------------------------------------

// rfCover is one declared COMPLETE (partition) cover: the parent and every
// REQUIRED member. metering.ComponentKey is not comparable, so a cover set is
// an ordered slice rather than a map.
type rfCover struct {
	parent   metering.ComponentKey
	children []metering.ComponentKey
}

// rfSchema builds one frozen component schema declaring every cover in the set
// as complete coverage of all of its members, with no optional member. The
// declared order is irrelevant because the snapshot content hash canonicalises
// it.
func rfSchema(covers ...rfCover) []metering.ComponentSchema {
	relationships := make([]metering.ComponentRelationship, 0, len(covers))
	for _, cover := range covers {
		for _, child := range cover.children {
			relationships = append(relationships, metering.ComponentRelationship{
				Kind: metering.RelationshipPartition, Parent: cover.parent, Child: child,
			})
		}
	}
	if len(relationships) == 0 {
		return nil
	}
	return []metering.ComponentSchema{{ID: b1SchemaID, Version: "1", Relationships: relationships}}
}
