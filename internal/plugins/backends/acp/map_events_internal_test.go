package acp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type ctxKey struct{}

type unarySpyTransport struct {
	mu          sync.Mutex
	lastCtx     context.Context
	errAtInvoke error
}

func (u *unarySpyTransport) CallUnary(ctx context.Context, body []byte, expectStatus int) ([]byte, error) {
	u.mu.Lock()
	u.errAtInvoke = ctx.Err()
	u.lastCtx = ctx
	u.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if expectStatus == http.StatusNoContent || expectStatus == http.StatusOK {
		return nil, nil
	}
	return nil, nil
}

func (u *unarySpyTransport) CallPromptStream(ctx context.Context, body []byte) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (u *unarySpyTransport) SendJSONRPC(ctx context.Context, body []byte) error {
	return nil
}

func (u *unarySpyTransport) last() context.Context {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastCtx
}

func (u *unarySpyTransport) invokeErr() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.errAtInvoke
}

func TestParseNDJSONLine_planEmitsReasoning(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"plan","entries":[]}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != lipapi.EventReasoningDelta {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_planDisabledYieldsWarning(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"plan","entries":[]}}}`
	mapper := mergeMapperOptions(Config{SessionUpdate: SessionUpdateMapperOptions{DisablePlanReasoning: true}})
	evs, err := parseNDJSONLine(context.Background(), mapper, line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != lipapi.EventWarning {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_chunkYieldsDelta(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != lipapi.EventTextDelta || evs[0].Delta != "hi" {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_textDelta(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"textDelta":"x"}}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Delta != "x" {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_terminal(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":10,"result":{"stopReason":"end_turn"}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != lipapi.EventResponseFinished {
		t.Fatalf("got %#v", evs)
	}
}

func TestPromptStream_signalCancelContextNotCanceledWithConsumer(t *testing.T) {
	t.Parallel()
	spy := &unarySpyTransport{}
	cli := &client{t: spy}
	parent := context.WithValue(context.Background(), ctxKey{}, "trace")
	body := io.NopCloser(strings.NewReader(""))
	s := newPromptNDJSONStream(parent, body, cli, "sid", 1, "mid", mergeMapperOptions(Config{}), nil, mergeCancelProfile(Config{}))

	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Recv(cctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("recv err: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for spy.last() == nil {
		if time.Now().After(deadline) {
			t.Fatal("cancelSession was not invoked")
		}
		time.Sleep(1 * time.Millisecond)
	}
	last := spy.last()
	if spy.invokeErr() != nil {
		t.Fatalf("cancel ctx should not inherit consumer cancellation at RPC start: %v", spy.invokeErr())
	}
	if _, ok := last.Deadline(); !ok {
		t.Fatal("expected bounded deadline on cancel RPC context")
	}
	if got, _ := last.Value(ctxKey{}).(string); got != "trace" {
		t.Fatalf("expected stream context value propagated, got %q", got)
	}
}
