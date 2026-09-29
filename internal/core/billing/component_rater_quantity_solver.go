package billing

// QUANTITY SOLVER. This file owns the interval-constraint solver that is the
// single authority on component quantity semantics for one reduction scope: the
// per-scope solve, the evidence states, the monotone up/down passes with their
// settled prune and markMoved closure, the per-node contradiction flags, the
// represented-exact derivation it is handed, and the per-scope verdict the
// rating loop reports.
//
// It deliberately owns NO declared topology (it reads the compiled program), NO
// cover verdict beyond the represented-exact values resolveCompleteCovers hands
// it, NO payability and NO money: the solver decides only what the evidence
// forces. The ORDER IS PART OF THE RESULT block on propagateQuantityIntervals is
// a recorded invariant of these passes and travels with them.

import (
	"math/big"
	"sort"
)

// schemaQuantityVerdict is the quantity-only classification produced by the
// compiled constraint solver for one reduction. It maps each reduction scope to
// the canonical component keys that violate that rule.
//
// subset names nodes whose containment bound is violated: the enforced lower
// bound (from themselves or a contained descendant, across the transitive union
// of subset and complete-coverage edges) exceeds the enforced upper bound (from
// themselves or a containing ancestor). negative names nodes whose effective
// reduced ordinary-usage quantity is strictly below zero. Both are quantity
// facts, so they never depend on which side happens to be priced. unknown carries
// the decision-8 unknown containment intersections.
type schemaQuantityVerdict struct {
	subset   map[string]map[string]struct{}
	negative map[string]map[string]struct{}
	// unknown carries the decision-8 unknown containment intersections. They are
	// deliberately NON-BLOCKING: they never downgrade completeness and never
	// change money. They are reported as an auxiliary diagnostic only when the
	// valuation is already non-complete.
	unknown map[string][]string
}

// schemaQuantityContradictions is the single quantity authority for the rater.
// It compiles nothing at call time: the schema program is already frozen on the
// rater. Only the ONE full reduction is passed here, the same state pricing is
// derived from, so a covered but unpriced child that is commercially redundant
// for billing is still a physical quantity fact for the containment arithmetic.
func (r *ReferenceRater) schemaQuantityContradictions(aggregates []aggregateMeasure, diagnosed map[string]map[string]struct{}) schemaQuantityVerdict {
	if r == nil || len(r.program.keyOf) == 0 || len(aggregates) == 0 {
		return schemaQuantityVerdict{}
	}
	byScope := make(map[string][]aggregateMeasure)
	for _, item := range aggregates {
		byScope[item.scopeKey] = append(byScope[item.scopeKey], item)
	}
	scopes := make([]string, 0, len(byScope))
	for scope := range byScope {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	var verdict schemaQuantityVerdict
	for _, scope := range scopes {
		subset, negative, unknown := solveScopeQuantityConstraints(&r.program, byScope[scope], diagnosed[scope])
		mergeSchemaQuantitySet(&verdict.subset, scope, subset)
		mergeSchemaQuantitySet(&verdict.negative, scope, negative)
		if len(unknown) != 0 {
			if verdict.unknown == nil {
				verdict.unknown = make(map[string][]string)
			}
			verdict.unknown[scope] = unknown
		}
	}
	return verdict
}

func mergeSchemaQuantitySet(target *map[string]map[string]struct{}, scope string, keys map[string]struct{}) {
	if len(keys) == 0 {
		return
	}
	if *target == nil {
		*target = make(map[string]map[string]struct{})
	}
	scopeSet := (*target)[scope]
	if scopeSet == nil {
		scopeSet = make(map[string]struct{}, len(keys))
		(*target)[scope] = scopeSet
	}
	for key := range keys {
		scopeSet[key] = struct{}{}
	}
}

// quantityEvidenceState is the stored state of one node's evidence in one
// reduction scope. The three states are exhaustive and mutually exclusive, and
// the distinction is deliberate: Exact(0) is a genuine observed zero while
// Absent and Unavailable are unknown and must never be read as zero.
type quantityEvidenceState uint8

const (
	quantityAbsent quantityEvidenceState = iota
	quantityExact
	quantityUnavailable
)

// solveScopeQuantityConstraints runs one deterministic, monotone interval
// fixed point over the compiled program using only evidence from a single
// reduction scope. Evidence never crosses scopes.
//
// Each node starts at lower=0, upper=+infinity. Exact(q) pins both bounds to q
// (a negative q clamps lower to 0 and is then reported as a structural
// contradiction, never silently treated as zero). Absent and Unavailable keep
// lower=0, upper=+infinity but remain distinct in the stored state.
//
// The constraints are:
//
//	subset   P -> C : lower(P) = max(lower(P), lower(C)); upper(C) = min(upper(C), upper(P))
//
//	complete P -> {Ci} : lower(P) >= sum(lower(Ci)); upper(P) <= sum(upper(Ci)) when all finite
//
// plus the child-directed reverse rules. Bound propagation is INFERENCE, not
// observation: the subset upper bound reaches an absent or unavailable child
// exactly like any other, and a reverse rule inverts a conserved equation only for
// a child with a known exact representation. Which members have one, the
// absent-optional edge-local zero, the all-finite guard, and the rule that only
// resolveCompleteCovers may create a represented value, are stated on that
// authority, which is the one the conservation proof and the overlap resolver read.
//
// "complete" here is the synonym of aggregate and partition. A node whose enforced
// lower bound exceeds its upper bound is a quantity contradiction, and the solver
// stops propagating from an already-contradicted node so one inconsistent subtree
// cannot manufacture a second, unrelated report. Bounds are integers of the input
// rationals only. The sweep bound and the reason it suffices live on
// propagateQuantityIntervals. Node order is the sorted-id topological order and edge
// order is the sorted-id compile order, so no map iteration order can affect the
// result.
func solveScopeQuantityConstraints(program *schemaProgram, items []aggregateMeasure, diagnosed map[string]struct{}) (map[string]struct{}, map[string]struct{}, []string) {
	count := len(program.keyOf)
	// The scope's evidence reading AND its recursive complete-cover
	// representation come from the one shared authority, so the reverse
	// complete-coverage inversion below can never disagree with the conservation
	// proof or the overlap resolver about which member has a known share.
	cover := resolveCompleteCovers(program, items)
	lower, _, contradicted := propagateQuantityIntervals(program, cover.state, cover.exact, cover.represented)
	// A node the partition proof already classified -- and any node contained
	// beneath it -- is left to that more precise diagnosis: the containment
	// violation below it is the same physical inconsistency expressed less
	// specifically, so restating it here would only add a duplicate. Parents are
	// visited before children in topo order, so one pass propagates the flag.
	diagnosedBelow := make([]bool, count)
	for _, node := range program.topo {
		flagged := false
		if diagnosed != nil {
			if _, ok := diagnosed[program.keyStrings[node]]; ok {
				flagged = true
			}
		}
		if !flagged {
			for _, parent := range program.reverseContainment[node] {
				if diagnosedBelow[parent] {
					flagged = true
					break
				}
			}
		}
		diagnosedBelow[node] = flagged
	}
	subset := make(map[string]struct{})
	negativeKeys := make(map[string]struct{})
	for node := range count {
		if diagnosedBelow[node] {
			continue
		}
		key := program.keyStrings[node]
		if cover.negative[node] {
			negativeKeys[key] = struct{}{}
		}
		if contradicted[node] {
			subset[key] = struct{}{}
		}
	}
	return subset, negativeKeys, containmentUnknownIntersections(program, lower, cover.represented)
}

// propagateQuantityIntervals runs the monotone interval fixed point described on
// solveScopeQuantityConstraints and returns the final lower/upper bounds plus the
// per-node contradiction flags.
//
// SWEEP BOUND. The loop is bounded by a NO-CHANGE sweep, not by the counter: a
// sweep that changes no bound has reached the fixed point, because the transfer
// functions are monotone and a monotone operator applied to its own fixed point is a
// fixed point. The counter is a backstop, co-bounded by the program's PRECOMPUTED
// longest containment path rather than by the node count alone:
//
//	sweeps = min(len(topo)+1, 2*propagationDepth+3)
//
// 2*propagationDepth+3 is a PROVEN sufficient height, not an assumption: a bound's
// influence travels DOWN or UP the acyclic containment DAG, never both at once, and
// every edge is one hop, so a propagation phase needs at most propagationDepth
// sweeps to carry every derived bound everywhere it can reach, plus one sweep to
// observe that nothing changed. The lower bounds are a function of the lower bounds
// alone; the upper bounds read both, so they settle in the same window once the
// lower ones are final. Contradiction is monotone (a node is only ever ADDED) and
// only ever removes a bound, so it settles with the phase that sets it and cannot
// restart the other. Three is the slack for both settle windows plus the confirming
// sweep, so the fixed point is reached strictly inside the cap for every acyclic
// program. The cap is only ever the MINIMUM of that and len(topo)+1, the historical
// node-count bound, so no graph that converged under the old cap can fail to
// converge here.
//
// ORDER IS PART OF THE RESULT, NOT AN IMPLEMENTATION DETAIL. The DOWN pass reads a
// parent and writes its children, so topological order already carries it the length
// of its path; the UP pass reads children and writes the parent that absorbs them,
// so topological order carries it ONE HOP per sweep and a deep chain needs
// Theta(depth) sweeps. Running the UP pass in reverse topological order would settle
// both directions in one sweep, and that is tempting to read as a pure speedup, but
// it is NOT result-preserving: a contradicted node stops contributing (boundLower
// becomes zero, boundUpper becomes nil), which makes the operator ANTITONE in the
// contradiction set, so the terminal state depends on the visiting order and not
// only on the constraint system. Acceptance vector 06 is the minimal witness: with
// ancestor 30 over middle over leaves 20/20 and a third absent required leaf, this
// order flags MIDDLE (the down pass caps it at 30 and 20+20 > 30 collapses it) while
// the reverse order has already lifted the ancestor's own lower bound to 40 and flags
// the ANCESTOR. Both fail closed; the reported class changes, so the order is pinned.
func propagateQuantityIntervals(program *schemaProgram, state []quantityEvidenceState, exact []*big.Rat, represented []*big.Rat) ([]*big.Rat, []*big.Rat, []bool) {
	count := len(program.keyOf)
	lower := make([]*big.Rat, count)
	upper := make([]*big.Rat, count)
	contradicted := make([]bool, count)
	for node := range count {
		if state[node] == quantityExact {
			lower[node] = new(big.Rat).Set(exact[node])
			upper[node] = new(big.Rat).Set(exact[node])
			continue
		}
		lower[node] = new(big.Rat)
	}
	boundLower := func(node int) *big.Rat {
		if contradicted[node] {
			return new(big.Rat)
		}
		return lower[node]
	}
	boundUpper := func(node int) *big.Rat {
		if contradicted[node] {
			return nil
		}
		return upper[node]
	}
	// The two cover-equation accumulators live for the whole solve. They are reset
	// at the top of every equation and COPIED into lower/upper only when a bound
	// moves, so no stored bound can be mutated by a later sweep.
	sumLower, sumUpper := new(big.Rat), new(big.Rat)
	changed := false
	// A node's body in EITHER pass is a pure function of its own bounds and of its
	// children's effective bounds, so re-running it when none of those has moved can
	// only reproduce the value the node already holds. settled carries one flag per
	// node per pass (the second half is the down pass), which makes the solve's
	// big-rational work proportional to the number of bound MOVES instead of to the
	// node count times the sweep count. Every move clears the node's flag and each
	// containment ancestor's flag in BOTH passes, and only a completed body sets it,
	// so no move is dropped. That closure is exactly each body's read set: the up
	// body reads the node and its children, the down body reads the node and its
	// children too, and a node is a child of nothing but its own parents' bodies, so
	// no reader is ever left settled after a move. Each body writes only to itself
	// (up) or only to its own children (down), so a write ALWAYS clears the writer's
	// own flag: a set flag therefore means the last execution wrote nothing AND left
	// every value it read untouched, so a skipped body would have written nothing
	// either. The pruned trajectory is the unpruned one term for term.
	settled := make([]bool, 2*count)
	markMoved := func(node int) {
		changed = true
		settled[node] = false
		settled[count+node] = false
		for _, ancestor := range program.reverseContainment[node] {
			settled[ancestor] = false
			settled[count+ancestor] = false
		}
	}
	maxSweeps := min(len(program.topo)+1, 2*program.propagationDepth+3)
	for range maxSweeps {
		for _, parent := range program.topo {
			if contradicted[parent] || settled[parent] {
				continue
			}
			if children := program.completeChildren[parent]; len(children) != 0 {
				sumLower.SetInt64(0)
				sumUpper.SetInt64(0)
				allFinite := true
				for index, child := range children {
					if state[child] == quantityAbsent && program.completeOptional[parent][index] {
						// The schema's declared edge-local zero for an absent
						// optional member contributes zero to THIS cover equation
						// and never becomes a global zero. Its zero upper is
						// still finite, so a cover whose every member is either
						// present-with-a-known-upper or absent-optional sums to a
						// determined value and may bound the parent, and through
						// it the parent's contained descendants. An absent
						// REQUIRED member has no finite upper, so the parent's
						// own value stays unknown and it bounds nothing.
						continue
					}
					sumLower.Add(sumLower, boundLower(child))
					childUpper := boundUpper(child)
					if childUpper == nil {
						allFinite = false
						continue
					}
					sumUpper.Add(sumUpper, childUpper)
				}
				if sumLower.Cmp(lower[parent]) > 0 {
					lower[parent] = new(big.Rat).Set(sumLower)
					markMoved(parent)
				}
				if allFinite && (upper[parent] == nil || sumUpper.Cmp(upper[parent]) < 0) {
					upper[parent] = new(big.Rat).Set(sumUpper)
					markMoved(parent)
				}
			}
			for _, child := range program.subsetChildren[parent] {
				childLower := boundLower(child)
				if childLower.Cmp(lower[parent]) > 0 {
					lower[parent] = childLower
					markMoved(parent)
				}
			}
			if upper[parent] != nil && lower[parent].Cmp(upper[parent]) > 0 {
				contradicted[parent] = true
				markMoved(parent)
			}
			settled[parent] = true
		}
		for _, parent := range program.topo {
			if contradicted[parent] || settled[count+parent] {
				continue
			}
			children := program.completeChildren[parent]
			// The two sibling sums are re-read from the state THIS loop has already
			// mutated, so they are carried as one running total per direction and
			// this member's own term is removed from it instead of re-summing every
			// sibling per member. total-minus-own is the same exact rational the
			// re-summation produces, and a member whose term moved rebuilds the
			// total before the next member reads it, so every read is the value the
			// quadratic form read at that index. This is a regrouping, not a reorder:
			// big.Rat is canonical, so the regrouped sum is bit-identical.
			totalLower, totalUpper, infiniteUppers, stale := new(big.Rat), new(big.Rat), 0, true
			for index, child := range children {
				if contradicted[child] || represented[child] == nil {
					// A child with no exact representation has no conserved
					// share to invert: applying the reverse rule to it would
					// synthesize a global value out of absence. An absent
					// OPTIONAL member that IS represented through its own
					// evidence still takes the inferred bound normally, which is
					// what catches a cover whose edge-local zero contradicts the
					// member's separately represented quantity.
					continue
				}
				if stale {
					stale = false
					totalLower.SetInt64(0)
					totalUpper.SetInt64(0)
					infiniteUppers = 0
					for member, node := range children {
						if state[node] == quantityAbsent && program.completeOptional[parent][member] {
							continue
						}
						totalLower.Add(totalLower, boundLower(node))
						if value := boundUpper(node); value != nil {
							totalUpper.Add(totalUpper, value)
						} else {
							infiniteUppers++
						}
					}
				}
				ownLower, ownUpper := lower[child], upper[child]
				counted := state[child] != quantityAbsent || !program.completeOptional[parent][index]
				if upper[parent] != nil {
					sumLower.Set(totalLower)
					if counted {
						sumLower.Sub(sumLower, ownLower)
					}
					candidate := new(big.Rat).Sub(upper[parent], sumLower)
					if candidate.Sign() < 0 {
						candidate.SetInt64(0)
					}
					if upper[child] == nil || candidate.Cmp(upper[child]) < 0 {
						upper[child] = candidate
						markMoved(child)
					}
				}
				remaining := infiniteUppers
				if counted && ownUpper == nil {
					remaining--
				}
				if remaining == 0 {
					sumUpper.Set(totalUpper)
					if counted && ownUpper != nil {
						sumUpper.Sub(sumUpper, ownUpper)
					}
					candidate := new(big.Rat).Sub(lower[parent], sumUpper)
					if candidate.Sign() < 0 {
						candidate.SetInt64(0)
					}
					if candidate.Cmp(lower[child]) > 0 {
						lower[child] = candidate
						markMoved(child)
					}
				}
				if upper[child] != nil && lower[child].Cmp(upper[child]) > 0 {
					contradicted[child] = true
					markMoved(child)
				}
				stale = stale || lower[child] != ownLower || upper[child] != ownUpper || contradicted[child]
			}
			for _, child := range program.subsetChildren[parent] {
				if contradicted[child] || upper[parent] == nil {
					// An upper bound is INFERENCE, not an observation: it
					// propagates into an absent or unavailable child normally.
					// Only a nil (infinite) parent upper carries no information.
					continue
				}
				if upper[child] == nil || upper[parent].Cmp(upper[child]) < 0 {
					upper[child] = new(big.Rat).Set(upper[parent])
					markMoved(child)
				}
				if upper[child] != nil && lower[child].Cmp(upper[child]) > 0 {
					contradicted[child] = true
					markMoved(child)
				}
			}
			settled[count+parent] = true
		}
		if !changed {
			break
		}
	}
	return lower, upper, contradicted
}
