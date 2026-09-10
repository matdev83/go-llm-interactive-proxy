package openaicompat_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicred"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestResolveEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		base    string
		flavor  openaicompat.Flavor
		want    string
		wantErr bool
	}{
		{
			name:   "responses_standard",
			base:   "https://api.openai.com/v1",
			flavor: openaicompat.FlavorResponses,
			want:   "https://api.openai.com/v1/responses",
		},
		{
			name:   "responses_trailing_slash",
			base:   "https://api.openai.com/v1/",
			flavor: openaicompat.FlavorResponses,
			want:   "https://api.openai.com/v1/responses",
		},
		{
			name:   "chat_standard",
			base:   "https://api.openai.com/v1",
			flavor: openaicompat.FlavorChat,
			want:   "https://api.openai.com/v1/chat/completions",
		},
		{
			name:   "chat_gateway_prefix",
			base:   "https://gateway.example.com/team/v1",
			flavor: openaicompat.FlavorChat,
			want:   "https://gateway.example.com/team/v1/chat/completions",
		},
		{
			name:    "empty_base",
			base:    "",
			flavor:  openaicompat.FlavorResponses,
			wantErr: true,
		},
		{
			name:    "invalid_scheme",
			base:    "ftp://example.com",
			flavor:  openaicompat.FlavorResponses,
			wantErr: true,
		},
		{
			name:    "relative_url",
			base:    "/v1/chat",
			flavor:  openaicompat.FlavorChat,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := openaicompat.ResolveEndpoint(tt.base, tt.flavor)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveEndpoint() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("ResolveEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildOutboundHeaders(t *testing.T) {
	t.Parallel()

	t.Run("streaming_with_secret", func(t *testing.T) {
		t.Parallel()
		h := openaicompat.BuildOutboundHeaders("sk-test-secret", true, nil)
		if got := h.Get("Authorization"); got != "Bearer sk-test-secret" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer sk-test-secret")
		}
		if got := h.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want %q", got, "application/json")
		}
		if got := h.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("Accept = %q, want %q", got, "text/event-stream")
		}
	})

	t.Run("non_streaming_no_secret", func(t *testing.T) {
		t.Parallel()
		h := openaicompat.BuildOutboundHeaders("", false, nil)
		if got := h.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
		if got := h.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept = %q, want %q", got, "application/json")
		}
	})

	t.Run("filters_forbidden_headers", func(t *testing.T) {
		t.Parallel()
		extra := http.Header{
			"Connection":        []string{"upgrade"},
			"Transfer-Encoding": []string{"chunked"},
			"Content-Length":    []string{"123"},
			"Expect":            []string{"100-continue"},
			"X-Session-ID":      []string{"sess-123"},
			"X-Resume-Token":    []string{"token-abc"},
			"X-Custom-Allowed":  []string{"val-xyz"},
		}
		h := openaicompat.BuildOutboundHeaders("sk-key", true, extra)
		if h.Get("Connection") != "" {
			t.Fatalf("Connection should be stripped")
		}
		if h.Get("Transfer-Encoding") != "" {
			t.Fatalf("Transfer-Encoding should be stripped")
		}
		if h.Get("Content-Length") != "" {
			t.Fatalf("Content-Length should be stripped")
		}
		if h.Get("Expect") != "" {
			t.Fatalf("Expect should be stripped")
		}
		if h.Get("X-Session-ID") != "" || h.Get("X-Resume-Token") != "" {
			t.Fatalf("session/resume control headers should be stripped")
		}
		if got := h.Get("X-Custom-Allowed"); got != "val-xyz" {
			t.Fatalf("X-Custom-Allowed = %q, want val-xyz", got)
		}
	})
}

func TestResolveHTTPClient(t *testing.T) {
	t.Parallel()

	custom := &http.Client{Timeout: 5 * time.Second}
	if got := openaicompat.ResolveHTTPClient(custom); got != custom {
		t.Fatalf("expected custom client returned directly")
	}

	gotDefault := openaicompat.ResolveHTTPClient(nil)
	if gotDefault == nil {
		t.Fatal("expected non-nil default client")
	}
	tr, ok := gotDefault.Transport.(*http.Transport)
	if !ok || !tr.ForceAttemptHTTP2 {
		t.Fatalf("expected Standard transport with ForceAttemptHTTP2=true")
	}
}

func TestPeekFirstEvent(t *testing.T) {
	t.Parallel()

	t.Run("prepends_event_successfully", func(t *testing.T) {
		t.Parallel()
		events := []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
			{Kind: lipapi.EventTextDelta, Delta: " world"},
		}
		base := lipapi.NewFixedEventStream(events)
		peeked, err := openaicompat.PeekFirstEvent(context.Background(), base)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if peeked == nil {
			t.Fatal("expected non-nil peeked stream")
		}

		ev1, err := peeked.Recv(context.Background())
		if err != nil || ev1.Delta != "hello" {
			t.Fatalf("ev1 = %v, err = %v, want hello", ev1, err)
		}
		ev2, err := peeked.Recv(context.Background())
		if err != nil || ev2.Delta != " world" {
			t.Fatalf("ev2 = %v, err = %v, want world", ev2, err)
		}
		_, err = peeked.Recv(context.Background())
		if err != io.EOF {
			t.Fatalf("expected EOF, got %v", err)
		}
	})

	t.Run("empty_stream_returns_eof", func(t *testing.T) {
		t.Parallel()
		base := lipapi.NewFixedEventStream(nil)
		_, err := openaicompat.PeekFirstEvent(context.Background(), base)
		if err != io.EOF {
			t.Fatalf("expected io.EOF on empty stream, got %v", err)
		}
	})
}

func TestParseStreamResponse_HTTPErrorClassification(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	rec.Header().Set("Retry-After", "90")
	rec.WriteHeader(http.StatusTooManyRequests)
	rec.WriteString(`{"error":{"message":"rate limit reached"}}`)
	resp := rec.Result()

	_, err := openaicompat.ParseStreamResponse("test-provider", resp, openaicompat.FlavorResponses, 100)
	if err == nil {
		t.Fatal("expected error for 429 status")
	}
	kind, ra := openaicred.ClassifyOpenAIAPIError(err)
	if kind != openaicred.FailureRateLimited || ra != "90" {
		t.Fatalf("kind = %v, retryAfter = %q, want FailureRateLimited, 90", kind, ra)
	}
}

func TestParseStreamResponse_ResponsesSSE(t *testing.T) {
	t.Parallel()

	ssePayload := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_123","status":"in_progress"}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"hello from sse"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_123","status":"completed"}}`,
		``,
		``,
	}, "\n")

	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusOK)
	rec.Header().Set("Content-Type", "text/event-stream")
	rec.WriteString(ssePayload)
	resp := rec.Result()

	stream, err := openaicompat.ParseStreamResponse("test-provider", resp, openaicompat.FlavorResponses, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream == nil {
		t.Fatal("expected non-nil stream")
	}

	peeked, err := openaicompat.PeekFirstEvent(context.Background(), stream)
	if err != nil {
		t.Fatalf("PeekFirstEvent error: %v", err)
	}

	var deltas []string
	for {
		ev, err := peeked.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected recv error: %v", err)
		}
		if ev.Kind == lipapi.EventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
	}
	if len(deltas) == 0 || deltas[0] != "hello from sse" {
		t.Fatalf("unexpected deltas: %v", deltas)
	}
}

func TestWireOpenPrimitives_Integration(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-wire-test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"wire response\"}\n\n")
	}))
	defer server.Close()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "sk-wire-test"},
	})
	if err != nil {
		t.Fatal(err)
	}

	prims := openaicompat.WireOpenPrimitives{
		ProviderID:        "test-provider",
		BaseURL:           server.URL + "/v1",
		Flavor:            openaicompat.FlavorResponses,
		Pool:              pool,
		HTTPClient:        httpclient.Standard(),
		RateLimitFallback: time.Minute,
		MaxPending:        50,
	}

	targetURL, err := prims.ResolveURL()
	if err != nil {
		t.Fatalf("ResolveURL failed: %v", err)
	}
	if targetURL != server.URL+"/v1/responses" {
		t.Fatalf("targetURL = %q, want %q", targetURL, server.URL+"/v1/responses")
	}

	stream, err := prims.Execute(context.Background(), func(ctx context.Context, cred credpool.Credential) (lipapi.ManagedEventStream, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(`{}`))
		if err != nil {
			return nil, err
		}
		req.Header = prims.BuildHeaders(cred.Secret, true)
		resp, err := prims.Client().Do(req)
		if err != nil {
			return nil, err
		}
		return prims.ParseAndPeekStream(ctx, resp)
	})
	if err != nil {
		t.Fatalf("prims.Execute failed: %v", err)
	}
	defer stream.Close()

	var textDelta string
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv failed: %v", err)
		}
		if ev.Kind == lipapi.EventTextDelta {
			textDelta = ev.Delta
			break
		}
	}
	if textDelta != "wire response" {
		t.Fatalf("textDelta = %q, want wire response", textDelta)
	}
}
