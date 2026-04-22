package stdhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestMountBundledFrontends_geminiDoesNotRegisterRoot(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	var healthCalled bool
	mux.HandleFunc("/healthz", func(http.ResponseWriter, *http.Request) {
		healthCalled = true
	})
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", nil)
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg); err != nil {
		t.Fatal(err)
	}
	plugins := []config.PluginConfig{
		{ID: gemini.ID, Enabled: true},
	}
	if err := MountBundledFrontends(mux, ex, "stub:gemini-2.0-flash", plugins, 0, reg); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if !healthCalled {
		t.Fatal("expected /healthz to hit explicit handler, not Gemini")
	}
}

func TestMountBundledFrontends_explicitRegistryMissingFrontend(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBackendsOn(reg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", nil)
	err := MountBundledFrontends(mux, ex, "stub:x", []config.PluginConfig{{ID: "openai-responses", Enabled: true}}, 0, reg)
	if err == nil {
		t.Fatal("expected error when registry lacks frontend factories")
	}
	if !strings.Contains(err.Error(), `unknown frontend plugin "openai-responses"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
