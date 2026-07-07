package pluginreg

import (
	"strings"
)

// EffectiveAPIKeys merges YAML api_key (first), then api_keys in order: trims, drops empties,
// de-duplicates by secret string while preserving first-seen order. When the YAML side yields
// no credentials, defaults (typically from the environment) are used with the same normalization.
// The returned strings are secrets; callers must not log them.
func EffectiveAPIKeys(yamlKey string, yamlKeys []string, defaults []string) []string {
	n := 1 + len(yamlKeys) + len(defaults)
	seen := make(map[string]struct{}, n)
	out := make([]string, 0, n)

	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	if strings.TrimSpace(yamlKey) != "" {
		add(yamlKey)
	}
	for _, k := range yamlKeys {
		add(k)
	}
	if len(out) > 0 {
		return out
	}

	for _, k := range defaults {
		add(k)
	}
	return out
}
