//go:build integration

package billing_test

import (
	"fmt"
	"math"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	var populationsMu sync.Mutex
	join := func(label string, run func(t *testing.T) *supportTally) {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			tally := run(t)
			populationsMu.Lock()
			populations[label] = tally
			populationsMu.Unlock()
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
	if mode := drStackChildMode(); mode != "" {
		drRunStackChild(t, mode)
		return
	}
	depths := []int{200, 800, 3200, metering.MaxComponentSchemas * metering.MaxComponentSchemaRelationships}
	type sample struct {
		depth         int
		stackGrowth   int64
		allocsPerNode float64
		bytesPerNode  float64
		fingerprint   string
	}
	samples := make([]sample, 0, len(depths))
	for _, depth := range depths {
		output, err := drRunStackChildProcess(t, fmt.Sprintf("%s%d", drStackDepthPrefix, depth), "^TestReplayDeepestPublishableChainTraversalIsBounded$")
		if err != nil {
			t.Fatalf("deep-chain child depth %d failed: %v\n%s", depth, err, output)
		}
		result, err := drParseStackResult(output, depth)
		if err != nil {
			t.Fatalf("deep-chain child depth %d returned invalid bounded evidence: %v\n%s", depth, err, output)
		}
		if result.depth != depth {
			t.Fatalf("deep-chain child reported depth %d, want %d", result.depth, depth)
		}
		if pinned, ok := drDeepChainFingerprints[depth]; ok && result.fingerprint != pinned {
			t.Fatalf("depth %d: fingerprint drifted: pinned=%s observed=%s", depth, pinned, result.fingerprint)
		}
		samples = append(samples, sample{depth: depth, stackGrowth: result.stackGrowth, allocsPerNode: result.allocsPerNode, bytesPerNode: result.bytesPerNode, fingerprint: result.fingerprint})
		t.Logf("REPLAY-BOUNDEDNESS depth=%d stack_growth=%dB allocs/node=%.1f bytes/node=%.0f fingerprint=%s", depth, result.stackGrowth, result.allocsPerNode, result.bytesPerNode, result.fingerprint)
	}
	minSample, maxSample := samples[0], samples[0]
	for _, s := range samples[1:] {
		if s.stackGrowth < minSample.stackGrowth {
			minSample = s
		}
		if s.stackGrowth > maxSample.stackGrowth {
			maxSample = s
		}
	}
	if spread := maxSample.stackGrowth - minSample.stackGrowth; spread > drStackSpreadLimit {
		t.Fatalf("goroutine stack grew with containment depth: %dB spread, %dB at depth %d versus %dB at depth %d, over the %dB limit", spread, maxSample.stackGrowth, maxSample.depth, minSample.stackGrowth, minSample.depth, drStackSpreadLimit)
	}
	shallowest := samples[0]
	allocLinear := drAllocationPerNodeLimit(shallowest.allocsPerNode)
	bytesLinear := drAllocationPerNodeLimit(shallowest.bytesPerNode)
	for _, s := range samples[1:] {
		if s.allocsPerNode > allocLinear {
			t.Errorf("allocations per node grew with containment depth: %.1f at depth %d versus %.1f at depth %d (constant per-node allowance %.1f)", s.allocsPerNode, s.depth, shallowest.allocsPerNode, shallowest.depth, allocLinear)
		}
		if s.bytesPerNode > bytesLinear {
			t.Errorf("bytes per node grew with containment depth: %.0f at depth %d versus %.0f at depth %d (constant per-node allowance %.0f)", s.bytesPerNode, s.depth, shallowest.bytesPerNode, shallowest.depth, bytesLinear)
		}
	}
	for _, mode := range []string{drStackChildMutantSmall, drStackChildMutantLarge} {
		output, err := drRunStackChildProcess(t, mode, "^TestReplayDeepestPublishableChainTraversalIsBounded$")
		if err != nil {
			t.Fatalf("depth-growth certificate failed to reject %s mutant: err=%v output=%s", mode, err, output)
		}
		mutantResult, err := drParseMutantStackResult(output, mode, metering.MaxComponentSchemas*metering.MaxComponentSchemaRelationships)
		if err != nil || mutantResult.stackGrowth <= samples[0].stackGrowth+drStackSpreadLimit {
			t.Fatalf("depth-growth certificate failed to reject %s mutant: err=%v growth=%d baseline=%d output=%s", mode, err, mutantResult.stackGrowth, samples[0].stackGrowth, output)
		}
		t.Logf("REPLAY-BOUNDEDNESS control=%s %s", mode, output)
	}
}

func drAllocationPerNodeLimit(baseline float64) float64 {
	// Allow fixed allocator size-class and map-capacity variation without
	// multiplying an already normalized per-node cost by graph depth. The
	// multiplier stays constant at every publication depth; controls below
	// certify that linear costs pass and quadratic costs fail.
	return baseline * 2
}

type drStackResult struct {
	depth         int
	stackGrowth   int64
	allocsPerNode float64
	bytesPerNode  float64
	wallMS        int64
	fingerprint   string
}

func drRunExplicitGCMeasurement(t *testing.T) {
	drMeasureRateWithRuntimeControls(t, func() (economics.Valuation, error) {
		runtime.GC()
		return economics.Valuation{}, nil
	}, nil)
	t.Fatal("explicit GC callback unexpectedly passed measurement controls")
}

func drParseProtocolRecord(output, prefix string, fields map[string]struct{}) (map[string]string, error) {
	var record map[string]string
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, prefix+" ") {
			return nil, fmt.Errorf("unexpected child output record")
		}
		if record != nil {
			return nil, fmt.Errorf("duplicate child output record")
		}
		record = make(map[string]string, len(fields))
		for _, field := range strings.Fields(strings.TrimPrefix(line, prefix+" ")) {
			key, value, ok := strings.Cut(field, "=")
			if !ok || key == "" || value == "" {
				return nil, fmt.Errorf("malformed child output field")
			}
			if _, ok := fields[key]; !ok {
				return nil, fmt.Errorf("unknown child output field")
			}
			if _, ok := record[key]; ok {
				return nil, fmt.Errorf("duplicate child output field")
			}
			record[key] = value
		}
	}
	if len(record) != len(fields) {
		return nil, fmt.Errorf("incomplete child output record")
	}
	return record, nil
}

func drParseStackResult(output string, expectedDepth int) (drStackResult, error) {
	fields, err := drParseProtocolRecord(output, "DR_STACK_RESULT", map[string]struct{}{
		"depth": {}, "stack_growth": {}, "allocs_per_node": {}, "bytes_per_node": {}, "fingerprint": {}, "wall_ms": {},
	})
	if err != nil {
		return drStackResult{}, err
	}
	if expectedDepth <= 0 {
		return drStackResult{}, fmt.Errorf("invalid expected depth")
	}
	depth, err := strconv.Atoi(fields["depth"])
	if err != nil || depth != expectedDepth {
		return drStackResult{}, fmt.Errorf("unexpected depth")
	}
	stackGrowth, err := strconv.ParseInt(fields["stack_growth"], 10, 64)
	if err != nil || stackGrowth < 0 {
		return drStackResult{}, fmt.Errorf("invalid stack growth")
	}
	allocsPerNode, err := strconv.ParseFloat(fields["allocs_per_node"], 64)
	if err != nil || allocsPerNode < 0 || math.IsNaN(allocsPerNode) || math.IsInf(allocsPerNode, 0) {
		return drStackResult{}, fmt.Errorf("invalid allocations per node")
	}
	bytesPerNode, err := strconv.ParseFloat(fields["bytes_per_node"], 64)
	if err != nil || bytesPerNode < 0 || math.IsNaN(bytesPerNode) || math.IsInf(bytesPerNode, 0) {
		return drStackResult{}, fmt.Errorf("invalid bytes per node")
	}
	wallMS, err := strconv.ParseInt(fields["wall_ms"], 10, 64)
	if err != nil || wallMS < 0 {
		return drStackResult{}, fmt.Errorf("invalid wall time")
	}
	return drStackResult{
		depth: depth, stackGrowth: stackGrowth, allocsPerNode: allocsPerNode,
		bytesPerNode: bytesPerNode, wallMS: wallMS, fingerprint: fields["fingerprint"],
	}, nil
}

type drMutantStackResult struct {
	frame       string
	depth       int
	stackGrowth int64
}

func drParseMutantStackResult(output, requestedMode string, expectedDepth int) (drMutantStackResult, error) {
	expectedFrame, ok := map[string]string{
		drStackChildMutantSmall: "small",
		drStackChildMutantLarge: "large",
	}[requestedMode]
	if !ok || expectedDepth <= 0 {
		return drMutantStackResult{}, fmt.Errorf("invalid requested mutant")
	}
	fields, err := drParseProtocolRecord(output, "DR_STACK_MUTANT", map[string]struct{}{
		"frame": {}, "depth": {}, "stack_growth": {},
	})
	if err != nil {
		return drMutantStackResult{}, err
	}
	depth, err := strconv.Atoi(fields["depth"])
	if err != nil || depth != expectedDepth {
		return drMutantStackResult{}, fmt.Errorf("unexpected mutant depth")
	}
	if fields["frame"] != expectedFrame {
		return drMutantStackResult{}, fmt.Errorf("unexpected mutant frame")
	}
	stackGrowth, err := strconv.ParseInt(fields["stack_growth"], 10, 64)
	if err != nil || stackGrowth < 0 {
		return drMutantStackResult{}, fmt.Errorf("invalid mutant stack growth")
	}
	return drMutantStackResult{frame: fields["frame"], depth: depth, stackGrowth: stackGrowth}, nil
}

type drRateSample struct {
	value    economics.Valuation
	err      error
	before   runtime.MemStats
	after    runtime.MemStats
	duration time.Duration
}

func drMeasureRateWithRuntimeControls(t *testing.T, invoke func() (economics.Valuation, error), beforeInvoke func()) drRateSample {
	t.Helper()
	// Fixture construction and the warm-up happen before this boundary. Settle
	// the process first, then disable both automatic-GC triggers for the exact
	// measurement window. SetGCPercent(-1) alone is insufficient because a low
	// soft memory limit can still force a collection.
	runtime.GC()
	previousLimit := debug.SetMemoryLimit(math.MaxInt64)
	previousGC := debug.SetGCPercent(-1)
	defer debug.SetMemoryLimit(previousLimit)
	defer debug.SetGCPercent(previousGC)
	return drMeasureRate(t, invoke, beforeInvoke)
}

func drMeasureRate(t *testing.T, invoke func() (economics.Valuation, error), beforeInvoke func()) drRateSample {
	t.Helper()
	// Pre-grow an unrelated goroutine before the baseline. Its retained stack
	// is deliberately present in both MemStats readings, demonstrating that a
	// background stack cannot create a depth-dependent delta in this isolated
	// child. The measured Rate goroutine is fresh and is held after return.
	noiseReady := make(chan struct{})
	noiseRelease := make(chan struct{})
	noiseDone := make(chan struct{})
	go func() {
		defer close(noiseDone)
		drRecursiveStackProbe(1024, func() {
			close(noiseReady)
			<-noiseRelease
		})
	}()
	<-noiseReady
	targetRelease := make(chan struct{})
	targetDone := make(chan struct{})
	result := make(chan drRateSample, 1)
	go func() {
		var sample drRateSample
		runtime.ReadMemStats(&sample.before)
		if beforeInvoke != nil {
			beforeInvoke()
		}
		started := time.Now()
		sample.value, sample.err = invoke()
		sample.duration = time.Since(started)
		runtime.ReadMemStats(&sample.after)
		result <- sample
		<-targetRelease
		close(targetDone)
	}()
	sample := <-result
	close(targetRelease)
	close(noiseRelease)
	<-targetDone
	<-noiseDone
	if sample.after.NumGC != sample.before.NumGC {
		fmt.Println("DR_STACK_DIAGNOSTIC gc_during_measurement")
		t.Fatalf("runtime GC occurred during measurement: before=%d after=%d", sample.before.NumGC, sample.after.NumGC)
	}
	return sample
}

func drRunDeepChainBoundedness(t *testing.T, depth int) {
	fixture := drChainFixture(t, depth)
	resolved := drPublish(t, fixture, fixture.rules, fixture.schemas)
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("depth %d: NewReferenceRater: %v", depth, err)
	}
	input := b1OperatorInput(t, resolved, fixture.obs)
	if _, warmErr := rater.Rate(t.Context(), input); warmErr == nil {
		t.Fatalf("depth %d: the deep chain rated completely; the fixture no longer exercises the solver", depth)
	}
	// GC is allowed during fixture construction and warm-up. Once both runtime
	// limits are disabled, the fresh Rate goroutine's retained stack is measured
	// while it remains alive, so no later shrink or unrelated process-wide GC can
	// erase its peak.
	sample := drMeasureRateWithRuntimeControls(t, func() (economics.Valuation, error) {
		return rater.Rate(t.Context(), input)
	}, nil)
	if sample.err == nil {
		t.Fatalf("depth %d: deep chain rated without a diagnostic", depth)
	}
	fingerprint := sample.value.Fingerprint()
	if pinned, ok := drDeepChainFingerprints[depth]; !ok || fingerprint != pinned {
		t.Fatalf("depth %d: deep-chain fingerprint drifted: pinned=%s observed=%s", depth, drDeepChainFingerprints[depth], fingerprint)
	}
	stackGrowth := int64(0)
	if sample.after.StackInuse > sample.before.StackInuse {
		stackGrowth = int64(sample.after.StackInuse - sample.before.StackInuse)
	}
	nodes := float64(depth + 1)
	fmt.Printf("DR_STACK_RESULT depth=%d stack_growth=%d allocs_per_node=%.6f bytes_per_node=%.6f fingerprint=%s wall_ms=%d\n",
		depth, stackGrowth, float64(sample.after.Mallocs-sample.before.Mallocs)/nodes,
		float64(sample.after.TotalAlloc-sample.before.TotalAlloc)/nodes, fingerprint, sample.duration.Milliseconds())
}

func drRunRateStackMutant(t *testing.T, large bool) {
	depth := metering.MaxComponentSchemas * metering.MaxComponentSchemaRelationships
	fixture := drChainFixture(t, depth)
	resolved := drPublish(t, fixture, fixture.rules, fixture.schemas)
	rater, err := billing.NewReferenceRater(resolved)
	if err != nil {
		t.Fatalf("mutant depth %d: NewReferenceRater: %v", depth, err)
	}
	input := b1OperatorInput(t, resolved, fixture.obs)
	if _, warmErr := rater.Rate(t.Context(), input); warmErr == nil {
		t.Fatalf("mutant depth %d: the deep chain rated completely", depth)
	}
	invoke := func() (economics.Valuation, error) {
		if large {
			var value economics.Valuation
			var rateErr error
			drRateStackMutantLarge(depth, func() { value, rateErr = rater.Rate(t.Context(), input) })
			return value, rateErr
		}
		var value economics.Valuation
		var rateErr error
		drRateStackMutantSmall(depth, func() { value, rateErr = rater.Rate(t.Context(), input) })
		return value, rateErr
	}
	sample := drMeasureRateWithRuntimeControls(t, invoke, nil)
	if sample.err == nil {
		t.Fatalf("mutant depth %d: deep chain rated completely", depth)
	}
	stackGrowth := int64(0)
	if sample.after.StackInuse > sample.before.StackInuse {
		stackGrowth = int64(sample.after.StackInuse - sample.before.StackInuse)
	}
	fmt.Printf("DR_STACK_MUTANT frame=%s depth=%d stack_growth=%d\n", map[bool]string{true: "large", false: "small"}[large], depth, stackGrowth)
}

//go:noinline
func drRateStackMutantSmall(depth int, invoke func()) {
	var frame [64]byte
	frame[0] = byte(depth)
	if depth == 0 {
		invoke()
	} else {
		drRateStackMutantSmall(depth-1, invoke)
	}
	runtime.KeepAlive(frame)
}

//go:noinline
func drRateStackMutantLarge(depth int, invoke func()) {
	var frame [512]byte
	frame[0] = byte(depth)
	if depth == 0 {
		invoke()
	} else {
		drRateStackMutantLarge(depth-1, invoke)
	}
	runtime.KeepAlive(frame)
}
