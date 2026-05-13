package pluginreg

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"gopkg.in/yaml.v3"
)

func TestOpenAIStyleYAML_decodesAPIKeys(t *testing.T) {
	t.Parallel()
	raw := `
base_url: https://example.com/v1
api_key: primary
api_keys:
  - second
  - " third "
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(root, &y); err != nil {
		t.Fatal(err)
	}
	if y.BaseURL != "https://example.com/v1" {
		t.Fatalf("base_url: %q", y.BaseURL)
	}
	if y.APIKey != "primary" {
		t.Fatalf("api_key: %q", y.APIKey)
	}
	wantKeys := []string{"second", " third "}
	if len(y.APIKeys) != len(wantKeys) {
		t.Fatalf("api_keys len: got %d want %d", len(y.APIKeys), len(wantKeys))
	}
	for i := range wantKeys {
		if y.APIKeys[i] != wantKeys[i] {
			t.Fatalf("api_keys[%d]: got %q want %q", i, y.APIKeys[i], wantKeys[i])
		}
	}
}

func TestOpenAIStyleYAML_decodesStructuredCredentials(t *testing.T) {
	t.Parallel()
	raw := `
credentials:
  - id: prod-primary
    api_key: sk-test
    remote_org_id: org-1
    remote_project_id: proj-1
    remote_workspace_id: ws-1
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(root, &y); err != nil {
		t.Fatal(err)
	}
	creds := hostedCredentials(y.Credentials)
	if len(creds) != 1 {
		t.Fatalf("credentials len: got %d", len(creds))
	}
	if creds[0].ID != "prod-primary" || creds[0].Secret != "sk-test" {
		t.Fatalf("credential identity/secret decode: %+v", creds[0])
	}
	if creds[0].RemoteOrgID != "org-1" || creds[0].RemoteProjectID != "proj-1" ||
		creds[0].RemoteWorkspaceID != "ws-1" {
		t.Fatalf("remote metadata: %+v", creds[0])
	}
}
