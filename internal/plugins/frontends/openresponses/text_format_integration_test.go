package openresponses_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	front "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

func textFormatBodies() map[string]string {
	return map[string]string{
		"omitted":     `{"model":"stub:gpt-4o-mini","input":"ping","store":false}`,
		"null":        `{"model":"stub:gpt-4o-mini","input":"ping","text":null,"store":false}`,
		"empty":       `{"model":"stub:gpt-4o-mini","input":"ping","text":{},"store":false}`,
		"format null": `{"model":"stub:gpt-4o-mini","input":"ping","text":{"format":null},"store":false}`,
		"text":        `{"model":"stub:gpt-4o-mini","input":"ping","text":{"format":{"type":"text"}},"store":false}`,
		"json_object": `{"model":"stub:gpt-4o-mini","input":"ping","text":{"format":{"type":"json_object"}},"store":false}`,
	}
}

func invalidTextFormatBodies() map[string]string {
	return map[string]string{
		"verbosity":      `{"model":"stub:gpt-4o-mini","input":"ping","text":{"verbosity":"low"}}`,
		"unknown field":  `{"model":"stub:gpt-4o-mini","input":"ping","text":{"format":{"type":"text"},"unknown":true}}`,
		"unknown type":   `{"model":"stub:gpt-4o-mini","input":"ping","text":{"format":{"type":"xml"}}}`,
		"invalid format": `{"model":"stub:gpt-4o-mini","input":"ping","text":{"format":"text"}}`,
	}
}

func capturedMIME(t *testing.T, capture *sync.Map) string {
	t.Helper()
	value, ok := capture.Load("last")
	if !ok {
		t.Fatal("executor was not called")
	}
	return value.(lipapi.Call).Options.ResponseMIMEType
}

func mountTextFormatHTTP(t *testing.T, ex *runtime.Executor) *httptest.Server {
	t.Helper()
	ex.DefaultBackend = "stub"
	var cfg yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := front.Mount(mux, lipsdk.FrontendMountOptions{
		AllowUnauthenticated: true,
		PluginCfg:            cfg,
		Exec:                 ex,
		DefaultRoute:         "stub:stub:gpt-4o-mini",
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPTextFormatReachesExecutorOnce(t *testing.T) {
	for name, body := range textFormatBodies() {
		want := ""
		if name == "text" {
			want = "text/plain"
		} else if name == "json_object" {
			want = "application/json"
		}
		t.Run(name, func(t *testing.T) {
			var capture sync.Map
			inner := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityOrderedItems, lipapi.CapabilityStructuredOutputs), "ok", &capture)
			srv := mountTextFormatHTTP(t, inner)
			resp, err := testkit.LocalTestServerHTTPClient().Post(srv.URL+"/openresponses/v1/responses", "application/json", bytes.NewReader([]byte(body)))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				data, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d body = %s", resp.StatusCode, data)
			}
			if got := capturedMIME(t, &capture); got != want {
				t.Fatalf("ResponseMIMEType = %q, want %q", got, want)
			}
		})
	}
}

func TestHTTPInvalidTextFormatDoesNotExecute(t *testing.T) {
	for name, body := range invalidTextFormatBodies() {
		t.Run(name, func(t *testing.T) {
			var capture sync.Map
			inner := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityOrderedItems, lipapi.CapabilityStructuredOutputs), "ok", &capture)
			srv := mountTextFormatHTTP(t, inner)
			resp, err := testkit.LocalTestServerHTTPClient().Post(srv.URL+"/openresponses/v1/responses", "application/json", bytes.NewReader([]byte(body)))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if _, ok := capture.Load("last"); ok {
				t.Fatal("executor was called for invalid text format")
			}
		})
	}
}

func TestWebSocketTextFormatReachesExecutorOnce(t *testing.T) {
	for name, body := range textFormatBodies() {
		if name != "text" && name != "json_object" {
			continue
		}
		want := map[string]string{"text": "text/plain", "json_object": "application/json"}[name]
		t.Run(name, func(t *testing.T) {
			var capture sync.Map
			inner := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityOrderedItems, lipapi.CapabilityStructuredOutputs), "ok", &capture)
			srv := mountTextFormatHTTP(t, inner)
			u := "ws" + srv.URL[len("http"):] + "/openresponses/v1/responses"
			conn, _, err := websocket.DefaultDialer.Dial(u, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create",`+body[1:])); err != nil {
				t.Fatal(err)
			}
			wsReadUntilTerminal(t, conn, 5*time.Second)
			if got := capturedMIME(t, &capture); got != want {
				t.Fatalf("ResponseMIMEType = %q, want %q", got, want)
			}
		})
	}
}

func TestWebSocketInvalidTextFormatDoesNotExecute(t *testing.T) {
	for name, body := range invalidTextFormatBodies() {
		t.Run(name, func(t *testing.T) {
			var capture sync.Map
			inner := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityOrderedItems, lipapi.CapabilityStructuredOutputs), "ok", &capture)
			srv := mountTextFormatHTTP(t, inner)
			u := "ws" + srv.URL[len("http"):] + "/openresponses/v1/responses"
			conn, _, err := websocket.DefaultDialer.Dial(u, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create",`+body[1:])); err != nil {
				t.Fatal(err)
			}
			frames := wsReadUntilTerminal(t, conn, 5*time.Second)
			if len(frames) == 0 || frames[len(frames)-1].data["type"] != "error" {
				t.Fatal("invalid text format did not produce websocket error")
			}
			if _, ok := capture.Load("last"); ok {
				t.Fatal("executor was called for invalid text format")
			}
		})
	}
}
