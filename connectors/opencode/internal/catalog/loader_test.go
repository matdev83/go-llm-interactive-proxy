package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestLoadModelEntries_remoteSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.4"}]}`))
	}))
	t.Cleanup(srv.Close)

	entries, source, warnings, err := LoadModelEntries(context.Background(), ModelLoaderConfig{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Kind:       BackendZen,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source != modelinventory.SourceRemote {
		t.Fatalf("source = %q", source)
	}
	if len(entries) != 1 || entries[0].RawID != "gpt-5.4" {
		t.Fatalf("entries = %+v", entries)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestLoadModelEntries_openCodeGoRemoteListEnrichesFlavorMetadata(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"kimi-k2.7-code","object":"model","created":1782378346,"owned_by":"opencode"},{"id":"minimax-m3","object":"model","created":1782378346,"owned_by":"opencode"},{"id":"qwen3.7-plus","object":"model","created":1782378346,"owned_by":"opencode"}]}`))
	}))
	t.Cleanup(srv.Close)

	entries, _, _, err := LoadModelEntries(context.Background(), ModelLoaderConfig{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Kind:       BackendGo,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	flavors := map[string]Flavor{}
	for _, entry := range entries {
		flavors[entry.RawID] = InferFlavor(entry)
	}
	if flavors["kimi-k2.7-code"] != FlavorOpenAIChat {
		t.Fatalf("kimi flavor = %q", flavors["kimi-k2.7-code"])
	}
	if flavors["minimax-m3"] != FlavorAnthropicMessages {
		t.Fatalf("minimax flavor = %q", flavors["minimax-m3"])
	}
	if flavors["qwen3.7-plus"] != FlavorAnthropicMessages {
		t.Fatalf("qwen flavor = %q", flavors["qwen3.7-plus"])
	}
}

func TestLoadModelEntries_remoteFailureUsesFallback(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	fallback := []ModelEntry{{RawID: "kimi-k2.7-code"}}
	entries, source, warnings, err := LoadModelEntries(context.Background(), ModelLoaderConfig{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
	}, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if source != modelinventory.SourceStaticInline {
		t.Fatalf("source = %q", source)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "remote model discovery failed") {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestLoadModelEntries_noFallbackOnRemoteFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, _, _, err := LoadModelEntries(context.Background(), ModelLoaderConfig{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
	}, nil)
	if err == nil {
		t.Fatal("expected error without fallback")
	}
}
