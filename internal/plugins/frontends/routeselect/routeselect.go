// Package routeselect derives explicit route selectors from model identifiers.
// Route selectors use "backend-prefix:model" to route requests to a backend family.
package routeselect

import (
	"encoding/json"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

type PrefixSet map[string]struct{}

func NewPrefixSet(prefixes []string) PrefixSet {
	filtered := routing.FilterRoutePrefixes(prefixes)
	out := make(PrefixSet, len(filtered))
	for _, prefix := range filtered {
		out[prefix] = struct{}{}
	}
	return out
}

// InlineOrDefault returns model when it has a known backend prefix before the colon delimiter.
// Otherwise it returns defaultRoute with surrounding whitespace removed.
func (p PrefixSet) InlineOrDefault(model, defaultRoute string) string {
	model = strings.TrimSpace(model)
	prefix, _, ok := strings.Cut(model, ":")
	if ok {
		if _, known := p[strings.TrimSpace(prefix)]; known {
			return model
		}
	}
	return strings.TrimSpace(defaultRoute)
}

// FromModelOrDefault parses body for a model field and returns it when it carries a known inline route prefix.
// If decoding fails or the model has no known prefix, it returns defaultRoute with surrounding whitespace removed.
func (p PrefixSet) FromModelOrDefault(body []byte, defaultRoute string) string {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return p.InlineOrDefault(req.Model, defaultRoute)
	}
	return strings.TrimSpace(defaultRoute)
}
