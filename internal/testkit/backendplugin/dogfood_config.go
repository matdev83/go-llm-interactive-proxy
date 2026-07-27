package backendplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// WriteDogfoodLocalStubConfig stages connectors/localstub and writes a dogfood
// YAML that discovers it. Use this instead of config/examples/dogfood-local-stub.yaml
// in automated tests (example file expects a prior make package-full layout).
func WriteDogfoodLocalStubConfig(tb testing.TB) string {
	tb.Helper()
	pluginRoot := StageLocalStub(tb)
	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:18080"
routing:
  max_attempts: 3
  default_route: "dogfood-local:stub-default"
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
    development_mode: true
    paths:
      - %q
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
    - kind: local-stub
      id: dogfood-local
      enabled: true
      config:
        text: "[dogfood] local stub"
        input_tokens: 3
        output_tokens: 7
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
    - id: tool-call-repair
      enabled: true
      config: {}
`, filepath.ToSlash(pluginRoot))
	path := filepath.Join(tb.TempDir(), "dogfood-local-stub.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		tb.Fatal(err)
	}
	return path
}
