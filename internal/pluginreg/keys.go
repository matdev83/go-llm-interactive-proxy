package pluginreg

import (
	"os"
	"strings"
)

// UpstreamAPIKeys carries default API key material resolved once at the composition root
// (typically from [ResolveUpstreamAPIKeysFromEnv]) when plugin YAML leaves api_key empty.
type UpstreamAPIKeys struct {
	OpenAI    string
	Anthropic string
	Gemini    string
}

// ResolveUpstreamAPIKeysFromEnv reads OPENAI_API_KEY, ANTHROPIC_API_KEY, and GEMINI_API_KEY once.
// Call from the composition root and pass the result to [InstallStandardBundleOn].
func ResolveUpstreamAPIKeysFromEnv() UpstreamAPIKeys {
	return UpstreamAPIKeys{
		OpenAI:    strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		Anthropic: strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")),
		Gemini:    strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
	}
}

func effectiveAPIKey(yamlKey, bootstrap string) string {
	if s := strings.TrimSpace(yamlKey); s != "" {
		return s
	}
	return strings.TrimSpace(bootstrap)
}
