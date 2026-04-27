package archtest

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestPkgLipapiDoesNotImportAuthSDKPackages keeps canonical LLM contracts free of transport auth
// DTOs (authentication-architecture-refactor task 10.4).
func TestPkgLipapiDoesNotImportAuthSDKPackages(t *testing.T) {
	t.Parallel()
	forbidden := []struct {
		sub, msg string
	}{
		{"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auth", "pkg/lipapi must not import pkg/lipsdk/auth"},
		{"github.com/matdev83/go-llm-interactive-proxy/internal/core/auth", "pkg/lipapi must not import internal/core/auth"},
	}
	assertGoListImportsExclude(t, "./pkg/lipapi", forbidden)
}

// lipsdkAuthImportClosureForbidden is matched against every ImportPath in `go list -deps` for
// ./pkg/lipsdk/auth (transitive closure, same policy as the former direct-imports-only check).
var lipsdkAuthImportClosureForbidden = []forbiddenDep{
	{Substr: "/internal/core/", ErrMsg: "pkg/lipsdk/auth must not depend on internal/core (transitively)"},
	{Substr: "/internal/plugins/", ErrMsg: "pkg/lipsdk/auth must not depend on internal/plugins (transitively)"},
	{Substr: "github.com/openai/openai-go", ErrMsg: "pkg/lipsdk/auth must not depend on OpenAI Go SDK (transitively)"},
	{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "pkg/lipsdk/auth must not depend on Anthropic SDK (transitively)"},
	{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "pkg/lipsdk/auth must not depend on AWS SDKs (transitively)"},
}

// TestPkgLipsdkAuthImportClosure_staysSDKLocal ensures public auth DTOs do not pull core, plugins,
// or provider SDKs into the stable auth package (task 10.4, complements task 1.4).
func TestPkgLipsdkAuthImportClosure_staysSDKLocal(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./pkg/lipsdk/auth"}, lipsdkAuthImportClosureForbidden)
}

func assertGoListImportsExclude(t *testing.T, goListPattern string, forbidden []struct {
	sub, msg string
}) {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "-test=false", goListPattern)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %s: %v", goListPattern, err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if strings.HasSuffix(pkg.ImportPath, "_test") {
			continue
		}
		for _, imp := range pkg.Imports {
			for _, r := range forbidden {
				if strings.Contains(imp, r.sub) {
					t.Fatalf("%s: forbidden import %q in %q", r.msg, imp, pkg.ImportPath)
				}
			}
		}
	}
}
