package standardplugins

import (
	"fmt"
	"os"
	"strings"
)

// maxNumberedAPIKeysEnv is the highest OPENAI_API_KEY_N (and Anthropic/Gemini equivalents) read from the environment.
const maxNumberedAPIKeysEnv = 32

// UpstreamAPIKeys carries default API key material resolved once at the composition root
// (typically from [ResolveUpstreamAPIKeysFromEnv]) when plugin YAML leaves api_key empty.
// Treat all string values as secrets: do not log them or include them in error text.
type UpstreamAPIKeys struct {
	OpenAI    []string
	Anthropic []string
	Gemini    []string
	// Cursor is an optional composition-root default for experimental cursorsdk
	// helpers (CURSOR_API_KEY). It is not part of the essential backend table;
	// optional connectors still receive credentials via plugin YAML / secrets.
	Cursor string
}

// ResolveUpstreamAPIKeysFromEnv reads OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY
// plus numbered suffixes until the first missing or empty value. The bare env var fills
// the first slot; OpenAI/Anthropic/Gemini read suffixes starting at _2.
// CURSOR_API_KEY is a single non-numbered optional default for experimental cursorsdk
// helpers retained outside EssentialBackendBundle.
// Migrated external connectors (OpenRouter, NVIDIA, Hugging Face, Ollama, local runtimes,
// OpenCode, Codex) receive credentials only via plugin config YAML / secrets.
// Call from the composition root and pass the result to [InstallStandardBundleOn].
func ResolveUpstreamAPIKeysFromEnv() UpstreamAPIKeys {
	return UpstreamAPIKeys{
		OpenAI:    collectNumberedEnvKeys("OPENAI_API_KEY"),
		Anthropic: collectNumberedEnvKeys("ANTHROPIC_API_KEY"),
		Gemini:    collectNumberedEnvKeys("GEMINI_API_KEY"),
		Cursor:    strings.TrimSpace(os.Getenv("CURSOR_API_KEY")),
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
