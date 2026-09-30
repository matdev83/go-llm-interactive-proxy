//go:build integration

package billing_test

import (
	"fmt"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Full topology products and maximum-publication-depth resource probes are
// certification workloads. All cases, assertions and fingerprint pins remain.
func TestSchemaModelOrderInvariance(t *testing.T) {
	t.Parallel()
	start := time.Now()
	report := newSMReport()
	graphs := smGraphs()
	evidenceSets := [][3]smEv{
		{smOne, smOne, smOne},
		{smTwo, smOne, smZero},
		{smAbsent, smOne, smUnavailable},
	}
	seams := review5beSeams()
	report.graphCount = len(graphs)
	report.evidenceCount = len(evidenceSets)
	report.seams = len(seams)

	t.Cleanup(func() { report.finish(t, "order", time.Since(start)) })

	// Skip publication-rejected graphs, exactly as the structure and commercial
	// sweeps already do. Both variants of an order-invariance pair are refused
	// identically, so the pair carries no signal; the skip is counted rather than
	// silently dropped. Filtering happens here, before the parallel subtests
	// start, so the shared skipped counter is never written concurrently.
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			report.skipped++
			t.Logf("validator skipped graph [%s]: %v", smOptsName(opts), err)
			continue
		}
		valid = append(valid, opts)
	}

	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			rels := smRelationships(opts)
			rules := smFreeRules(t)
			resolvedA := f356Schema(t, fmt.Sprintf("sm-order-a-%d", gi), rules, rels)
			resolvedB := f3Resolved(t, fmt.Sprintf("sm-order-b-%d", gi), rules, []metering.ComponentSchema{
				{ID: b1SchemaID, Version: "1", Relationships: smReverse(rels)},
				{ID: b1SchemaID + "_empty", Version: "1"},
			})
			for _, ev := range evidenceSets {
				for _, seam := range seams {
					caseID := smCaseID("order", seam.name, opts, ev)
					obsID := "sm-order-" + seam.name + "-" + smOptsCode(opts) + "-" + smEvCode(ev)
					obs := f3Observation(t, obsID, smMeasures(t, ev)...)
					valA, errA := seam.rate(t, resolvedA, obs)
					valB, errB := seam.rate(t, resolvedB, obs)
					outA, outB := smInspect(valA), smInspect(valB)
					outA.errClass, outB.errClass = smErrorClass(errA), smErrorClass(errB)
					report.guardA4()
					report.mu.Lock()
					report.cases++
					report.mu.Unlock()
					if outA.errClass != outB.errClass ||
						outA.completeness != outB.completeness ||
						!slices.Equal(outA.components, outB.components) ||
						outA.total != outB.total {
						report.add("A4", fmt.Sprintf("%s forward{err=%s compl=%s total=%s components=%v} reversed+empty{err=%s compl=%s total=%s components=%v}",
							caseID, outA.errClass, outA.completeness, outA.total, outA.components,
							outB.errClass, outB.completeness, outB.total, outB.components))
					}
				}
			}
		})
	}
}

func TestMetamorphicPricingMetamorphism(t *testing.T) {
	t.Parallel()
	graphs := smGraphs()
	evidenceSets := [][3]smEv{
		{smOne, smOne, smOne},
		{smTwo, smTwo, smTwo},
		{smZero, smOne, smTwo},
		{smOne, smAbsent, smOne},
		{smAbsent, smOne, smOne},
		{smOne, smUnavailable, smOne},
		{smTwo, smZero, smAbsent},
		{smAbsent, smAbsent, smAbsent},
	}
	nodes := &mtpGraph{name: "p5", keys: smNodeKeySet()}
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			continue
		}
		valid = append(valid, opts)
	}
	tally := newMTPTally("pricing_metamorphism", len(valid))
	t.Cleanup(func() { tally.finish(t) })

	// The three tallies below are written from parallel subtests, so they are
	// atomic rather than plain counters: a non-deterministic summary line would be
	// indistinguishable from a non-deterministic verdict.
	var contradictory, clean, outranked atomic.Int64
	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			rels := smRelationships(opts)
			schemas := smSchemas(rels)
			for _, ev := range evidenceSets {
				oracle := mtpOracle(t, "p5-oracle", schemas, smEvidenceForNodes(ev))
				wantContradiction := len(oracle.Contradicted) > 0
				if wantContradiction {
					contradictory.Add(1)
				} else {
					clean.Add(1)
				}
				obs := smMeasures(t, ev)
				// observed collects, per seam, the class each pricing state
				// reported, so clause 2 can compare them across the tariffs.
				observed := map[string][]string{}
				for _, state := range []struct {
					label  string
					prices mtpPricing
				}{
					{label: "paid", prices: mtpPaid(nodes)},
					{label: "explicit_free", prices: mtpFree(nodes)},
					{label: "no_rule", prices: mtpNoRule(nodes)},
				} {
					refID := fmt.Sprintf("p5-g%03d-%s-%s", gi, state.label, smEvCode(ev))
					resolved := f3Resolved(t, refID, state.prices.rules(t, nodes, fmt.Sprintf("g%03d-%s", gi, state.label)), schemas)
					for _, seam := range review5beSeams() {
						val, err := seam.rate(t, resolved, f3Observation(t, refID, obs...))
						outcome := mtpCollect(seam.name, val, err)
						label := fmt.Sprintf("p5 graph[%s] evidence[%s] pricing=%s seam=%s", smOptsName(opts), smEvName(ev), state.label, seam.name)
						observed[seam.name] = append(observed[seam.name], state.label+"="+outcome.errClass)
						tally.countCase()

						// Clause 1, the non-shadowable one.
						if wantContradiction && outcome.completeness == economics.CompletenessComplete {
							t.Errorf("%s: the tariff allowed a structurally CONTRADICTED graph to certify complete money; the model reports contradicted=%v and %s",
								label, oracle.Contradicted, outcome.tuple())
						}

						if outcome.errClass == "overlap_conflict" {
							// Outranked by a structural overlap, whose firing is a
							// tariff fact, so it is excluded from the class comparison.
							// Clause 1 does not skip it.
							outranked.Add(1)
							continue
						}
						if verdict := smIsQuantityContradiction(outcome.errClass); verdict && !mtpIs(err, outcome.errClass) {
							t.Errorf("%s: a quantity contradiction was reported as class %q but the retained error does not wrap the real sentinel",
								label, outcome.errClass)
						}
					}
				}
				// Clause 2, the cross-tariff class invariance.
				for _, seam := range review5beSeams() {
					distinct := map[string]struct{}{}
					for _, class := range mtpStructuralClasses(observed[seam.name]) {
						distinct[class] = struct{}{}
					}
					if len(distinct) <= 1 {
						continue
					}
					tally.countCase()
					t.Errorf("p5 graph[%s] evidence[%s] seam=%s: the tariff changed the reported STRUCTURAL class across the three pricing states; only a structural overlap may preempt another diagnosis, because only its firing is tariff-dependent. Reported: %v",
						smOptsName(opts), smEvName(ev), seam.name, observed[seam.name])
				}
			}
		})
	}
	t.Cleanup(func() {
		t.Logf("METAMORPHIC[pricing_metamorphism] evidence_assignments=%d model_contradictory_pairs=%d model_clean_pairs=%d outcomes_outranked_by_overlap=%d",
			len(evidenceSets), contradictory.Load(), clean.Load(), outranked.Load())
	})
}

func TestMetamorphicStructuralVerdictAgreesWithModel(t *testing.T) {
	t.Parallel()
	graphs := smGraphs()
	evidenceSets := [][3]smEv{
		{smOne, smOne, smOne},
		{smTwo, smTwo, smTwo},
		{smZero, smOne, smTwo},
		{smOne, smAbsent, smOne},
		{smAbsent, smOne, smOne},
		{smOne, smUnavailable, smOne},
		{smTwo, smZero, smAbsent},
		{smAbsent, smAbsent, smAbsent},
	}
	nodes := &mtpGraph{name: "p5x", keys: smNodeKeySet()}
	valid := make([][3]smEdgeKind, 0, len(graphs))
	for _, opts := range graphs {
		if err := metering.ValidateComponentSchemas(smSchemas(smRelationships(opts))); err != nil {
			continue
		}
		valid = append(valid, opts)
	}
	tally := newMTPTally("structural_verdict_vs_model", len(valid))
	t.Cleanup(func() { tally.finish(t) })

	var compared, ambiguousSkipped atomic.Int64
	for gi, opts := range valid {
		t.Run(fmt.Sprintf("g%03d", gi), func(t *testing.T) {
			t.Parallel()
			schemas := smSchemas(smRelationships(opts))
			resolved := f3Resolved(t, fmt.Sprintf("p5x-g%03d", gi), mtpFree(nodes).rules(t, nodes, fmt.Sprintf("g%03d", gi)), schemas)
			for _, ev := range evidenceSets {
				oracle := mtpOracle(t, "p5x-oracle", schemas, smEvidenceForNodes(ev))
				if len(oracle.Ambiguous) > 0 {
					ambiguousSkipped.Add(1)
					continue
				}
				wantContradiction := len(oracle.Contradicted) > 0
				compared.Add(1)
				obs := smMeasures(t, ev)
				for _, seam := range review5beSeams() {
					val, err := seam.rate(t, resolved, f3Observation(t, fmt.Sprintf("p5x-g%03d-%s", gi, smEvCode(ev)), obs...))
					outcome := mtpCollect(seam.name, val, err)
					label := fmt.Sprintf("p5x graph[%s] evidence[%s] seam=%s", smOptsName(opts), smEvName(ev), seam.name)
					tally.countCase()
					verdict := smIsQuantityContradiction(outcome.errClass)
					// When the model finds a physical impossibility but the
					// complete-coverage proof already produced a MORE SPECIFIC
					// diagnosis for the same node - the member was never reported,
					// or its children are ambiguously shared - that diagnosis is
					// authoritative and the solver's own verdict is deliberately
					// suppressed as a duplicate of it. Requiring class equality
					// here would forbid a better answer, so the substituted class is
					// accepted, and the SAFETY property that actually matters is
					// asserted instead: production must still never certify
					// complete money.
					substituted := wantContradiction && !verdict && mtpIsSpecificCoverDiagnosis(outcome.errClass)
					if verdict != wantContradiction && !substituted {
						t.Errorf("%s: production and the independent model disagree on the STRUCTURAL quantity verdict: production reports contradiction=%v (class %q) and the model reports contradiction=%v (model_contradicted=%v); %s",
							label, verdict, outcome.errClass, wantContradiction, oracle.Contradicted, outcome.tuple())
					}
					if wantContradiction && outcome.completeness == economics.CompletenessComplete {
						t.Errorf("%s: the model found a physical impossibility but production certified COMPLETE (class %q); %s",
							label, outcome.errClass, outcome.tuple())
					}
					if substituted && outcome.completeness == economics.CompletenessComplete {
						t.Errorf("%s: a more specific cover diagnosis (%q) was substituted yet the valuation is still COMPLETE; %s",
							label, outcome.errClass, outcome.tuple())
					}
					if verdict && !mtpIs(err, outcome.errClass) {
						t.Errorf("%s: a quantity contradiction was reported as class %q but the retained error does not wrap the real sentinel",
							label, outcome.errClass)
					}
				}
			}
		})
	}
	t.Cleanup(func() {
		t.Logf("METAMORPHIC[structural_verdict_vs_model] graph_evidence_pairs_compared=%d pairs_skipped_model_ambiguous=%d",
			compared.Load(), ambiguousSkipped.Load())
	})
}

func TestSupportAgreementShadowPredicate(t *testing.T) {
	t.Parallel()
	started := time.Now()
	populations := map[string]*supportTally{}
	join := func(label string, run func(t *testing.T) *supportTally) {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			populations[label] = run(t)
		})
	}
	join("model_structure", func(t *testing.T) *supportTally {
		return supportAgreementModelSweep(t, "model_structure", smFreeRules)
	})
	join("model_commercial", func(t *testing.T) *supportTally {
		return supportAgreementModelSweep(t, "model_commercial", smCommercialRules)
	})
	join("acceptance_vectors", supportAgreementAcceptanceVectors)
	join("regression_schemas", supportAgreementRegressionSchemas)
	t.Cleanup(func() {
		total := newSupportTally("ALL_POPULATIONS")
		for _, label := range supportSortedKeys(supportTallyKeys(populations)) {
			tally := populations[label]
			tally.report(t, 0)
			supportMerge(total, tally)
		}
		total.report(t, time.Since(started))
	})
}

func TestReplayDeepestPublishableChainTraversalIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("deep-chain boundedness probe measures multi-second ratings at the maximum publishable depth")
	}
	// DELIBERATELY NOT t.Parallel(). A process-wide StackInuse delta is only
	// meaningful in a quiet process, and Go guarantees exactly that here: a
	// top-level test that does not call t.Parallel runs to completion during the
	// serial phase, while every queued parallel test is still paused. That is
	// what lets this test ASSERT the non-recursion bound rather than merely
	// report it, and the mandatory certification job collects this bound on every
	// relevant change. The cost is that this
	// probe's multi-second ratings are no longer overlapped with the rest of the
	// package; that is the price of a gate that actually gates.
	maxDepth := metering.MaxComponentSchemas * metering.MaxComponentSchemaRelationships
	t.Logf("REPLAY-BOUNDEDNESS deepest publishable chain depth=%d over MaxComponentSchemas=%d x MaxComponentSchemaRelationships=%d",
		maxDepth, metering.MaxComponentSchemas, metering.MaxComponentSchemaRelationships)

	depths := []int{200, 800, 3200, maxDepth}
	type sample struct {
		depth         int
		nodes         int
		duration      time.Duration
		stackGrowth   int64
		allocsPerNode float64
		bytesPerNode  float64
		fingerprint   string
		completeness  economics.Completeness
	}
	samples := make([]sample, 0, len(depths))
	for _, depth := range depths {
		fixture := drChainFixture(t, depth)
		resolved := drPublish(t, fixture, fixture.rules, fixture.schemas)
		rater, err := billing.NewReferenceRater(resolved)
		if err != nil {
			t.Fatalf("depth %d: NewReferenceRater: %v", depth, err)
		}
		input := b1OperatorInput(t, resolved, fixture.obs)

		// The reported call is the second one: the warm-up asserts the fixture
		// still exercises the solver and pays the first-touch costs that are not
		// this traversal's own.
		if _, warmErr := rater.Rate(t.Context(), input); warmErr == nil {
			t.Fatalf("depth %d: the deep chain rated completely; the fixture no longer exercises the solver", depth)
		}

		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		val, rateErr := rater.Rate(t.Context(), input)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		if rateErr == nil {
			t.Fatalf("depth %d: deep chain rated without a diagnostic (lines=%+v)", depth, val.Lines)
		}
		// The section-18 non-recursion claim, enforced. The invariant is that the
		// stack does not grow WITH DEPTH: every containment traversal in the
		// solver and the compiled program is iterative, so a chain at the
		// publication maximum must cost what a shallow chain costs. The ABSOLUTE
		// reading is not the claim - it carries a fixed offset from wherever the
		// allocator happened to be, measured flat at 327680 B here but a single
		// quantum in a fresh process - so the assertion below compares depths
		// against each other rather than against a constant.
		stackGrowth := drStackGrowth(after.StackInuse-before.StackInuse, after.StackInuse)
		// The canonical fingerprint of the deepest publishable chain is PINNED
		// per depth, not merely reported: a drifted durable identity must fail
		// here rather than appear in the log.
		if pinned, ok := drDeepChainFingerprints[depth]; ok {
			if val.Fingerprint() != pinned {
				t.Errorf("depth %d: the deep-chain valuation fingerprint drifted.\n  pinned:   %s\n  observed: %s\n"+
					"  this is the durable replay identity at this containment depth, so a change here is a behavior change, not noise",
					depth, pinned, val.Fingerprint())
			}
		} else {
			t.Errorf("depth %d: the deep-chain fingerprint is not pinned; a reproducible but unpinned fingerprint is not a pin", depth)
		}
		samples = append(samples, sample{
			depth: depth, nodes: depth + 1, duration: elapsed, stackGrowth: stackGrowth,
			allocsPerNode: float64(after.Mallocs-before.Mallocs) / float64(depth+1),
			bytesPerNode:  float64(after.TotalAlloc-before.TotalAlloc) / float64(depth+1),
			fingerprint:   val.Fingerprint(), completeness: val.Completeness,
		})
	}

	for _, s := range samples {
		t.Logf("REPLAY-BOUNDEDNESS depth=%d nodes=%d wall=%s stack_growth=%dB allocs/node=%.1f bytes/node=%.0f completeness=%s fingerprint=%s",
			s.depth, s.nodes, s.duration.Round(time.Millisecond), s.stackGrowth,
			s.allocsPerNode, s.bytesPerNode, s.completeness, s.fingerprint)
	}
	// Cost per node must not grow with depth. TotalAlloc and Mallocs are
	// process-wide, so this reading is only trustworthy because the test is
	// serial; the invariant it can carry is SUBLINEARITY, not an absolute
	// budget. A traversal that swept the whole graph once per containment level
	// would leave stack growth flat - it holds no frames - while multiplying
	// allocations and bytes by the depth ratio, so a stack-only bound cannot
	// see it. The depth ratio between the extreme rungs is 8193/201, about 40x;
	// a linear-in-depth walk would drive per-node cost up by that same factor.
	shallowest, deepest := samples[0], samples[len(samples)-1]
	allocLinear := shallowest.allocsPerNode * (float64(deepest.depth) / float64(shallowest.depth))
	bytesLinear := shallowest.bytesPerNode * (float64(deepest.depth) / float64(shallowest.depth))
	if deepest.allocsPerNode > allocLinear {
		t.Errorf("allocations per node grew with containment depth: %.1f at depth %d versus %.1f at depth %d.\n"+
			"  The shallow chain's per-node cost extrapolated linearly over the %dx depth range is %.1f,\n"+
			"  so cost per node is not sublinear and a whole-graph sweep per containment level has reappeared.\n"+
			"  Every containment traversal is iterative, so depth must not multiply work",
			deepest.allocsPerNode, deepest.depth, shallowest.allocsPerNode, shallowest.depth,
			deepest.depth/shallowest.depth, allocLinear)
	}
	if deepest.bytesPerNode > bytesLinear {
		t.Errorf("bytes per node grew with containment depth: %.0f at depth %d versus %.0f at depth %d.\n"+
			"  The shallow chain's per-node cost extrapolated linearly over the %dx depth range is %.0f,\n"+
			"  so cost per node is not sublinear and a whole-graph sweep per containment level has reappeared",
			deepest.bytesPerNode, deepest.depth, shallowest.bytesPerNode, shallowest.depth,
			deepest.depth/shallowest.depth, bytesLinear)
	}
	// Depth is the variable under test, so compare every depth against every
	// other. A traversal that recursed would hold one frame per level, so the
	// spread across the 200 to 8192 ladder would be thousands of live frames.
	// Comparing the whole ladder rather than its two endpoints also closes the
	// interior-depth hole: a regression confined to a middle rung is still a
	// spread, while a two-endpoint comparison would only see it if the outermost
	// depth happened to regress too. One stack quantum of slack absorbs
	// allocator noise between two separate readings.
	const stackQuantum = 64 << 10
	minSample, maxSample := samples[0], samples[0]
	for _, s := range samples[1:] {
		if s.stackGrowth < minSample.stackGrowth {
			minSample = s
		}
		if s.stackGrowth > maxSample.stackGrowth {
			maxSample = s
		}
	}
	if spread := maxSample.stackGrowth - minSample.stackGrowth; spread > stackQuantum {
		t.Errorf("goroutine stack grew with containment depth: %d B spread, %d B at depth %d versus %d B at depth %d,\n"+
			"  over the %d B slack. Every containment traversal in the solver and the compiled\n"+
			"  program is iterative, so depth must not add stack frames",
			spread, maxSample.stackGrowth, maxSample.depth, minSample.stackGrowth, minSample.depth, stackQuantum)
	}
}
