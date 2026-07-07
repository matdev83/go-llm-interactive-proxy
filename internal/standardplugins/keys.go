package standardplugins

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// maxNumberedAPIKeysEnv is the highest OPENAI_API_KEY_N (and Anthropic/Gemini equivalents) read from the environment.
const maxNumberedAPIKeysEnv = 32

// UpstreamAPIKeys carries default API key material resolved once at the composition root
// (typically from [ResolveUpstreamAPIKeysFromEnv]) when plugin YAML leaves api_key empty.
// Treat all string values as secrets: do not log them or include them in error text.
type UpstreamAPIKeys struct {
	OpenAI      []string
	Anthropic   []string
	Gemini      []string
	OpenRouter  []string
	Nvidia      []string
	HuggingFace []string
	OpenCodeGo  []string
	OpenCodeZen []string
	OpenAICodex []string
}

// ResolveUpstreamAPIKeysFromEnv reads OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY,
// OPENROUTER_API_KEY, NVIDIA_API_KEY, HUGGINGFACE_API_KEY plus numbered suffixes until the first
// missing or empty value. The bare env var fills the first slot. OpenAI, Anthropic, and Gemini read
// suffixes starting at _2 (an explicit _1 is not read for them); OpenRouter, NVIDIA, and Hugging Face
// use _1-indexed numbering for consistency with OPENROUTER_API_KEY_1, NVIDIA_API_KEY_1,
// HUGGINGFACE_API_KEY_1, etc.
// Call from the composition root and pass the result to [InstallStandardBundleOn].
func ResolveUpstreamAPIKeysFromEnv() UpstreamAPIKeys {
	return UpstreamAPIKeys{
		OpenAI:      collectNumberedEnvKeys("OPENAI_API_KEY"),
		Anthropic:   collectNumberedEnvKeys("ANTHROPIC_API_KEY"),
		Gemini:      collectNumberedEnvKeys("GEMINI_API_KEY"),
		OpenRouter:  collectOpenRouterEnvKeys(),
		Nvidia:      collectNvidiaEnvKeys(),
		HuggingFace: collectHuggingFaceEnvKeys(),
		OpenCodeGo:  collectNumberedEnvKeys("OPENCODE_GO_API_KEY"),
		OpenCodeZen: collectOpenCodeZenEnvKeys(),
		OpenAICodex: collectOpenAICodexEnvKeys(),
	}
}

func collectOpenAICodexEnvKeys() []string {
	out := collectNumberedEnvKeys("OPENAI_CODEX_ACCESS_TOKEN")
	if len(out) > 0 {
		return out
	}
	return collectNumberedEnvKeys("OPENAI_CODEX_API_KEY")
}

func collectOpenCodeZenEnvKeys() []string {
	out := collectNumberedEnvKeys("OPENCODE_API_KEY")
	if len(out) > 0 {
		return out
	}
	return collectNumberedEnvKeys("OPENCODE_ZEN_API_KEY")
}

func collectOpenRouterEnvKeys() []string {
	return collect1IndexedEnvKeys("OPENROUTER_API_KEY")
}

func collectNvidiaEnvKeys() []string {
	return collect1IndexedEnvKeys("NVIDIA_API_KEY")
}

func collectHuggingFaceEnvKeys() []string {
	return collect1IndexedEnvKeys("HUGGINGFACE_API_KEY")
}

func collect1IndexedEnvKeys(envPrefix string) []string {
	out := make([]string, 0, maxNumberedAPIKeysEnv)
	if s := strings.TrimSpace(os.Getenv(envPrefix)); s != "" {
		out = append(out, s)
	}
	for i := 1; i <= maxNumberedAPIKeysEnv; i++ {
		name := fmt.Sprintf("%s_%d", envPrefix, i)
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			if i == 1 {
				continue
			}
			break
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
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
