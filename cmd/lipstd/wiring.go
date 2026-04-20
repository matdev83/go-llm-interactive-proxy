package main

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"

// mandatoryStandardPlugins lists plugins that must appear in loaded configuration for the
// reference distribution composition root (IDs must align with config/config.yaml).
func mandatoryStandardPlugins() []lipsdk.Requirement {
	return []lipsdk.Requirement{
		{Kind: lipsdk.PluginKindFrontend, ID: "openai-responses"},
		{Kind: lipsdk.PluginKindFrontend, ID: "openai-legacy"},
		{Kind: lipsdk.PluginKindFrontend, ID: "anthropic"},
		{Kind: lipsdk.PluginKindFrontend, ID: "gemini"},
		{Kind: lipsdk.PluginKindBackend, ID: "openai-responses"},
		{Kind: lipsdk.PluginKindBackend, ID: "openai-legacy"},
		{Kind: lipsdk.PluginKindBackend, ID: "anthropic"},
		{Kind: lipsdk.PluginKindBackend, ID: "gemini"},
		{Kind: lipsdk.PluginKindBackend, ID: "bedrock"},
		{Kind: lipsdk.PluginKindBackend, ID: "acp"},
		{Kind: lipsdk.PluginKindFeature, ID: "submit-noop"},
		{Kind: lipsdk.PluginKindFeature, ID: "parts-noop"},
		{Kind: lipsdk.PluginKindFeature, ID: "tool-reactor-noop"},
	}
}
