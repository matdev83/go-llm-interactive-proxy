package ollama

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func CapsFromOllamaCapabilities(caps []string) lipapi.BackendCaps {
	out := lipapi.NewBackendCaps()
	for _, cap := range caps {
		switch cap {
		case "completion":
			out[lipapi.CapabilityStreaming] = struct{}{}
		case "tools":
			out[lipapi.CapabilityTools] = struct{}{}
		case "thinking":
			out[lipapi.CapabilityReasoning] = struct{}{}
		case "vision":
			out[lipapi.CapabilityVision] = struct{}{}
		}
	}
	return out
}

func defaultModelCaps() lipapi.BackendCaps {
	return lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
}
