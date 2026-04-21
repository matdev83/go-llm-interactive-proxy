// Package openaicaps holds small, shared capability rules for OpenAI-hosted backends.
package openaicaps

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// HostedFull is the default capability surface for modern OpenAI Chat and Responses routes.
var HostedFull = lipapi.NewBackendCaps(
	lipapi.CapabilityStreaming,
	lipapi.CapabilityTools,
	lipapi.CapabilityVision,
	lipapi.CapabilityDocuments,
	lipapi.CapabilityReasoning,
	lipapi.CapabilityParallelToolCalls,
)

// ForHostedModel applies conservative, model-id-based capability narrowing.
// Rules are intentionally small and explicit; expand here as catalog data grows.
func ForHostedModel(model string) lipapi.BackendCaps {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return HostedFull
	}
	// gpt-3.5 Chat models do not accept multimodal image/file inputs on the hosted API.
	if strings.Contains(m, "gpt-3.5") {
		out := lipapi.NewBackendCaps()
		for c := range HostedFull {
			if c == lipapi.CapabilityVision || c == lipapi.CapabilityDocuments {
				continue
			}
			out[c] = struct{}{}
		}
		return out
	}
	// o-series hosted models use a conservative tool/vision matrix in this catalog.
	if strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") {
		out := lipapi.NewBackendCaps()
		for c := range HostedFull {
			if c == lipapi.CapabilityParallelToolCalls {
				continue
			}
			out[c] = struct{}{}
		}
		return out
	}
	return HostedFull
}
