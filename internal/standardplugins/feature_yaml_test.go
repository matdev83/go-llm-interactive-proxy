package standardplugins

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"gopkg.in/yaml.v3"
)

func testRegistryWithStdBundle(t *testing.T) *pluginreg.Registry {
	t.Helper()
	r := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(r, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStandardBundle_buildsPreRequestPolicyHandlers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.md"), []byte("classify the request"), 0o600); err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(fmt.Appendf(nil, `
prompt_dir: %q
handlers:
  - id: compliance
    priority: 3
    prompt_filename: policy.md
    model_routing_string: local:policy
    deny_pattern: DENY
`, dir), &n); err != nil {
		t.Fatal(err)
	}
	reg := testRegistryWithStdBundle(t)
	b, err := reg.BuildFeatureBundle("pre-request-policy", n)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.PreRequestHandlers) != 1 {
		t.Fatalf("pre-request handlers: %d", len(b.PreRequestHandlers))
	}
	if b.PreRequestHandlers[0].ID() != "compliance" {
		t.Fatalf("handler id: %q", b.PreRequestHandlers[0].ID())
	}
}

func TestStandardBundle_buildsSecretsGuard(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("action: block"), &n); err != nil {
		t.Fatal(err)
	}
	reg := testRegistryWithStdBundle(t)
	b, err := reg.BuildFeatureBundle("secrets-guard", n)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.SecretGuards) != 1 {
		t.Fatalf("secret guards: %d", len(b.SecretGuards))
	}
	if b.SecretGuards[0].ID() != "secrets-guard" {
		t.Fatalf("guard id: %q", b.SecretGuards[0].ID())
	}
}

func TestRequireEmptyFeatureYAML_acceptsEmptyMapping(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	if err := requireEmptyFeatureYAML("parts-noop", n); err != nil {
		t.Fatal(err)
	}
}

func TestRequireEmptyFeatureYAML_rejectsUnknownKey(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("unexpected: true"), &n); err != nil {
		t.Fatal(err)
	}
	if err := requireEmptyFeatureYAML("parts-noop", n); err == nil {
		t.Fatal("expected error")
	}
}
