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

// TestPkgLipsdkAuthImportClosure_staysSDKLocal ensures public auth DTOs do not pull core, plugins,
// or provider SDKs into the stable auth package (task 10.4, complements task 1.4).
func TestPkgLipsdkAuthImportClosure_staysSDKLocal(t *testing.T) {
	t.Parallel()
	forbidden := []struct {
		sub, msg string
	}{
		{"/internal/core/", "pkg/lipsdk/auth must not import internal/core"},
		{"/internal/plugins/", "pkg/lipsdk/auth must not import concrete protocol plugins"},
		{"github.com/openai/openai-go", "pkg/lipsdk/auth must not import OpenAI provider SDK"},
		{"github.com/anthropics/anthropic-sdk-go", "pkg/lipsdk/auth must not import Anthropic SDK"},
		{"github.com/aws/aws-sdk-go-v2", "pkg/lipsdk/auth must not import AWS SDKs"},
	}
	assertGoListImportsExclude(t, "./pkg/lipsdk/auth", forbidden)
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
