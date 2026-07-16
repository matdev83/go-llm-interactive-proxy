package standardplugins_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openrouter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	refanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/anthropicmessages"
	refchat "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaichat"
	refresponses "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openairesponses"
	refopenrouter "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openrouter"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestOpenRouterAttribution_passthroughExtensionsBothFlavors(t *testing.T) {
	t.Parallel()

	flavors := []struct {
		name string
		op   lipapi.Operation
		ext  map[string]json.RawMessage
	}{
		{
			name: "chat_nonstream",
			op:   lipapi.OperationOpenAIChatCompletions,
			ext: map[string]json.RawMessage{
				openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"chat"`),
				openrouterwire.ExtHTTPReferer:    json.RawMessage(`"https://client-passthrough.example/app"`),
				openrouterwire.ExtTitle:          json.RawMessage(`"ClientPassthroughTitle"`),
				openrouterwire.ExtCategories:     json.RawMessage(`"ai,chat"`),
				openrouterwire.ExtMetadataHeader: json.RawMessage(`"{\"sid\":\"1\"}"`),
			},
		},
		{
			name: "responses_nonstream",
			op:   lipapi.OperationOpenAIResponses,
			ext: map[string]json.RawMessage{
				openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"responses"`),
				openrouterwire.ExtHTTPReferer:    json.RawMessage(`"https://client-passthrough.example/app"`),
				openrouterwire.ExtTitle:          json.RawMessage(`"ClientPassthroughTitle"`),
				openrouterwire.ExtCategories:     json.RawMessage(`"ai,chat"`),
				openrouterwire.ExtMetadataHeader: json.RawMessage(`"{\"sid\":\"1\"}"`),
			},
		},
		{
			name: "chat_stream",
			op:   lipapi.OperationOpenAIChatCompletions,
			ext: map[string]json.RawMessage{
				openrouterwire.ExtUpstreamFlavor: json.RawMessage(`"chat"`),
				openrouterwire.ExtHTTPReferer:    json.RawMessage(`"https://client-passthrough.example/app"`),
				openrouterwire.ExtTitle:          json.RawMessage(`"ClientPassthroughTitle"`),
			},
		},
	}

	for _, fl := range flavors {
		t.Run(fl.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var presentRef, presentTit, presentLegacy bool
			var sawReferer, sawTitle, sawCat, sawMeta string
			inner := refopenrouter.NewHandler(refopenrouter.Config{
				OnRequestHeaders: func(h http.Header) {
					mu.Lock()
					_, presentRef = h["Http-Referer"]
					sawReferer = h.Get("HTTP-Referer")
					_, presentTit = h["X-Openrouter-Title"]
					sawTitle = h.Get("X-OpenRouter-Title")
					_, presentLegacy = h["X-Title"]
					sawCat = h.Get("X-OpenRouter-Categories")
					sawMeta = h.Get("X-OpenRouter-Metadata")
					mu.Unlock()
				},
			})
			srv := httptest.NewServer(inner)
			t.Cleanup(srv.Close)

			g := identity.Config{
				Upstream: identity.UpstreamPolicy{
					OpenRouter: identity.OpenRouterPolicy{
						AppURL:   identity.FieldPolicy{Mode: identity.ModePassthrough},
						AppTitle: identity.FieldPolicy{Mode: identity.ModePassthrough},
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
			raw := "base_url: " + srv.URL + "\napi_key: or-test\n"
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
				t.Fatal(err)
			}
			be, err := reg.BuildBackend(openrouter.ID, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
			if err != nil {
				t.Fatal(err)
			}

			delivery := lipapi.DeliveryModeNonStreaming
			transport := lipapi.TransportModeNonStreaming
			if strings.Contains(fl.name, "stream") {
				delivery = lipapi.DeliveryModeStreaming
				transport = lipapi.TransportModeStreaming
			}
			call := lipapi.Call{
				Messages: []lipapi.Message{{
					Role:  lipapi.RoleUser,
					Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
				}},
				Extensions: fl.ext,
				Invocation: lipapi.Invocation{
					Operation:     fl.op,
					DeliveryMode:  delivery,
					TransportMode: transport,
				},
			}
			es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "openai/gpt-4o-mini"}})
			if err != nil {
				t.Fatal(err)
			}
			_ = es.Close()

			mu.Lock()
			defer mu.Unlock()
			if presentLegacy {
				t.Fatal("must not emit legacy X-Title")
			}
			if !presentRef || sawReferer != "https://client-passthrough.example/app" {
				t.Fatalf("HTTP-Referer present=%v value=%q", presentRef, sawReferer)
			}
			if !presentTit || sawTitle != "ClientPassthroughTitle" {
				t.Fatalf("X-OpenRouter-Title present=%v value=%q", presentTit, sawTitle)
			}
			if fl.ext[openrouterwire.ExtCategories] != nil && sawCat != "ai,chat" {
				t.Fatalf("categories unchanged want ai,chat got %q", sawCat)
			}
			if fl.ext[openrouterwire.ExtMetadataHeader] != nil && sawMeta != `{"sid":"1"}` {
				t.Fatalf("metadata unchanged want {\"sid\":\"1\"} got %q", sawMeta)
			}
		})
	}
}

func TestOpenRouterAttribution_defaultProxyOverridesCapturedClient(t *testing.T) {
	t.Parallel()
	var presentRef, presentTit bool
	var sawReferer, sawTitle string
	inner := refchat.NewHandler(refchat.Config{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, presentRef = r.Header["Http-Referer"]
		sawReferer = r.Header.Get("HTTP-Referer")
		_, presentTit = r.Header["X-Openrouter-Title"]
		sawTitle = r.Header.Get("X-OpenRouter-Title")
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
	raw := "base_url: " + srv.URL + "/v1\napi_key: or-test\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(openrouter.ID, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
	if err != nil {
		t.Fatal(err)
	}
	call := identityTransportCall(lipapi.OperationOpenAIChatCompletions)
	call.Extensions = map[string]json.RawMessage{
		openrouterwire.ExtHTTPReferer: json.RawMessage(`"https://client.example/should-not-win"`),
		openrouterwire.ExtTitle:       json.RawMessage(`"ClientShouldNotWin"`),
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "openrouter/auto"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), es)

	if !presentRef || sawReferer != "https://github.com/matdev83/go-llm-interactive-proxy" {
		t.Fatalf("default proxy HTTP-Referer present=%v value=%q", presentRef, sawReferer)
	}
	if !presentTit || sawTitle != "go-llm-interactive-proxy" {
		t.Fatalf("default proxy X-OpenRouter-Title present=%v value=%q", presentTit, sawTitle)
	}
}

func TestOpenRouterAttribution_dropOmitsHeaderKey(t *testing.T) {
	t.Parallel()
	var presentRef, presentTit bool
	inner := refchat.NewHandler(refchat.Config{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, presentRef = r.Header["Http-Referer"]
		_, presentTit = r.Header["X-Openrouter-Title"]
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
	raw := "base_url: " + srv.URL + "/v1\napi_key: or-test\nidentity:\n  openrouter:\n    app_url:\n      mode: drop\n    app_title:\n      mode: drop\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(openrouter.ID, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
	if err != nil {
		t.Fatal(err)
	}
	es, err := be.Open(context.Background(), identityTransportCall(lipapi.OperationOpenAIChatCompletions), routing.AttemptCandidate{Primary: routing.Primary{Model: "openrouter/auto"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lipapi.Collect(context.Background(), es)
	if presentRef {
		t.Fatal("HTTP-Referer key must be absent on drop")
	}
	if presentTit {
		t.Fatal("X-OpenRouter-Title key must be absent on drop")
	}
}

func TestOpenRouterAttribution_legacyConflictsBothFields(t *testing.T) {
	t.Parallel()
	yamlBody := `
base_url: https://openrouter.ai/api/v1
api_key: or-test
static_referer: https://legacy.example/
static_title: LegacyTitle
identity:
  openrouter:
    app_url:
      mode: drop
    app_title:
      mode: custom
      value: NewTitle
`
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(yamlBody), &node); err != nil {
		t.Fatal(err)
	}
	_, err := reg.BuildBackend(openrouter.ID, node, http.DefaultClient, pluginreg.BackendFactoryDeps{})
	if err == nil {
		t.Fatal("expected conflict for both legacy and new fields")
	}
	msg := err.Error()
	if !strings.Contains(msg, "static_referer") || !strings.Contains(msg, "static_title") {
		// First conflict may short-circuit; at least one field-qualified conflict required.
		if !strings.Contains(msg, "static_referer") && !strings.Contains(msg, "static_title") {
			t.Fatalf("error %q should mention static_referer or static_title", msg)
		}
	}
}

func TestApprovedNonOpenRouter_noAttributionHeaders_multiBackend(t *testing.T) {
	t.Parallel()

	backends := []struct {
		id       string
		yamlBase func(u string) string
		handler  http.Handler
		open     func(t *testing.T, beOpen func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error))
	}{
		{
			id: openairesponses.ID,
			yamlBase: func(u string) string {
				return "base_url: " + u + "/v1\napi_key: sk-test\n"
			},
			handler: refresponses.NewHandler(refresponses.Config{}),
			open: func(t *testing.T, beOpen func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error)) {
				t.Helper()
				es, err := beOpen(context.Background(), identityTransportCall(lipapi.OperationOpenAIResponses), routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
				if err != nil {
					t.Fatal(err)
				}
				_, _ = lipapi.Collect(context.Background(), es)
			},
		},
		{
			id: openailegacy.ID,
			yamlBase: func(u string) string {
				return "base_url: " + u + "/v1\napi_key: sk-test\n"
			},
			handler: refchat.NewHandler(refchat.Config{}),
			open: func(t *testing.T, beOpen func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error)) {
				t.Helper()
				es, err := beOpen(context.Background(), identityTransportCall(lipapi.OperationOpenAIChatCompletions), routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
				if err != nil {
					t.Fatal(err)
				}
				_, _ = lipapi.Collect(context.Background(), es)
			},
		},
		{
			id: anthropic.ID,
			yamlBase: func(u string) string {
				return "base_url: " + u + "\napi_key: " + testkit.SyntheticAnthropicAPIKey + "\n"
			},
			handler: refanthropic.NewHandler(refanthropic.Config{}),
			open: func(t *testing.T, beOpen func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error)) {
				t.Helper()
				es, err := beOpen(context.Background(), identityTransportCall(lipapi.OperationOpenAIChatCompletions), routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-haiku-20240307"}})
				if err != nil {
					t.Fatal(err)
				}
				_, _ = lipapi.Collect(context.Background(), es)
			},
		},
	}

	for _, beCase := range backends {
		t.Run(beCase.id, func(t *testing.T) {
			t.Parallel()
			var sawReferer, sawORTitle, sawLegacyTitle bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, ok := r.Header["Http-Referer"]; ok || r.Header.Get("HTTP-Referer") != "" {
					sawReferer = true
				}
				if _, ok := r.Header["X-Openrouter-Title"]; ok || r.Header.Get("X-OpenRouter-Title") != "" {
					sawORTitle = true
				}
				if _, ok := r.Header["X-Title"]; ok || r.Header.Get("X-Title") != "" {
					sawLegacyTitle = true
				}
				beCase.handler.ServeHTTP(w, r)
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
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(beCase.yamlBase(srv.URL)), &node); err != nil {
				t.Fatal(err)
			}
			be, err := reg.BuildBackend(beCase.id, node, srv.Client(), pluginreg.BackendFactoryDeps{Identity: g})
			if err != nil {
				t.Fatal(err)
			}
			beCase.open(t, be.Open)
			if sawReferer {
				t.Fatal("non-OpenRouter adapter must not emit HTTP-Referer")
			}
			if sawORTitle {
				t.Fatal("non-OpenRouter adapter must not emit X-OpenRouter-Title")
			}
			if sawLegacyTitle {
				t.Fatal("non-OpenRouter adapter must not emit X-Title")
			}
		})
	}
}
