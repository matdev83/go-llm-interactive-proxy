package runtime_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestExecutor_InterleavedDiagnostics_HiddenFlowObservesTransitionsWithoutMemoLeakage(t *testing.T) {
	t.Parallel()

	const secretMemo = "SECRET_MEMO_PLAN_DO_NOT_LOG"

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	backends := map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(caps, nil, func() lipapi.ManagedEventStream {
			return executorTextStream("executor answer")
		}),
		"thinker-be": *interleavedBackendWithStream(caps, nil, func() lipapi.ManagedEventStream {
			return thinkerMemoStream(secretMemo)
		}),
	}

	logBuf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	ex.Log = log
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}
	ex.MemoStore = memoStore

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := interleavedBaseCall(selector)
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := interleavedBaseCall(selector)
	second.Session = lipapi.SessionRef{
		AuthoritativeSessionID: first.Session.AuthoritativeSessionID,
		ALegID:                 first.Session.ALegID,
		ClientSessionID:        first.Session.ClientSessionID,
		ResumeToken:            first.Session.ResumeToken,
	}
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("second collect: %v", err)
	}

	out := logBuf.String()
	for _, msg := range []string{
		"interleaved_route_selected",
		"interleaved_memo_captured",
		"interleaved_phase_transition",
		"interleaved_thinker_suppressed",
		"interleaved_memo_injected",
	} {
		if !strings.Contains(out, `"msg":"`+msg+`"`) && !strings.Contains(out, `"msg": "`+msg+`"`) {
			t.Fatalf("missing diagnostic %q in logs:\n%s", msg, out)
		}
	}
	// Turn-cycle observability: the selector is a two-slot cycle (executor,
	// thinker), so the first turn logs 1/2, the second logs 2/2.
	if !strings.Contains(out, `"interleaved_cycle_index":1`) || !strings.Contains(out, `"interleaved_cycle_total":2`) {
		t.Fatalf("missing footer cycle position in logs:\n%s", out)
	}
	if !strings.Contains(out, `"interleaved_cycle_index":2`) {
		t.Fatalf("missing second-turn cycle position in logs:\n%s", out)
	}
	// Injection observability: tail-anchored mode and the post-decrement budget.
	if !strings.Contains(out, `"injection_mode":"tail_anchored"`) {
		t.Fatalf("missing injection_mode=tail_anchored in logs:\n%s", out)
	}
	if !strings.Contains(out, `"turns_remaining":1`) {
		t.Fatalf("missing turns_remaining in logs:\n%s", out)
	}
	if strings.Contains(out, secretMemo) {
		t.Fatalf("memo body leaked into diagnostics: %s", out)
	}
	if strings.Contains(out, "Think step by step") {
		t.Fatalf("thinker instructions leaked into diagnostics: %s", out)
	}
}

func TestExecutor_InterleavedDiagnostics_ExpiredMemoEmitsExpiredWithoutBody(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)
	const secretMemo = "EXPIRED_SECRET_MEMO"

	var gotCall lipapi.Call
	capture := func(c lipapi.Call) { gotCall = c }

	logBuf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"exec-be": *interleavedBackendWithStream(
			lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
			capture,
			nil,
		),
	}
	ex.Log = log
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "Think step by step.",
		RegularTurnsRemaining: 2,
	}
	ex.MemoStore = memoStore

	first := interleavedBaseCall("[thinker]exec-be:m^exec-be:m")
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("seed execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("seed collect: %v", err)
	}
	aLegID := first.Session.ALegID

	memoRef, err := memoStore.Put(context.Background(), interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  secretMemo,
		SourceSelector:        "[thinker]exec-be:m^exec-be:m",
		Backend:               "exec-be",
		RegularTurnsRemaining: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle := thinkerCycleState(t, "[thinker]exec-be:m^exec-be:m", 0)
	if err := st.SetInterleavedState(context.Background(), aLegID, interleavedstate.State{
		Cycle:   cycle,
		MemoRef: &memoRef,
	}); err != nil {
		t.Fatal(err)
	}

	second := interleavedBaseCall("[thinker]exec-be:m^exec-be:m")
	resumeInterleavedCall(first, second)
	if _, err := ex.Execute(context.Background(), second); err != nil {
		t.Fatalf("second execute: %v", err)
	}
	_ = gotCall

	out := logBuf.String()
	if !strings.Contains(out, "interleaved_memo_expired") {
		t.Fatalf("missing interleaved_memo_expired in logs:\n%s", out)
	}
	if strings.Contains(out, secretMemo) {
		t.Fatalf("expired memo body leaked: %s", out)
	}
}

// emptyThinkerStream completes without ever emitting content deltas.
type emptyThinkerStream struct {
	phase int
}

func (s *emptyThinkerStream) Recv(context.Context) (lipapi.Event, error) {
	switch s.phase {
	case 0:
		s.phase++
		return lipapi.Event{Kind: lipapi.EventResponseStarted}, nil
	case 1:
		s.phase++
		return lipapi.Event{Kind: lipapi.EventResponseFinished}, nil
	default:
		return lipapi.Event{}, io.EOF
	}
}

func (s *emptyThinkerStream) Close() error { return nil }

func (s *emptyThinkerStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

// errorWithoutContentThinkerStream fails before emitting any content delta.
type errorWithoutContentThinkerStream struct {
	phase int
}

func (s *errorWithoutContentThinkerStream) Recv(context.Context) (lipapi.Event, error) {
	switch s.phase {
	case 0:
		s.phase++
		return lipapi.Event{Kind: lipapi.EventResponseStarted}, nil
	case 1:
		s.phase++
		return lipapi.Event{Kind: lipapi.EventMessageStarted}, nil
	default:
		return lipapi.Event{}, errors.New("thinker died before content")
	}
}

func (s *errorWithoutContentThinkerStream) Close() error { return nil }

func (s *errorWithoutContentThinkerStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

// TestExecutor_InterleavedDiagnostics_StoreSkipReasonsDifferentiated proves the
// memo store skip log distinguishes a stream interrupted before completion from
// a completed stream that simply produced no extractable memo.
func TestExecutor_InterleavedDiagnostics_StoreSkipReasonsDifferentiated(t *testing.T) {
	t.Parallel()

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
	emptyThinker := *interleavedBackendWithStream(caps, nil, func() lipapi.ManagedEventStream {
		return &emptyThinkerStream{}
	})
	errThinker := *interleavedBackendWithStream(caps, nil, func() lipapi.ManagedEventStream {
		return &errorWithoutContentThinkerStream{}
	})
	exec := *interleavedBackendWithStream(caps, nil, func() lipapi.ManagedEventStream {
		return executorTextStream("setup exec")
	})

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	logBuf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"exec-be":     exec,
		"empty-think": emptyThinker,
		"err-think":   errThinker,
	}
	ex.Log = log
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}
	ex.MemoStore = interleavedthinking.NewMemoStore(4096)

	// Round 1: turn 1 of a new session runs the executor slot (setup). The
	// resumed turn 2 runs the empty thinker, which completes without producing
	// content -> no_extractable_memo, and the executor continuation still runs.
	first := interleavedBaseCall("[thinker]empty-think:m^exec-be:m")
	stream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("empty-think setup execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("empty-think setup collect: %v", err)
	}
	second := interleavedBaseCall("[thinker]empty-think:m^exec-be:m")
	resumeInterleavedCall(first, second)
	stream, err = ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("empty-think execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("empty-think collect: %v", err)
	}

	// Round 2: a separate session's resumed turn errors before producing
	// content -> stream_interrupted. The client may see the interruption
	// directly or an exhausted-recovery error; the differentiated skip reason
	// is what this test asserts.
	first2 := interleavedBaseCall("[thinker]err-think:m^exec-be:m")
	stream2, err := ex.Execute(context.Background(), first2)
	if err != nil {
		t.Fatalf("err-think setup execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), stream2); err != nil {
		t.Fatalf("err-think setup collect: %v", err)
	}
	secondCall := interleavedBaseCall("[thinker]err-think:m^exec-be:m")
	resumeInterleavedCall(first2, secondCall)
	stream2, err = ex.Execute(context.Background(), secondCall)
	if err != nil {
		t.Fatalf("err-think execute: %v", err)
	}
	_, _ = lipapi.Collect(context.Background(), stream2)

	out := logBuf.String()
	if !strings.Contains(out, `"interleaved_memo_store_skipped"`) {
		t.Fatalf("missing interleaved_memo_store_skipped in logs:\n%s", out)
	}
	if !strings.Contains(out, `"memo_skip_reason":"no_extractable_memo"`) {
		t.Fatalf("missing no_extractable_memo skip reason in logs:\n%s", out)
	}
	if !strings.Contains(out, `"memo_skip_reason":"stream_interrupted"`) {
		t.Fatalf("missing stream_interrupted skip reason in logs:\n%s", out)
	}
}
