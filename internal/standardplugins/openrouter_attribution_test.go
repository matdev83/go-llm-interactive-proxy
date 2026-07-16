package standardplugins_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	refchat "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaichat"
	refresponses "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestOpenRouterAttribution_factoryMergeAndLegacy(t *testing.T) {
	t.Parallel()

	type want struct {
		referer string
		title   string
		omitRef bool
		omitTit bool
		ua      string
	}

	cases := []struct {
		name      string
		global    identity.Config
		yamlExtra string
		want      want
	}{
		{
			name:   "global_proxy_defaults",
			global: identity.Config{},
			want: want{
				referer: "https://github.com/matdev83/go-llm-interactive-proxy",
				title:   "go-llm-interactive-proxy",
				ua:      "go-llm-interactive-proxy",
			},
		},
		{
			name: "backend_title_override_inherits_url",
			global: identity.Config{
				Upstream: identity.UpstreamPolicy{
					OpenRouter: identity.OpenRouterPolicy{
						AppURL:   identity.FieldPolicy{Mode: identity.ModeCustom, Value: "https://global-url.example/"},
						AppTitle: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "GlobalTitle"},
					},
				},
			},
			yamlExtra: `
identity:
  openrouter:
    app_title:
      mode: custom
      value: BackendTitle
`,
			want: want{
				referer: "https://global-url.example/",
				title:   "BackendTitle",
				ua:      "go-llm-interactive-proxy",
			},
		},
		{
			name: "backend_explicit_drop_title",
			global: identity.Config{
				Upstream: identity.UpstreamPolicy{
					OpenRouter: identity.OpenRouterPolicy{
						AppURL:   identity.FieldPolicy{Mode: identity.ModeProxy},
						AppTitle: identity.FieldPolicy{Mode: identity.ModeProxy},
					},
				},
			},
			yamlExtra: `
identity:
  openrouter:
    app_title:
      mode: drop
`,
			want: want{
				referer: "https://github.com/matdev83/go-llm-interactive-proxy",
				omitTit: true,
				ua:      "go-llm-interactive-proxy",
			},
		},
		{
			name:   "legacy_static_when_no_backend_identity_field",
			global: identity.Config{},
			yamlExtra: `
static_referer: https://legacy.example/
static_title: LegacyTitle
`,
			want: want{
				referer: "https://legacy.example/",
				title:   "LegacyTitle",
				ua:      "go-llm-interactive-proxy",
			},
		},
		{
			name: "url_override_independent_from_ua",
			global: identity.Config{
				Upstream: identity.UpstreamPolicy{
					UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "UA-Custom/1"},
					OpenRouter: identity.OpenRouterPolicy{
						AppURL:   identity.FieldPolicy{Mode: identity.ModeProxy},
						AppTitle: identity.FieldPolicy{Mode: identity.ModeDrop},
					},
				},
			},
			yamlExtra: `
identity:
  openrouter:
    app_url:
      mode: custom
      value: https://url-only.example/
`,
			want: want{
				referer: "https://url-only.example/",
				omitTit: true,
				ua:      "UA-Custom/1",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sawReferer, sawTitle, sawUA string
			var presentRef, presentTit, presentUA bool
			var sawLegacyTitle bool
			inner := refchat.NewHandler(refchat.Config{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, presentRef = r.Header["Http-Referer"]
				if !presentRef {
					_, presentRef = r.Header["Referer"]
				}
				sawReferer = r.Header.Get("HTTP-Referer")
				_, presentTit = r.Header["X-Openrouter-Title"]
				sawTitle = r.Header.Get("X-OpenRouter-Title")
				_, sawLegacyTitle = r.Header["X-Title"]
				_, presentUA = r.Header["User-Agent"]
				sawUA = r.Header.Get("User-Agent")
				inner.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)

			g := tc.global
			if err := identity.Validate(&g); err != nil {
				t.Fatal(err)
			}
			reg := pluginreg.NewRegistry()
			if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
				t.Fatal(err)
			}
			raw := "base_url: " + srv.URL + "/v1\napi_key: or-test\n" + tc.yamlExtra
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
				t.Fatal(err)
			}
			be, err := reg.BuildBackend(openrouter.ID, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
			if err != nil {
				t.Fatal(err)
			}
			call := identityTransportCall(lipapi.OperationOpenAIChatCompletions)
			es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "openrouter/auto"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = lipapi.Collect(context.Background(), es)
			if err != nil {
				t.Fatal(err)
			}

			if sawLegacyTitle {
				t.Fatal("must not emit legacy X-Title")
			}
			if tc.want.omitRef {
				if presentRef {
					t.Fatalf("HTTP-Referer key present value=%q want key absent", sawReferer)
				}
			} else if !presentRef || sawReferer != tc.want.referer {
				t.Fatalf("HTTP-Referer present=%v value=%q want %q", presentRef, sawReferer, tc.want.referer)
			}
			if tc.want.omitTit {
				if presentTit {
					t.Fatalf("X-OpenRouter-Title key present value=%q want key absent", sawTitle)
				}
			} else if !presentTit || sawTitle != tc.want.title {
				t.Fatalf("X-OpenRouter-Title present=%v value=%q want %q", presentTit, sawTitle, tc.want.title)
			}
			if !presentUA || sawUA != tc.want.ua {
				t.Fatalf("User-Agent present=%v value=%q want %q", presentUA, sawUA, tc.want.ua)
			}
		})
	}
}

func TestOpenRouterAttribution_legacyConflictsWithBackendIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
		frag string
	}{
		{
			name: "static_referer_and_app_url",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
static_referer: https://legacy.example/
identity:
  openrouter:
    app_url:
      mode: drop
`,
			frag: "static_referer",
		},
		{
			name: "static_title_and_app_title",
			yaml: `
base_url: https://openrouter.ai/api/v1
api_key: or-test
static_title: LegacyTitle
identity:
  openrouter:
    app_title:
      mode: custom
      value: NewTitle
`,
			frag: "static_title",
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
			if err == nil {
				t.Fatal("expected conflict error")
			}
			if !strings.Contains(err.Error(), tc.frag) {
				t.Fatalf("error %q should mention %q", err.Error(), tc.frag)
			}
		})
	}
}

func TestOpenRouterAttribution_legacyDoesNotConflictWithGlobalOnly(t *testing.T) {
	t.Parallel()
	var sawTitle string
	inner := refchat.NewHandler(refchat.Config{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTitle = r.Header.Get("X-OpenRouter-Title")
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	g := identity.Config{
		Upstream: identity.UpstreamPolicy{
			OpenRouter: identity.OpenRouterPolicy{
				AppTitle: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "GlobalTitle"},
			},
		},
	}
	if err := identity.Validate(&g); err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	raw := "base_url: " + srv.URL + "/v1\napi_key: or-test\nstatic_title: LegacyTitle\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(openrouter.ID, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
	if err != nil {
		t.Fatalf("global identity must not conflict with backend legacy: %v", err)
	}
	es, err := be.Open(context.Background(), identityTransportCall(lipapi.OperationOpenAIChatCompletions), routing.AttemptCandidate{Primary: routing.Primary{Model: "openrouter/auto"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), es)
	if sawTitle != "LegacyTitle" {
		t.Fatalf("legacy backend static wins over global new policy: got %q", sawTitle)
	}
}

func TestApprovedNonOpenRouter_noAttributionHeaders(t *testing.T) {
	t.Parallel()
	var sawReferer, sawORTitle bool
	inner := refresponses.NewHandler(refresponses.Config{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawReferer = r.Header["Http-Referer"]
		if r.Header.Get("HTTP-Referer") != "" {
			sawReferer = true
		}
		_, sawORTitle = r.Header["X-Openrouter-Title"]
		if r.Header.Get("X-OpenRouter-Title") != "" {
			sawORTitle = true
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	g := identity.Config{}
	if err := identity.Validate(&g); err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	raw := "base_url: " + srv.URL + "/v1\napi_key: sk-test\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(openairesponses.ID, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
	if err != nil {
		t.Fatal(err)
	}
	es, err := be.Open(context.Background(), identityTransportCall(lipapi.OperationOpenAIResponses), routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = lipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if sawReferer {
		t.Fatal("non-OpenRouter adapter must not emit HTTP-Referer attribution")
	}
	if sawORTitle {
		t.Fatal("non-OpenRouter adapter must not emit X-OpenRouter-Title")
	}
}
