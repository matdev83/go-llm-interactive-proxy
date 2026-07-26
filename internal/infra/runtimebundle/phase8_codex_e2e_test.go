//go:build windows

package runtimebundle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestPhase8_CodexHTTPExternalViaBuildBootstrap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	emu := refbackend.New(refbackend.Config{Token: "codex-key", OutputText: "codex-hi"})
	srv := httptest.NewServer(emu.Handler())
	t.Cleanup(srv.Close)

	pluginRoot := bpkit.StageCodex(t)
	fakeCLI := stageFakeCodexCLI(t)
	pidFile := fakeCLI + ".pid"
	childPIDFile := fakeCLI + ".child.pid"
	cfgPath := writeCodexDiscoveryConfig(t, pluginRoot, srv.URL, true, fakeCLI)
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
	if !res.Registry.HasBackend("openai-codex") || !res.Registry.HasBackend("openai-codex-app-server") {
		t.Fatalf("discovered kinds missing: http=%v appserver=%v",
			res.Registry.HasBackend("openai-codex"), res.Registry.HasBackend("openai-codex-app-server"))
	}
	httpBE, ok := res.Built.Executor.Backends["codex-http"]
	if !ok || httpBE.Open == nil {
		t.Fatalf("backends=%v", keysOf(res.Built.Executor.Backends))
	}
	appBE, ok := res.Built.Executor.Backends["codex-app"]
	if !ok || appBE.Open == nil {
		t.Fatalf("backends=%v", keysOf(res.Built.Executor.Backends))
	}

	stream, err := httpBE.Open(ctx, lipapi.Call{
		ID: "p8-codex", Session: lipapi.SessionRef{ALegID: "a"},
		Invocation: lipapi.Invocation{
			Operation: lipapi.OperationOpenAIResponses, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming,
		},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "codex-http", Model: "openai-codex/gpt-5.3-codex-spark"},
		Key:     "codex-http:openai-codex/gpt-5.3-codex-spark",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !drainSawText(ctx, t, stream, "codex-hi") {
		t.Fatal("expected text from external openai-codex plugin")
	}
	sawAuth := emu.LatestRequest().Authorization
	if sawAuth != "Bearer codex-key" {
		t.Fatalf("auth leaked/wrong: %q", sawAuth)
	}

	appStream, err := appBE.Open(ctx, lipapi.Call{
		ID: "p8-codex-app", Session: lipapi.SessionRef{ALegID: "a2"},
		Invocation: lipapi.Invocation{
			Operation: lipapi.OperationOpenAIResponses, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming,
		},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		Extensions: map[string]json.RawMessage{
			"project_dir": json.RawMessage(strconv.Quote(filepath.Dir(pidFile))),
		},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "codex-app", Model: "openai-codex-app-server/auto"},
		Key:     "codex-app:openai-codex-app-server/auto",
	})
	if err != nil {
		t.Fatalf("appserver Open: %v", err)
	}
	if !drainSawText(ctx, t, appStream, "appserver-hi") {
		t.Fatal("expected text from external openai-codex-app-server via fake CLI")
	}
	waitFile(t, pidFile, 8*time.Second)
	waitFile(t, childPIDFile, 8*time.Second)
	cliPID := readPIDFile(t, pidFile)
	childPID := readPIDFile(t, childPIDFile)
	_ = appStream.Cancel(ctx, lipapi.CancelCause{Kind: lipapi.CancelExplicit, Detail: "e2e"})
	_ = appStream.Close()
	waitGone(t, cliPID, 10*time.Second)
	waitGone(t, childPID, 10*time.Second)
}

func TestPhase8_CodexConfiguredMissingFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	emptyPlugins := t.TempDir()
	cfgPath := writeCodexDiscoveryConfig(t, emptyPlugins, "http://127.0.0.1:9", true, "")
	_, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: cfgPath,
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		t.Fatal("expected configured-missing codex to fail bootstrap")
	}
}

func stageFakeCodexCLI(t *testing.T) (exe string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "fake-codex-cli.exe")
	cmd := exec.Command("go", "build", "-o", exe, "./cmd/fake-codex-cli")
	cmd.Dir = filepath.Join(codexRepoRoot(t), "connectors", "codex")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake-codex-cli: %v\n%s", err, out)
	}
	return exe
}

func codexRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "connectors", "codex", "go.mod")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func writeCodexDiscoveryConfig(t *testing.T, pluginRoot, codexURL string, enable bool, fakeCLI string) string {
	t.Helper()
	ws := filepath.ToSlash(t.TempDir())
	backends := `
    - kind: openai-codex
      id: codex-http
      enabled: false
      config: {}
    - kind: openai-codex-app-server
      id: codex-app
      enabled: false
      config: {}`
	if enable {
		appCfg := fmt.Sprintf(`
        default_workspace: %q
        catalog_enabled: false
        stale_kill_delay_seconds: 0.05`, ws)
		if fakeCLI != "" {
			appCfg = fmt.Sprintf(`
        executable: %q
        default_workspace: %q
        catalog_enabled: false
        stale_kill_delay_seconds: 0.05`, filepath.ToSlash(fakeCLI), ws)
		}
		backends = fmt.Sprintf(`
    - kind: openai-codex
      id: codex-http
      enabled: true
      config:
        base_url: %q
        access_token: codex-key
        catalog_enabled: false
    - kind: openai-codex-app-server
      id: codex-app
      enabled: true
      config:%s`, codexURL, appCfg)
	}
	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "codex-http:openai-codex/gpt-5.3-codex-spark"
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
	path := filepath.Join(t.TempDir(), "codex-discovery.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitFile(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", path)
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func waitGone(t *testing.T, pid int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !processAliveWindows(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}

func processAliveWindows(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}
