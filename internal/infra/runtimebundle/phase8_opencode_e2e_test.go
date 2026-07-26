//go:build windows

package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestPhase8_OpenCodeExternalViaBuildBootstrap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var goAuth, zenAuth string
	goEmu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goAuth = r.Header.Get("Authorization")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"go-emu-model"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"go-hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(goEmu.Close)
	zenEmu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zenAuth = r.Header.Get("Authorization")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"zen-emu-model"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp","object":"response","created_at":1,"status":"completed","model":"wire","output":[{"type":"message","id":"m","status":"completed","role":"assistant","content":[{"type":"output_text","text":"zen-hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(zenEmu.Close)

	pluginRoot := bpkit.StageOpenCode(t)
	cfgPath := writeOpenCodeDiscoveryConfig(t, pluginRoot, goEmu.URL, zenEmu.URL, true)
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: cfgPath,
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err != nil {
		t.Fatalf("BuildBootstrap: %v", err)
	}
	t.Cleanup(func() {
		if res.Built != nil {
			for i := len(res.Built.Closers) - 1; i >= 0; i-- {
				_ = res.Built.Closers[i]()
			}
		}
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(context.Background())
		}
	})
	if !res.Registry.HasBackend("opencode-go") || !res.Registry.HasBackend("opencode-zen") {
		t.Fatalf("discovered kinds missing: go=%v zen=%v", res.Registry.HasBackend("opencode-go"), res.Registry.HasBackend("opencode-zen"))
	}
	goBE, ok := res.Built.Executor.Backends["ocgo"]
	if !ok || goBE.Open == nil {
		t.Fatalf("backends=%v", keysOf(res.Built.Executor.Backends))
	}
	zenBE, ok := res.Built.Executor.Backends["oczen"]
	if !ok || zenBE.Open == nil {
		t.Fatalf("backends=%v", keysOf(res.Built.Executor.Backends))
	}

	goStream, err := goBE.Open(ctx, lipapi.Call{
		ID: "p8-go", Session: lipapi.SessionRef{ALegID: "a"},
		Invocation: lipapi.Invocation{
			Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming,
		},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "ocgo", Model: "opencode-go/go-emu-model"},
		Key:     "ocgo:opencode-go/go-emu-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !drainSawText(ctx, t, goStream, "go-hi") {
		t.Fatal("expected text from external opencode-go plugin")
	}
	if goAuth != "Bearer go-key" {
		t.Fatalf("go auth leaked/wrong: %q", goAuth)
	}

	zenStream, err := zenBE.Open(ctx, lipapi.Call{
		ID: "p8-zen", Session: lipapi.SessionRef{ALegID: "a2"},
		Invocation: lipapi.Invocation{
			Operation: lipapi.OperationOpenAIResponses, DeliveryMode: lipapi.DeliveryModeNonStreaming, TransportMode: lipapi.TransportModeNonStreaming,
		},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "oczen", Model: "opencode-zen/zen-emu-model"},
		Key:     "oczen:opencode-zen/zen-emu-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !drainSawText(ctx, t, zenStream, "zen-hi") {
		t.Fatal("expected text from external opencode-zen plugin")
	}
	if zenAuth != "Bearer zen-key" {
		t.Fatalf("zen auth leaked/wrong: %q", zenAuth)
	}
	if goAuth == zenAuth {
		t.Fatal("go and zen instances must not share credential material")
	}
}

func TestPhase8_OpenCodeConfiguredMissingFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	emptyPlugins := t.TempDir()
	cfgPath := writeOpenCodeDiscoveryConfig(t, emptyPlugins, "http://127.0.0.1:9", "http://127.0.0.1:9", true)
	_, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: cfgPath,
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		t.Fatal("expected configured-missing opencode to fail bootstrap")
	}
}

func drainSawText(ctx context.Context, t *testing.T, stream lipapi.ManagedEventStream, want string) bool {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var saw bool
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return saw
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, want) {
			saw = true
		}
	}
}

func writeOpenCodeDiscoveryConfig(t *testing.T, pluginRoot, goURL, zenURL string, enable bool) string {
	t.Helper()
	backends := `
    - kind: opencode-go
      id: ocgo
      enabled: false
      config: {}
    - kind: opencode-zen
      id: oczen
      enabled: false
      config: {}`
	if enable {
		backends = fmt.Sprintf(`
    - kind: opencode-go
      id: ocgo
      enabled: true
      config:
        base_url: %q
        api_key: go-key
        models:
          - id: go-emu-model
            endpoint: %q
            ai_sdk_package: "@ai-sdk/openai-compatible"
    - kind: opencode-zen
      id: oczen
      enabled: true
      config:
        base_url: %q
        api_key: zen-key
        models:
          - id: zen-emu-model
            endpoint: %q
            ai_sdk_package: "@ai-sdk/openai"`, goURL, goURL+"/v1/chat/completions", zenURL, zenURL+"/v1/responses")
	}
	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "ocgo:opencode-go/go-emu-model"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
plugins:
  backend_discovery:
    enabled: true
    paths:
      - %q
    development_mode: true
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}%s
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
`, filepath.ToSlash(pluginRoot), backends)
	path := filepath.Join(t.TempDir(), "opencode-discovery.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
