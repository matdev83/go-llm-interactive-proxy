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
	"stub":             {},
}

// InlineOrDefault returns model when it has a known route prefix, otherwise defaultRoute.
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

// FromModelOrDefault parses model from JSON body and returns it if it has an inline route prefix, otherwise defaultRoute.
func FromModelOrDefault(body []byte, defaultRoute string) string {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return InlineOrDefault(req.Model, defaultRoute)
	}
	return strings.TrimSpace(defaultRoute)
}
