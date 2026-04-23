package archtest

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

type goListPackage struct {
	ImportPath string   `json:"ImportPath"`
	Standard   bool     `json:"Standard"`
	Imports    []string `json:"Imports"`
}

// forbiddenDep is a substring match against ImportPath from `go list -deps -test=false`.
type forbiddenDep struct {
	Substr string
	ErrMsg string
}

// publicContractVendorSDKDeps: official provider SDKs must not appear in pkg/lipapi or pkg/lipsdk
// (extension platform + hexagonal public-contract rules).
var publicContractVendorSDKDeps = []forbiddenDep{
	{
		Substr: "github.com/anthropics/anthropic-sdk-go",
		ErrMsg: "public contract packages must not depend on Anthropic SDK",
	},
	{
		Substr: "github.com/openai/openai-go",
		ErrMsg: "public contract packages must not depend on OpenAI Go SDK",
	},
	{
		Substr: "google.golang.org/genai",
		ErrMsg: "public contract packages must not depend on Google GenAI SDK",
	},
	{
		Substr: "github.com/aws/aws-sdk-go-v2/service/bedrockruntime",
		ErrMsg: "public contract packages must not depend on Bedrock runtime SDK",
	},
}

// publicLipsdkForbiddenDeps is the full hexagonal public-contract surface for ./pkg/lipsdk/...
// (introduce-hexagonal-architecture task 2.1): no internal orchestration, wiring roots, or vendor SDKs.
var publicLipsdkForbiddenDeps = append([]forbiddenDep{
	{
		Substr: "/internal/",
		ErrMsg: "pkg/lipsdk must not depend on internal packages " +
			"(orchestration lives in internal/core; same-module internal/ " +
			"imports are otherwise unconstrained by the compiler)",
	},
	{
		Substr: "/internal/pluginreg",
		ErrMsg: "pkg/lipsdk must not depend on pluginreg (composition root)",
	},
	{
		Substr: "/internal/infra/runtimebundle",
		ErrMsg: "pkg/lipsdk must not depend on runtimebundle (composition assembly)",
	},
	{
		Substr: "/internal/stdhttp",
		ErrMsg: "pkg/lipsdk must not depend on stdhttp (driving adapter / HTTP server layer)",
	},
}, publicContractVendorSDKDeps...)

// publicLipapiForbiddenDeps is the same policy for ./pkg/lipapi/... (canonical contract only).
var publicLipapiForbiddenDeps = append([]forbiddenDep{
	{
		Substr: "/internal/",
		ErrMsg: "pkg/lipapi must not depend on internal packages " +
			"(contract surface only; same-module internal/ imports are " +
			"otherwise unconstrained by the compiler)",
	},
	{
		Substr: "/internal/pluginreg",
		ErrMsg: "pkg/lipapi must not depend on pluginreg (composition root)",
	},
	{
		Substr: "/internal/infra/runtimebundle",
		ErrMsg: "pkg/lipapi must not depend on runtimebundle (composition assembly)",
	},
	{
		Substr: "/internal/stdhttp",
		ErrMsg: "pkg/lipapi must not depend on stdhttp (driving adapter / HTTP server layer)",
	},
}, publicContractVendorSDKDeps...)

// TestPkgLipapiPublicContractDoesNotImportInternalOrWiring ensures the canonical public contract
// does not take dependencies on the policy core, composition roots, transport server, or provider SDKs
// (introduce-hexagonal-architecture, task 2.1).
func TestPkgLipapiPublicContractDoesNotImportInternalOrWiring(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./pkg/lipapi/..."}, publicLipapiForbiddenDeps)
}

// TestPkgLipsdkDoesNotDependOnVendorSDKs ensures the stable feature/plugin SDK does not pull
// official provider client modules (Q2 / design section 18), and (task 2.1) does not import internal/,
// composition roots, or stdhttp.
func TestPkgLipsdkDoesNotDependOnVendorSDKs(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./pkg/lipsdk/..."}, publicLipsdkForbiddenDeps)
}

// featurePluginsForbiddenDeps: official feature plugins must consume extension/runtime only through
// pkg/lipsdk and pkg/lipapi (introduce-hexagonal-architecture task 5.3).
var featurePluginsForbiddenDeps = []forbiddenDep{
	{
		Substr: "/internal/core/",
		ErrMsg: "internal/plugins/features must not depend on internal/core (use pkg/lipsdk contracts)",
	},
}

// TestOfficialFeaturePluginsDoNotDependOnInternalCore ensures feature plugin packages do not pull
// orchestration or extension wiring from internal/core; contract tests keep the seam replaceable.
func TestOfficialFeaturePluginsDoNotDependOnInternalCore(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/plugins/features/..."}, featurePluginsForbiddenDeps)
}

// TestInternalCoreDoesNotDependOnStdhttpOrProtocolPlugins keeps orchestration free of the HTTP
// server layer, official protocol plugins, and transport-labeled SDK paths (introduce-hexagonal
// task 4.1). Principal context uses [github.com/.../pkg/lipsdk/execview] from core instead.
func TestInternalCoreDoesNotDependOnStdhttpOrProtocolPlugins(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, []forbiddenDep{
		{
			Substr: "/internal/stdhttp",
			ErrMsg: "internal/core must not depend on stdhttp (transport/server layer)",
		},
		{
			Substr: "/internal/plugins/frontends/",
			ErrMsg: "internal/core must not depend on concrete frontend plugins",
		},
		{
			Substr: "/internal/plugins/backends/",
			ErrMsg: "internal/core must not depend on concrete backend plugins",
		},
		{
			Substr: "/internal/plugins/features/",
			ErrMsg: "internal/core must not depend on concrete feature plugins",
		},
		{
			Substr: "/pkg/lipsdk/transport/",
			ErrMsg: "internal/core must not depend on pkg/lipsdk/transport " +
				"(driving-adapter/transport layer; use execview principal " +
				"context from core)",
		},
	})
}

// TestInternalCoreRuntimeDoesNotImportNetHTTP keeps protocol decode, encode, and wire handling in
// driving adapters (introduce-hexagonal-architecture 4.2): the executor path must not depend
// on net/http directly. Subpackages of internal/core/runtime are the same [runtime] module package.
func TestInternalCoreRuntimeDoesNotImportNetHTTP(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("go", "list", "-json", "-test=false", "./internal/core/runtime")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if !dec.More() {
		t.Fatal("go list: empty output")
	}
	var pkg goListPackage
	if err := dec.Decode(&pkg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	const wantPath = "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	if pkg.ImportPath != wantPath {
		t.Fatalf("unexpected package: got %q want %q", pkg.ImportPath, wantPath)
	}
	for _, imp := range pkg.Imports {
		if imp == "net/http" {
			t.Fatalf(
				"internal/core/runtime must not import net/http (keep HTTP at driving adapters); "+
					"found direct import in %s",
				pkg.ImportPath,
			)
		}
	}
}

func assertDepsExcludeForbidden(t *testing.T, patterns []string, rules []forbiddenDep) {
	t.Helper()
	args := append([]string{"list", "-deps", "-test=false", "-json"}, patterns...)
	cmd := exec.Command("go", args...)
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
		if pkg.Standard {
			continue
		}
		for _, r := range rules {
			if strings.Contains(pkg.ImportPath, r.Substr) {
				t.Fatalf("%s: %s", r.ErrMsg, pkg.ImportPath)
			}
		}
	}
}
