package standardplugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestOpenAIStyleYAML_decodesSDKMaxRetries(t *testing.T) {
	t.Parallel()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("sdk_max_retries: 2\n"), &root); err != nil {
		t.Fatal(err)
	}
	var y openAIStyleYAML
	if err := config.DecodeYAMLNode(root, &y); err != nil {
		t.Fatal(err)
	}
	if y.SDKMaxRetries == nil || *y.SDKMaxRetries != 2 {
		t.Fatalf("sdk_max_retries: got %v", y.SDKMaxRetries)
	}
}

func TestSDKMaxRetriesOrDefault(t *testing.T) {
	t.Parallel()
	got := sdkMaxRetriesOrDefault(nil)
	if got == nil || *got != 0 {
		t.Fatalf("nil operator value must default to explicit 0, got %v", got)
	}
	explicit := 3
	if got := sdkMaxRetriesOrDefault(&explicit); got != &explicit || *got != 3 {
		t.Fatalf("operator override must pass through, got %v", got)
	}
}

// The standard distribution default must disable SDK-internal retries so all retry
// policy above the HTTP round trip lives in the credential-rotation loop and core.
// Against a forced 429 with a single credential, the openai-go SDK would retry twice
// more when SDKMaxRetries were left at the SDK default (~2); with the factory default
// of 0 exactly one upstream HTTP attempt is made.
func TestBackendOpenAIResponses_defaultDisablesSDKRetries(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate","type":"requests","code":"rate_limit_exceeded"}}`))
	}))
	t.Cleanup(srv.Close)

	var root yaml.Node
	if err := yaml.Unmarshal([]byte("base_url: "+srv.URL+"/v1\napi_key: sk-test\n"), &root); err != nil {
		t.Fatal(err)
	}
	be, err := backendOpenAIResponses(root, srv.Client(), UpstreamAPIKeys{}, identity.Config{})
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	if _, err := be.Open(context.Background(), call, cand); err == nil {
		t.Fatal("expected Open error from forced 429")
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("upstream HTTP attempts: %d want 1 (factory must default sdk_max_retries to 0)", n)
	}
}

func TestBackendAnthropic_defaultDisablesSDKRetries(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate"}}`))
	}))
	t.Cleanup(srv.Close)

	var root yaml.Node
	if err := yaml.Unmarshal([]byte("base_url: "+srv.URL+"\napi_key: sk-ant\n"), &root); err != nil {
		t.Fatal(err)
	}
	be, err := backendAnthropic(root, srv.Client(), UpstreamAPIKeys{}, identity.Config{})
	if err != nil {
		t.Fatal(err)
	}
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	if _, err := be.Open(context.Background(), call, cand); err == nil {
		t.Fatal("expected Open error from forced 429")
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("upstream HTTP attempts: %d want 1 (factory must default sdk_max_retries to 0)", n)
	}
}

func TestBackendOpenAILegacy_operatorOverrideHonored(t *testing.T) {
	t.Parallel()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte("base_url: http://127.0.0.1:9/v1\nsdk_max_retries: 4\n"), &root); err != nil {
		t.Fatal(err)
	}
	var y openAIHostedYAML
	if err := config.DecodeYAMLNode(root, &y); err != nil {
		t.Fatal(err)
	}
	if y.SDKMaxRetries == nil || *y.SDKMaxRetries != 4 {
		t.Fatalf("sdk_max_retries override must decode, got %v", y.SDKMaxRetries)
	}
}
