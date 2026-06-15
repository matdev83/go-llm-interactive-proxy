package routing

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNoEligibleCandidate means every candidate in the relevant scope was excluded or unhealthy.
var ErrNoEligibleCandidate = errors.New("routing: no eligible route candidate")

// ErrWeightedTotalTooLarge means the sum of effective branch weights exceeds [math.MaxInt]
// (cannot pass safely to the RNG) or overflowed int64 during accumulation.
var ErrWeightedTotalTooLarge = errors.New("routing: weighted branch total too large")

// SessionRoutingState holds session-scoped routing flags (mirrors B2BUA A-leg state; persisted by caller in v1).
type SessionRoutingState struct {
	FirstRequestConsumed bool
}

// PlanOptions configures planning: exclusions, health, session first-request consumption, RNG, retry path.
type PlanOptions struct {
	Excluded  map[string]struct{}
	Unhealthy map[string]struct{}
	// RequestSize is the current request size estimate used for per-leaf min/max context filters.
	// When unavailable, size filters fail open and do not exclude candidates.
	RequestSize RequestSizeEstimate
	Session     *SessionRoutingState
	// PreferredCandidateKeys hints expand order when keys are already eligible (design §12, advisory).
	PreferredCandidateKeys []string
	// StickyBackendID forces a currently bound backend instance ahead of normal weighted/failover selection
	// when at least one candidate for that backend is eligible in the current selector.
	StickyBackendID string
	// Rand supplies weighted-branch rolls. When nil, weighted selection uses a fixed-seeded
	// math/rand/v2 PCG stream (deterministic, not crypto-safe); inject for tests or concurrency-safe rolls.
	Rand        Rng
	IsRetryPath bool
}

// RequestSizeEstimate is a provider-neutral request token estimate for route planning.
type RequestSizeEstimate struct {
	Available bool
	Tokens    int64
	Basis     string
}

// Rng abstracts a uniform integer RNG for weighted picks (see [NewSeededRng], [WrapRandV2]).
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
	// IsParallel is true when this candidate belongs to a parallel group (selector '!' arm).
	IsParallel bool
	// Handicap is the per-leg head-start delay for parallel routing (0 = no handicap).
	Handicap time.Duration
}

// AttemptGroup is one eligible failover arm after planning.
// Single-candidate arms (primary/weighted) contain exactly one candidate; parallel arms contain one or more legs.
type AttemptGroup struct {
	Candidates []AttemptCandidate
}

// ExpandFailoverGroups resolves a parsed selector into ordered candidate groups (one group per eligible arm).
// Primary/weighted arms produce one candidate group of length 1; parallel arms produce one group with all
// eligible parallel legs.
func ExpandFailoverGroups(sel *Selector, opt PlanOptions) ([]AttemptGroup, error) {
	if sel == nil {
		return nil, fmt.Errorf("%w: nil selector", ErrInvalidSelector)
	}
	if c, ok := stickyCandidate(sel, opt); ok {
		return []AttemptGroup{{Candidates: []AttemptCandidate{c}}}, nil
	}
	groups := make([]AttemptGroup, 0, len(sel.Alternatives))
	for _, alt := range sel.Alternatives {
		switch {
		case alt.Primary != nil:
			c := AttemptCandidate{Primary: *alt.Primary, Key: alt.Primary.String()}
			if !candidateEligible(c.Primary, c.Key, opt) {
				continue
			}
			groups = append(groups, AttemptGroup{Candidates: []AttemptCandidate{c}})
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
			groups = append(groups, AttemptGroup{Candidates: []AttemptCandidate{c}})
		case alt.Parallel != nil:
			legs := expandParallel(alt.Parallel, opt)
			if len(legs) == 0 {
				continue
			}
			legs = reorderPreferredCandidates(legs, opt.PreferredCandidateKeys)
			groups = append(groups, AttemptGroup{Candidates: legs})
		default:
			return nil, fmt.Errorf("%w: invalid failover alternative", ErrInvalidSelector)
		}
	}
	if len(groups) == 0 {
		return nil, ErrNoEligibleCandidate
	}
	return reorderPreferredGroups(groups, opt.PreferredCandidateKeys), nil
}

// ExpandFailover resolves a parsed selector into an ordered list of candidates for the first pass:
// one entry per failover arm (left-to-right), skipping primary arms that are excluded/unhealthy,
// and resolving each weighted arm with pickWeighted. Arms with no eligible candidates are skipped.
// When a [first] branch is chosen, Session.FirstRequestConsumed is set (if Session is non-nil).
func ExpandFailover(sel *Selector, opt PlanOptions) ([]AttemptCandidate, error) {
	groups, err := ExpandFailoverGroups(sel, opt)
	if err != nil {
		return nil, err
	}
	out := make([]AttemptCandidate, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Candidates...)
	}
	return out, nil
}

func stickyCandidate(sel *Selector, opt PlanOptions) (AttemptCandidate, bool) {
	backend := strings.TrimSpace(opt.StickyBackendID)
	if sel == nil || backend == "" {
		return AttemptCandidate{}, false
	}
	for _, alt := range sel.Alternatives {
		switch {
		case alt.Primary != nil:
			if strings.TrimSpace(alt.Primary.Backend) != backend {
				continue
			}
			c := AttemptCandidate{Primary: *alt.Primary, Key: alt.Primary.String()}
			if candidateEligible(c.Primary, c.Key, opt) {
				return c, true
			}
		case alt.Weighted != nil:
			for _, b := range alt.Weighted.Branches {
				if strings.TrimSpace(b.Target.Backend) != backend {
					continue
				}
				c := AttemptCandidate{Primary: b.Target, Key: b.Target.String()}
				if candidateEligible(c.Primary, c.Key, opt) {
					c.MarkedFirst = b.IsFirst && !opt.IsRetryPath && opt.Session != nil && !opt.Session.FirstRequestConsumed
					return c, true
				}
			}
		case alt.Parallel != nil:
			for _, b := range alt.Parallel.Branches {
				if strings.TrimSpace(b.Target.Backend) != backend {
					continue
				}
				c := AttemptCandidate{Primary: b.Target, Key: b.Target.String(), IsParallel: true, Handicap: b.Handicap}
				if candidateEligible(c.Primary, c.Key, opt) {
					return c, true
				}
			}
		}
	}
	return AttemptCandidate{}, false
}

func expandParallel(p *Parallel, opt PlanOptions) []AttemptCandidate {
	if p == nil {
		return nil
	}
	out := make([]AttemptCandidate, 0, len(p.Branches))
	for _, b := range p.Branches {
		c := AttemptCandidate{
			Primary:    b.Target,
			Key:        b.Target.String(),
			IsParallel: true,
			Handicap:   b.Handicap,
		}
		if !candidateEligible(c.Primary, c.Key, opt) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func reorderPreferredCandidates(list []AttemptCandidate, preferred []string) []AttemptCandidate {
	if len(list) <= 1 || len(preferred) == 0 {
		return list
	}
	seen := make(map[string]struct{}, len(list))
	out := make([]AttemptCandidate, 0, len(list))
	for _, k := range preferred {
		if k == "" {
			continue
		}
		for _, c := range list {
			if c.Key == k {
				if _, ok := seen[c.Key]; !ok {
					out = append(out, c)
					seen[c.Key] = struct{}{}
				}
				break
			}
		}
	}
	for _, c := range list {
		if _, ok := seen[c.Key]; !ok {
			out = append(out, c)
		}
	}
	return out
}

func reorderPreferredGroups(groups []AttemptGroup, preferred []string) []AttemptGroup {
	if len(groups) <= 1 || len(preferred) == 0 {
		return groups
	}
	type groupKey struct {
		group int
	}
	indexByCandidate := map[string]groupKey{}
	for gi, g := range groups {
		for _, c := range g.Candidates {
			if c.Key == "" {
				continue
			}
			if _, exists := indexByCandidate[c.Key]; !exists {
				indexByCandidate[c.Key] = groupKey{group: gi}
			}
		}
	}
	seenGroup := map[int]struct{}{}
	out := make([]AttemptGroup, 0, len(groups))
	for _, key := range preferred {
		meta, ok := indexByCandidate[key]
		if !ok {
			continue
		}
		if _, exists := seenGroup[meta.group]; exists {
			continue
		}
		out = append(out, groups[meta.group])
		seenGroup[meta.group] = struct{}{}
	}
	for gi, g := range groups {
		if _, exists := seenGroup[gi]; exists {
			continue
		}
		out = append(out, g)
	}
	return out
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

func candidateEligible(p Primary, key string, opt PlanOptions) bool {
	if excluded(key, opt.Excluded, opt.Unhealthy) {
		return false
	}
	return requestSizeEligible(p.Size, opt.RequestSize)
}

func requestSizeEligible(c RequestSizeConstraint, est RequestSizeEstimate) bool {
	if !est.Available {
		return true
	}
	if c.MaxContextTokens != nil && est.Tokens > *c.MaxContextTokens {
		return false
	}
	if c.MinContextTokens != nil && est.Tokens <= *c.MinContextTokens {
		return false
	}
	return true
}

// SelectorHasRequestSizeConstraints reports whether any leaf has min/max context filters.
func SelectorHasRequestSizeConstraints(sel *Selector) bool {
	if sel == nil {
		return false
	}
	for _, alt := range sel.Alternatives {
		if alt.Primary != nil && hasRequestSizeConstraint(alt.Primary.Size) {
			return true
		}
		if alt.Weighted != nil {
			for _, b := range alt.Weighted.Branches {
				if hasRequestSizeConstraint(b.Target.Size) {
					return true
				}
			}
		}
		if alt.Parallel != nil {
			for _, b := range alt.Parallel.Branches {
				if hasRequestSizeConstraint(b.Target.Size) {
					return true
				}
			}
		}
	}
	return false
}

func hasRequestSizeConstraint(c RequestSizeConstraint) bool {
	return c.MinContextTokens != nil || c.MaxContextTokens != nil
}

// ReplanWeighted picks again within the same weighted group after excluding failed candidates.
// Req 7.4: retry/failover path ignores [first] and only considers remaining eligible weighted candidates.
func ReplanWeighted(w *Weighted, opt PlanOptions) (AttemptCandidate, error) {
	opt2 := opt
	opt2.IsRetryPath = true
	c, _, err := pickWeighted(w, opt2)
	return c, err
}
