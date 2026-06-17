package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
)

func TestNewBackend_configErrorsReturnErrorBackendWithHostedCaps(t *testing.T) {
	t.Parallel()

	be := NewBackend(BackendSpec{ID: "test", APIKey: "sk-test"})
	assertHostedBackend(t, be)
	_, err := be.Open(context.Background(), invokeTestCall(), invokeTestCandidate("gpt-test"))
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("Open error = %v", err)
	}
}

func TestNewBackend_invalidCredentialConfigReturnsErrorBackend(t *testing.T) {
	t.Parallel()

	be := NewBackend(BackendSpec{
		ID:          "test",
		BaseURL:     "http://127.0.0.1",
		Credentials: []credpool.Credential{{ID: "dup", Secret: "sk-a"}, {ID: "dup", Secret: "sk-b"}},
	})
	assertHostedBackend(t, be)
	_, err := be.Open(context.Background(), invokeTestCall(), invokeTestCandidate("gpt-test"))
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("Open error = %v", err)
	}
}

func TestNewBackend_transportCapsIncludeChatAndResponses(t *testing.T) {
	t.Parallel()

	be := NewBackend(validBackendSpec("http://127.0.0.1"))
	if !be.TransportCaps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeStreaming) {
		t.Fatal("chat streaming support missing")
	}
	if !be.TransportCaps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeNonStreaming) {
		t.Fatal("chat non-streaming support missing")
	}
	if !be.TransportCaps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeStreaming) {
		t.Fatal("responses streaming support missing")
	}
	if !be.TransportCaps.Supports(lipapi.OperationOpenAIResponses, lipapi.TransportModeNonStreaming) {
		t.Fatal("responses non-streaming support missing")
	}
}

func TestNewBackend_resolveCapsUsesModelResolver(t *testing.T) {
	t.Parallel()

	be := NewBackend(validBackendSpec("http://127.0.0.1"))
	caps := be.ResolveCaps(context.Background(), invokeTestCall(), invokeTestCandidate("gpt-3.5-test"))
	if _, ok := caps[lipapi.CapabilityVision]; ok {
		t.Fatalf("expected hosted model narrowing to remove vision: %+v", caps)
	}
}

func TestNewBackend_routesByFlavor(t *testing.T) {
	t.Parallel()
	rec := newInvokeRecorder()
	srv := newInvokeServer(t, rec)

	be := NewBackend(validBackendSpec(srv.URL))
	chatCall := invokeTestCall()
	chatCall.Invocation.TransportMode = lipapi.TransportModeNonStreaming
	es, err := be.Open(context.Background(), chatCall, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasTextDelta(drainInvokeEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected chat route")
	}

	respCall := invokeTestCall()
	respCall.Invocation.TransportMode = lipapi.TransportModeNonStreaming
	respCall.Extensions = map[string]json.RawMessage{"flavor": json.RawMessage(`"responses"`)}
	es, err = be.Open(context.Background(), respCall, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasTextDelta(drainInvokeEvents(t, es), "responses-ns-ok") {
		t.Fatal("expected responses route")
	}
}

func TestNewBackend_nilContextReturnsProviderError(t *testing.T) {
	t.Parallel()

	be := NewBackend(validBackendSpec("http://127.0.0.1"))
	_, err := be.Open(nil, invokeTestCall(), invokeTestCandidate("gpt-test"))
	if err == nil {
		t.Fatal("expected nil context error")
	}
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
	if !strings.Contains(err.Error(), "test") {
		t.Fatalf("expected provider id in error, got %v", err)
	}
}

func TestNewBackend_forwardsRequestAndClientOptions(t *testing.T) {
	t.Parallel()
	rec := newInvokeRecorder()
	var mu sync.Mutex
	gotHeader := ""
	srv := newInvokeServerWithHook(t, rec, func(r *http.Request) {
		mu.Lock()
		gotHeader = r.Header.Get("X-Test-Client")
		mu.Unlock()
	})

	spec := validBackendSpec(srv.URL)
	spec.ClientOptions = func(lipapi.Call) []option.RequestOption {
		return []option.RequestOption{option.WithHeader("X-Test-Client", "yes")}
	}
	spec.RequestOptions = func(lipapi.Call) []option.RequestOption {
		return []option.RequestOption{option.WithJSONSet("max_tokens", 19), option.WithJSONDel("stream_options")}
	}
	be := NewBackend(spec)
	call := invokeTestCall()
	call.Invocation.TransportMode = lipapi.TransportModeNonStreaming
	_, err := be.Open(context.Background(), call, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	header := gotHeader
	mu.Unlock()
	if header != "yes" {
		t.Fatalf("client header = %q", header)
	}
	body := rec.lastBody(t)
	if !strings.Contains(string(body), `"max_tokens":19`) {
		t.Fatalf("request option not applied body=%s", string(body))
	}
	if strings.Contains(string(body), "stream_options") {
		t.Fatalf("stream_options should be removed body=%s", string(body))
	}
}

func TestNewBackend_authInvalidRetriesNextCredentialBeforeOutput(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	secrets := []string{}
	srv := newCredentialRetryServer(t, http.StatusUnauthorized, "", &mu, &secrets)
	spec := validBackendSpec(srv.URL)
	spec.APIKey = ""
	spec.Credentials = []credpool.Credential{{ID: "bad", Secret: "bad-key"}, {ID: "good", Secret: "good-key"}}
	be := NewBackend(spec)

	call := invokeTestCall()
	call.Invocation.TransportMode = lipapi.TransportModeNonStreaming
	es, err := be.Open(context.Background(), call, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasTextDelta(drainInvokeEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected retry success text")
	}

	mu.Lock()
	got := append([]string(nil), secrets...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "bad-key" || got[1] != "good-key" {
		t.Fatalf("credential attempts = %v", got)
	}
}

func TestNewBackend_rateLimitRetriesNextCredentialBeforeOutput(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	secrets := []string{}
	srv := newCredentialRetryServer(t, http.StatusTooManyRequests, "1", &mu, &secrets)
	spec := validBackendSpec(srv.URL)
	spec.APIKey = ""
	spec.Credentials = []credpool.Credential{{ID: "slow", Secret: "slow-key"}, {ID: "good", Secret: "good-key"}}
	spec.RateLimitFallback = time.Millisecond
	be := NewBackend(spec)

	call := invokeTestCall()
	call.Invocation.TransportMode = lipapi.TransportModeNonStreaming
	es, err := be.Open(context.Background(), call, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasTextDelta(drainInvokeEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected retry success text")
	}

	mu.Lock()
	got := append([]string(nil), secrets...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "slow-key" || got[1] != "good-key" {
		t.Fatalf("credential attempts = %v", got)
	}
}

func TestNewBackend_noUsableCredentialIsRecoverablePreOutput(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	secrets := []string{}
	srv := newCredentialRetryServer(t, http.StatusUnauthorized, "", &mu, &secrets)
	spec := validBackendSpec(srv.URL)
	spec.APIKey = ""
	spec.Credentials = []credpool.Credential{{ID: "bad", Secret: "bad-key"}}
	be := NewBackend(spec)
	call := invokeTestCall()
	call.Invocation.TransportMode = lipapi.TransportModeNonStreaming

	_, err := be.Open(context.Background(), call, invokeTestCandidate("gpt-test"))
	if !errors.Is(err, lipapi.ErrRecoverablePreOutput) {
		t.Fatalf("expected recoverable pre-output error, got %v", err)
	}
}

func validBackendSpec(baseURL string) BackendSpec {
	return BackendSpec{
		ID:                "test",
		BaseURL:           baseURL,
		APIKey:            "sk-test",
		RateLimitFallback: time.Millisecond,
		SDKMaxRetries:     intPtr(0),
		ResolveModel: func(cand routing.AttemptCandidate, _ lipapi.Call) string {
			return cand.Primary.Model
		},
		ResolveFlavor: func(call lipapi.Call) Flavor {
			if string(call.Extensions["flavor"]) == `"responses"` {
				return FlavorResponses
			}
			return FlavorChat
		},
	}
}

func assertHostedBackend(t *testing.T, be execbackend.Backend) {
	t.Helper()
	if be.Open == nil {
		t.Fatal("Open is nil")
	}
	if len(be.Caps) == 0 {
		t.Fatal("Caps are empty")
	}
	if be.ResolveCaps == nil {
		t.Fatal("ResolveCaps is nil")
	}
}

func newCredentialRetryServer(t *testing.T, status int, retryAfter string, mu *sync.Mutex, secrets *[]string) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		*secrets = append(*secrets, secret)
		attempt := len(*secrets)
		mu.Unlock()
		if attempt == 1 {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"retryable","type":"invalid_request_error","code":"retryable"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat-ns","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"chat-ns-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	})
	return httptest.NewServer(h)
}
