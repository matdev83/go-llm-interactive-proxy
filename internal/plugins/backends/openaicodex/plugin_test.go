package openaicodex_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gorillawebsocket "github.com/gorilla/websocket"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNew_configErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  backend.Config
	}{
		{name: "empty base url", cfg: backend.Config{AccessToken: "tok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			be := backend.New(tc.cfg)
			_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
				Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
			})
			if err == nil {
				t.Fatal("expected config error")
			}
			_, err = be.ModelInventory.LoadModels(context.Background())
			if err == nil {
				t.Fatal("expected inventory error")
			}
		})
	}
}

func TestNew_missingAccessTokenConfigError(t *testing.T) { //nolint:paralleltest // mutates HOME/USERPROFILE via t.Setenv
	withHomeDir(t, t.TempDir())
	be := backend.New(backend.Config{BaseURL: "http://127.0.0.1"})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err == nil {
		t.Fatal("expected config error")
	}
	_, err = be.ModelInventory.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected inventory error")
	}
}

func TestOpen_refbackendHeadersAndEvents(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "codex-stream-ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		AccountID:   "acct-42",
		HTTPClient:  ts.Client(),
	})
	call := lipapi.Call{
		ID: "call-1",
		Session: lipapi.SessionRef{
			ClientSessionID: "sess-correlation",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, es)
	if err := lipapi.ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
	kinds := eventKinds(events)
	if !kinds[lipapi.EventTextDelta] {
		t.Fatalf("events: %+v", events)
	}
	if !kinds[lipapi.EventUsageDelta] {
		t.Fatalf("missing usage: %+v", events)
	}
	got := srv.LatestRequest()
	if got.Authorization != "Bearer sk-codex" {
		t.Fatalf("authorization: %q", got.Authorization)
	}
	if got.OpenAIBeta != "responses=experimental" || got.Originator == "" || got.CodexTaskType == "" {
		t.Fatalf("codex headers: beta=%q originator=%q task=%q", got.OpenAIBeta, got.Originator, got.CodexTaskType)
	}
	if got.ConversationID != "sess-correlation" || got.SessionID != "sess-correlation" {
		t.Fatalf("conversation/session: conv=%q sess=%q", got.ConversationID, got.SessionID)
	}
	if got.ChatGPTAccountID != "acct-42" {
		t.Fatalf("account id: %q", got.ChatGPTAccountID)
	}
}

func TestOpen_baseURLEndingResponses(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/responses",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
	})
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := srv.LatestRequest().Path; got != "/responses" {
		t.Fatalf("path: %q", got)
	}
}

func TestOpen_nilContext(t *testing.T) {
	t.Parallel()
	be := backend.New(backend.Config{BaseURL: "http://127.0.0.1", AccessToken: "tok"})
	_, err := be.Open(nil, sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err == nil || !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpen_non2xxIncludesStatus(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{
		Token:            "sk-codex",
		ForcedHTTPStatus: http.StatusUnauthorized,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
		Transport:   backend.TransportHTTPS,
	})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpen_429ReturnsUpstreamErrorWithoutRefresh(t *testing.T) {
	t.Parallel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		calls++
		w.Header().Set("Retry-After", "60")
		http.Error(w, `{"error":"rate_limit"}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:      srv.URL + "/backend-api/codex",
		AccessToken:  "sk-codex",
		RefreshToken: "refresh-token",
		HTTPClient:   srv.Client(),
	})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	if !strings.Contains(err.Error(), "upstream HTTP 429") {
		t.Fatalf("error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("single-token 429 should not retry locally, calls=%d", calls)
	}
}

func TestOpen_routeParamsReachCodexPayload(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
	})
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secure, err := app.NewManager(memory.New(memory.Options{SimulateDurable: true}), app.NewRandGenerator([]byte("12345678901234567890123456789012")), b2bualineage.New(st), app.ManagerConfig{
		FingerprintKey: []byte("12345678901234567890123456789012"),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := &coreruntime.Executor{
		Store:                   st,
		SecureSession:           secure,
		SyntheticLocalPrincipal: true,
		Bus:                     hooks.New(hooks.Config{}),
		Rand:                    routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			backend.ID: be,
		},
	}
	call := sampleCall()
	call.Route.Selector = "openai-codex:gpt-5.4-mini?reasoning_effort=xhigh"
	es, err := ex.Execute(context.Background(), &call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	body := srv.LatestRequest().Body
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "xhigh" {
		t.Fatalf("reasoning payload: %#v", body["reasoning"])
	}
	if _, ok := body["temperature"]; ok {
		t.Fatalf("temperature should be omitted: %#v", body["temperature"])
	}
}

// TestOpen_rejectsUnsupportedGenerationParamsWithoutCompatExt proves plain calls with
// generation options the Codex Responses API does not support fail explicitly.
func TestOpen_rejectsUnsupportedGenerationParamsWithoutCompatExt(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
	})
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secure, err := app.NewManager(memory.New(memory.Options{SimulateDurable: true}), app.NewRandGenerator([]byte("12345678901234567890123456789012")), b2bualineage.New(st), app.ManagerConfig{
		FingerprintKey: []byte("12345678901234567890123456789012"),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := &coreruntime.Executor{
		Store:                   st,
		SecureSession:           secure,
		SyntheticLocalPrincipal: true,
		Bus:                     hooks.New(hooks.Config{}),
		Rand:                    routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			backend.ID: be,
		},
	}
	maxTok := 512
	call := sampleCall()
	call.Route.Selector = "openai-codex:gpt-5.4-mini"
	call.Options = lipapi.GenerationOptions{MaxOutputTokens: &maxTok}
	_, err = ex.Execute(context.Background(), &call)
	if err == nil {
		t.Fatal("expected error for unsupported max_output_tokens without compat extension")
	}
	if !strings.Contains(err.Error(), "max_output_tokens") {
		t.Fatalf("err = %v", err)
	}
}

// TestOpen_compatDropsUnsupportedGenerationParamsFromClient proves that generation options the
// Codex Responses API does not support are dropped when the compat extension opts in.
func TestOpen_compatDropsUnsupportedGenerationParamsFromClient(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
	})
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secure, err := app.NewManager(memory.New(memory.Options{SimulateDurable: true}), app.NewRandGenerator([]byte("12345678901234567890123456789012")), b2bualineage.New(st), app.ManagerConfig{
		FingerprintKey: []byte("12345678901234567890123456789012"),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := &coreruntime.Executor{
		Store:                   st,
		SecureSession:           secure,
		SyntheticLocalPrincipal: true,
		Bus:                     hooks.New(hooks.Config{}),
		Rand:                    routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			backend.ID: be,
		},
	}
	maxTok := 512
	temp := 0.2
	topP := 0.9
	call := sampleCall()
	call.Route.Selector = "openai-codex:gpt-5.4-mini"
	call.Options = lipapi.GenerationOptions{
		MaxOutputTokens: &maxTok,
		Temperature:     &temp,
		TopP:            &topP,
	}
	call.Extensions = map[string]json.RawMessage{
		backend.ExtIgnoreUnsupportedGenParams: json.RawMessage(`true`),
	}
	es, err := ex.Execute(context.Background(), &call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	body := srv.LatestRequest().Body
	for _, key := range []string{"max_output_tokens", "max_tokens", "temperature", "top_p"} {
		if _, ok := body[key]; ok {
			t.Fatalf("upstream body must not include %s: %#v", key, body[key])
		}
	}
}

// TestOpen_stripsOpenAIProviderModelPrefix proves that a client using the OpenCode
// provider/model convention (e.g. "openai/gpt-5.4-mini") has the "openai/" prefix stripped
// before the model reaches the Codex upstream, which rejects org-prefixed model names.
func TestOpen_stripsOpenAIProviderModelPrefix(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
	})
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secure, err := app.NewManager(memory.New(memory.Options{SimulateDurable: true}), app.NewRandGenerator([]byte("12345678901234567890123456789012")), b2bualineage.New(st), app.ManagerConfig{
		FingerprintKey: []byte("12345678901234567890123456789012"),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := &coreruntime.Executor{
		Store:                   st,
		SecureSession:           secure,
		SyntheticLocalPrincipal: true,
		Bus:                     hooks.New(hooks.Config{}),
		Rand:                    routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			backend.ID: be,
		},
	}
	call := sampleCall()
	call.Route.Selector = "openai-codex:openai/gpt-5.4-mini?reasoning_effort=low"
	es, err := ex.Execute(context.Background(), &call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	body := srv.LatestRequest().Body
	if got, _ := body["model"].(string); got != "gpt-5.4-mini" {
		t.Fatalf("upstream model = %q, want %q (openai/ prefix must be stripped): %#v", got, "gpt-5.4-mini", body)
	}
}

// TestOpen_looseToolSchemaSentStrictFalse proves at the executor level that a
// client tool whose JSON schema is not Codex-strict-compatible (missing
// additionalProperties:false, as OpenCode's apply_patch is) is forwarded with
// strict:false so the upstream Codex Responses API does not reject the request.
func TestOpen_normalizesToolSchemaAdditionalPropertiesForStrict(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
	})
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secure, err := app.NewManager(memory.New(memory.Options{SimulateDurable: true}), app.NewRandGenerator([]byte("12345678901234567890123456789012")), b2bualineage.New(st), app.ManagerConfig{
		FingerprintKey: []byte("12345678901234567890123456789012"),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := &coreruntime.Executor{
		Store:                   st,
		SecureSession:           secure,
		SyntheticLocalPrincipal: true,
		Bus:                     hooks.New(hooks.Config{}),
		Rand:                    routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			backend.ID: be,
		},
	}
	call := sampleCall()
	call.Route.Selector = "openai-codex:gpt-5.4-mini"
	call.Tools = []lipapi.ToolDef{{
		Name:       "apply_patch",
		Parameters: json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"]}`),
	}}
	es, err := ex.Execute(context.Background(), &call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	tools, ok := srv.LatestRequest().Body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("upstream tools: %#v", srv.LatestRequest().Body["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("upstream tool[0]: %#v", tools[0])
	}
	if name, _ := tool["name"].(string); name != "apply_patch" {
		t.Fatalf("upstream tool name = %q, want apply_patch: %#v", name, tool)
	}
	if strict, _ := tool["strict"].(bool); !strict {
		t.Fatalf("upstream apply_patch strict=false; normalizable schema should be strict: %#v", tool)
	}
	params, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("upstream parameters: %#v", tool["parameters"])
	}
	if ap, ok := params["additionalProperties"].(bool); !ok || ap {
		t.Fatalf("additionalProperties = %#v, want false: %#v", params["additionalProperties"], params)
	}
}

// TestOpen_chatCompletionsToolCallRoundTrip proves at the executor level that an
// assistant tool call decoded from a Chat Completions request (PartJSON with
// type:"function" and a nested "function" object, plus a matching tool result)
// is translated into Codex input function_call + function_call_output items
// linked by the same call_id, instead of failing with "unsupported part kind".
func TestOpen_chatCompletionsToolCallRoundTrip(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "done"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
	})
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secure, err := app.NewManager(memory.New(memory.Options{SimulateDurable: true}), app.NewRandGenerator([]byte("12345678901234567890123456789012")), b2bualineage.New(st), app.ManagerConfig{
		FingerprintKey: []byte("12345678901234567890123456789012"),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := &coreruntime.Executor{
		Store:                   st,
		SecureSession:           secure,
		SyntheticLocalPrincipal: true,
		Bus:                     hooks.New(hooks.Config{}),
		Rand:                    routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			backend.ID: be,
		},
	}
	call := sampleCall()
	call.Route.Selector = "openai-codex:gpt-5.4-mini"
	call.Messages = []lipapi.Message{
		{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("run it")}},
		{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
			Kind:    lipapi.PartJSON,
			Content: []byte(`{"id":"call_abc","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo pong\"}"}}`),
		}}},
		{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
			Kind:       lipapi.PartToolResult,
			ToolCallID: "call_abc",
			Content:    []byte("pong\n"),
		}}},
	}
	call.Tools = []lipapi.ToolDef{{Name: "bash", Parameters: json.RawMessage(`{"type":"object"}`)}}
	es, err := ex.Execute(context.Background(), &call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), es); err != nil {
		t.Fatalf("collect: %v", err)
	}
	input, ok := srv.LatestRequest().Body["input"].([]any)
	if !ok {
		t.Fatalf("upstream input missing: %#v", srv.LatestRequest().Body["input"])
	}
	var sawCall, sawOutput bool
	for _, it := range input {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "function_call" {
			if m["call_id"] == "call_abc" && m["name"] == "bash" {
				sawCall = true
			}
		}
		if m["type"] == "function_call_output" && m["call_id"] == "call_abc" {
			sawOutput = true
		}
	}
	if !sawCall {
		t.Fatalf("upstream input missing function_call(call_abc,bash): %#v", input)
	}
	if !sawOutput {
		t.Fatalf("upstream input missing function_call_output(call_abc): %#v", input)
	}
}

// TestOpen_routeSelectorRoutesArbitraryCodexModels proves manual routing to arbitrary
// openai-codex models via the full route selector "openai-codex:<model>?reasoning_effort=low".
// For each example model the request reaching the Codex backend carries that exact model and
// the reasoning_effort URI param converted into the payload reasoning.effort data structure.
func TestOpen_routeSelectorRoutesArbitraryCodexModels(t *testing.T) {
	t.Parallel()
	models := []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark"}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			t.Parallel()
			srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
			ts := httptest.NewServer(srv.Handler())
			t.Cleanup(ts.Close)

			be := backend.New(backend.Config{
				BaseURL:     ts.URL + "/backend-api/codex",
				AccessToken: "sk-codex",
				HTTPClient:  ts.Client(),
			})
			st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			secure, err := app.NewManager(memory.New(memory.Options{SimulateDurable: true}), app.NewRandGenerator([]byte("12345678901234567890123456789012")), b2bualineage.New(st), app.ManagerConfig{
				FingerprintKey: []byte("12345678901234567890123456789012"),
				StoreDurable:   true,
			})
			if err != nil {
				t.Fatal(err)
			}
			ex := &coreruntime.Executor{
				Store:                   st,
				SecureSession:           secure,
				SyntheticLocalPrincipal: true,
				Bus:                     hooks.New(hooks.Config{}),
				Rand:                    routing.NewSeededRng(1),
				Backends: map[string]execbackend.Backend{
					backend.ID: be,
				},
			}
			call := sampleCall()
			call.Route.Selector = "openai-codex:" + model + "?reasoning_effort=low"
			es, err := ex.Execute(context.Background(), &call)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lipapi.Collect(context.Background(), es); err != nil {
				t.Fatal(err)
			}
			body := srv.LatestRequest().Body
			if got, _ := body["model"].(string); got != model {
				t.Fatalf("payload model %q, want %q (body: %#v)", got, model, body)
			}
			reasoning, ok := body["reasoning"].(map[string]any)
			if !ok || reasoning["effort"] != "low" {
				t.Fatalf("reasoning payload: %#v", body["reasoning"])
			}
		})
	}
}

func TestOpen_httpsDoesNotSendContinuation(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		if n == 1 {
			_, _ = io.WriteString(w, `data: {"type":"response.created","response":{"id":"resp_1"}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
			return
		}
		_, _ = io.WriteString(w, `data: {"type":"response.created","response":{"id":"resp_2"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_2","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:     srv.URL + "/backend-api/codex",
		AccessToken: "tok",
		HTTPClient:  srv.Client(),
		Transport:   backend.TransportHTTPS,
	})
	call := lipapi.Call{
		ID:      "call_aaaaaaaaaaaaaaaa",
		Session: lipapi.SessionRef{ClientSessionID: "sess-https-continuation"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("inspect")},
		}},
		Tools: []lipapi.ToolDef{{Name: "read"}},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	call.Messages = append(call.Messages,
		lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
			Kind:    lipapi.PartJSON,
			Content: json.RawMessage(`{"id":"fc_1","call_id":"call_fc_1","type":"function_call","name":"read","arguments":"{\"filePath\":\"a.go\"}"}`),
		}}},
		lipapi.Message{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
			Kind:       lipapi.PartToolResult,
			ToolCallID: "call_fc_1",
			Content:    json.RawMessage(`{"content":"package main"}`),
		}}},
	)
	es, err = be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if _, ok := bodies[0]["previous_response_id"]; ok {
		t.Fatalf("first request unexpectedly had previous_response_id: %#v", bodies[0])
	}
	if _, ok := bodies[1]["previous_response_id"]; ok {
		t.Fatalf("HTTPS request must not send previous_response_id: %#v", bodies[1])
	}
	input, ok := bodies[1]["input"].([]any)
	if !ok {
		t.Fatalf("second input = %#v", bodies[1]["input"])
	}
	if len(input) <= 1 {
		t.Fatalf("HTTPS second request should be full replay, len=%d body=%#v", len(input), bodies[1])
	}
}

func TestOpen_websocketContinuationSendsDeltaAndPreservesTools(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var bodies []map[string]any
	var conns []*gorillawebsocket.Conn
	connCount := 0
	upgrader := gorillawebsocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gorillawebsocket.IsWebSocketUpgrade(r) {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		connCount++
		conns = append(conns, conn)
		mu.Unlock()
		defer func() { _ = conn.Close() }()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var body map[string]any
			if err := json.Unmarshal(data, &body); err != nil {
				return
			}
			mu.Lock()
			bodies = append(bodies, body)
			n := len(bodies)
			mu.Unlock()
			if n == 1 {
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_1"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
				continue
			}
			_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_2"}}`))
			_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_2","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
		}
	}))
	t.Cleanup(func() {
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		srv.CloseClientConnections()
		srv.Close()
	})

	be := backend.New(backend.Config{
		BaseURL:               srv.URL + "/backend-api/codex",
		AccessToken:           "tok",
		HTTPClient:            srv.Client(),
		Transport:             backend.TransportWebSocket,
		ExperimentalWebSocket: true,
	})
	call := lipapi.Call{
		ID:      "call_bbbbbbbbbbbbbbbb",
		Session: lipapi.SessionRef{ClientSessionID: "sess-ws-continuation"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("inspect")},
		}},
		Tools: []lipapi.ToolDef{{Name: "read"}},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	call.Messages = append(call.Messages,
		lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
			Kind:    lipapi.PartJSON,
			Content: json.RawMessage(`{"id":"fc_1","call_id":"call_fc_1","type":"function_call","name":"read","arguments":"{\"filePath\":\"a.go\"}"}`),
		}}},
		lipapi.Message{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
			Kind:       lipapi.PartToolResult,
			ToolCallID: "call_fc_1",
			Content:    json.RawMessage(`{"content":"package main"}`),
		}}},
	)
	es, err = be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	mu.Lock()
	defer mu.Unlock()
	if connCount != 1 {
		t.Fatalf("websocket connections = %d, want one reused connection", connCount)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if got, _ := bodies[1]["previous_response_id"].(string); got != "resp_1" {
		t.Fatalf("second previous_response_id = %q, body=%#v", got, bodies[1])
	}
	input, ok := bodies[1]["input"].([]any)
	if !ok {
		t.Fatalf("second input = %#v", bodies[1]["input"])
	}
	if len(input) != 1 {
		t.Fatalf("second input len = %d, body=%#v", len(input), bodies[1])
	}
	item, ok := input[0].(map[string]any)
	if !ok || item["type"] != "function_call_output" || item["call_id"] != "call_fc_1" {
		t.Fatalf("second delta input = %#v", input)
	}
	tools, ok := bodies[1]["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("WS continuation must preserve tools, got %#v", bodies[1]["tools"])
	}
}

func TestOpen_websocketStateIsIsolatedPerBackendInstance(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var bodies []map[string]any
	var conns []*gorillawebsocket.Conn
	connCount := 0
	upgrader := gorillawebsocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gorillawebsocket.IsWebSocketUpgrade(r) {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		connCount++
		conns = append(conns, conn)
		mu.Unlock()
		defer func() { _ = conn.Close() }()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var body map[string]any
			if err := json.Unmarshal(data, &body); err != nil {
				return
			}
			mu.Lock()
			bodies = append(bodies, body)
			n := len(bodies)
			mu.Unlock()
			switch n {
			case 1:
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_1"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
			case 2:
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_2"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_2","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
			}
		}
	}))
	t.Cleanup(func() {
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		srv.CloseClientConnections()
		srv.Close()
	})

	cfg := backend.Config{
		BaseURL:               srv.URL + "/backend-api/codex",
		AccessToken:           "tok",
		HTTPClient:            srv.Client(),
		Transport:             backend.TransportWebSocket,
		ExperimentalWebSocket: true,
	}
	be1 := backend.New(cfg)
	be2 := backend.New(cfg)
	call := lipapi.Call{
		ID:      "call_eeeeeeeeeeeeeeee",
		Session: lipapi.SessionRef{ClientSessionID: "sess-ws-instance-isolation"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("inspect")},
		}},
		Tools: []lipapi.ToolDef{{Name: "read"}},
	}
	es, err := be1.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	call.Messages = append(call.Messages,
		lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
			Kind:    lipapi.PartJSON,
			Content: json.RawMessage(`{"id":"fc_1","call_id":"call_fc_1","type":"function_call","name":"read","arguments":"{\"filePath\":\"a.go\"}"}`),
		}}},
		lipapi.Message{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
			Kind:       lipapi.PartToolResult,
			ToolCallID: "call_fc_1",
			Content:    json.RawMessage(`{"content":"package main"}`),
		}}},
	)
	es, err = be2.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	mu.Lock()
	defer mu.Unlock()
	if connCount != 2 {
		t.Fatalf("websocket connections = %d, want one connection per backend instance", connCount)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if _, ok := bodies[1]["previous_response_id"]; ok {
		t.Fatalf("second backend instance must not reuse first instance continuation: %#v", bodies[1])
	}
	input, ok := bodies[1]["input"].([]any)
	if !ok {
		t.Fatalf("second input = %#v", bodies[1]["input"])
	}
	if len(input) <= 1 {
		t.Fatalf("second backend instance should send full history, len=%d body=%#v", len(input), bodies[1])
	}
}

func TestOpen_websocketContinuationInvalidationRetriesFullPayload(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var bodies []map[string]any
	var conns []*gorillawebsocket.Conn
	connCount := 0
	upgrader := gorillawebsocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gorillawebsocket.IsWebSocketUpgrade(r) {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		connCount++
		conns = append(conns, conn)
		mu.Unlock()
		defer func() { _ = conn.Close() }()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var body map[string]any
			if err := json.Unmarshal(data, &body); err != nil {
				return
			}
			mu.Lock()
			bodies = append(bodies, body)
			n := len(bodies)
			mu.Unlock()
			switch n {
			case 1:
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_1"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
			case 2:
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_stale"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"error","error":{"code":"previous_response_not_found","message":"previous response not found"}}`))
				return
			case 3:
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_2"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_2","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
			}
		}
	}))
	t.Cleanup(func() {
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		srv.CloseClientConnections()
		srv.Close()
	})

	be := backend.New(backend.Config{
		BaseURL:               srv.URL + "/backend-api/codex",
		AccessToken:           "tok",
		HTTPClient:            srv.Client(),
		Transport:             backend.TransportAuto,
		ExperimentalWebSocket: true,
	})
	call := lipapi.Call{
		ID:      "call_dddddddddddddddd",
		Session: lipapi.SessionRef{ClientSessionID: "sess-ws-continuation-invalid"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("inspect")},
		}},
		Tools: []lipapi.ToolDef{{Name: "read"}},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	call.Messages = append(call.Messages,
		lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
			Kind:    lipapi.PartJSON,
			Content: json.RawMessage(`{"id":"fc_1","call_id":"call_fc_1","type":"function_call","name":"read","arguments":"{\"filePath\":\"a.go\"}"}`),
		}}},
		lipapi.Message{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
			Kind:       lipapi.PartToolResult,
			ToolCallID: "call_fc_1",
			Content:    json.RawMessage(`{"content":"package main"}`),
		}}},
	)
	es, err = be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	mu.Lock()
	defer mu.Unlock()
	if connCount != 2 {
		t.Fatalf("websocket connections = %d, want stale continuation retry on a fresh connection", connCount)
	}
	if len(bodies) != 3 {
		t.Fatalf("requests = %d, want initial + stale continuation + full retry", len(bodies))
	}
	if got, _ := bodies[1]["previous_response_id"].(string); got != "resp_1" {
		t.Fatalf("stale continuation previous_response_id = %q, body=%#v", got, bodies[1])
	}
	if _, ok := bodies[2]["previous_response_id"]; ok {
		t.Fatalf("full retry must drop previous_response_id after invalidation: %#v", bodies[2])
	}
	input, ok := bodies[2]["input"].([]any)
	if !ok {
		t.Fatalf("full retry input = %#v", bodies[2]["input"])
	}
	if len(input) <= 1 {
		t.Fatalf("full retry should replay history, len=%d body=%#v", len(input), bodies[2])
	}
}

func TestOpen_websocketContinuationReturnsAfterFirstEvent(t *testing.T) {
	t.Parallel()
	// Regression for a live OpenCode stall: strict WebSocket mode once waited for
	// committed output during continuation, so a fast response.created frame could
	// sit behind a blocked tool-result read until the frontend timed out. Strict WS
	// must return after the first canonical event and prepend it to the stream; only
	// auto mode waits longer so it can still fall back to HTTPS before commitment.
	var mu sync.Mutex
	var bodies []map[string]any
	var conns []*gorillawebsocket.Conn
	upgrader := gorillawebsocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gorillawebsocket.IsWebSocketUpgrade(r) {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		conns = append(conns, conn)
		mu.Unlock()
		defer func() { _ = conn.Close() }()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var body map[string]any
			if err := json.Unmarshal(data, &body); err != nil {
				return
			}
			mu.Lock()
			bodies = append(bodies, body)
			n := len(bodies)
			mu.Unlock()
			if n == 1 {
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_1"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}}`))
				_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"read","arguments":"{\"filePath\":\"a.go\"}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`))
				continue
			}
			_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_2"}}`))
			_, _, _ = conn.ReadMessage()
			return
		}
	}))
	t.Cleanup(func() {
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		srv.CloseClientConnections()
		srv.Close()
	})

	be := backend.New(backend.Config{
		BaseURL:               srv.URL + "/backend-api/codex",
		AccessToken:           "tok",
		HTTPClient:            srv.Client(),
		Transport:             backend.TransportWebSocket,
		ExperimentalWebSocket: true,
	})
	call := lipapi.Call{
		ID:      "call_cccccccccccccccc",
		Session: lipapi.SessionRef{ClientSessionID: "sess-ws-first-event"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("inspect")},
		}},
		Tools: []lipapi.ToolDef{{Name: "read"}},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	call.Messages = append(call.Messages,
		lipapi.Message{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
			Kind:    lipapi.PartJSON,
			Content: json.RawMessage(`{"id":"fc_1","call_id":"call_fc_1","type":"function_call","name":"read","arguments":"{\"filePath\":\"a.go\"}"}`),
		}}},
		lipapi.Message{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
			Kind:       lipapi.PartToolResult,
			ToolCallID: "call_fc_1",
			Content:    json.RawMessage(`{"content":"package main"}`),
		}}},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	es, err = be.Open(ctx, call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.4-mini"}})
	if err != nil {
		t.Fatalf("strict websocket continuation should return after response.created, got %v", err)
	}
	defer func() { _ = es.Close() }()
	if ev, err := es.Recv(context.Background()); err != nil || ev.Kind != lipapi.EventResponseStarted {
		t.Fatalf("first continuation event = (%v, %v), want response_started", ev.Kind, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if got, _ := bodies[1]["previous_response_id"].(string); got != "resp_1" {
		t.Fatalf("second previous_response_id = %q, body=%#v", got, bodies[1])
	}
}

func TestResolveCaps_returnsCodexBackendCaps(t *testing.T) {
	t.Parallel()
	be := backend.New(backend.Config{BaseURL: "http://127.0.0.1", AccessToken: "tok"})
	caps := be.ResolveCaps(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	for _, cap := range []lipapi.Capability{
		lipapi.CapabilityVision,
		lipapi.CapabilityDocuments,
		lipapi.CapabilityReasoning,
		lipapi.CapabilityTools,
	} {
		if _, ok := caps[cap]; !ok {
			t.Fatalf("missing %q in caps %v", cap, caps)
		}
	}
}

func TestModelInventory_builtinWhenNoneConfigured(t *testing.T) {
	t.Parallel()
	be := backend.New(backend.Config{BaseURL: "http://127.0.0.1", AccessToken: "tok"})
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(snap.Models))
	for _, m := range snap.Models {
		got = append(got, m.NativeID)
	}
	want := []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark"}
	if !slices.Equal(got, want) {
		t.Fatalf("builtin codex native IDs = %#v, want exactly %#v", got, want)
	}
}

func sampleCall() lipapi.Call {
	return lipapi.Call{
		ID: "sample",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
}

func drainEvents(t *testing.T, es lipapi.ManagedEventStream) []lipapi.Event {
	t.Helper()
	var out []lipapi.Event
	for {
		ev, err := es.Recv(context.Background())
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("recv: %v", err)
			}
			break
		}
		out = append(out, ev)
	}
	_ = es.Close()
	return out
}

func eventKinds(events []lipapi.Event) map[lipapi.EventKind]bool {
	out := make(map[lipapi.EventKind]bool, len(events))
	for _, ev := range events {
		out[ev.Kind] = true
	}
	return out
}
