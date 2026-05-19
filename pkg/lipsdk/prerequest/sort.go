package prerequest

import "slices"

// MaterializeSorted returns handlers sorted by Order, ID, and original registration index.
func MaterializeSorted(in []Handler) []Handler {
	if len(in) == 0 {
		return nil
	}
	idx := make([]int, len(in))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := in[hi], in[hj]
		if a.Order() < b.Order() {
			return -1
		}
		if a.Order() > b.Order() {
			return 1
		}
		if a.ID() < b.ID() {
			return -1
		}
		if a.ID() > b.ID() {
			return 1
		}
		if hi < hj {
			return -1
		}
		if hi > hj {
			return 1
		}
		return 0
	})
	out := make([]Handler, len(in))
	for k, ii := range idx {
		out[k] = in[ii]
	}
	return out
}
