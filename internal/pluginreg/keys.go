package pluginreg

import (
	"fmt"
	"os"
	"strings"
)

// maxNumberedAPIKeysEnv is the highest OPENAI_API_KEY_N (and Anthropic/Gemini equivalents) read from the environment.
const maxNumberedAPIKeysEnv = 32

// UpstreamAPIKeys carries default API key material resolved once at the composition root
// (typically from [ResolveUpstreamAPIKeysFromEnv]) when plugin YAML leaves api_key empty.
type UpstreamAPIKeys struct {
	OpenAI    []string
	Anthropic []string
	Gemini    []string
}

// EffectiveAPIKeys merges YAML api_key (first), then api_keys in order: trims, drops empties,
// de-duplicates by secret string while preserving first-seen order. When the YAML side yields
// no credentials, defaults (typically from the environment) are used with the same normalization.
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

// ResolveUpstreamAPIKeysFromEnv reads OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY plus
// numbered suffixes (_2, _3, …) until the first missing or empty value (contiguous from 2).
// Call from the composition root and pass the result to [InstallStandardBundleOn].
func ResolveUpstreamAPIKeysFromEnv() UpstreamAPIKeys {
	return UpstreamAPIKeys{
		OpenAI:    collectNumberedEnvKeys("OPENAI_API_KEY"),
		Anthropic: collectNumberedEnvKeys("ANTHROPIC_API_KEY"),
		Gemini:    collectNumberedEnvKeys("GEMINI_API_KEY"),
	}
}

func collectNumberedEnvKeys(prefix string) []string {
	out := make([]string, 0, maxNumberedAPIKeysEnv)
	if s := strings.TrimSpace(os.Getenv(prefix)); s != "" {
		out = append(out, s)
	}
	for i := 2; i <= maxNumberedAPIKeysEnv; i++ {
		name := fmt.Sprintf("%s_%d", prefix, i)
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}
