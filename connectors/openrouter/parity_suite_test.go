package openrouter_test

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
	"github.com/matdev83/go-llm-interactive-proxy/connectors/openrouter/internal/service"
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
	f := d.Factories[0]
	if f.Kind != service.FactoryKind || f.RoutePrefixes[0] != service.FactoryKind || f.SupportsFinalizeBilling {
		t.Fatalf("%+v", f)
	}
}

func TestConfigure_RequiresAPIKey(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), mustCfg(t, "base_url: http://127.0.0.1:9\n"))
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err=%v", err)
	}
}

func TestParity_AttributionHeadersAndExtraBody(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var referer, title string
	var body []byte
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		RequireBearer: true,
		OnRequestHeaders: func(h http.Header) {
			mu.Lock()
			referer = h.Get("HTTP-Referer")
			title = h.Get("X-OpenRouter-Title")
			mu.Unlock()
		},
		OnRequestBody: func(b []byte) {
			mu.Lock()
			body = append([]byte(nil), b...)
			mu.Unlock()
		},
	}))
	t.Cleanup(srv.Close)
	cfg, err := service.ParseConfigYAML([]byte(
		"base_url: " + srv.URL + "/v1\napi_key: sk\napp_url_mode: custom\napp_url_value: https://example.test\napp_title_mode: custom\napp_title_value: Title\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	cl := service.NewCompatClient(cfg, srv.Client())
	provider, _ := json.Marshal(map[string]any{"order": []string{"a"}, "allow_fallbacks": false})
	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Extensions: map[string]json.RawMessage{
			"openrouter.provider":   provider,
			"openai.extra_body.foo": json.RawMessage(`"bar"`),
		},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}
	es, err := cl.Open(context.Background(), call, "auto", openaicompat.FlavorChat)
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
	if referer != "https://example.test" || title != "Title" {
		t.Fatalf("referer=%q title=%q", referer, title)
	}
	if !strings.Contains(string(body), `"foo"`) || !strings.Contains(string(body), `"provider"`) {
		t.Fatalf("body=%s", body)
	}
}

// TestOpenRouterAttribution_defaultProxyOverridesCapturedClient proves proxy-mode
// product defaults win over captured client openrouter.* extension values
// (SB-IDENTITY-openrouter-attr). Custom attribution is covered by
// TestParity_AttributionHeadersAndExtraBody.
func TestOpenRouterAttribution_defaultProxyOverridesCapturedClient(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var presentRef, presentTit bool
	var referer, title string
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		RequireBearer: true,
		OnRequestHeaders: func(h http.Header) {
			mu.Lock()
			_, presentRef = h["Http-Referer"]
			referer = h.Get("HTTP-Referer")
			_, presentTit = h["X-Openrouter-Title"]
			title = h.Get("X-OpenRouter-Title")
			mu.Unlock()
		},
	}))
	t.Cleanup(srv.Close)
	// Omit app_url_mode / app_title_mode → proxy product defaults.
	cfg, err := service.ParseConfigYAML([]byte("base_url: " + srv.URL + "/v1\napi_key: sk\n"))
	if err != nil {
		t.Fatal(err)
	}
	cl := service.NewCompatClient(cfg, srv.Client())
	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Extensions: map[string]json.RawMessage{
			"openrouter.http_referer": json.RawMessage(`"https://client.example/should-not-win"`),
			"openrouter.title":        json.RawMessage(`"ClientShouldNotWin"`),
		},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}
	es, err := cl.Open(context.Background(), call, "auto", openaicompat.FlavorChat)
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
	if !presentRef || referer != service.DefaultAppURL {
		t.Fatalf("default proxy HTTP-Referer present=%v value=%q want %q", presentRef, referer, service.DefaultAppURL)
	}
	if !presentTit || title != service.DefaultAppTitle {
		t.Fatalf("default proxy X-OpenRouter-Title present=%v value=%q want %q", presentTit, title, service.DefaultAppTitle)
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
	cl := service.NewCompatClient(cfg, srv.Client())
	call := lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}
	es, err := cl.Open(context.Background(), call, "auto", openaicompat.FlavorResponses)
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

func TestParity_InventoryAndProviderError(t *testing.T) {
	t.Parallel()
	okSrv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		RequireBearer: true, ModelsJSON: `{"data":[{"id":"openai/gpt-4o-mini"}]}`,
	}))
	t.Cleanup(okSrv.Close)
	inst, err := service.New().Configure(context.Background(), mustCfg(t, "base_url: "+okSrv.URL+"/v1\napi_key: sk\n"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := inst.ListModels(context.Background(), 10)
	if err != nil || len(resp.Models) != 1 || resp.Models[0].CanonicalModelID != "openrouter/openai/gpt-4o-mini" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	bad := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{ForcedStatus: 401, ForcedBody: `{"error":{"message":"bad","code":"invalid_api_key"}}`}))
	t.Cleanup(bad.Close)
	inst2, err := service.New().Configure(context.Background(), mustCfg(t, "base_url: "+bad.URL+"/v1\napi_key: sk\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst2.ListModels(context.Background(), 10)
	if err == nil {
		t.Fatal("expected inventory error")
	}
	cfg, _ := service.ParseConfigYAML([]byte("base_url: " + bad.URL + "/v1\napi_key: sk\n"))
	cl := service.NewCompatClient(cfg, bad.Client())
	_, err = cl.Open(context.Background(), lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}, "m", openaicompat.FlavorChat)
	he, ok := err.(*openaicompat.HTTPError)
	if !ok || he.Status != 401 {
		t.Fatalf("err=%T %v", err, err)
	}
}

func TestParity_BillingNotAdvertised(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Factories[0].SupportsFinalizeBilling {
		t.Fatal("must not advertise finalize billing without evidence implementation")
	}
	inst, err := service.New().Configure(context.Background(), mustCfg(t, "base_url: http://127.0.0.1:9\napi_key: sk\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inst.(interface {
		FinalizeBilling(context.Context, backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error)
	}); ok {
		t.Fatal("FinalizeBilling must not be implemented")
	}
}

func TestParity_ConformanceAdvertised(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{RequireBearer: true}))
	t.Cleanup(srv.Close)
	rep := conformance.RunWith(context.Background(), service.New(), conformance.Options{
		ConfigYAML: []byte("base_url: " + srv.URL + "/v1\napi_key: sk\n"), SampleModel: "emu-model", DisableUsageRequirement: true, VisionInputOnly: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

// TestParity_ToolCallStreamFinish locks connector-support decodeChatSSE finish
// semantics (M4): finish_reason=="tool_calls" must emit EventToolCallFinished,
// matching essential openaicompat chatStream handling.
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
	cl := service.NewCompatClient(cfg, srv.Client())
	call := lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming, TransportMode: lipapi.TransportModeStreaming},
	}
	es, err := cl.Open(context.Background(), call, "auto", openaicompat.FlavorChat)
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
