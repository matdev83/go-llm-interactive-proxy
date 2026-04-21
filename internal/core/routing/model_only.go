package routing

import (
	"fmt"
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
			if strings.TrimSpace(a.Weighted.Branches[j].Target.Backend) == "" {
				a.Weighted.Branches[j].Target.Backend = backend
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
			for _, b := range alt.Weighted.Branches {
				if strings.TrimSpace(b.Target.Backend) == "" {
					return true
				}
			}
		}
	}
	return false
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
			b := strings.TrimSpace(alt.Weighted.Branches[0].Target.Backend)
			if b == "" {
				return "", fmt.Errorf("routing default_route weighted branch must name a backend")
			}
			return b, nil
		}
	}
	return "", fmt.Errorf("routing default_route has no primary")
}
