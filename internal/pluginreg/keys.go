package pluginreg

import (
	"os"
	"strings"
)

func resolveOpenAIKey(apiKey string) string {
	if strings.TrimSpace(apiKey) != "" {
		return strings.TrimSpace(apiKey)
	}
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
}

func resolveAnthropicKey(apiKey string) string {
	if strings.TrimSpace(apiKey) != "" {
		return strings.TrimSpace(apiKey)
	}
	return strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
}

func resolveGeminiKey(apiKey string) string {
	if strings.TrimSpace(apiKey) != "" {
		return strings.TrimSpace(apiKey)
	}
	return strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
}
