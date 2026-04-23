package request

import (
	"cmp"
	"slices"
)

// participantLess is design §17 ordering: order, id, registration index.
func participantLess(orderA, orderB int, idA, idB string, regIdxA, regIdxB int) int {
	if c := cmp.Compare(orderA, orderB); c != 0 {
		return c
	}
	if c := cmp.Compare(idA, idB); c != 0 {
		return c
	}
	return cmp.Compare(regIdxA, regIdxB)
}

// MaterializeSorted returns a copy of transforms sorted for stable execution
// (same rule as the hook bus: ascending Order, ID, registration index).
func MaterializeSorted(transforms []Transform) []Transform {
	if len(transforms) == 0 {
		return nil
	}
	h := slices.Clone(transforms)
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		return participantLess(a.Order(), b.Order(), a.ID(), b.ID(), hi, hj)
	})
	out := make([]Transform, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}
