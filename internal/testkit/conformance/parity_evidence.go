package conformance

import "slices"

// ExpectedMigrationGoldenJSON lists migration parity JSON under testdata/migration/
// (Req. 15.13). Keep in sync with docs/release-gates.md and docs/conformance-golden-coverage.md.
var ExpectedMigrationGoldenJSON = []string{
	"python_lip_anthropic_messages_nonstream.json",
	"python_lip_openai_responses_http_nonstream.json",
	"python_lip_openai_responses_http_streaming.json",
}

// ParitySuiteGoFiles lists parity suite sources (.kiro/specs/llm-api-parity/tasks.md Phase 5).
// Keep in sync with docs/conformance-golden-coverage.md.
var ParitySuiteGoFiles = []string{
	"parity_openai_test.go",
	"parity_anthropic_test.go",
	"parity_gemini_test.go",
	"parity_bedrock_test.go",
	"parity_acp_test.go",
}

// ParityProtocolEvidence maps every bundled frontend/backend protocol id to at least one
// parity suite source file in this package (llm-api-parity P5.1). Shared families may
// list the same file for multiple ids.
var ParityProtocolEvidence = map[string][]string{
	"openai-responses": {"parity_openai_test.go"},
	"openai-legacy":    {"parity_openai_test.go"},
	"anthropic":        {"parity_anthropic_test.go"},
	"gemini":           {"parity_gemini_test.go"},
	"bedrock":          {"parity_bedrock_test.go"},
	"acp":              {"parity_acp_test.go"},
	"openrouter":       {"parity_openai_test.go"},
	"nvidia":           {"parity_openai_test.go"},
}

// AllBundledProtocolIDs returns the sorted union of bundled frontend and backend protocol ids.
func AllBundledProtocolIDs() []string {
	m := map[string]struct{}{}
	for _, id := range BundledFrontendIDs() {
		m[id] = struct{}{}
	}
	for _, id := range BundledBackendIDs() {
		m[id] = struct{}{}
	}
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
