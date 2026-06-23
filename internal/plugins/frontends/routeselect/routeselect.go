// Package routeselect derives explicit route selectors from model identifiers.
// Route selectors use "backend-prefix:model" to route requests to a backend family.
package routeselect

import (
	"encoding/json"
	"strings"
)

var inlineRoutePrefixes = map[string]struct{}{
	"acp":              {},
	"anthropic":        {},
	"bedrock":          {},
	"gemini":           {},
	"local-stub":       {},
	"nvidia":           {},
	"ollama":           {},
	"ollama-cloud":     {},
	"openai-legacy":    {},
	"openai-responses": {},
	"openrouter":       {},
	// Test frontends use stub route selectors with executor stubs outside the production backend registry.
	"stub": {},
}

// InlineOrDefault returns model when it has a known backend prefix before the colon delimiter.
// Otherwise it returns defaultRoute with surrounding whitespace removed.
func InlineOrDefault(model, defaultRoute string) string {
	model = strings.TrimSpace(model)
	prefix, _, ok := strings.Cut(model, ":")
	if ok {
		if _, known := inlineRoutePrefixes[strings.TrimSpace(prefix)]; known {
			return model
		}
	}
	return strings.TrimSpace(defaultRoute)
}

// FromModelOrDefault parses body for a model field and returns it when it carries a known inline route prefix.
// If decoding fails or the model has no known prefix, it returns defaultRoute with surrounding whitespace removed.
func FromModelOrDefault(body []byte, defaultRoute string) string {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return InlineOrDefault(req.Model, defaultRoute)
	}
	return strings.TrimSpace(defaultRoute)
}
