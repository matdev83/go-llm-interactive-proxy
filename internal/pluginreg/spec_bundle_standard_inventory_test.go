package pluginreg

import (
	"slices"
	"testing"
)

// TestSpecBundle_standardBundleIDInventory locks the standard distribution plugin IDs.
// When adding a bundled plugin, update StandardBundle / StandardBackendBundle and this expectation.
func TestSpecBundle_standardBundleIDInventory(t *testing.T) {
	t.Parallel()
	b := StandardBundle()
	be := StandardBackendBundle(UpstreamAPIKeys{})

	wantFE := []string{
		"anthropic",
		"gemini",
		"openai-legacy",
		"openai-responses",
	}
	var gotFE []string
	for _, e := range b.Frontends {
		gotFE = append(gotFE, e.ID)
	}
	slices.Sort(gotFE)
	if !slices.Equal(gotFE, wantFE) {
		t.Fatalf("frontend IDs\ngot  %#v\nwant %#v", gotFE, wantFE)
	}

	wantBE := []string{
		"acp",
		"anthropic",
		"bedrock",
		"gemini",
		"local-stub",
		"nvidia",
		"ollama",
		"ollama-cloud",
		"openai-legacy",
		"openai-responses",
		"openrouter",
	}
	var gotBE []string
	for _, e := range be.Backends {
		gotBE = append(gotBE, e.ID)
	}
	slices.Sort(gotBE)
	if !slices.Equal(gotBE, wantBE) {
		t.Fatalf("backend IDs\ngot  %#v\nwant %#v", gotBE, wantBE)
	}

	wantFeat := []string{
		"parts-noop",
		"pre-request-policy",
		"ref-autoappend-file",
		"ref-request-suffix",
		"ref-submit-annotate",
		"ref-tool-policy",
		"ref-tool-prefix",
		"ref-traffic-transcript",
		"ref-verifier-stub",
		"ref-workspace-guard",
		"submit-noop",
		"tool-reactor-noop",
	}
	var gotFeat []string
	for _, e := range b.Features {
		gotFeat = append(gotFeat, e.ID)
	}
	slices.Sort(gotFeat)
	if !slices.Equal(gotFeat, wantFeat) {
		t.Fatalf("feature IDs\ngot  %#v\nwant %#v", gotFeat, wantFeat)
	}
}
