package lipsdk

// StandardDistributionRequirements lists plugin ids the reference cmd/lipstd distribution
// expects in configuration and registry factories. Single source for mandatory validation.
func StandardDistributionRequirements() []Requirement {
	return []Requirement{
		{Kind: PluginKindFrontend, ID: "openai-responses"},
		{Kind: PluginKindFrontend, ID: "openai-legacy"},
		{Kind: PluginKindFrontend, ID: "anthropic"},
		{Kind: PluginKindFrontend, ID: "gemini"},
		{Kind: PluginKindBackend, ID: "openai-responses"},
		{Kind: PluginKindBackend, ID: "openai-legacy"},
		{Kind: PluginKindBackend, ID: "anthropic"},
		{Kind: PluginKindBackend, ID: "gemini"},
		{Kind: PluginKindBackend, ID: "bedrock"},
		{Kind: PluginKindBackend, ID: "acp"},
		{Kind: PluginKindBackend, ID: "openrouter"},
		{Kind: PluginKindFeature, ID: "submit-noop"},
		{Kind: PluginKindFeature, ID: "parts-noop"},
		{Kind: PluginKindFeature, ID: "tool-reactor-noop"},
	}
}
