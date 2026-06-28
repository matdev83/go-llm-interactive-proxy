package runtime_test

import (
	"bytes"
	"context"
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

	ex := &runtime.Executor{
		Store:    st,
		Bus:      hooks.New(hooks.Config{}),
		Rand:     routing.NewSeededRng(2),
		Backends: backends,
		Log:      log,
		InterleavedConfig: interleavedthinking.ShapeConfig{
			Instructions:          "Think step by step.",
			StreamToClient:        "hidden",
			MaxMemoBytes:          4096,
			RegularTurnsRemaining: 2,
		},
		MemoStore: memoStore,
	}

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

	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"exec-be": *interleavedBackendWithStream(
				lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
				capture,
				nil,
			),
		},
		Log: log,
		InterleavedConfig: interleavedthinking.ShapeConfig{
			Instructions:          "Think step by step.",
			RegularTurnsRemaining: 2,
		},
		MemoStore: memoStore,
	}

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
