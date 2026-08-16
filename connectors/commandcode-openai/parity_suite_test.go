package commandcodeopenai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/commandcode-openai/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/conformance"
)

func TestDescribe_FactoryKind(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f := d.Factories[0]
	if f.Kind != service.FactoryKind || f.RoutePrefixes[0] != service.FactoryKind || f.SupportsFinalizeBilling {
		t.Fatalf("%+v", f)
	}
}

func TestConfigure_RequiresAPIKey(t *testing.T) {
	t.Setenv("COMMANDCODE_API_KEY", "")
	_, err := service.New().Configure(context.Background(), mustCfg(t, "base_url: http://127.0.0.1:9\n"))
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err=%v", err)
	}
}

func TestParity_ExtraBodyPropagation(t *testing.T) {
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
	cl := service.NewCompatClient(cfg, srv.Client(), service.ProviderHooks())
	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Extensions: map[string]json.RawMessage{
			"commandcode.extra_body.custom_param": json.RawMessage(`"val"`),
			"openai.extra_body.foo":               json.RawMessage(`"bar"`),
		},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}
	es, err := cl.Open(context.Background(), call, "Qwen/Qwen3.7-Flash", openaicompat.FlavorChat)
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
	if !strings.Contains(string(body), `"custom_param":"val"`) || !strings.Contains(string(body), `"foo":"bar"`) {
		t.Fatalf("body=%s", body)
	}
}

func TestParity_ResponsesPathAndUsage(t *testing.T) {
	t.Parallel()
	var path string
	emu := openaicompat.NewEmulator(openaicompat.EmulatorConfig{RequireBearer: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		emu.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	cfg, err := service.ParseConfigYAML([]byte("base_url: " + srv.URL + "/v1\napi_key: sk\n"))
	if err != nil {
		t.Fatal(err)
	}
	cl := service.NewCompatClient(cfg, srv.Client(), service.ProviderHooks())
	call := lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}
	es, err := cl.Open(context.Background(), call, "Qwen/Qwen3.7-Flash", openaicompat.FlavorResponses)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	var sawUsage bool
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == lipapi.EventUsageDelta {
			sawUsage = true
		}
	}
	if !strings.HasSuffix(path, "/responses") || !sawUsage {
		t.Fatalf("path=%q usage=%v", path, sawUsage)
	}
}

func TestParity_InventoryAndProviderErrors(t *testing.T) {
	t.Parallel()
	okSrv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		RequireBearer: true, ModelsJSON: `{"data":[{"id":"Qwen/Qwen3.7-Flash"}]}`,
	}))
	t.Cleanup(okSrv.Close)
	inst, err := service.New().Configure(context.Background(), mustCfg(t, "base_url: "+okSrv.URL+"/v1\napi_key: sk\n"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := inst.ListModels(context.Background(), 10)
	if err != nil || len(resp.Models) != 1 || resp.Models[0].CanonicalModelID != "commandcode-openai/Qwen/Qwen3.7-Flash" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}

	// 401 unauthorized
	bad401 := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{ForcedStatus: 401, ForcedBody: `{"error":{"message":"bad key","code":"invalid_api_key"}}`}))
	t.Cleanup(bad401.Close)
	inst401, err := service.New().Configure(context.Background(), mustCfg(t, "base_url: "+bad401.URL+"/v1\napi_key: sk\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst401.ListModels(context.Background(), 10)
	if err == nil {
		t.Fatal("expected inventory error on 401")
	}

	// 403 forbidden
	bad403 := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{ForcedStatus: 403, ForcedBody: `{"error":{"message":"not entitled","type":"permission_error"}}`}))
	t.Cleanup(bad403.Close)
	cfg403, _ := service.ParseConfigYAML([]byte("base_url: " + bad403.URL + "/v1\napi_key: sk\n"))
	cl403 := service.NewCompatClient(cfg403, bad403.Client(), service.ProviderHooks())
	_, err = cl403.Open(context.Background(), lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}, "m", openaicompat.FlavorChat)
	he, ok := err.(*openaicompat.HTTPError)
	if !ok || he.Status != 403 {
		t.Fatalf("expected HTTP 403 error, got: %T %v", err, err)
	}

	// 429 rate limited
	bad429 := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{ForcedStatus: 429, ForcedBody: `{"error":{"message":"rate limited","type":"rate_limit_error"}}`}))
	t.Cleanup(bad429.Close)
	cfg429, _ := service.ParseConfigYAML([]byte("base_url: " + bad429.URL + "/v1\napi_key: sk\n"))
	cl429 := service.NewCompatClient(cfg429, bad429.Client(), service.ProviderHooks())
	_, err = cl429.Open(context.Background(), lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}, "m", openaicompat.FlavorChat)
	he, ok = err.(*openaicompat.HTTPError)
	if !ok || he.Status != 429 {
		t.Fatalf("expected HTTP 429 error, got: %T %v", err, err)
	}
}

func TestParity_ConformanceAdvertised(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{RequireBearer: true}))
	t.Cleanup(srv.Close)
	rep := conformance.RunWith(context.Background(), service.New(), conformance.Options{
		ConfigYAML: []byte("base_url: " + srv.URL + "/v1\napi_key: sk\n"), SampleModel: "Qwen/Qwen3.7-Flash", DisableUsageRequirement: true, VisionInputOnly: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func TestParity_ToolCallStreamFinish(t *testing.T) {
	t.Parallel()
	sse := strings.Join([]string{
		`data: {"id":"cc_tool","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather"}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_tool","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_tool","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		RequireBearer: true,
		ChatStreamSSE: sse,
	}))
	t.Cleanup(srv.Close)
	cfg, err := service.ParseConfigYAML([]byte("base_url: " + srv.URL + "/v1\napi_key: sk\n"))
	if err != nil {
		t.Fatal(err)
	}
	cl := service.NewCompatClient(cfg, srv.Client(), service.ProviderHooks())
	call := lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}
	es, err := cl.Open(context.Background(), call, "Qwen/Qwen3.7-Flash", openaicompat.FlavorChat)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	var sawStarted, sawFinished bool
	var args strings.Builder
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Kind {
		case lipapi.EventToolCallStarted:
			if ev.ToolCallID == "call_ab" && ev.ToolName == "get_weather" {
				sawStarted = true
			}
		case lipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "call_ab" {
				args.WriteString(ev.Delta)
			}
		case lipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_ab" {
				sawFinished = true
			}
		}
	}
	if !sawStarted {
		t.Fatal("expected ToolCallStarted")
	}
	if args.String() != `{"city":"NYC"}` {
		t.Fatalf("args=%q", args.String())
	}
	if !sawFinished {
		t.Fatal("expected ToolCallFinished for call_ab")
	}
}
