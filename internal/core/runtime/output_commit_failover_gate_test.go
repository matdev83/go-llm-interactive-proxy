package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// failAfterCommittedStream emits prefix events then returns fail on the next Recv.
// Used to prove OutputCommitted gating: a recoverable-shaped error after a content
// event must not open a secondary candidate.
type failAfterCommittedStream struct {
	events []lipapi.Event
	i      int
	fail   error
}

func (s *failAfterCommittedStream) Recv(context.Context) (lipapi.Event, error) {
	if s.i < len(s.events) {
		ev := s.events[s.i]
		s.i++
		return ev, nil
	}
	if s.fail != nil {
		return lipapi.Event{}, s.fail
	}
	return lipapi.Event{}, io.EOF
}
func (*failAfterCommittedStream) Close() error { return nil }
func (*failAfterCommittedStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func commitGateBaseCall(selector string) *lipapi.Call {
	return &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
}

func newCommitGateExecutor(t *testing.T, primary, secondary execbackend.Backend) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.MaxAttempts = 3
	ex.Rand = routing.NewSeededRng(11)
	ex.Backends = map[string]execbackend.Backend{
		"primary":   primary,
		"secondary": secondary,
	}
	return ex
}

func commitGateOpenCounter(opens *atomic.Int64, open func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error)) execbackend.Backend {
	return execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return open(ctx, call, cand)
		},
	}
}

func secondarySuccessBackend(opens *atomic.Int64) execbackend.Backend {
	return commitGateOpenCounter(opens, func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventMessageStarted},
			{Kind: lipapi.EventTextDelta, Delta: "secondary-ok"},
			{Kind: lipapi.EventResponseFinished},
		}), nil
	})
}

func drainUntilError(t *testing.T, stream lipapi.EventStream) (committed []lipapi.Event, err error) {
	t.Helper()
	for {
		ev, rerr := stream.Recv(context.Background())
		if rerr != nil {
			return committed, rerr
		}
		if lipapi.OutputCommitted(ev) {
			committed = append(committed, ev)
		}
	}
}

func assertPostOutputNonRecoverable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected terminal error after committed output")
	}
	if lipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("post-commit error must not remain recoverable pre-output: %v", err)
	}
	var uf *lipapi.UpstreamFailure
	if !errors.As(err, &uf) {
		t.Fatalf("want UpstreamFailure classification, got %T %v", err, err)
	}
	if uf.Phase != lipapi.PhasePostOutput || uf.Recoverable {
		t.Fatalf("want PhasePostOutput non-recoverable, got phase=%q recoverable=%v", uf.Phase, uf.Recoverable)
	}
	msg := strings.ToLower(err.Error())
	for _, leak := range []string{"secret-opaque", "secret-text", `"summary"`, "rs_leak"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("error diagnostics must not echo reasoning/opaque body: %v", err)
		}
	}
}

// Positive control: RecoverablePreOutputError before any content-class event must
// open secondary. Proves the dual-candidate harness can failover when uncommitted.
// Drain manually (not Collect): primary may already have emitted response/message
// started before replacement, so a second response_started is expected on wire.
func TestOutputCommitFailoverGate_preOutputRecoverableReachesSecondary(t *testing.T) {
	t.Parallel()
	var primaryOpens, secondaryOpens atomic.Int64
	ex := newCommitGateExecutor(
		t,
		commitGateOpenCounter(&primaryOpens, func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &failAfterCommittedStream{
				events: []lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
				},
				fail: lipapi.RecoverablePreOutputError(errors.New("pre-output-temp")),
			}, nil
		}),
		secondarySuccessBackend(&secondaryOpens),
	)
	stream, err := ex.Execute(context.Background(), commitGateBaseCall("primary:m|secondary:m"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	var sawSecondaryText bool
	for {
		ev, rerr := stream.Recv(context.Background())
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			// Replacement may surface intermediate recoverable errors; keep draining.
			if lipapi.IsRecoverablePreOutput(rerr) {
				continue
			}
			t.Fatalf("unexpected recv error during pre-output failover: %v", rerr)
		}
		if ev.Kind == lipapi.EventTextDelta && ev.Delta == "secondary-ok" {
			sawSecondaryText = true
		}
	}
	if !sawSecondaryText {
		t.Fatal("positive control: secondary text must surface after recoverable pre-output primary failure")
	}
	if primaryOpens.Load() == 0 {
		t.Fatal("primary must open")
	}
	if secondaryOpens.Load() == 0 {
		t.Fatal("positive control: secondary must open after recoverable pre-output primary failure")
	}
}

func TestOutputCommitFailoverGate_textDeltaThenRecoverableBlocksSecondary(t *testing.T) {
	t.Parallel()
	var primaryOpens, secondaryOpens atomic.Int64
	ex := newCommitGateExecutor(
		t,
		commitGateOpenCounter(&primaryOpens, func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &failAfterCommittedStream{
				events: []lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "committed-visible"},
				},
				fail: lipapi.RecoverablePreOutputError(errors.New("would-retry-if-uncommitted")),
			}, nil
		}),
		secondarySuccessBackend(&secondaryOpens),
	)
	stream, err := ex.Execute(context.Background(), commitGateBaseCall("primary:m|secondary:m"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	committed, rerr := drainUntilError(t, stream)
	if len(committed) == 0 || committed[0].Kind != lipapi.EventTextDelta || committed[0].Delta != "committed-visible" {
		t.Fatalf("first committed event must be delivered TextDelta; got %#v", committed)
	}
	assertPostOutputNonRecoverable(t, rerr)
	if primaryOpens.Load() != 1 {
		t.Fatalf("primary opens=%d want 1", primaryOpens.Load())
	}
	if secondaryOpens.Load() != 0 {
		t.Fatalf("commit gate: secondary opens=%d want 0 (no failover after OutputCommitted TextDelta)", secondaryOpens.Load())
	}
}

func TestOutputCommitFailoverGate_reasoningPartThenRecoverableBlocksSecondary(t *testing.T) {
	t.Parallel()
	var primaryOpens, secondaryOpens atomic.Int64
	part := &lipapi.ReasoningPart{
		Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
		Opaque:  json.RawMessage(`{"id":"rs_gate","type":"reasoning","summary":[]}`),
	}
	ex := newCommitGateExecutor(
		t,
		commitGateOpenCounter(&primaryOpens, func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &failAfterCommittedStream{
				events: []lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventReasoningPart, Reasoning: part},
				},
				fail: lipapi.RecoverablePreOutputError(errors.New("would-retry-if-uncommitted")),
			}, nil
		}),
		secondarySuccessBackend(&secondaryOpens),
	)
	stream, err := ex.Execute(context.Background(), commitGateBaseCall("primary:m|secondary:m"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	committed, rerr := drainUntilError(t, stream)
	if len(committed) == 0 || committed[0].Kind != lipapi.EventReasoningPart || committed[0].Reasoning == nil {
		t.Fatalf("first committed event must be delivered EventReasoningPart; got %#v", committed)
	}
	assertPostOutputNonRecoverable(t, rerr)
	if primaryOpens.Load() != 1 {
		t.Fatalf("primary opens=%d want 1", primaryOpens.Load())
	}
	if secondaryOpens.Load() != 0 {
		t.Fatalf("commit gate: secondary opens=%d want 0 (no failover after OutputCommitted EventReasoningPart)", secondaryOpens.Load())
	}
}
