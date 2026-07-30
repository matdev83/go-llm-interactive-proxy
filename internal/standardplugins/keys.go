package standardplugins

import (
	"os"
	"strconv"
	"strings"
)

// maxNumberedAPIKeysEnv is the highest OPENAI_API_KEY_N (and Anthropic/Gemini equivalents) read from the environment.
const maxNumberedAPIKeysEnv = 32

// UpstreamAPIKeys carries default API key material resolved once at the composition root
// (typically from [ResolveUpstreamAPIKeysFromEnv]) when plugin YAML leaves api_key empty.
// Treat all string values as secrets: do not log them or include them in error text.
type UpstreamAPIKeys struct {
	OpenAI           []string
	Anthropic        []string
	AlibabaTokenPlan []string
	Gemini           []string
}

// ResolveUpstreamAPIKeysFromEnv reads OPENAI_API_KEY, ANTHROPIC_API_KEY,
// ALIBABA_TOKEN_PLAN_API_KEY, and GEMINI_API_KEY plus numbered suffixes until
// the first missing or empty value. The bare env var fills the first slot;
// numbered suffixes start at _2. ALIBABA_TOKEN_PLAN_API_KEY additionally falls
// back to the persistent Windows environment, because a long-lived launcher can
// hand a stale process snapshot that omits a freshly-configured User env var.
// Migrated external connectors (OpenRouter, NVIDIA, Hugging Face, Ollama, local
// runtimes, OpenCode, Codex) receive credentials only via plugin config YAML /
// secrets. Call from the composition root and pass the result to
// [InstallStandardBundleOn].
func ResolveUpstreamAPIKeysFromEnv() UpstreamAPIKeys {
	return UpstreamAPIKeys{
		OpenAI:           collectNumberedEnvKeys("OPENAI_API_KEY"),
		Anthropic:        collectNumberedEnvKeys("ANTHROPIC_API_KEY"),
		AlibabaTokenPlan: collectNumberedEnvKeysWithPersistentFallback("ALIBABA_TOKEN_PLAN_API_KEY"),
		Gemini:           collectNumberedEnvKeys("GEMINI_API_KEY"),
	}
}

// collectNumberedEnvKeys returns the bare env var plus numbered suffixes
// (PREFIX_2..PREFIX_N) until the first missing or empty value.
func collectNumberedEnvKeys(prefix string) []string {
	out := make([]string, 0, maxNumberedAPIKeysEnv)
	if s := strings.TrimSpace(os.Getenv(prefix)); s != "" {
		out = append(out, s)
	}
	for i := 2; i <= maxNumberedAPIKeysEnv; i++ {
		// ⚡ Bolt: replace fmt.Sprintf with direct string concatenation and strconv for performance
		name := prefix + "_" + strconv.Itoa(i)
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

// collectNumberedEnvKeysWithPersistentFallback is [collectNumberedEnvKeys]
// with a persistent-environment fallback applied to the first (bare) slot. It is
// used for credentials that a Windows user may configure after the launching
// process captured its environment snapshot.
func collectNumberedEnvKeysWithPersistentFallback(prefix string) []string {
	out := collectNumberedEnvKeys(prefix)
	persistent := envValueWithPersistentFallback(prefix)
	if persistent == "" {
		return out
	}
	if len(out) == 0 {
		return []string{persistent}
	}
	out[0] = persistent
	return out
}

// envValueWithPersistentFallback returns the process environment value for name,
// preferring the persistent OS environment (Windows registry) when it holds a
// different, non-empty value. On non-Windows platforms it returns the process
// value unchanged.
func envValueWithPersistentFallback(name string) string {
	processValue := strings.TrimSpace(os.Getenv(name))
	persistentValue := strings.TrimSpace(persistentEnvValue(name))
	if persistentValue != "" && persistentValue != processValue {
		return persistentValue
	}
	return processValue
}
