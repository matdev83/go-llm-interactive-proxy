package runtime_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

type countingVerifier struct {
	cnt atomic.Int32
}

func (c *countingVerifier) Verify(ctx context.Context, ev stopguard.Evidence) (stopguard.Verdict, error) {
	n := c.cnt.Add(1)
	if n == 1 {
		return stopguard.Verdict{
			Kind:               stopguard.VerdictContinue,
			RemainingObjective: "more text",
			Reason:             "continue to b2",
		}, nil
	}
	return stopguard.Verdict{
		Kind:   stopguard.VerdictAllowStop,
		Reason: "complete",
	}, nil
}

func setupE2EExecutorWithScriptedAttempts(t *testing.T, attempts [][]lipapi.Event, v stopguard.Verifier) *runtime.Executor {
	t.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	var callIdx atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"stub": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				idx := int(callIdx.Add(1) - 1)
				if idx < len(attempts) {
					return lipapi.NewFixedEventStream(attempts[idx]), nil
				}
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	if v != nil {
		ex.LoopGuardFactory = runtime.NewLoopGuardFactory(
			stopgate.Ports{Verifier: v, Now: time.Now},
			stopgate.Config{
				Enabled:                  true,
				ExplicitCompletionPolicy: stopguard.PolicyTrust,
				MaxSemanticContinuations: 3,
				NoProgressLimit:          2,
			},
		)
	}
	return ex
}

// TestAgentLoopGuard_FrontendE2E_RealRuntimeViaWire is the primary E2E:
// exercises real production runtime.Executor, retryRecvStream, LoopGuardFactory,
// and B1->B2 continuation leg opener wired to real frontend wire handlers.
func TestAgentLoopGuard_FrontendE2E_RealRuntimeViaWire(t *testing.T) {
	t.Parallel()

	standardAttempts := [][]lipapi.Event{
		// B1 attempt output
		{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventMessageStarted},
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
			{Kind: lipapi.EventResponseFinished, FinishReason: "raw_b1"},
		},
		// B2 continuation attempt output
		{
			{Kind: lipapi.EventTextDelta, Delta: " world"},
			{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
		},
	}

	t.Run("openailegacy wire B1 to B2 stitching", func(t *testing.T) {
		t.Parallel()
		verifier := &countingVerifier{}
		ex := setupE2EExecutorWithScriptedAttempts(t, standardAttempts, verifier)

		mux := http.NewServeMux()
		if err := openailegacy.Mount(mux, lipsdk.FrontendMountOptions{
			Exec:                 ex,
			DefaultRoute:         "stub:default",
			AllowUnauthenticated: true,
			ContinuationStore:    lipcont.NewMemoryStore(),
		}); err != nil {
			t.Fatalf("mount: %v", err)
		}

		req := httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"/v1/chat/completions",
			strings.NewReader(`{"model":"stub:default","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		wire := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d wire=%q", rec.Code, wire)
		}
		if strings.Count(wire, "[DONE]") != 1 {
			t.Fatalf("want exactly 1 [DONE] terminal, got %d wire=%q", strings.Count(wire, "[DONE]"), wire)
		}
		if !strings.Contains(wire, "hello") || !strings.Contains(wire, "world") {
			t.Fatalf("wire missing stitched text hello/world wire=%q", wire)
		}
		if verifier.cnt.Load() < 1 {
			t.Fatalf("verifier should have been called, got count %d", verifier.cnt.Load())
		}
	})

	t.Run("openresponses wire B1 to B2 stitching", func(t *testing.T) {
		t.Parallel()
		verifier := &countingVerifier{}
		ex := setupE2EExecutorWithScriptedAttempts(t, standardAttempts, verifier)

		mux := http.NewServeMux()
		if err := openresponses.Mount(mux, lipsdk.FrontendMountOptions{
			Exec:                 ex,
			DefaultRoute:         "stub:default",
			AllowUnauthenticated: true,
			ContinuationStore:    lipcont.NewMemoryStore(),
		}); err != nil {
			t.Fatalf("mount: %v", err)
		}

		req := httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"/openresponses/v1/responses",
			strings.NewReader(`{"model":"stub:default","input":"hi","stream":true,"store":false}`),
		)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		wire := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d wire=%q", rec.Code, wire)
		}
		if strings.Count(wire, "event: response.created") != 1 {
			t.Fatalf("want 1 event: response.created, got %d wire=%q", strings.Count(wire, "event: response.created"), wire)
		}
		if strings.Count(wire, "event: response.completed") != 1 {
			t.Fatalf("want 1 event: response.completed, got %d wire=%q", strings.Count(wire, "event: response.completed"), wire)
		}
		if !strings.Contains(wire, "hello") || !strings.Contains(wire, "world") {
			t.Fatalf("wire missing stitched text hello/world wire=%q", wire)
		}
	})

	t.Run("anthropic wire B1 to B2 stitching", func(t *testing.T) {
		t.Parallel()
		verifier := &countingVerifier{}
		ex := setupE2EExecutorWithScriptedAttempts(t, standardAttempts, verifier)

		mux := http.NewServeMux()
		if err := anthropic.Mount(mux, lipsdk.FrontendMountOptions{
			Exec:                 ex,
			DefaultRoute:         "stub:default",
			AllowUnauthenticated: true,
			ContinuationStore:    lipcont.NewMemoryStore(),
		}); err != nil {
			t.Fatalf("mount: %v", err)
		}

		req := httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"/v1/messages",
			strings.NewReader(`{"model":"stub:default","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
		)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		wire := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d wire=%q", rec.Code, wire)
		}
		if strings.Count(wire, "event: message_stop") != 1 {
			t.Fatalf("want 1 event: message_stop, got %d wire=%q", strings.Count(wire, "event: message_stop"), wire)
		}
		if !strings.Contains(wire, "hello") || !strings.Contains(wire, "world") {
			t.Fatalf("wire missing stitched text hello/world wire=%q", wire)
		}
	})

	t.Run("gemini wire B1 to B2 stitching", func(t *testing.T) {
		t.Parallel()
		verifier := &countingVerifier{}
		ex := setupE2EExecutorWithScriptedAttempts(t, standardAttempts, verifier)

		mux := http.NewServeMux()
		if err := gemini.Mount(mux, lipsdk.FrontendMountOptions{
			Exec:                 ex,
			DefaultRoute:         "stub:default",
			AllowUnauthenticated: true,
			ContinuationStore:    lipcont.NewMemoryStore(),
		}); err != nil {
			t.Fatalf("mount: %v", err)
		}

		req := httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"/v1beta/models/stub:default:streamGenerateContent?alt=sse",
			strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
		)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		wire := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d wire=%q", rec.Code, wire)
		}
		if !strings.Contains(wire, "hello") || !strings.Contains(wire, "world") {
			t.Fatalf("wire missing stitched text hello/world wire=%q", wire)
		}
	})

	t.Run("unsupported operation degrades to single final", func(t *testing.T) {
		t.Parallel()
		attempts := [][]lipapi.Event{
			// B1 attempt output
			{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventMessageStarted},
				{Kind: lipapi.EventTextDelta, Delta: "hello"},
				{Kind: lipapi.EventResponseFinished, FinishReason: "raw_b1"},
			},
			// B2 attempt should not be reached
			{
				{Kind: lipapi.EventTextDelta, Delta: "should not appear"},
				{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
			},
		}
		// Verifier tries to continue, but since operation is unsupported it must downgrade to final
		verifier := &countingVerifier{}
		ex := setupE2EExecutorWithScriptedAttempts(t, attempts, verifier)

		mux := http.NewServeMux()
		unsupportedExec := &unsupportedOpWrapper{inner: ex}
		if err := openailegacy.Mount(mux, lipsdk.FrontendMountOptions{
			Exec:                 unsupportedExec,
			DefaultRoute:         "stub:default",
			AllowUnauthenticated: true,
			ContinuationStore:    lipcont.NewMemoryStore(),
		}); err != nil {
			t.Fatalf("mount: %v", err)
		}

		req := httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"/v1/chat/completions",
			strings.NewReader(`{"model":"stub:default","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		wire := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d wire=%q", rec.Code, wire)
		}
		if strings.Count(wire, "[DONE]") != 1 {
			t.Fatalf("want 1 [DONE], got %d wire=%q", strings.Count(wire, "[DONE]"), wire)
		}
		if strings.Contains(wire, "should not appear") {
			t.Fatalf("unsupported operation should not stitch B2 text wire=%q", wire)
		}
		if !strings.Contains(wire, "hello") {
			t.Fatalf("wire missing B1 text wire=%q", wire)
		}
	})
}

type unsupportedOpWrapper struct {
	inner lipsdk.ExecutorView
}

func (u *unsupportedOpWrapper) Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	if call != nil {
		c := *call
		c.Invocation.Operation = lipapi.Operation("unknown.unsupported")
		return u.inner.Execute(ctx, &c)
	}
	return u.inner.Execute(ctx, call)
}

func (u *unsupportedOpWrapper) CancelALeg(ctx context.Context, req lipapi.ALegCancelRequest) error {
	return u.inner.CancelALeg(ctx, req)
}

func (u *unsupportedOpWrapper) WallClock() func() time.Time {
	return u.inner.WallClock()
}
