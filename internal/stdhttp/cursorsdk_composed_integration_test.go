package stdhttp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

const cursorCLIACPKind = "cursorcliacp"

func TestCursorSDK_composedOpenAIResponses_explicitRoutesAndProvenance(t *testing.T) {
	acp.ResetLookPathCache()
	t.Cleanup(acp.ResetLookPathCache)

	exe := fakebridge.BuildExe(t)
	ws := t.TempDir()
	reg := testRegistryWithStdBundle(t)
	if err := standardplugins.InstallBundleOn(reg, standardplugins.Bundle{Backends: []standardplugins.BackendRegistration{
		standardplugins.ExperimentalCursorSDKRegistration(standardplugins.UpstreamAPIKeys{}),
		{
			ID: cursorCLIACPKind,
			Factory: func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
				return execbackend.Backend{
					Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					BackendPrefixes: []string{cursorCLIACPKind},
					ModelInventory: modelinventory.StaticProvider{
						Models: []modelinventory.Model{{
							CanonicalID: "cursor/gpt-5.3-codex",
							NativeID:    "gpt-5.3-codex",
						}},
					},
					Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						return lipapi.NewFixedEventStream([]lipapi.Event{
							{Kind: lipapi.EventResponseStarted},
							{Kind: lipapi.EventMessageStarted},
							{Kind: lipapi.EventTextDelta, Delta: "acp-stub"},
							{Kind: lipapi.EventResponseFinished},
						}), nil
					},
				}, nil
			},
			Profile: pluginreg.BackendSecurityProfile{
				CredentialMode: pluginreg.CredentialNone,
				AccessScope:    pluginreg.BackendAccessLocalOnly,
			},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	sdkYAML := fmt.Sprintf(`api_key: composed-test-key
bridge_executable: %q
default_workspace: %q
`, exe, ws)
	var sdkNode yaml.Node
	if err := yaml.Unmarshal([]byte(sdkYAML), &sdkNode); err != nil {
		t.Fatal(err)
	}

	var acpNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}\n"), &acpNode); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{Kind: cursorsdk.ID, ID: "cursor-sdk", Enabled: true, Config: sdkNode},
				{Kind: cursorCLIACPKind, ID: "cursor-acp", Enabled: true, Config: acpNode},
			},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	_, cand := compileTestCandidate(t, cfg, reg)

	if len(cand.Executor().Backends) != 2 {
		t.Fatalf("backends=%d want 2", len(cand.Executor().Backends))
	}
	refs, ok := cand.ModelRegistry().Lookup("cursor/gpt-5.3-codex")
	if !ok {
		t.Fatal("Lookup(cursor/gpt-5.3-codex) missing")
	}
	if len(refs) != 2 {
		t.Fatalf("provenance rows=%d want 2 (%+v)", len(refs), refs)
	}
	byKind := map[string]string{}
	for _, r := range refs {
		byKind[r.Kind] = r.BackendID
	}
	if byKind[cursorsdk.ID] != "cursor-sdk" || byKind[cursorCLIACPKind] != "cursor-acp" {
		t.Fatalf("provenance mismatch: %#v", byKind)
	}

	var sdkOpens, acpOpens atomic.Int32
	sdkBE := cand.Executor().Backends["cursor-sdk"]
	origSDK := sdkBE.Open
	sdkBE.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		sdkOpens.Add(1)
		return origSDK(ctx, call, cand)
	}
	cand.Executor().Backends["cursor-sdk"] = sdkBE

	cand.Executor().Backends["cursor-acp"] = execbackend.Backend{
		Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		BackendPrefixes: []string{cursorCLIACPKind},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			acpOpens.Add(1)
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "acp-stub"},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		},
	}

	serve := func(t *testing.T, route string) *httptest.ResponseRecorder {
		t.Helper()
		mux := http.NewServeMux()
		if err := MountBundledFrontends(MountBundledFrontendsInput{
			Mux: mux,
			Frontends: HTTPFrontendInput{
				Executor:             cand.Executor(),
				DefaultRouteSelector: route,
				Plugins:              []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
				RoutePrefixes:        cand.RoutePrefixes(),
				Registry:             reg,
			},
		}); err != nil {
			t.Fatalf("MountBundledFrontends: %v", err)
		}
		body := []byte(`{"model":"gpt-5.3-codex","stream":false,"input":[{"role":"user","content":"ping"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-test")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}

	t.Run("sdk_route_opens_only_sdk", func(t *testing.T) {
		sdkOpens.Store(0)
		acpOpens.Store(0)
		rr := serve(t, "cursor-sdk:gpt-5.3-codex")
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"text":"hello"`) {
			t.Fatalf("response missing fake-bridge text: %s", rr.Body.String())
		}
		if sdkOpens.Load() < 1 {
			t.Fatal("expected SDK Open")
		}
		if acpOpens.Load() != 0 {
			t.Fatalf("ACP opens=%d want 0 on SDK route", acpOpens.Load())
		}
	})

	t.Run("acp_route_opens_only_acp_stub", func(t *testing.T) {
		sdkOpens.Store(0)
		acpOpens.Store(0)
		rr := serve(t, "cursor-acp:gpt-5.3-codex")
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"text":"acp-stub"`) {
			t.Fatalf("response missing ACP stub text: %s", rr.Body.String())
		}
		if acpOpens.Load() != 1 {
			t.Fatalf("ACP opens=%d want 1", acpOpens.Load())
		}
		if sdkOpens.Load() != 0 {
			t.Fatalf("SDK opens=%d want 0 on ACP route", sdkOpens.Load())
		}
	})

	t.Run("model_only_ambiguous_rejected_no_connector_fallback", func(t *testing.T) {
		sdkOpens.Store(0)
		acpOpens.Store(0)
		cand.Executor().DefaultBackend = ""
		rr := serve(t, "gpt-5.3-codex")
		if rr.Code == http.StatusOK {
			t.Fatalf("model-only ambiguous route must fail, got OK: %s", rr.Body.String())
		}
		if sdkOpens.Load() != 0 || acpOpens.Load() != 0 {
			t.Fatalf("connector fallback opens sdk=%d acp=%d want 0/0", sdkOpens.Load(), acpOpens.Load())
		}
		if strings.TrimSpace(rr.Body.String()) == "" {
			t.Fatal("expected non-empty error response for unresolved model-only selector")
		}
	})
}
