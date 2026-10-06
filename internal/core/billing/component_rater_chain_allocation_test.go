package billing

import (
	"math"
	"math/big"
	"runtime"
	"runtime/debug"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestContainmentUnknownIntersectionsDisjointChainsAvoidReachabilityAllocations(t *testing.T) {
	for _, disconnected := range []bool{false, true} {
		for _, count := range []int{201, 801} {
			program := schemaProgram{
				keyOf: make([]metering.ComponentKey, count), keyStrings: make([]string, count),
				containmentChildren: make([][]int, count), reverseContainment: make([][]int, count),
			}
			lower := make([]*big.Rat, count)
			represented := make([]*big.Rat, count)
			for node := range count {
				lower[node] = big.NewRat(1, 1)
				// Two disconnected chains; the split must not be mistaken for
				// unrelated siblings sharing an ancestor.
				if node+1 < count && (!disconnected || node != count/2) {
					program.containmentChildren[node] = []int{node + 1}
					program.reverseContainment[node+1] = []int{node}
				}
			}
			allocs := testing.AllocsPerRun(1, func() {
				if got := containmentUnknownIntersections(&program, lower, represented); got != nil {
					t.Fatal("disjoint chains produced unknown intersections")
				}
			})
			if allocs != 0 {
				t.Errorf("disjoint chains allocated transitive reachability: nodes=%d allocations=%.0f", count, allocs)
			}
		}
	}
}

func TestContainmentUnknownIntersectionsBranchStillReportsSiblings(t *testing.T) {
	t.Parallel()
	program := schemaProgram{
		keyOf: make([]metering.ComponentKey, 3), keyStrings: []string{"root", "left", "right"},
		containmentChildren: [][]int{{1, 2}, nil, nil}, reverseContainment: [][]int{nil, {0}, {0}},
	}
	got := containmentUnknownIntersections(&program, []*big.Rat{nil, big.NewRat(1, 1), big.NewRat(1, 1)}, make([]*big.Rat, 3))
	if !slices.Equal(got, []string{"left ~ right"}) {
		t.Fatalf("branch lost unknown-intersection diagnosis: got=%v", got)
	}
}

func TestContainmentUnknownIntersectionsDiamondStillReportsSiblings(t *testing.T) {
	t.Parallel()
	program := schemaProgram{
		keyOf: make([]metering.ComponentKey, 4), keyStrings: []string{"root", "left", "right", "leaf"},
		containmentChildren: [][]int{{1, 2}, {3}, {3}, nil}, reverseContainment: [][]int{nil, {0}, {0}, {1, 2}},
	}
	got := containmentUnknownIntersections(&program, []*big.Rat{nil, big.NewRat(1, 1), big.NewRat(1, 1), nil}, make([]*big.Rat, 4))
	if !slices.Equal(got, []string{"left ~ right"}) {
		t.Fatalf("diamond lost unknown-intersection diagnosis: got=%v", got)
	}
}

func TestSchemaProgramReachableAllocationTracksVisitedNodes(t *testing.T) {
	var baseline int64
	for _, count := range []int{201, 8193} {
		program := schemaProgram{completeChildren: make([][]int, count)}
		program.completeChildren[0] = []int{1}
		bytesPerWalk := fixedReachabilityBytes(t, &program)
		t.Logf("nodes=%d bytes/walk=%d", count, bytesPerWalk)
		if baseline == 0 {
			baseline = bytesPerWalk
		} else if bytesPerWalk > baseline*2 {
			t.Errorf("one-hop walk allocation grew with unrelated graph nodes: nodes=%d bytes/walk=%d baseline=%d", count, bytesPerWalk, baseline)
		}
	}
}

func fixedReachabilityBytes(t *testing.T, program *schemaProgram) int64 {
	t.Helper()
	// Warm before a fixed, short measurement window. The test is sequential
	// because the runtime controls and allocation counters are process-wide.
	_ = program.reachable(completeInclusionEdges, 0)
	runtime.GC()
	previousLimit := debug.SetMemoryLimit(math.MaxInt64)
	previousGC := debug.SetGCPercent(-1)
	defer debug.SetMemoryLimit(previousLimit)
	defer debug.SetGCPercent(previousGC)
	var before, after runtime.MemStats
	const walks = 32
	var got map[int]struct{}
	runtime.ReadMemStats(&before)
	for range walks {
		got = program.reachable(completeInclusionEdges, 0)
		if _, ok := got[1]; !ok || len(got) != 1 {
			t.Fatal("one-hop reachability changed")
		}
	}
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(got)
	if after.NumGC != before.NumGC {
		t.Fatal("GC occurred during fixed allocation measurement")
	}
	return int64(after.TotalAlloc-before.TotalAlloc) / walks
}
