package opencode_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/opencode/internal/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/opencode/internal/catalog/vendor"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/opencode/internal/service"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/opencode/internal/upstream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/conformance"
)

func TestDescribe_BothFactories(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]backendplugin.FactoryDescriptor{}
	for _, f := range d.Factories {
		kinds[f.Kind] = f
	}
	goF, ok := kinds[service.FactoryKindGo]
	if !ok || goF.ProcessSharing != backendplugin.ProcessSharingPerInstance || goF.CredentialMode != backendplugin.CredentialModeStatic {
		t.Fatalf("go=%+v", goF)
	}
	zenF, ok := kinds[service.FactoryKindZen]
	if !ok || zenF.ProcessSharing != backendplugin.ProcessSharingPerInstance {
		t.Fatalf("zen=%+v", zenF)
	}
}

func TestConfigure_RequiresAPIKey(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{service.FactoryKindGo, service.FactoryKindZen} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			_, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: kind, InstanceID: "i1", ConfigYAML: []byte("base_url: http://127.0.0.1:9\n"),
				Negotiation: backendplugin.Negotiation{Compatible: true},
			})
			if err == nil || !strings.Contains(err.Error(), "api_key") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestConfigure_DefaultBaseURLs(t *testing.T) {
	t.Parallel()
	goCfg, err := service.ParseConfigYAML(service.FactoryKindGo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if goCfg.BaseURL != service.DefaultGoURL {
		t.Fatalf("go base=%q", goCfg.BaseURL)
	}
	zenCfg, err := service.ParseConfigYAML(service.FactoryKindZen, nil)
	if err != nil {
		t.Fatal(err)
	}
	if zenCfg.BaseURL != service.DefaultZenURL {
		t.Fatalf("zen base=%q", zenCfg.BaseURL)
	}
}

func TestInventory_GoAndZenSeparate(t *testing.T) {
	t.Parallel()
	goSrv, goAuth := opencodeModelServer(t, `{"data":[{"id":"kimi-k2.7-code"}]}`)
	zenSrv, zenAuth := opencodeModelServer(t, `{"data":[{"id":"gpt-5.4"}]}`)
	goInst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindGo, "base_url: "+goSrv.URL+"\napi_key: go-key\n"))
	if err != nil {
		t.Fatal(err)
	}
	zenInst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindZen, "base_url: "+zenSrv.URL+"\napi_key: zen-key\n"))
	if err != nil {
		t.Fatal(err)
	}
	goResp, err := goInst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	zenResp, err := zenInst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if *goAuth != "Bearer go-key" {
		t.Fatalf("go auth=%q", *goAuth)
	}
	if *zenAuth != "Bearer zen-key" {
		t.Fatalf("zen auth=%q", *zenAuth)
	}
	if len(goResp.Models) != 1 || goResp.Models[0].NativeModelID != "kimi-k2.7-code" {
		t.Fatalf("go=%+v", goResp.Models)
	}
	if len(zenResp.Models) != 1 || zenResp.Models[0].NativeModelID != "gpt-5.4" {
		t.Fatalf("zen=%+v", zenResp.Models)
	}
	if goResp.InventorySource != service.FactoryKindGo || zenResp.InventorySource != service.FactoryKindZen {
		t.Fatalf("provenance go=%q zen=%q", goResp.InventorySource, zenResp.InventorySource)
	}
}

func TestParity_TwoGoInstancesNoGlobalLeak(t *testing.T) {
	t.Parallel()
	aSrv, aAuth := opencodeModelServer(t, `{"data":[{"id":"catalog-a"}]}`)
	bSrv, bAuth := opencodeModelServer(t, `{"data":[{"id":"catalog-b"}]}`)
	aInst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindGo,
		"base_url: "+aSrv.URL+"\napi_key: key-a\n"))
	if err != nil {
		t.Fatal(err)
	}
	bInst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindGo,
		"base_url: "+bSrv.URL+"\napi_key: key-b\n"))
	if err != nil {
		t.Fatal(err)
	}
	aResp, err := aInst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	bResp, err := bInst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if *aAuth != "Bearer key-a" || *bAuth != "Bearer key-b" {
		t.Fatalf("auth leak a=%q b=%q", *aAuth, *bAuth)
	}
	if aResp.Models[0].NativeModelID == bResp.Models[0].NativeModelID {
		t.Fatalf("catalog leak a=%+v b=%+v", aResp.Models, bResp.Models)
	}
}

func TestParity_GoRoutesOpenAIChat(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv, entries := flavorServerWithModels(t, &capture, catalog.BackendGo)
	router := upstream.NewRouter(catalog.BackendGo, srv.URL, "test-key", srv.Client())
	resolved, err := catalog.NewModelCatalog(catalog.BackendGo, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
		Resolve("moonshotai/kimi-k2.7-code")
	if err != nil {
		t.Fatal(err)
	}
	es, err := router.Open(context.Background(), nonStreamCall(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(drainEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected chat response")
	}
	if !strings.HasSuffix(capture.Path, "/v1/chat/completions") {
		t.Fatalf("path=%q", capture.Path)
	}
	if capture.Authorization != "Bearer test-key" {
		t.Fatalf("auth=%q", capture.Authorization)
	}
}

func TestParity_GoRoutesAnthropicMessages(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv, entries := flavorServerWithModels(t, &capture, catalog.BackendGo)
	router := upstream.NewRouter(catalog.BackendGo, srv.URL, SyntheticAnthropicAPIKey, srv.Client())
	resolved, err := catalog.NewModelCatalog(catalog.BackendGo, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
		Resolve("minimax/minimax-m3")
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, mustOpen(t, router, resolved))
	if !strings.HasSuffix(capture.Path, "/v1/messages") {
		t.Fatalf("path=%q", capture.Path)
	}
	if capture.AnthropicAPIKey != SyntheticAnthropicAPIKey {
		t.Fatalf("x-api-key=%q", capture.AnthropicAPIKey)
	}
}

func TestParity_ZenRoutesResponses(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv, entries := flavorServerWithModels(t, &capture, catalog.BackendZen)
	router := upstream.NewRouter(catalog.BackendZen, srv.URL, "test-key", srv.Client())
	resolved, err := catalog.NewModelCatalog(catalog.BackendZen, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
		Resolve("openai/gpt-5.4")
	if err != nil {
		t.Fatal(err)
	}
	es, err := router.Open(context.Background(), nonStreamCall(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !hasText(drainEvents(t, es), "responses-ns-ok") {
		t.Fatal("expected responses output")
	}
	if !strings.HasSuffix(capture.Path, "/v1/responses") {
		t.Fatalf("path=%q", capture.Path)
	}
}

func TestParity_ZenRoutesGemini(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv, entries := flavorServerWithModels(t, &capture, catalog.BackendZen)
	router := upstream.NewRouter(catalog.BackendZen, srv.URL, "gemini-key", srv.Client())
	resolved, err := catalog.NewModelCatalog(catalog.BackendZen, entries, vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)).
		Resolve("google/gemini-3.1-pro")
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, mustOpen(t, router, resolved))
	if !strings.Contains(capture.Path, "generateContent") && !strings.Contains(capture.Path, "gemini") {
		t.Fatalf("path=%q", capture.Path)
	}
	if capture.GoogleAPIKey != "gemini-key" {
		t.Fatalf("google key=%q", capture.GoogleAPIKey)
	}
}

func TestParity_VendorFamiliesAccepted(t *testing.T) {
	t.Parallel()
	idx := vendor.NewSnapshotIndex(map[string]vendor.ModelFacts{
		"openai/gpt-5.4":            {Source: vendor.FactSourceCatalog},
		"anthropic/claude-sonnet-4": {Source: vendor.FactSourceCatalog},
		"google/gemini-3.1-pro":     {Source: vendor.FactSourceCatalog},
		"amazon/claude-sonnet-4":    {Source: vendor.FactSourceCatalog},
	})
	r := vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{Index: idx}, true)
	for _, id := range []string{
		"openai/gpt-5.4", "anthropic/claude-sonnet-4", "google/gemini-3.1-pro", "amazon/claude-sonnet-4",
	} {
		got := r.Resolve(id)
		if got.Kind == vendor.VendorResolveNoMatch || got.CanonicalID == "" {
			t.Fatalf("%s unresolved: %+v", id, got)
		}
	}
}

func TestParity_UnknownModelFails(t *testing.T) {
	t.Parallel()
	c := catalog.NewModelCatalog(catalog.BackendGo, goTestModels("http://unused"), vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true))
	_, err := c.Resolve("unknown/vendor-model")
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err=%v", err)
	}
}

func TestParity_ExecuteStreamingGo(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv := NewFlavorServer(t, &capture)
	inst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindGo,
		"base_url: "+srv.URL+"\napi_key: sk\nmodels:\n  - id: emu-model\n    endpoint: "+srv.URL+"/v1/chat/completions\n    ai_sdk_package: \"@ai-sdk/openai-compatible\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	frames := mustExecute(t, inst, "opencode-go/emu-model", true)
	if !framesHaveText(frames, "chat-stream-ok") {
		t.Fatalf("frames=%v", frames)
	}
	if capture.Authorization != "Bearer sk" {
		t.Fatalf("auth=%q", capture.Authorization)
	}
}

func TestParity_ExecuteStreamingZen(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv := NewFlavorServer(t, &capture)
	inst, err := service.New().Configure(context.Background(), mustCfg(t, service.FactoryKindZen,
		"base_url: "+srv.URL+"\napi_key: sk\nmodels:\n  - id: emu-model\n    endpoint: "+srv.URL+"/v1/responses\n    ai_sdk_package: \"@ai-sdk/openai\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	frames := mustExecute(t, inst, "opencode-zen/emu-model", true)
	if !framesHaveText(frames, "responses-stream-ok") {
		t.Fatalf("frames=%v path=%q", frames, capture.Path)
	}
}

func TestParity_ConformanceGo(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv := NewFlavorServer(t, &capture)
	// SkipExecute: static caps include Tools/Vision/Documents; kit execute proof
	// requires tool/image events. Dedicated TestParity_Execute* covers streaming.
	rep := conformance.RunWith(context.Background(), service.New(), conformance.Options{
		FactoryKind: service.FactoryKindGo,
		ConfigYAML:  []byte("base_url: " + srv.URL + "\napi_key: sk\nmodels:\n  - id: emu-model\n    endpoint: " + srv.URL + "/v1/chat/completions\n"),
		SampleModel: "opencode-go/emu-model", DisableUsageRequirement: true, VisionInputOnly: true, SkipExecute: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func TestParity_ConformanceZen(t *testing.T) {
	t.Parallel()
	var capture RequestCapture
	srv := NewFlavorServer(t, &capture)
	rep := conformance.RunWith(context.Background(), service.New(), conformance.Options{
		FactoryKind: service.FactoryKindZen,
		ConfigYAML:  []byte("base_url: " + srv.URL + "\napi_key: sk\nmodels:\n  - id: emu-model\n    endpoint: " + srv.URL + "/v1/responses\n"),
		SampleModel: "opencode-zen/emu-model", DisableUsageRequirement: true, VisionInputOnly: true, SkipExecute: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func mustCfg(t *testing.T, kind, yaml string) backendplugin.ConfigureRequest {
	t.Helper()
	return backendplugin.ConfigureRequest{
		FactoryKind: kind, InstanceID: "i1", ConfigYAML: []byte(yaml),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}
}

func opencodeModelServer(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

func goTestModels(base string) []catalog.ModelEntry {
	return []catalog.ModelEntry{
		{RawID: "kimi-k2.7-code", Endpoint: base + "/v1/chat/completions", AISDKPackage: "@ai-sdk/openai-compatible"},
		{RawID: "minimax-m3", Endpoint: base + "/v1/messages", AISDKPackage: "@ai-sdk/anthropic"},
	}
}

func zenTestModels(base string) []catalog.ModelEntry {
	return []catalog.ModelEntry{
		{RawID: "gpt-5.4", Endpoint: base + "/v1/responses", AISDKPackage: "@ai-sdk/openai"},
		{RawID: "gemini-3.1-pro", Endpoint: base + "/v1beta/models/gemini-3.1-pro", AISDKPackage: "@ai-sdk/google"},
	}
}

func flavorServerWithModels(t *testing.T, capture *RequestCapture, kind catalog.BackendKind) (*httptest.Server, []catalog.ModelEntry) {
	t.Helper()
	if capture == nil {
		capture = &RequestCapture{}
	}
	srv := NewFlavorServer(t, capture)
	switch kind {
	case catalog.BackendZen:
		return srv, zenTestModels(srv.URL)
	default:
		return srv, goTestModels(srv.URL)
	}
}

func mustOpen(t *testing.T, router *upstream.Router, resolved catalog.ResolvedModel) lipapi.ManagedEventStream {
	t.Helper()
	es, err := router.Open(context.Background(), nonStreamCall(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func nonStreamCall() lipapi.Call {
	return lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
		Invocation: lipapi.Invocation{
			DeliveryMode: lipapi.DeliveryModeNonStreaming, TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
}

func drainEvents(t *testing.T, es lipapi.ManagedEventStream) []lipapi.Event {
	t.Helper()
	defer func() { _ = es.Close() }()
	out := []lipapi.Event{}
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		out = append(out, ev)
	}
}

func hasText(events []lipapi.Event, want string) bool {
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, want) {
			return true
		}
	}
	return false
}

type memExecuteStream struct {
	ctx    context.Context
	inbox  []backendplugin.ClientFrame
	outbox []backendplugin.ServerFrame
	ri     int
}

func (m *memExecuteStream) Context() context.Context { return m.ctx }
func (m *memExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	if m.ri >= len(m.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	f := m.inbox[m.ri]
	m.ri++
	return f, nil
}
func (m *memExecuteStream) Send(frame backendplugin.ServerFrame) error {
	m.outbox = append(m.outbox, frame)
	return nil
}

func mustExecute(t *testing.T, inst backendplugin.ConfiguredInstance, model string, streaming bool) []backendplugin.ServerFrame {
	t.Helper()
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r1", AttemptID: "a1", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: model,
		Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	call := lipapi.Call{
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
	if streaming {
		call.Invocation.DeliveryMode = lipapi.DeliveryModeStreaming
		call.Invocation.TransportMode = lipapi.TransportModeStreaming
	}
	backendplugin.ApplyCallWireMetadata(&inv, call, nil)
	ms := &memExecuteStream{
		ctx: context.Background(),
		inbox: []backendplugin.ClientFrame{{
			Kind: backendplugin.ClientFrameStart, InstanceID: "i1", Invocation: &inv,
		}},
	}
	if err := inst.Execute(ms); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return ms.outbox
}

func framesHaveText(frames []backendplugin.ServerFrame, want string) bool {
	for _, f := range frames {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil &&
			f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil &&
			strings.Contains(*f.Event.Delta, want) {
			return true
		}
	}
	return false
}
