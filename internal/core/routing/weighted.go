package routing

import (
	"fmt"
	"math"
)

func effectiveWeight(w int) int64 {
	if w <= 0 {
		return 1
	}
	return int64(w)
}

// sumEligibleWeights returns the sum of effective weights or an error if the sum overflows int64
// or exceeds [math.MaxInt] (so it cannot be passed to [Rng.Intn]).
func sumEligibleWeights(eligible []WeightedBranch) (int64, error) {
	var total int64
	for _, b := range eligible {
		wt := effectiveWeight(b.Weight)
		if wt > 0 && total > math.MaxInt64-wt {
			return 0, ErrWeightedTotalTooLarge
		}
		total += wt
	}
	if total < 1 {
		return 0, ErrNoEligibleCandidate
	}
	if total > int64(math.MaxInt) {
		return 0, ErrWeightedTotalTooLarge
	}
	return total, nil
}

// pickWeighted selects one branch from w using opt (exclusions, session, RNG, retry path).
// The second return is true when the [first] branch was taken and the session should record consumption.
func pickWeighted(w *Weighted, opt PlanOptions) (AttemptCandidate, bool, error) {
	if w == nil || len(w.Branches) == 0 {
		return AttemptCandidate{}, false, ErrNoEligibleCandidate
	}
	eligible := make([]WeightedBranch, 0, len(w.Branches))
	for _, b := range w.Branches {
		key := b.Target.String()
		if !candidateEligible(b.Target, key, opt) {
			continue
		}
		eligible = append(eligible, b)
	}
	if len(eligible) == 0 {
		return AttemptCandidate{}, false, ErrNoEligibleCandidate
	}

	// Req 7.1 / 7.2 / 7.4: first-request steering only on initial path, not retry/failover path.
	if !opt.IsRetryPath && opt.Session != nil && !opt.Session.FirstRequestConsumed {
		var first *WeightedBranch
		for i := range eligible {
			if eligible[i].IsFirst {
				if first != nil {
					return AttemptCandidate{}, false, fmt.Errorf("internal: multiple [first] in eligible set")
				}
				f := eligible[i]
				first = &f
			}
		}
		if first != nil {
			return AttemptCandidate{
				Primary:     first.Target,
				Key:         first.Target.String(),
				MarkedFirst: true,
			}, true, nil
		}
	}

	rng := opt.Rand
	if rng == nil {
		rng = NewSeededRng(0)
	}
	total, err := sumEligibleWeights(eligible)
	if err != nil {
		return AttemptCandidate{}, false, err
	}
	n := int(total)
	roll := rng.Intn(n)
	var sum int64
	var chosen WeightedBranch
	for _, b := range eligible {
		sum += effectiveWeight(b.Weight)
		if int64(roll) < sum {
			chosen = b
			break
		}
	}
	return AttemptCandidate{
		Primary:     chosen.Target,
		Key:         chosen.Target.String(),
		MarkedFirst: chosen.IsFirst && !opt.IsRetryPath,
	}, false, nil
}
