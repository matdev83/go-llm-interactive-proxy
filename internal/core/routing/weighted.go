package routing

import (
	"fmt"
	"math/rand"
)

// pickWeighted selects one branch from w using opt (exclusions, session, RNG, retry path).
// The second return is true when the [first] branch was taken and the session should record consumption.
func pickWeighted(w *Weighted, opt PlanOptions) (AttemptCandidate, bool, error) {
	if w == nil || len(w.Branches) == 0 {
		return AttemptCandidate{}, false, ErrNoEligibleCandidate
	}
	eligible := make([]WeightedBranch, 0, len(w.Branches))
	for _, b := range w.Branches {
		key := b.Target.String()
		if excluded(key, opt.Excluded, opt.Unhealthy) {
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
			if eligible[i].First {
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
		rng = rand.New(rand.NewSource(0))
	}
	total := 0
	for _, b := range eligible {
		wt := b.Weight
		if wt <= 0 {
			wt = 1
		}
		total += wt
	}
	if total <= 0 {
		return AttemptCandidate{}, false, ErrNoEligibleCandidate
	}
	roll := rng.Intn(total)
	sum := 0
	var chosen WeightedBranch
	for _, b := range eligible {
		wt := b.Weight
		if wt <= 0 {
			wt = 1
		}
		sum += wt
		if roll < sum {
			chosen = b
			break
		}
	}
	return AttemptCandidate{
		Primary:     chosen.Target,
		Key:         chosen.Target.String(),
		MarkedFirst: chosen.First && !opt.IsRetryPath,
	}, false, nil
}
