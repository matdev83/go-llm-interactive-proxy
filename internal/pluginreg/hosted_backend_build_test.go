package pluginreg

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/huggingface"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/nvidia"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestBuildBackend_openAIResponses_multiKeyYAML_oneInstance(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{OpenAI: []string{"env-should-not-apply"}}); err != nil {
		t.Fatal(err)
	}
	raw := `api_key: first
api_keys:
  - second
  - third
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(openairesponses.ID, node, nil, BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Open == nil {
		t.Fatal("expected backend Open")
	}
}

func TestBuildBackend_nvidia_envDefaultsWhenYAMLHasNoKeys(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{Nvidia: []string{"nvapi-a", "nvapi-b"}}); err != nil {
		t.Fatal(err)
	}
	raw := `base_url: https://integrate.api.nvidia.com/v1`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(nvidia.ID, node, nil, BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Open == nil {
		t.Fatal("expected backend Open")
	}
}

func TestBuildBackend_huggingface_envDefaultsWhenYAMLHasNoKeys(t *testing.T) {
	t.Parallel()

	auths := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-hf","object":"chat.completion","created":1715620000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)

	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{HuggingFace: []string{"hf-a", "hf-b"}}); err != nil {
		t.Fatal(err)
	}
	raw := "base_url: " + srv.URL + "/v1"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(huggingface.ID, node, srv.Client(), BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	es, err := b.Open(context.Background(), hostedBuildTestCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	if auth := <-auths; auth != "Bearer hf-a" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func hostedBuildTestCall() lipapi.Call {
	return lipapi.Call{
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}},
		}},
	}
}

func TestBuildBackend_openAIResponses_envDefaultsWhenYAMLHasNoKeys(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{OpenAI: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	raw := `base_url: https://api.openai.com/v1`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(openairesponses.ID, node, nil, BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Open == nil {
		t.Fatal("expected backend Open")
	}
}

func TestBuildBackend_openAIResponses_modelInventoryUsesUpstreamHTTPClient(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://model.test/v1/models" {
			t.Fatalf("url = %q", r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-4o-mini"}]}`)),
		}, nil
	})}
	raw := `base_url: http://model.test/v1
api_key: test-key
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	b, err := reg.BuildBackend(openairesponses.ID, node, client, BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := b.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "openai/gpt-4o-mini" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
