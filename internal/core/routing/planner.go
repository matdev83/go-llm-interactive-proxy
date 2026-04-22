package routing

import (
	"errors"
	"fmt"
)

// ErrNoEligibleCandidate means every candidate in the relevant scope was excluded or unhealthy.
var ErrNoEligibleCandidate = errors.New("routing: no eligible route candidate")

// SessionRoutingState holds session-scoped routing flags (mirrors B2BUA A-leg state; persisted by caller in v1).
type SessionRoutingState struct {
	FirstRequestConsumed bool
}

// PlanOptions configures planning: exclusions, health, session first-request consumption, RNG, retry path.
type PlanOptions struct {
	Excluded  map[string]struct{}
	Unhealthy map[string]struct{}
	Session   *SessionRoutingState
	// Rand supplies weighted-branch rolls. When nil, weighted selection uses a fixed-seeded
	// math/rand source (deterministic, not crypto-safe); inject for tests or concurrency-safe rolls.
	Rand        Rng
	IsRetryPath bool
}

// Rng abstracts math/rand for testing.
type Rng interface {
	Intn(n int) int
}

// AttemptCandidate is one concrete backend:model attempt after parsing and resolution.
type AttemptCandidate struct {
	Primary Primary
	// Key matches Primary.String() for exclusion maps.
	Key string
	// MarkedFirst is true when this candidate came from a [first]-annotated weighted branch.
	MarkedFirst bool
}

// ExpandFailover resolves a parsed selector into an ordered list of candidates for the first pass:
// one entry per failover arm (left-to-right), skipping primary arms that are excluded/unhealthy,
// and resolving each weighted arm with pickWeighted. Arms with no eligible candidates are skipped.
// When a [first] branch is chosen, Session.FirstRequestConsumed is set (if Session is non-nil).
func ExpandFailover(sel *Selector, opt PlanOptions) ([]AttemptCandidate, error) {
	if sel == nil {
		return nil, fmt.Errorf("%w: nil selector", ErrInvalidSelector)
	}
	out := make([]AttemptCandidate, 0, len(sel.Alternatives))
	for _, alt := range sel.Alternatives {
		switch {
		case alt.Primary != nil:
			c := AttemptCandidate{Primary: *alt.Primary, Key: alt.Primary.String()}
			if excluded(c.Key, opt.Excluded, opt.Unhealthy) {
				continue
			}
			out = append(out, c)
		case alt.Weighted != nil:
			c, consumeFirst, err := pickWeighted(alt.Weighted, opt)
			if errors.Is(err, ErrNoEligibleCandidate) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if consumeFirst && opt.Session != nil {
				opt.Session.FirstRequestConsumed = true
			}
			out = append(out, c)
		default:
			return nil, fmt.Errorf("%w: invalid failover alternative", ErrInvalidSelector)
		}
	}
	if len(out) == 0 {
		return nil, ErrNoEligibleCandidate
	}
	return out, nil
}

func excluded(key string, ex, uh map[string]struct{}) bool {
	if ex != nil {
		if _, ok := ex[key]; ok {
			return true
		}
	}
	if uh != nil {
		if _, ok := uh[key]; ok {
			return true
		}
	}
	return false
}

// ReplanWeighted picks again within the same weighted group after excluding failed candidates.
// Req 7.4: retry/failover path ignores [first] and only considers remaining eligible weighted candidates.
func ReplanWeighted(w *Weighted, opt PlanOptions) (AttemptCandidate, error) {
	opt2 := opt
	opt2.IsRetryPath = true
	c, _, err := pickWeighted(w, opt2)
	return c, err
}
