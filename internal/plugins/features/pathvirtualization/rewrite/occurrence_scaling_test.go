package rewrite_test

// Spec: b-leg-path-virtualization Task 11.1, requirements.md 9.2, 9.3 and 9.4, and the
// prior finding this feature recorded about a quadratic parse cost: the standing guard
// that a super-linear cost cannot come back silently.
//
// Benchmarks are measurements and cannot gate anything. These tests are the guard, and
// they are expressed in ALLOCATIONS for one reason: an allocation count is a
// deterministic function of the code and its input, so it is identical on every host,
// on every CPU, and under every timing condition, while a wall-clock threshold is
// none of those things. A relative-scaling assertion is used rather than an absolute
// count, because the absolute count legitimately moves whenever the byte-splice
// internals change, whereas a growth factor between two input sizes only moves if the
// cost stops being proportional to its input.
//
// Both assertions here are deliberately LOOSE. Each compares the per-occurrence
// allocation cost at 1000 occurrences against the per-occurrence cost at 10 and allows
// a factor of two. Linear cost passes with a factor near one; a reintroduced quadratic
// pass would show a factor of about 100, which is far outside a bound of two while
// leaving ordinary implementation churn plenty of room.
//
// Neither test calls t.Parallel, and that is load-bearing rather than an oversight.
// testing.AllocsPerRun pins GOMAXPROCS to one and panics inside a parallel test, and its
// sample counts process-wide allocations, so a concurrent parallel sibling would be
// counted as if it were this test's own. The testing package runs serial top-level tests
// one at a time and releases parallel ones only afterwards, so a serial test here is
// measured on a quiet process.

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/rewrite"
)

// scalingRuns is how many measured executions each allocation sample averages.
//
// The count is a count, not a time: repeated identical executions allocate identically,
// so more runs cost time without buying accuracy. Three keeps the sample cheap enough to
// sit in the ordinary test suite while still running the measured path more than once.
const scalingRuns = 3

// scalingTolerance is how much the per-occurrence cost may grow between the smallest and
// the largest tier. See the file comment for why it is two and not one.
const scalingTolerance = 2.0

// TestSelectorGuidedMutationAllocationsScaleWithOccurrenceCount pins requirement 9.2's
// bounded-cost claim on the selector-guided argument path: one payload's cost is
// proportional to the number of occurrences it carries, not quadratic in it.
//
// This is the guard the benchmark tiers exist to inform. The benchmark shows the
// per-occurrence numbers; this test refuses a regression that would be easy to miss in
// a total.
func TestSelectorGuidedMutationAllocationsScaleWithOccurrenceCount(t *testing.T) {
	fixture := benchRootFixtures[0]
	mapping := benchActiveMapping(t, fixture.root)
	pointers := benchArgPointerSet(t)
	decide := benchVirtualizeDecider(mapping)

	perOccurrence := make([]float64, 0, len(benchOccurrenceTiers))
	for _, occurrences := range benchOccurrenceTiers {
		raw := benchArgumentDocument(fixture, occurrences)
		sample := func() {
			if _, _, err := rewrite.ApplySelectedValues(raw, pointers, decide); err != nil {
				t.Fatalf("ApplySelectedValues over %d occurrences: %v", occurrences, err)
			}
		}
		allocs := testing.AllocsPerRun(scalingRuns, sample)
		t.Logf("occurrences=%4d allocs=%7d allocs-per-occurrence=%.2f",
			occurrences, int(allocs), allocs/float64(occurrences))
		perOccurrence = append(perOccurrence, allocs/float64(occurrences))
	}

	smallest := perOccurrence[0]
	largest := perOccurrence[len(perOccurrence)-1]
	if largest > scalingTolerance*smallest {
		t.Fatalf("selector-guided mutation allocated %.1f objects per occurrence at the largest tier "+
			"and %.1f at the smallest: the per-occurrence cost grew by %.1fx, which is not proportional "+
			"to the occurrence count (requirement 9.2)",
			largest, smallest, largest/smallest)
	}
}

// TestSecondOutboundPassIsCheaperAndChangesNoByte pins design.md's "Second outbound
// pass fast-skips already virtualized roots" as a measurable property rather than as a
// claim: the second pass must allocate strictly less than the first and must return the
// very same call.
//
// The byte half is asserted with pointer identity, which is stronger than comparing
// bytes: a pass that returns its input pointer cannot have published anything, so no
// comparison of the published bytes can add anything to it. The cost half is asserted on
// allocation counts for the same reason the scaling test uses them.
func TestSecondOutboundPassIsCheaperAndChangesNoByte(t *testing.T) {
	// The largest occurrence tier is the one where a redundant clone or a redundant
	// re-serialization would be most visible, so it is the tier the claim is made about.
	const occurrences = 1000

	fixture := benchRootFixtures[0]
	rewriter := benchOperatorRewriter(t, fixture.root, pathvirtualization.ToolProfile{
		Names:       []string{benchUnclaimedToolName},
		ArgPointers: []string{"/file_path", "/paths"},
	})
	first := benchRequestCall(t, fixture, occurrences, benchUnclaimedToolName, nil)

	published, firstStats, err := rewriter.RewriteCall(first)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if firstStats.Rewritten != occurrences {
		t.Fatalf("first pass rewrote %d occurrences, want %d", firstStats.Rewritten, occurrences)
	}

	second, secondStats, err := rewriter.RewriteCall(published)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second != published {
		t.Fatal("the second pass published a new call; an already-virtualized request " +
			"must be republished as the very same call")
	}
	if secondStats.Rewritten != 0 || secondStats.BytesSaved() != 0 {
		t.Fatalf("the second pass reported %d rewritten occurrences and %d saved bytes, want 0 and 0",
			secondStats.Rewritten, secondStats.BytesSaved())
	}

	firstAllocs := testing.AllocsPerRun(scalingRuns, func() {
		if _, _, err := rewriter.RewriteCall(first); err != nil {
			t.Fatalf("first pass: %v", err)
		}
	})
	secondAllocs := testing.AllocsPerRun(scalingRuns, func() {
		if _, _, err := rewriter.RewriteCall(published); err != nil {
			t.Fatalf("second pass: %v", err)
		}
	})
	if secondAllocs >= firstAllocs {
		t.Fatalf("the second pass allocated %.0f objects and the first %.0f: reapplying a rewrite "+
			"to an already-virtualized request must not cost what publishing it cost",
			secondAllocs, firstAllocs)
	}
}
