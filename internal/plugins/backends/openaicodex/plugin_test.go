package openaicodex_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
				Primary: routing.Primary{Model: "gpt-5.3-codex"},
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
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
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
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}}
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
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
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
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
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
	})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
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
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
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

func TestResolveCaps_returnsCodexBackendCaps(t *testing.T) {
	t.Parallel()
	be := backend.New(backend.Config{BaseURL: "http://127.0.0.1", AccessToken: "tok"})
	caps := be.ResolveCaps(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
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
	found := false
	for _, m := range snap.Models {
		if m.NativeID == "gpt-5.3-codex" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("models: %+v", snap.Models)
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
