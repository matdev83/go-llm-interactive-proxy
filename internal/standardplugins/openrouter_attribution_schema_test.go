package standardplugins_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"gopkg.in/yaml.v3"
)

func TestOpenRouterAttribution_identitySchemaStrict(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid_nested_openrouter_and_user_agent",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
identity:
  user_agent:
    mode: proxy
  openrouter:
    app_url:
      mode: drop
    app_title:
      mode: custom
      value: NestedOK
`,
		},
		{
			name: "flat_app_url_rejected",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
identity:
  app_url:
    mode: drop
`,
			wantErr: "identity.app_url",
		},
		{
			name: "flat_app_title_rejected",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
identity:
  app_title:
    mode: custom
    value: Flat
`,
			wantErr: "identity.app_title",
		},
		{
			name: "unknown_identity_key_rejected",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
identity:
  user_agent:
    mode: proxy
  unexpected:
    mode: drop
`,
			wantErr: "identity.unexpected",
		},
		{
			name: "unknown_openrouter_key_rejected",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
identity:
  openrouter:
    app_url:
      mode: proxy
    referer:
      mode: drop
`,
			wantErr: "identity.openrouter.referer",
		},
		{
			name: "unknown_field_policy_key_rejected",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
identity:
  user_agent:
    mode: proxy
    typo: 1
`,
			wantErr: "identity.user_agent.typo",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := pluginreg.NewRegistry()
			if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
				t.Fatal(err)
			}
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.yaml), &node); err != nil {
				t.Fatal(err)
			}
			_, err := reg.BuildBackend(openrouter.ID, node, http.DefaultClient, pluginreg.BackendFactoryDeps{})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected schema error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q should mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestOpenRouterAttribution_legacyStaticValidatedAtFactory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "invalid_static_referer",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
static_referer: not-a-url
`,
			wantErr: "static_referer",
		},
		{
			name: "control_static_title",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
static_title: "Bad\nTitle"
`,
			wantErr: "static_title",
		},
		{
			name: "overlong_static_title",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
static_title: "` + strings.Repeat("t", 257) + `"
`,
			wantErr: "static_title",
		},
		{
			name: "valid_legacy_accepted",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
static_referer: https://legacy.example/app
static_title: LegacyOK
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := pluginreg.NewRegistry()
			if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
				t.Fatal(err)
			}
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.yaml), &node); err != nil {
				t.Fatal(err)
			}
			_, err := reg.BuildBackend(openrouter.ID, node, http.DefaultClient, pluginreg.BackendFactoryDeps{})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q should mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}
