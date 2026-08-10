package backend

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/anthropicmessages"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaichat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/semantic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

type standardProbe struct {
	mu    sync.Mutex
	count int
	last  CapturedRequest
}

func (p *standardProbe) RequestCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.count }
func (p *standardProbe) LastRequest() CapturedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}
func (p *standardProbe) Reset() { p.mu.Lock(); p.count = 0; p.last = CapturedRequest{}; p.mu.Unlock() }

func TestStandardComposition_FalseCapabilityMutationIsCaught(t *testing.T) {
	probe := &standardProbe{}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", countingHandler(probe, openairesponses.NewHandler(openairesponses.Config{AllowMissingBearer: true})))
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	be, model := buildStandardFamily(t, reg, "openai-responses", "http://127.0.0.1:1", http.DefaultClient)
	delete(be.Caps, lipapi.CapabilityStreaming)
	h := realExecHarness{subject: semantic.SubjectDescriptor{ID: "openai-responses", Kind: semantic.KindBackendFamily, Capabilities: []lipapi.Capability{lipapi.CapabilityStreaming}, Transports: []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming}}, view: ExecBackendView{Backend: be, Candidate: routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-responses", Model: model}}, Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIResponses, TransportMode: lipapi.TransportModeStreaming}}, probe: probe}
	if _, err := CertifyBackend(context.Background(), h); err == nil {
		t.Fatal("false streaming capability declaration was not caught")
	}
}

func TestStandardComposition_CertifiesEveryInProcessFamily(t *testing.T) {
	probe := &standardProbe{}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", countingHandler(probe, openairesponses.NewHandler(openairesponses.Config{AllowMissingBearer: true})))
	mux.Handle("/responses", countingHandler(probe, openResponsesCompatHandler()))
	mux.Handle("/responses/compact", countingHandler(probe, openResponsesCompatHandler()))
	mux.Handle("/v1/chat/completions", countingHandler(probe, openaichat.NewHandler(openaichat.Config{AllowMissingBearer: true})))
	mux.Handle("/v1/messages", countingHandler(probe, anthropicmessages.NewHandler(anthropicmessages.Config{AllowMissingAPIKey: true})))
	mux.Handle("/v1beta/models/m:generateContent", countingHandler(probe, gemini.NewHandler(gemini.Config{AllowMissingAPIKey: true})))
	mux.Handle("/v1beta/models/m:streamGenerateContent", countingHandler(probe, gemini.NewHandler(gemini.Config{AllowMissingAPIKey: true})))
	mux.Handle("/model/m/converse", countingHandler(probe, bedrock.NewHandler(bedrock.Config{AllowMissingAuthorization: true})))
	mux.Handle("/model/m/converse-stream", countingHandler(probe, bedrock.NewHandler(bedrock.Config{AllowMissingAuthorization: true})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tckClient := srv.Client()

	keys := standardplugins.UpstreamAPIKeys{OpenAI: []string{"test-openai"}, Anthropic: []string{"test-anthropic"}, Gemini: []string{"test-gemini"}, AlibabaTokenPlan: []string{"test-alibaba"}}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, keys); err != nil {
		t.Fatal(err)
	}
	for _, id := range standardplugins.EssentialBackendKinds() {
		id := id
		t.Run(id, func(t *testing.T) {
			be, model := buildStandardFamily(t, reg, id, srv.URL, tckClient)
			if id == standardplugins.CustomOpenResponsesCompatibleID {
				delete(be.Caps, lipapi.CapabilityStreaming)
			}
			caps := make([]lipapi.Capability, 0, len(be.Caps))
			for c := range be.Caps {
				caps = append(caps, c)
			}
			mode := lipapi.TransportModeStreaming
			if id == standardplugins.CustomOpenResponsesCompatibleID {
				mode = lipapi.TransportModeNonStreaming
			}
			transports := []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming, semantic.TransportConnector}
			if id == standardplugins.CustomOpenResponsesCompatibleID {
				transports = []semantic.ScenarioTransport{semantic.TransportHTTP}
			}
			h := realExecHarness{subject: semantic.SubjectDescriptor{ID: id, Kind: semantic.KindBackendFamily, Capabilities: caps, Dialects: be.DialectSupport, Transports: transports}, view: ExecBackendView{Backend: be, Candidate: routing.AttemptCandidate{Primary: routing.Primary{Backend: id, Model: model}}, Invocation: lipapi.Invocation{Operation: operationForFamily(id), TransportMode: mode}}, probe: probe}
			cert, err := CertifyBackend(context.Background(), h)
			if err != nil {
				t.Fatalf("%v; last upstream request=%s", err, string(probe.LastRequest().Body))
			}
			if err := cert.ValidateReleaseReady(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type realExecHarness struct {
	subject semantic.SubjectDescriptor
	view    ExecBackendView
	probe   UpstreamProbe
}

func (h realExecHarness) Subject() semantic.SubjectDescriptor          { return h.subject }
func (h realExecHarness) Backend(context.Context) (BackendView, error) { return h.view, nil }
func (h realExecHarness) Upstream() UpstreamProbe                      { return h.probe }
func (h realExecHarness) Reset(context.Context) error                  { h.probe.Reset(); return nil }

func countingHandler(probe *standardProbe, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		probe.mu.Lock()
		probe.count++
		probe.last = CapturedRequest{Method: r.Method, Path: r.URL.Path, Body: append([]byte(nil), body...)}
		probe.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func openResponsesCompatHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/compact") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c","object":"response.compaction","status":"completed","model":"m","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":0,"output_tokens":1,"total_tokens":1}}`)
			return
		}
		if !strings.Contains(string(body), "stream") {
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(string(body), "tools") {
				_, _ = io.WriteString(w, `{"id":"r","object":"response","status":"completed","model":"m","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]},{"type":"function_call","id":"call_1","name":"weather","arguments":"{}"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"r","object":"response","status":"completed","model":"m","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":0,"output_tokens":1,"total_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		usage := `{"input_tokens":10,"output_tokens":5,"total_tokens":15}`
		if strings.Contains(string(body), `"max_output_tokens":1`) {
			usage = `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`
		}
		stream := `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"r","status":"in_progress","model":"m"}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"message","id":"msg","role":"assistant","status":"in_progress","content":[]}}

event: response.content_part.added
data: {"type":"response.content_part.added","item_id":"msg","content_index":0,"part":{"type":"output_text","text":""}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg","content_index":0,"delta":"ok"}

event: response.completed
data: {"type":"response.completed","sequence_number":2,"response":{"id":"r","status":"completed","model":"m","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":USAGE}}`
		stream = strings.Replace(stream, "USAGE", usage, 1)
		_, _ = io.WriteString(w, stream)
	})
}

func buildStandardFamily(t *testing.T, reg *pluginreg.Registry, id, baseURL string, client *http.Client) (execbackend.Backend, string) {
	raw := ""
	model := "m"
	switch id {
	case "openai-responses", "openai-legacy":
		raw = "base_url: " + baseURL + "/v1\n"
	case "anthropic", "alibaba-token-plan-intl", "gemini":
		raw = "base_url: " + baseURL + "\n"
	case "bedrock":
		raw = "region: us-east-1\naccess_key_id: AKID\nsecret_access_key: SECRET\nbase_endpoint: " + baseURL + "\ndisable_https: true\n"
	case standardplugins.CustomOpenAILegacyCompatibleID, standardplugins.CustomOpenAIResponsesCompatibleID:
		raw = "backend_prefix: " + id + "\nbase_url: " + baseURL + "/v1\napi_key_env_var_root: TCK_KEY\n"
		t.Setenv("TCK_KEY", "test-key")
	case standardplugins.CustomAnthropicCompatibleID, standardplugins.CustomOpenResponsesCompatibleID:
		raw = "backend_prefix: " + id + "\nbase_url: " + baseURL + "\napi_key_env_var_root: TCK_KEY\n"
		t.Setenv("TCK_KEY", "test-key")
	default:
		t.Fatalf("unhandled standard family %q", id)
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	result, err := reg.BuildBackendWithLifecycle(id, id+"-tck", node, client, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Cleanup != nil {
		t.Cleanup(func() { _ = result.Cleanup() })
	}
	return result.Backend, model
}
func operationForFamily(id string) lipapi.Operation {
	switch id {
	case "openai-responses", standardplugins.CustomOpenAIResponsesCompatibleID:
		return lipapi.OperationOpenAIResponses
	case "openai-legacy", standardplugins.CustomOpenAILegacyCompatibleID:
		return lipapi.OperationOpenAIChatCompletions
	default:
		return ""
	}
}
