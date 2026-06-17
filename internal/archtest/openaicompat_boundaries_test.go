package archtest

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

var openaicompatForbiddenConcreteProviders = []forbiddenDep{
	{
		Substr: "/internal/plugins/backends/openrouter",
		ErrMsg: "internal/plugins/backends/openaicompat must not import concrete openrouter provider package",
	},
	{
		Substr: "/internal/plugins/backends/nvidia",
		ErrMsg: "internal/plugins/backends/openaicompat must not import concrete nvidia provider package",
	},
}

// TestInternalCoreDoesNotDependOnVendorSDKs keeps orchestration and core contracts free of
// official provider client modules (openai-go, anthropic-sdk-go, genai, bedrockruntime).
func TestInternalCoreDoesNotDependOnVendorSDKs(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, publicContractVendorSDKDeps)
}

// TestOpenaiCompatSharedAdapterDoesNotImportConcreteProviders ensures the shared
// openai-go adapter layer stays provider-agnostic: concrete backends (openrouter, nvidia)
// compose openaicompat, not the reverse.
func TestOpenaiCompatSharedAdapterDoesNotImportConcreteProviders(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/plugins/backends/openaicompat/..."}, openaicompatForbiddenConcreteProviders)
}

// TestConcreteOpenAICompatProvidersImportSharedAdapter documents the allowed dependency
// direction: provider-specific backends may depend on openaicompat shared helpers.
func TestConcreteOpenAICompatProvidersImportSharedAdapter(t *testing.T) {
	t.Parallel()
	const openaicompatPath = "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	for _, pattern := range []string{
		"./internal/plugins/backends/openrouter/...",
		"./internal/plugins/backends/nvidia/...",
	} {
		t.Run(strings.TrimPrefix(pattern, "./internal/plugins/backends/"), func(t *testing.T) {
			t.Parallel()
			assertDepsInclude(t, pattern, openaicompatPath)
		})
	}
}

func assertDepsInclude(t *testing.T, pattern, wantImportPath string) {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-test=false", "-json", pattern)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if pkg.ImportPath == wantImportPath {
			return
		}
	}
	t.Fatalf("%s closure must include %q (concrete providers compose shared openaicompat adapter)", pattern, wantImportPath)
}
