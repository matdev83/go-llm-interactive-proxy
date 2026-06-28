package routing

import (
	"fmt"
	"slices"
	"strings"
)

// ApplyModelOnlyBackends sets Primary.Backend to defaultBackend wherever it is empty.
// defaultBackend must be non-empty; otherwise this is a no-op.
func ApplyModelOnlyBackends(sel *Selector, defaultBackend string) {
	if sel == nil {
		return
	}
	defaultBackend = strings.TrimSpace(defaultBackend)
	if defaultBackend == "" {
		return
	}
	for i := range sel.Alternatives {
		applyModelOnlyToAlt(&sel.Alternatives[i], defaultBackend)
	}
}

func applyModelOnlyToAlt(a *FailoverAlt, backend string) {
	switch {
	case a.Primary != nil && strings.TrimSpace(a.Primary.Backend) == "":
		a.Primary.Backend = backend
	case a.Weighted != nil:
		for j := range a.Weighted.Branches {
			applyModelOnlyToWeightedBranch(&a.Weighted.Branches[j], backend)
		}
	case a.Parallel != nil:
		for j := range a.Parallel.Branches {
			if strings.TrimSpace(a.Parallel.Branches[j].Target.Backend) == "" {
				a.Parallel.Branches[j].Target.Backend = backend
			}
		}
	}
}

// SelectorHasEmptyBackend reports whether any primary still has an empty backend id.
func SelectorHasEmptyBackend(sel *Selector) bool {
	if sel == nil {
		return false
	}
	for _, alt := range sel.Alternatives {
		if alt.Primary != nil && strings.TrimSpace(alt.Primary.Backend) == "" {
			return true
		}
		if alt.Weighted != nil {
			if slices.ContainsFunc(alt.Weighted.Branches, weightedBranchHasEmptyBackend) {
				return true
			}
		}
		if alt.Parallel != nil {
			for _, b := range alt.Parallel.Branches {
				if strings.TrimSpace(b.Target.Backend) == "" {
					return true
				}
			}
		}
	}
	return false
}

func applyModelOnlyToWeightedBranch(b *WeightedBranch, backend string) {
	if b == nil {
		return
	}
	if b.Parallel != nil {
		for j := range b.Parallel.Branches {
			if strings.TrimSpace(b.Parallel.Branches[j].Target.Backend) == "" {
				b.Parallel.Branches[j].Target.Backend = backend
			}
		}
		return
	}
	if strings.TrimSpace(b.Target.Backend) == "" {
		b.Target.Backend = backend
	}
}

func defaultBackendFromWeightedBranch(b WeightedBranch) (string, error) {
	if b.Parallel != nil {
		if len(b.Parallel.Branches) == 0 {
			return "", fmt.Errorf("routing default_route parallel branch must name a backend")
		}
		backend := strings.TrimSpace(b.Parallel.Branches[0].Target.Backend)
		if backend == "" {
			return "", fmt.Errorf("routing default_route parallel branch must name a backend")
		}
		return backend, nil
	}
	backend := strings.TrimSpace(b.Target.Backend)
	if backend == "" {
		return "", fmt.Errorf("routing default_route weighted branch must name a backend")
	}
	return backend, nil
}

func weightedBranchHasEmptyBackend(b WeightedBranch) bool {
	if b.Parallel != nil {
		for _, leg := range b.Parallel.Branches {
			if strings.TrimSpace(leg.Target.Backend) == "" {
				return true
			}
		}
		return false
	}
	return strings.TrimSpace(b.Target.Backend) == ""
}

// DefaultBackendFromRouteSelector extracts the backend id from a configured default route
// (for example "openai-responses:gpt-4o-mini"). The route must contain at least one primary
// with a non-empty backend (not model-only).
func DefaultBackendFromRouteSelector(defaultRoute string) (string, error) {
	defaultRoute = strings.TrimSpace(defaultRoute)
	if defaultRoute == "" {
		return "", nil
	}
	sel, err := Parse(defaultRoute)
	if err != nil {
		return "", fmt.Errorf("routing default_route: %w", err)
	}
	for _, alt := range sel.Alternatives {
		if alt.Primary != nil {
			b := strings.TrimSpace(alt.Primary.Backend)
			if b == "" {
				return "", fmt.Errorf("routing default_route must not be model-only (missing backend)")
			}
			return b, nil
		}
		if alt.Weighted != nil && len(alt.Weighted.Branches) > 0 {
			b, err := defaultBackendFromWeightedBranch(alt.Weighted.Branches[0])
			if err != nil {
				return "", err
			}
			return b, nil
		}
		if alt.Parallel != nil && len(alt.Parallel.Branches) > 0 {
			b := strings.TrimSpace(alt.Parallel.Branches[0].Target.Backend)
			if b == "" {
				return "", fmt.Errorf("routing default_route parallel branch must name a backend")
			}
			return b, nil
		}
	}
	return "", fmt.Errorf("routing default_route has no primary")
}
