package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew(t *testing.T) {
	t.Parallel()

	// 1. Test basic structural completion
	cfg := Config{
		BaseURL: "http://localhost:11434",
	}

	be := New(cfg)

	if be.Open == nil {
		t.Fatal("expected Open to be populated")
	}

	if be.ResolveCaps == nil {
		t.Fatal("expected ResolveCaps to be populated")
	}

	if be.ResolveTransportCaps == nil {
		t.Fatal("expected ResolveTransportCaps to be populated")
	}
}

func TestNewCloud(t *testing.T) {
	t.Parallel()

	cfg := Config{
		BaseURL: "https://api.ollama.com",
	}

	be := NewCloud(cfg)

	if be.Open == nil {
		t.Fatal("expected Open to be populated")
	}

	if be.ResolveCaps == nil {
		t.Fatal("expected ResolveCaps to be populated")
	}

	if be.ResolveTransportCaps == nil {
		t.Fatal("expected ResolveTransportCaps to be populated")
	}
}

func TestNew_VersionDetectionAndResponses(t *testing.T) {
	t.Parallel()

	// 1. Setup server for older version without responses support
	srvOld := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			_, _ = w.Write([]byte(`{"version": "0.1.0"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srvOld.Close)

	cfgOld := Config{
		BaseURL:    srvOld.URL,
		HTTPClient: srvOld.Client(),
	}

	beOld := New(cfgOld)

	capsOld := beOld.ResolveTransportCaps(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if capsOld.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected old version to not support responses API")
	}

	// 2. Setup server for new version with responses support
	srvNew := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			_, _ = w.Write([]byte(`{"version": "0.13.3"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srvNew.Close)

	cfgNew := Config{
		BaseURL:    srvNew.URL,
		HTTPClient: srvNew.Client(),
	}

	beNew := New(cfgNew)
	capsNew := beNew.ResolveTransportCaps(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if !capsNew.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected new version to support responses API")
	}

	// 3. Test explicit failure when calling responses API on old version
	callOld := lipapi.Call{
		Extensions: map[string]json.RawMessage{
			"openairesponses.model": json.RawMessage(`"some-model"`),
		},
	}
	_, err := beOld.Open(context.Background(), callOld, routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected error when trying to use responses API on old version")
	}
	if !strings.Contains(err.Error(), "responses API is not available") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// 4. Test explicit config to enable/disable responses
	cfgExplicitEnable := Config{
		BaseURL:      "http://localhost:11434",
		ResponsesAPI: "enabled",
	}
	beExplicitEnable := New(cfgExplicitEnable)
	capsExplicitEnable := beExplicitEnable.ResolveTransportCaps(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if !capsExplicitEnable.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected explicitly enabled responses to be supported")
	}

	cfgExplicitDisable := Config{
		BaseURL:      "http://localhost:11434",
		ResponsesAPI: "disabled",
	}
	beExplicitDisable := New(cfgExplicitDisable)
	capsExplicitDisable := beExplicitDisable.ResolveTransportCaps(context.Background(), lipapi.Call{}, routing.AttemptCandidate{})
	if capsExplicitDisable.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("expected explicitly disabled responses to not be supported")
	}
}

func TestFetchVersion_OOM_Regression(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		chunk := make([]byte, 1024*1024)
		for i := 0; i < 50; i++ {
			w.Write(chunk)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := fetchVersion(ctx, srv.Client(), srv.URL)
	if err == nil {
		t.Fatalf("expected error due to large payload or invalid json, got none")
	}
}
