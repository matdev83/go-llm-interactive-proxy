package stdhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	refanth "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/anthropicmessages"
	refchat "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaichat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/reasoninge2e"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

const (
	rpE2EModel     = "moonshot-v1-8k"
	rpE2EAnthModel = "claude-3-5-haiku-20241022"
	rpE2EFakeKey   = "sk-test-reasoning-e2e"
	rpE2EAnthKey   = "sk-ant-test-reasoning-e2e"
)

type rpHTTPStack struct {
	proxyURL string
	proxy    *httptest.Server
	emulator *httptest.Server
	ledger   *refchat.OracleLedger
	oracleCh chan []byte // optional diagnostics for negative-control body inspection
	cleanup  func()
}

func startReasoningPreservationChatStack(t *testing.T, action string, turns []refchat.ScriptedTurn, validators ...refchat.RequestValidator) *rpHTTPStack {
	t.Helper()
	stack, err := startReasoningPreservationChatStackErr(action, turns, validators...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stack.cleanup)
	return stack
}

func startReasoningPreservationChatStackErr(action string, turns []refchat.ScriptedTurn, validators ...refchat.RequestValidator) (*rpHTTPStack, error) {
	var ledger *refchat.OracleLedger
	if len(validators) > 0 {
		ledger = refchat.NewOracleLedger(validators...)
	}
	oracleCh := make(chan []byte, len(turns)+4)
	emu := httptest.NewServer(refchat.NewHandler(refchat.Config{
		AllowMissingBearer: false,
		OnRequestBody: func(body []byte) {
			if ledger != nil {
				ledger.Observe(body)
			}
			cloned := append([]byte(nil), body...)
			select {
			case oracleCh <- cloned:
			default:
			}
		},
		Responder: refchat.ScriptedResponder(turns),
	}))
	cfgPath, err := writeReasoningPreservationConfigErr(action, "openai-legacy", emu.URL+"/v1", rpE2EFakeKey, rpE2EModel)
	if err != nil {
		emu.Close()
		return nil, err
	}
	proxy, proxyCleanup, err := startRPBootstrapProxyErr(cfgPath)
	if err != nil {
		emu.Close()
		return nil, err
	}
	stack := &rpHTTPStack{
		proxyURL: proxy.URL,
		proxy:    proxy,
		emulator: emu,
		ledger:   ledger,
		oracleCh: oracleCh,
		cleanup: func() {
			proxy.Close()
			emu.Close()
			proxyCleanup()
			_ = os.RemoveAll(filepath.Dir(cfgPath))
		},
	}
	return stack, nil
}

func startReasoningPreservationAnthropicStack(t *testing.T, action string, turns []refanth.ThinkingTurn) *rpHTTPStack {
	t.Helper()
	oracleCh := make(chan []byte, len(turns)+4)
	emu := httptest.NewServer(refanth.NewHandler(refanth.Config{
		AllowMissingAPIKey: false,
		OnRequestBody: func(body []byte) {
			oracleCh <- append([]byte(nil), body...)
		},
		Responder: refanth.ScriptedThinkingResponder(turns),
	}))
	cfgPath := writeReasoningPreservationConfig(t, action, "anthropic", emu.URL, rpE2EAnthKey, rpE2EAnthModel)
	proxy := startRPBootstrapProxy(t, cfgPath)
	stack := &rpHTTPStack{
		proxyURL: proxy.URL,
		proxy:    proxy,
		emulator: emu,
		oracleCh: oracleCh,
		cleanup: func() {
			proxy.Close()
			emu.Close()
		},
	}
	t.Cleanup(stack.cleanup)
	return stack
}

func startRPBootstrapProxy(t *testing.T, cfgPath string) *httptest.Server {
	t.Helper()
	srv, cleanup, err := startRPBootstrapProxyErr(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return srv
}

func startRPBootstrapProxyErr(cfgPath string) (*httptest.Server, func(), error) {
	ctx := context.Background()
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: cfgPath,
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("BuildBootstrap: %w", err)
	}
	h, handlerCleanup, err := stdhttp.NewStandardHandler(ctx, res.Config, res.App, res.Logger, res.Built)
	if err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(shutdownCtx)
		}
		return nil, nil, fmt.Errorf("NewStandardHandler: %w", err)
	}
	srv := httptest.NewServer(h)
	cleanup := func() {
		srv.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		handlerCleanup(shutdownCtx)
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(shutdownCtx)
		}
	}
	return srv, cleanup, nil
}

func writeReasoningPreservationConfig(t *testing.T, action, backendKind, baseURL, apiKey, model string) string {
	t.Helper()
	path, err := writeReasoningPreservationConfigTo(t.TempDir(), action, backendKind, baseURL, apiKey, model)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeReasoningPreservationConfigErr(action, backendKind, baseURL, apiKey, model string) (string, error) {
	dir, err := os.MkdirTemp("", "rp-e2e-*")
	if err != nil {
		return "", err
	}
	path, err := writeReasoningPreservationConfigTo(dir, action, backendKind, baseURL, apiKey, model)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

func writeReasoningPreservationConfigTo(dir, action, backendKind, baseURL, apiKey, model string) (string, error) {
	enabled := "true"
	featureBlock := ""
	switch action {
	case "disabled":
		enabled = "false"
		featureBlock = `
        action: observe
        use_builtin_catalog: false
        rules:
          - id: e2e-rule
            backend: ` + backendKind + `
            enabled: true
        on_ambiguous: log_skip
        on_unrepresentable: reject
        on_state_error: reject
        state:
          ttl: 1h
          max_turns_per_session: 64
          max_reasoning_bytes_per_turn: 65536
          max_session_bytes: 1048576
`
	case "observe", "restore":
		featureBlock = `
        action: ` + action + `
        use_builtin_catalog: false
        rules:
          - id: e2e-rule
            backend: ` + backendKind + `
            enabled: true
        on_ambiguous: log_skip
        on_unrepresentable: reject
        on_state_error: reject
        state:
          ttl: 1h
          max_turns_per_session: 64
          max_reasoning_bytes_per_turn: 65536
          max_session_bytes: 1048576
`
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
	route := backendKind + ":" + model
	yaml := fmt.Sprintf(`
server:
  address: "127.0.0.1:0"
routing:
  max_attempts: 1
  default_route: %q
continuity:
  in_memory: true
  store: memory
secure_session:
  store: memory
  non_durable_warning: silent
logging:
  level: error
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
plugins:
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
      enabled: %t
      config:
        base_url: %q
        api_key: %q
    - id: anthropic
      enabled: %t
      config:
        base_url: %q
        api_key: %q
    - id: gemini
      enabled: false
      config: {}
    - id: openai-codex
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: acp
      enabled: false
      config: {}
    - id: openrouter
      enabled: false
      config: {}
    - id: nvidia
      enabled: false
      config: {}
    - id: opencode-go
      enabled: false
      config: {}
    - id: opencode-zen
      enabled: false
      config: {}
    - id: ollama
      enabled: false
      config: {}
    - id: ollama-cloud
      enabled: false
      config: {}
    - id: llamacpp
      enabled: false
      config: {}
    - id: lmstudio
      enabled: false
      config: {}
    - id: vllm
      enabled: false
      config: {}
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
    - id: reasoning-output-preservation
      enabled: %s
      config:%s
`, route,
		backendKind == "openai-legacy", baseURL, apiKey,
		backendKind == "anthropic", baseURL, apiKey,
		enabled, featureBlock)
	path := filepath.Join(dir, "rp-e2e.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func chatReasoning(text string) []reasoninge2e.ReasoningBlock {
	return []reasoninge2e.ReasoningBlock{{
		Dialect: reasoninge2e.DialectOpenAIChatTextV1,
		Text:    text,
	}}
}

func planTurnIDs(plan reasoninge2e.Plan) []string {
	turns := plan.Turns()
	ids := make([]string, len(turns))
	for i := range turns {
		ids[i] = turns[i].ID
	}
	return ids
}

// chatRestoreValidators precomputes per-request oracle checks against plan ExpectedBackend.
// Request i is expected to carry assistant history prefix length i.
// When requireStream is non-nil, each backend body must decode with that stream flag.
func chatRestoreValidators(plan reasoninge2e.Plan, requestCount int, requireStream *bool) []refchat.RequestValidator {
	ids := planTurnIDs(plan)
	out := make([]refchat.RequestValidator, requestCount)
	for i := range requestCount {
		histLen := i
		out[i] = func(body []byte) error {
			if requireStream != nil {
				var probe struct {
					Stream bool `json:"stream"`
				}
				if err := json.Unmarshal(body, &probe); err != nil {
					return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: backend_body_parse", plan.Seed)
				}
				if probe.Stream != *requireStream {
					return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: stream_flag", plan.Seed)
				}
			}
			prefixIDs := ids
			if histLen < len(ids) {
				prefixIDs = ids[:histLen]
			}
			obs, err := reasoninge2e.ObserveChatBackendRequest(body, prefixIDs)
			if err != nil {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: backend_body_parse", plan.Seed)
			}
			return reasoninge2e.CheckPrefix(plan, obs)
		}
	}
	return out
}

// chatRestoreValidatorsPerTurnStream is like chatRestoreValidators and asserts the
// backend stream flag is true on every request (streaming-primary distribution).
// Client stream vs non-stream framing is asserted by the HTTP driver against the plan.
func chatRestoreValidatorsPerTurnStream(plan reasoninge2e.Plan, requestCount int) []refchat.RequestValidator {
	ids := planTurnIDs(plan)
	out := make([]refchat.RequestValidator, requestCount)
	for i := range requestCount {
		histLen := i
		out[i] = func(body []byte) error {
			var probe struct {
				Stream bool `json:"stream"`
			}
			if err := json.Unmarshal(body, &probe); err != nil {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: backend_body_parse", plan.Seed)
			}
			if !probe.Stream {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: stream_flag", plan.Seed)
			}
			prefixIDs := ids
			if histLen < len(ids) {
				prefixIDs = ids[:histLen]
			}
			obs, err := reasoninge2e.ObserveChatBackendRequest(body, prefixIDs)
			if err != nil {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: backend_body_parse", plan.Seed)
			}
			return reasoninge2e.CheckPrefix(plan, obs)
		}
	}
	return out
}

// chatNoRestoreValidators asserts history turns carry no restored reasoning (disabled/observe).
func chatNoRestoreValidators(plan reasoninge2e.Plan, requestCount int) []refchat.RequestValidator {
	ids := planTurnIDs(plan)
	out := make([]refchat.RequestValidator, requestCount)
	for i := range requestCount {
		histLen := i
		out[i] = func(body []byte) error {
			prefixIDs := ids
			if histLen < len(ids) {
				prefixIDs = ids[:histLen]
			}
			obs, err := reasoninge2e.ObserveChatBackendRequest(body, prefixIDs)
			if err != nil {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: backend_body_parse", plan.Seed)
			}
			if len(obs.AssistantTurns) != histLen {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: turn_count got=%d want=%d",
					plan.Seed, len(obs.AssistantTurns), histLen)
			}
			for _, turn := range obs.AssistantTurns {
				if len(turn.Reasoning) != 0 {
					return fmt.Errorf("reasoninge2e oracle: seed=%d turn=%s structural mismatch: unexpected_reasoning_insertion",
						plan.Seed, turn.TurnID)
				}
			}
			return nil
		}
	}
	return out
}

func drainOracleBodies(t *testing.T, ch <-chan []byte, n int) [][]byte {
	t.Helper()
	out := make([][]byte, 0, n)
	deadline := time.After(10 * time.Second)
	for len(out) < n {
		select {
		case b := <-ch:
			out = append(out, b)
		case <-deadline:
			t.Fatalf("oracle bodies: got %d want %d", len(out), n)
		}
	}
	return out
}

func requireLedgerOK(t *testing.T, stack *rpHTTPStack) {
	t.Helper()
	if stack.ledger == nil {
		return
	}
	if err := stack.ledger.Err(); err != nil {
		t.Fatalf("oracle ledger: %v", err)
	}
}

func requireHTTPOK(t *testing.T, status int, body []byte) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("proxy status=%d body_bytes=%d", status, len(body))
	}
}

func requireStreamWire(t *testing.T, resp reasoninge2e.ChatTurnResponse) {
	t.Helper()
	if !strings.Contains(strings.ToLower(resp.ContentType), "text/event-stream") {
		t.Fatalf("stream Content-Type structural mismatch: want text/event-stream")
	}
	if !bytes.Contains(resp.RawBody, []byte("[DONE]")) {
		t.Fatal("stream framing structural mismatch: missing SSE [DONE]")
	}
}

type logWriter struct{ b *strings.Builder }

func (w *logWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

func truncateRunLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
