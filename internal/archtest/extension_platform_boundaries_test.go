package archtest

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

type goListPackage struct {
	ImportPath string
	Standard   bool
}

// forbiddenDep is a substring match against ImportPath from `go list -deps -test=false`.
type forbiddenDep struct {
	Substr string
	ErrMsg string
}

// TestPkgLipsdkDoesNotDependOnVendorSDKs ensures the stable feature/plugin SDK does not
// pull official provider client modules (Q2 / design §18).
func TestPkgLipsdkDoesNotDependOnVendorSDKs(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./pkg/lipsdk/..."}, []forbiddenDep{
		{"github.com/anthropics/anthropic-sdk-go", "pkg/lipsdk must not depend on Anthropic SDK"},
		{"github.com/openai/openai-go", "pkg/lipsdk must not depend on OpenAI Go SDK"},
		{"google.golang.org/genai", "pkg/lipsdk must not depend on Google GenAI SDK"},
		{"github.com/aws/aws-sdk-go-v2/service/bedrockruntime", "pkg/lipsdk must not depend on Bedrock runtime SDK"},
	})
}

// TestInternalCoreDoesNotDependOnStdhttpOrProtocolPlugins keeps orchestration free of the HTTP
// server layer and official frontend/backend adapters (Q2 / design §13, §18).
//
// pkg/lipsdk/transport/httpauth is allowed: it is the stable context contract for principal
// propagation, not stdhttp middleware types.
func TestInternalCoreDoesNotDependOnStdhttpOrProtocolPlugins(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, []forbiddenDep{
		{"/internal/stdhttp", "internal/core must not depend on stdhttp (transport/server layer)"},
		{"/internal/plugins/frontends/", "internal/core must not depend on concrete frontend plugins"},
		{"/internal/plugins/backends/", "internal/core must not depend on concrete backend plugins"},
		{"/internal/plugins/features/", "internal/core must not depend on concrete feature plugins"},
	})
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
	dec := json.NewDecoder(strings.NewReader(string(out)))
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
