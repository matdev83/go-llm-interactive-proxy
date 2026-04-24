// Package runtime_test holds integration-style tests for the executor against real
// bundled-backend constructors (e.g. openairesponses) and composition helpers (pluginreg
// for default wire models). These imports are intentional: production code in
// internal/core/runtime does not depend on pluginreg or concrete backends; architecture
// tests use go list -deps -test=false, so this test-only package expands the dependency
// graph without violating introduce-hexagonal-architecture guardrails.
package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	openaibe "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	refopenai "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// candProbeBackend records the last routing.AttemptCandidate seen on Open and delegates to inner.
type candProbeBackend struct {
	inner   execbackend.Backend
	lastKey string
}

func wrapCandProbe(inner execbackend.Backend) (execbackend.Backend, *candProbeBackend) {
	p := &candProbeBackend{inner: inner}
	out := inner
	out.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
		p.lastKey = cand.Key
		return p.inner.Open(ctx, call, cand)
	}
	return out, p
}

func parseBearer(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

func TestExecutor_openAIResponses_candidateKeyStable_singleVsMultiKey(t *testing.T) {
	t.Parallel()
	const continuityKey = "cred-cand-probe"
	model := pluginreg.DefaultWireModel(openaibe.ID)
	selector := "openai-responses:" + model

	run := func(t *testing.T, apiKey string, apiKeys []string) string {
		t.Helper()
		srv := httptest.NewServer(refopenai.NewHandler(refopenai.Config{}))
		t.Cleanup(srv.Close)

		inner := openaibe.New(openaibe.Config{
			BaseURL:       srv.URL + "/v1",
			APIKey:        apiKey,
			APIKeys:       apiKeys,
			HTTPClient:    srv.Client(),
			SDKMaxRetries: new(int),
		})
		be, probe := wrapCandProbe(inner)

		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ex := &runtime.Executor{
			Store:    st,
			Bus:      hooks.New(hooks.Config{}),
			Rand:     routing.NewSeededRng(7),
			Backends: map[string]execbackend.Backend{openaibe.ID: be},
		}
		call := &lipapi.Call{
			Session: lipapi.SessionRef{ContinuityKey: continuityKey},
			Route:   lipapi.RouteIntent{Selector: selector},
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("hi")},
			}},
		}
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stream.Close() })
		if _, err := lipapi.Collect(context.Background(), stream); err != nil {
			t.Fatal(err)
		}
		if probe.lastKey == "" {
			t.Fatal("expected non-empty candidate key on Open")
		}
		if !strings.Contains(probe.lastKey, openaibe.ID) || !strings.Contains(probe.lastKey, model) {
			t.Fatalf("candidate key should reference backend and model, got %q", probe.lastKey)
		}
		return probe.lastKey
	}

	t.Run("single_key", func(t *testing.T) {
		t.Parallel()
		run(t, "sk-single", nil)
	})

	t.Run("multi_key_matches_single", func(t *testing.T) {
		t.Parallel()
		// Same route; second pool credential must not change routing candidate identity.
		k1 := run(t, "sk-a", []string{"sk-a", "sk-b"})
		k2 := run(t, "sk-a", []string{"sk-a", "sk-b"})
		if k1 != k2 {
			t.Fatalf("candidate keys differ across identical runs: %q vs %q", k1, k2)
		}
	})

	t.Run("multi_key_matches_single_key_mode", func(t *testing.T) {
		t.Parallel()
		single := run(t, "sk-only", nil)
		multi := run(t, "sk-only", []string{"sk-only", "sk-alt"})
		if single != multi {
			t.Fatalf("candidate key changed with credential pool: single=%q multi=%q", single, multi)
		}
	})
}

func TestExecutor_openAIResponses_attemptLineageOmitsCredentialMaterial(t *testing.T) {
	t.Parallel()
	const continuityKey = "cred-lineage-hygiene"
	const secretA = "__POOL_LINEAGE_SECRET_A__"
	const secretB = "__POOL_LINEAGE_SECRET_B__"
	selector := "openai-responses:" + pluginreg.DefaultWireModel(openaibe.ID)

	srv := httptest.NewServer(refopenai.NewHandler(refopenai.Config{}))
	t.Cleanup(srv.Close)

	be := openaibe.New(openaibe.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        secretA,
		APIKeys:       []string{secretA, secretB},
		HTTPClient:    srv.Client(),
		SDKMaxRetries: new(int),
	})

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := &runtime.Executor{
		Store:    st,
		Bus:      hooks.New(hooks.Config{}),
		Rand:     routing.NewSeededRng(11),
		Backends: map[string]execbackend.Backend{openaibe.ID: be},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: continuityKey},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	leg, err := st.ResolveALeg(context.Background(), continuityKey)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one attempt row")
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, forbidden := range []string{secretA, secretB} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("attempt lineage JSON must not contain credential material %q: %s", forbidden, s)
		}
	}
	// Pool-local credential ids (c0, c1, …) must not appear as standalone tokens (hex ids may contain "c1" as substring).
	if regexp.MustCompile(`\bc0\b`).MatchString(s) || regexp.MustCompile(`\bc1\b`).MatchString(s) {
		t.Fatalf("attempt lineage JSON must not contain pool credential id tokens: %s", s)
	}
	if !strings.Contains(s, openaibe.ID) {
		t.Fatalf("expected backend id in lineage for diagnostics, got: %s", s)
	}
}

func TestExecutor_openAIResponses_multiKeyPostOutputTruncatedStream_noThirdUpstreamHTTP(t *testing.T) {
	t.Parallel()
	const continuityKey = "cred-post-output-stream"
	selector := "openai-responses:" + pluginreg.DefaultWireModel(openaibe.ID)

	var reqs atomic.Int32
	// Minimal SSE: text delta commits output; then kill TCP — no response.completed / [DONE].
	const sseHead = "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r_trunc\",\"object\":\"response\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":\"it\",\"output_index\":0,\"delta\":\"x\"}\n\n"

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		switch parseBearer(r) {
		case "sk-429":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate","type":"requests","code":"rate_limit_exceeded"}}`))
			return
		case "sk-ok":
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(sseHead)); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("ResponseWriter is not a Hijacker")
				return
			}
			conn, bufw, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = bufw.Flush()
			_ = conn.Close()
			return
		default:
			http.Error(w, "unexpected bearer", http.StatusBadRequest)
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	be := openaibe.New(openaibe.Config{
		BaseURL:       srv.URL + "/v1",
		APIKey:        "sk-429",
		APIKeys:       []string{"sk-429", "sk-ok"},
		HTTPClient:    testkit.IntegrationHTTPClient(srv.Client()),
		SDKMaxRetries: new(int),
	})

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := &runtime.Executor{
		Store:    st,
		Bus:      hooks.New(hooks.Config{}),
		Rand:     routing.NewSeededRng(13),
		Backends: map[string]execbackend.Backend{openaibe.ID: be},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{ContinuityKey: continuityKey},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	ctx := context.Background()
	var sawText bool
	for {
		ev, rerr := stream.Recv(ctx)
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				if sawText {
					break
				}
				t.Fatalf("unexpected EOF before text: %v", rerr)
			}
			// After committed text, truncated upstream may surface as transport/SDK errors.
			if sawText {
				if lipapi.IsRecoverablePreOutput(rerr) {
					t.Fatalf("post-output failure must not be recoverable-pre-output for executor retry: %v", rerr)
				}
				break
			}
			t.Fatalf("recv before text: %v", rerr)
		}
		if ev.Kind == lipapi.EventTextDelta && strings.Contains(ev.Delta, "x") {
			sawText = true
		}
	}
	if !sawText {
		t.Fatal("expected at least one text delta before stream failure")
	}
	if n := reqs.Load(); n != 2 {
		t.Fatalf("upstream HTTP requests: got %d want 2 (429 on first credential, one streaming attempt on second; no third credential retry)", n)
	}
}
