package billing_test

// Determinism of FIRST-GAP selection when more than one component's reduced
// value is unusable.
//
// aggregateMeasures records evidence gaps with setFirstErr, which keeps the FIRST
// one. The whole diagnostic identity of a failed rating therefore rests on WHICH
// component is visited first, and a Go map range is deliberately randomized per
// iteration: a reducer that decided the winner by ranging a map would name a
// different component on nearly every run, and a rating error is part of the
// durable replay surface. The per-component gap attribution is walked in
// canonical order (the reducer's own audit envelope is sorted by
// ComponentKey.CanonicalKey in Observation.Canonical) and the later
// valueless-entry scan is walked over a sorted key slice, so the first gap is a
// structural property of the evidence rather than of a traversal order.
//
// The fixture deliberately declares the two unusable components in the order that
// puts the canonically LAST one first, so "whatever the loop happened to see
// first" and "the canonically first component" are different answers and the
// assertion has teeth.
//
// SCOPE, stated plainly, because a test that overstates its reach is worse than
// no test: this pins the first gap as it is OBSERVABLE, through Rate. It does NOT
// guard the sorted slice in the valueless-entry scan, and reverting that slice to
// a map range still passes here.
//
// The reason is narrow and was measured, not assumed. The scan IS the first
// setter of this error in the current corpus - it fires on real inputs - but on
// every one of them exactly one entry is valueless, and a scan over a map
// returns the same single candidate whatever the iteration order. So map order
// cannot change the winner, which is why the test cannot detect its removal.
//
// The scan is kept in canonical order because it is the next attribution to run
// when the per-measure pass stops covering a case, and a map range there would
// make the first gap traversal-dependent from that moment on: the per-measure
// pass and the scan both build identity from a normalized key, so two measures
// whose raw keys differ from their normalized keys can both reach the scan
// valueless, and only a total order then decides which one is reported.

import (
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// a1gNoValue is a measure with NO effective value: Value is nil, so the reducer
// never produces a reduced measure for it and the rater reports it through the
// "has no effective value" gap. QualityUnknown is one of the qualities a nil
// value is legal with; a present-value quality would fail Measure.Validate.
func a1gNoValue(name string) metering.Measure {
	return metering.Measure{Key: r7Key(name), Quality: metering.QualityUnknown, MethodRef: b1SchemaID}
}

// TestRatingReportsTheCanonicallyFirstComponentWithNoEffectiveValue pins the
// first-gap invariant: when several components have no effective value, the
// reported diagnostic names the CANONICALLY FIRST of them, identically on every
// repetition and under every declaration and delivery order.
//
// It runs the same evidence three ways, because first-gap selection has two
// orderings to get right and only exercising one leaves the other untested:
//
//   - one observation carrying all the measures, so the winner is chosen by the
//     canonical MEASURE order inside a canonicalized envelope;
//   - the same evidence delivered as two observations in each slice order, so
//     the winner must not move with the caller's slice permutation.
//
// The expected winner is derived from the keys with the same comparator the
// implementation must use, so the test states the invariant instead of pinning a
// literal that would need editing if the fixture were renamed.
func TestRatingReportsTheCanonicallyFirstComponentWithNoEffectiveValue(t *testing.T) {
	t.Parallel()
	alpha := a1gNoValue("vendor:a1g_alpha")
	zulu := a1gNoValue("vendor:a1g_zulu")
	mid := b1Measure(t, r7Key("vendor:a1g_mid"), "10")
	canonicallyFirst, canonicallyLast := alpha.Key, zulu.Key
	if canonicallyFirst.CanonicalKey() >= canonicallyLast.CanonicalKey() {
		t.Fatalf("fixture no longer orders as intended: %q >= %q",
			canonicallyFirst.CanonicalKey(), canonicallyLast.CanonicalKey())
	}

	// The rated component is the only one with evidence, so no other diagnostic
	// can pre-empt or join the gap under test.
	rules := []economics.RatingRule{b1Rule(t, "a1g-mid-rate", r7Key("vendor:a1g_mid"), "2")}
	// Two rule orders over the same priced component set: the rule slice is
	// hashed into the tariff identity, so a permuted but equivalent slice is a
	// real permutation of the input that must not reach the diagnostic. The
	// second rule prices a component the fixture never observes.
	ruleOrders := map[string][]economics.RatingRule{
		"one_rule": rules,
		"two_rules": {
			b1Rule(t, "a1g-mid-rate", r7Key("vendor:a1g_mid"), "2"),
			b1Rule(t, "a1g-unused-rate", r7Key("vendor:a1g_never_observed"), "0"),
		},
	}

	type delivery struct {
		name         string
		observations func() []metering.Observation
	}
	deliveries := map[string]delivery{
		// Canonically last declared first: first-seen-wins would answer "zulu".
		"one_observation_last_declared_first": {observations: func() []metering.Observation {
			return []metering.Observation{drObservation(t, "a1g-one", zulu, mid, alpha)}
		}},
		"one_observation_canonical_order": {observations: func() []metering.Observation {
			return []metering.Observation{drObservation(t, "a1g-one", alpha, mid, zulu)}
		}},
		"one_observation_reversed": {observations: func() []metering.Observation {
			return []metering.Observation{drObservation(t, "a1g-one", zulu, mid, alpha)}
		}},
		"two_observations_forward": {observations: func() []metering.Observation {
			return []metering.Observation{
				drObservation(t, "a1g-obs-1", mid, alpha),
				drObservation(t, "a1g-obs-2", zulu),
			}
		}},
		"two_observations_reversed": {observations: func() []metering.Observation {
			return []metering.Observation{
				drObservation(t, "a1g-obs-2", zulu),
				drObservation(t, "a1g-obs-1", mid, alpha),
			}
		}},
	}

	for deliveryName, delivered := range deliveries {
		for ruleName, orderedRules := range ruleOrders {
			label := deliveryName + "/" + ruleName
			tariff := b1Tariff(t, "a1g-"+label, orderedRules, nil)
			resolved := b1Resolve(t, tariff)
			rater, err := billing.NewReferenceRater(resolved)
			if err != nil {
				t.Fatalf("%s: NewReferenceRater: %v", label, err)
			}
			input := b1OperatorInput(t, resolved, delivered.observations())

			// Repeated ratings of the SAME input. Go randomizes map iteration
			// order per range, so a map-order-dependent first gap changes its
			// answer between repetitions of one identical call rather than
			// merely being wrong once.
			var want string
			for attempt := range 8 {
				_, rateErr := rater.Rate(t.Context(), input)
				if rateErr == nil {
					t.Fatalf("%s attempt %d: two components have no effective value but the rating succeeded", label, attempt)
				}
				if !errors.Is(rateErr, billing.ErrQuantityIncomplete) {
					t.Fatalf("%s attempt %d: err=%v, want %v", label, attempt, rateErr, billing.ErrQuantityIncomplete)
				}
				if attempt == 0 {
					want = rateErr.Error()
				} else if rateErr.Error() != want {
					t.Fatalf("%s attempt %d: the first-gap diagnostic is not deterministic.\n  first: %s\n  now:   %s",
						label, attempt, want, rateErr.Error())
				}
			}
			if !strings.Contains(want, "has no effective value") {
				t.Fatalf("%s: want the no-effective-value gap, got: %s", label, want)
			}
			if !strings.Contains(want, canonicallyFirst.CanonicalKey()) {
				t.Fatalf("%s: the diagnostic does not name the canonically first unusable component %s:\n  %s",
					label, canonicallyFirst.CanonicalKey(), want)
			}
			if strings.Contains(want, canonicallyLast.CanonicalKey()) {
				t.Fatalf("%s: the diagnostic named the canonically LAST unusable component %s as well:\n  %s",
					label, canonicallyLast.CanonicalKey(), want)
			}
			t.Logf("A1-FIRST-GAP %s reports the canonically first unusable component", label)
		}
	}
}
