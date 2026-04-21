package stdhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
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
	plugins := []config.PluginConfig{
		{ID: gemini.ID, Enabled: true},
	}
	if err := MountBundledFrontends(mux, ex, "stub:gemini-2.0-flash", plugins, 0); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if !healthCalled {
		t.Fatal("expected /healthz to hit explicit handler, not Gemini")
	}
}
