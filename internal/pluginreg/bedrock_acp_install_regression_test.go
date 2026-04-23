package pluginreg

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"gopkg.in/yaml.v3"
)

func Test_decodeBedrockBackendYAML_regression(t *testing.T) {
	t.Parallel()
	raw := `region: us-east-1
access_key_id: AKIDTEST
secret_access_key: SECRETTEST
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	var y bedrockBackendYAML
	if err := config.DecodeYAMLNode(root, &y); err != nil {
		t.Fatal(err)
	}
	if y.Region != "us-east-1" || y.AccessKeyID != "AKIDTEST" || y.SecretAccessKey != "SECRETTEST" {
		t.Fatalf("unexpected decode: %+v", y)
	}
}

func Test_backendACP_buildsFromYAML(t *testing.T) {
	t.Parallel()
	raw := `base_url: http://127.0.0.1:9/acp`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	b, err := backendACP(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Open == nil {
		t.Fatal("expected non-nil Open")
	}
}

func Test_registryBuildBedrockAndACP_afterHostedKeyChanges(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	acpRaw := `base_url: http://127.0.0.1:9/acp`
	var acpNode yaml.Node
	if err := yaml.Unmarshal([]byte(acpRaw), &acpNode); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.BuildBackend(acp.ID, acpNode, nil); err != nil {
		t.Fatalf("acp BuildBackend: %v", err)
	}

	brRaw := `region: us-east-1
access_key_id: AKIDTEST
secret_access_key: SECRETTEST
`
	var brNode yaml.Node
	if err := yaml.Unmarshal([]byte(brRaw), &brNode); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.BuildBackend(bedrock.ID, brNode, nil); err != nil {
		t.Fatalf("bedrock BuildBackend: %v", err)
	}
}
