package service

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"

func capsFromOllama(caps []string) backendplugin.CapabilitySummary {
	out := backendplugin.CapabilitySummary{Streaming: true}
	for _, cap := range caps {
		switch cap {
		case "tools":
			out.Tools = true
		case "thinking":
			out.Reasoning = true
		case "vision":
			out.Vision = true
		}
	}
	return out
}
