package openresponses_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type compactExecutor struct {
	calls  int
	call   *lipapi.Call
	stream lipapi.EventStream
	err    error
}

func (s *compactExecutor) Execute(_ context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	s.calls++
	s.call = call
	if s.err != nil {
		return nil, s.err
	}
	return s.stream, nil
}

type closeCountingCompactStream struct {
	events []lipapi.Event
	pos    int
	closes int
}

func (s *closeCountingCompactStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	if s.pos == len(s.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := s.events[s.pos]
	s.pos++
	return ev, nil
}

func (s *closeCountingCompactStream) Close() error {
	s.closes++
	return nil
}

type compactIDClock struct{}

func (compactIDClock) NewCompactResourceID() string { return "comp_proxy_1" }
func (compactIDClock) NewResponseID() string        { return "unused" }
func (compactIDClock) Now() time.Time               { return time.Unix(1_700_000_000, 0) }

func TestCompact_RoutesCanonicalOperationToNormalExecutor(t *testing.T) {
	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "compressed"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 8, OutputTokens: 3, TotalTokens: 11},
		{Kind: lipapi.EventResponseFinished},
	})
	executor := &compactExecutor{stream: stream}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		Executor:                executor,
		CompactResourceIDSource: compactIDClock{},
		ResponseClock:           compactIDClock{},
	})

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{
		"model":"gpt-4o",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"compact this"}]}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if executor.calls != 1 {
		t.Fatalf("executor calls=%d, want 1", executor.calls)
	}
	if executor.call == nil {
		t.Fatal("executor received nil call")
	}
	if got := executor.call.Invocation.Operation; got != lipapi.OperationContextCompaction {
		t.Fatalf("operation=%q, want %q", got, lipapi.OperationContextCompaction)
	}
	if got := executor.call.Invocation.TransportMode; got != lipapi.TransportModeNonStreaming {
		t.Fatalf("transport=%q, want %q", got, lipapi.TransportModeNonStreaming)
	}
	if !executor.call.HasItemAuthority() || len(executor.call.Items) != 1 {
		t.Fatalf("executor call lost ordered item authority: %+v", executor.call.Items)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"object":"response.compaction"`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"model":"gpt-4o"`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"total_tokens":11`)) {
		t.Fatalf("compact resource lost required fields: %s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"previous_response_id"`)) || bytes.Contains(rec.Body.Bytes(), []byte(`"store"`)) {
		t.Fatalf("compact resource must not create continuation metadata: %s", rec.Body.String())
	}
}

func TestCompact_AccumulatesUsageDeltas(t *testing.T) {
	stream := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 2, TotalTokens: 5, CacheReadTokens: 1, ReasoningTokens: 1},
		{Kind: lipapi.EventUsageDelta, InputTokens: 4, OutputTokens: 3, TotalTokens: 7, CacheReadTokens: 2, ReasoningTokens: 2},
		{Kind: lipapi.EventResponseFinished},
	})
	executor := &compactExecutor{stream: stream}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		Executor:                executor,
		CompactResourceIDSource: compactIDClock{},
		ResponseClock:           compactIDClock{},
	})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o","input":"compact this"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"input_tokens":7`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"output_tokens":5`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"total_tokens":12`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"cached_tokens":3`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"reasoning_tokens":3`)) {
		t.Fatalf("usage deltas were not accumulated: %s", rec.Body.String())
	}
}

func TestCompact_ClosesCanonicalStreamExactlyOnceAndSanitizesPreOutputFailure(t *testing.T) {
	stream := &closeCountingCompactStream{events: []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
	}}
	executor := &compactExecutor{stream: stream}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{Executor: executor})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o","input":"compact this"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusBadGateway)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("native_backend_secret")) {
		t.Fatalf("stream failure leaked provider detail: %s", rec.Body.String())
	}
	if stream.closes != 1 {
		t.Fatalf("stream closes=%d, want exactly 1", stream.closes)
	}
}

func TestCompact_UnsupportedCapabilityIsSanitizedBeforeResource(t *testing.T) {
	executor := &compactExecutor{err: &lipapi.RejectError{Reason: "provider-specific compaction secret"}}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{Executor: executor})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o","input":"compact this"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusBadGateway)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("provider-specific compaction secret")) {
		t.Fatalf("capability error leaked into response: %s", rec.Body.String())
	}
}

func TestCompact_ExecutorInvokedOnceForCanonicalOperation(t *testing.T) {
	executor := &compactExecutor{err: errors.New("native_backend_secret")}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{Executor: executor})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o","input":"compact this"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if executor.calls != 1 {
		t.Fatalf("executor calls=%d, want exactly 1", executor.calls)
	}
}
