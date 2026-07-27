package nvidia_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/nvidia/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/conformance"
)

func TestDescribe_FactoryKind(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Factories[0].Kind != service.FactoryKind {
		t.Fatal("missing kind")
	}
}

func TestConfigure_DefaultsAndAPIKey(t *testing.T) {
	t.Parallel()
	cfg, err := service.ParseConfigYAML(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != service.DefaultURL {
		t.Fatalf("base=%s", cfg.BaseURL)
	}
	_, err = service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "1", ConfigYAML: []byte(""),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err=%v", err)
	}
}

func TestParity_PayloadRemapAndBoundedExtraBody(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var body []byte
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		RequireBearer: true,
		OnRequestBody: func(b []byte) {
			mu.Lock()
			body = append([]byte(nil), b...)
			mu.Unlock()
		},
	}))
	t.Cleanup(srv.Close)
	cfg, err := service.ParseConfigYAML([]byte("base_url: " + srv.URL + "/v1\napi_key: sk\n"))
	if err != nil {
		t.Fatal(err)
	}
	maxTok := 128
	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Options:  lipapi.GenerationOptions{MaxOutputTokens: &maxTok},
		Extensions: map[string]json.RawMessage{
			"nvidia.extra_body.safe_field": json.RawMessage(`"ok"`),
			"nvidia.extra_body.bad.nested": json.RawMessage(`"no"`),
			"openai.extra_body.other_safe": json.RawMessage(`1`),
		},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}
	cl := service.NewCompatClient(cfg, srv.Client(), service.ProviderHooks())
	es, err := cl.Open(context.Background(), call, "m", openaicompat.FlavorChat)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	for {
		_, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["stream_options"]; ok {
		t.Fatal("stream_options must be stripped")
	}
	if parsed["max_tokens"] != float64(128) {
		t.Fatalf("max_tokens=%v", parsed["max_tokens"])
	}
	if _, ok := parsed["max_completion_tokens"]; ok {
		t.Fatal("max_completion_tokens must be removed")
	}
	if parsed["safe_field"] != "ok" || parsed["other_safe"] != float64(1) {
		t.Fatalf("extra=%v", parsed)
	}
	if _, ok := parsed["bad.nested"]; ok {
		t.Fatal("unsafe field injected")
	}
}

func TestParity_InventoryError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{ForcedStatus: 503}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "1",
		ConfigYAML:    []byte("base_url: " + srv.URL + "/v1\napi_key: sk\n"),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.ListModels(context.Background(), 8)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParity_ConformanceAdvertised(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{RequireBearer: true}))
	t.Cleanup(srv.Close)
	rep := conformance.RunWith(context.Background(), service.New(), conformance.Options{
		ConfigYAML: []byte("base_url: " + srv.URL + "/v1\napi_key: test\n"), SampleModel: "emu-model", DisableUsageRequirement: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}
