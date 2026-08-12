package contracttest

// ConnectorFamilyCoverage identifies an executable connector module and the
// implementation family represented by its host-path contract test.
type ConnectorFamilyCoverage struct {
	ModulePath string `json:"module_path"`
	Family     string `json:"family"`
	Subject    string `json:"subject"`
}

// CurrentConnectorFamilyCoverage is the checked-in coverage manifest for the
// executable connector modules. It is intentionally data-only so external
// connector authors can audit coverage without importing connector internals.
var CurrentConnectorFamilyCoverage = []ConnectorFamilyCoverage{
	{ModulePath: "connectors/acp", Family: "acp", Subject: "acp"},
	{ModulePath: "connectors/agycliacp", Family: "acp-derivative", Subject: "agycliacp"},
	{ModulePath: "connectors/cursorcliacp", Family: "acp-derivative", Subject: "cursorcliacp"},
	{ModulePath: "connectors/geminicliacp", Family: "acp-derivative", Subject: "geminicliacp"},
	{ModulePath: "connectors/codex", Family: "codex", Subject: "codex"},
	{ModulePath: "connectors/opencode", Family: "opencode", Subject: "opencode"},
	{ModulePath: "connectors/ollama", Family: "ollama", Subject: "ollama"},
	{ModulePath: "connectors/vllm", Family: "vllm", Subject: "vllm"},
	{ModulePath: "connectors/huggingface", Family: "huggingface", Subject: "huggingface"},
	{ModulePath: "connectors/nvidia", Family: "openai-compatible", Subject: "nvidia"},
	{ModulePath: "connectors/openrouter", Family: "openai-compatible", Subject: "openrouter"},
	{ModulePath: "connectors/cursorsdk", Family: "acp-sdk", Subject: "cursorsdk"},
	{ModulePath: "connectors/llamacpp", Family: "openai-compatible", Subject: "llamacpp"},
	{ModulePath: "connectors/lmstudio", Family: "openai-compatible", Subject: "lmstudio"},
	{ModulePath: "connectors/localstub", Family: "test-emulator", Subject: "localstub"},
}
