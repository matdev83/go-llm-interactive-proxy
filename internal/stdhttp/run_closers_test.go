package stdhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
)

func TestRunClosers_panicContinuesReverseOrder(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var order []int
	closer := func(i int, fn func()) func() error {
		return func() error {
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			fn()
			return nil
		}
	}
	closers := []func() error{
		closer(0, func() {}),
		closer(1, func() { panic("closer1") }),
		closer(2, func() {}),
	}
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runClosers(log, closers)
	mu.Lock()
	got := append([]int(nil), order...)
	mu.Unlock()
	want := []int{2, 1, 0}
	if len(got) != len(want) {
		t.Fatalf("call order len=%d want %d: got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call order at %d: got %v want %v", i, got, want)
		}
	}
	if !strings.Contains(logBuf.String(), "isolated panic") {
		t.Fatalf("expected panic log, got %q", logBuf.String())
	}
}

func TestRunClosers_panic_logsIsolatedCrashAttrsNoPanicMessage(t *testing.T) {
	t.Parallel()
	closers := []func() error{
		func() error {
			panic("do-not-leak-this-string")
		},
	}
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError}))
	runClosers(log, closers)
	raw := logBuf.String()
	if !strings.Contains(raw, "stdhttp: isolated panic in resource closer") {
		t.Fatalf("missing expected msg, got %q", raw)
	}
	if strings.Contains(raw, "do-not-leak-this-string") {
		t.Fatalf("log must not include raw panic text")
	}
	var m map[string]any
	if err := json.Unmarshal(logBuf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["panic_boundary"] != string(safety.BoundaryWorker) {
		t.Fatalf("panic_boundary=%v want %q", m["panic_boundary"], safety.BoundaryWorker)
	}
	if m["operation"] != "resource_closer" {
		t.Fatalf("operation=%v want resource_closer", m["operation"])
	}
	if m["panic_value_type"] != "string" {
		t.Fatalf("panic_value_type=%v want string", m["panic_value_type"])
	}
	if ps, ok := m["panic_stack"].(string); !ok || ps == "" {
		t.Fatalf("panic_stack missing or empty: %#v", m["panic_stack"])
	}
	if _, has := m["panic_message"]; has {
		t.Fatalf("unexpected panic_message field")
	}
}

func TestRunClosers_errorsWarnOnceJoined(t *testing.T) {
	t.Parallel()
	e1 := errors.New("e1")
	e2 := errors.New("e2")
	closers := []func() error{
		func() error { return nil },
		func() error { return e1 },
		func() error { return e2 },
	}
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	runClosers(log, closers)
	raw := logBuf.String()
	if !strings.Contains(raw, "stdhttp: resource closer errors") {
		t.Fatalf("expected warn msg, got %q", raw)
	}
	if !strings.Contains(raw, "e1") || !strings.Contains(raw, "e2") {
		t.Fatalf("expected joined errors in log, got %q", raw)
	}
}

func TestRunClosers_panicThenError_bothReported(t *testing.T) {
	t.Parallel()
	e := errors.New("close err")
	closers := []func() error{
		func() error { return e },
		func() error { panic(42) },
		func() error { return nil },
	}
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runClosers(log, closers)
	raw := logBuf.String()
	if !strings.Contains(raw, "isolated panic") {
		t.Fatalf("expected panic log, got %q", raw)
	}
	if !strings.Contains(raw, "resource closer errors") {
		t.Fatalf("expected closer errors warn, got %q", raw)
	}
}

func TestRunClosers_panic_classifiesPanicError(t *testing.T) {
	t.Parallel()
	closers := []func() error{
		func() error { panic(struct{}{}) },
	}
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError}))
	runClosers(log, closers)
	var m map[string]any
	if err := json.Unmarshal(logBuf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["panic_boundary"] != string(safety.BoundaryWorker) {
		t.Fatalf("panic_boundary=%v want %q", m["panic_boundary"], safety.BoundaryWorker)
	}
	if m["operation"] != "resource_closer" {
		t.Fatalf("operation=%v want resource_closer", m["operation"])
	}
	if m["panic_value_type"] != "struct {}" {
		t.Fatalf("panic_value_type=%v", m["panic_value_type"])
	}
}
