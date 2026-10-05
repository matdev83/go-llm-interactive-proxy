//go:build integration

package billing_test

import "testing"

func TestDRAllocationEnvelopeRejectsQuadraticGrowth(t *testing.T) {
	t.Parallel()
	const shallowDepth = 200
	for _, coefficient := range []float64{1, 64} {
		shallowNodes := float64(shallowDepth + 1)
		baseline := coefficient * shallowNodes * shallowNodes / shallowNodes
		for _, depth := range []int{800, 3200, 8192} {
			nodes := float64(depth + 1)
			// Independent total-cost model, normalized exactly as the runtime
			// probe: a depth-d chain has d+1 nodes. No timing or GC noise.
			quadraticPerNode := coefficient * nodes * nodes / nodes
			limit := drAllocationPerNodeLimit(baseline)
			if quadraticPerNode <= limit {
				t.Errorf("quadratic total cost accepted: depth=%d coefficient=%.0f per_node=%.2f limit=%.2f", depth, coefficient, quadraticPerNode, limit)
			}
		}
	}
}

func TestDRAllocationEnvelopeAcceptsLinearGrowth(t *testing.T) {
	t.Parallel()
	const shallowDepth = 200
	for _, coefficient := range []float64{1, 64} {
		baseline := (coefficient*float64(shallowDepth+1) + 512) / float64(shallowDepth+1)
		for _, depth := range []int{800, 3200, 8192} {
			nodes := float64(depth + 1)
			linearPerNode := (coefficient*nodes + 512) / nodes
			if limit := drAllocationPerNodeLimit(baseline); linearPerNode > limit {
				t.Errorf("linear total cost rejected: depth=%d per_node=%.2f limit=%.2f", depth, linearPerNode, limit)
			}
		}
	}
}
