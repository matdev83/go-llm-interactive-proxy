package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

func TestDeferBootstrapTracingShutdown_nilResult(t *testing.T) {
	t.Parallel()
	deferBootstrapTracingShutdown(context.Background(), nil)
}

func TestDeferBootstrapTracingShutdown_nilShutdown(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	res := &runtimebundle.BootstrapResult{Logger: log, ShutdownTracing: nil}
	deferBootstrapTracingShutdown(context.Background(), res)
	if buf.Len() != 0 {
		t.Fatalf("unexpected log: %s", buf.String())
	}
}

func TestDeferBootstrapTracingShutdown_passesDeadlineContext(t *testing.T) {
	t.Parallel()
	var got context.Context
	res := &runtimebundle.BootstrapResult{
		ShutdownTracing: func(ctx context.Context) error {
			got = ctx
			return nil
		},
	}
	deferBootstrapTracingShutdown(context.Background(), res)
	if got == nil {
		t.Fatal("expected ShutdownTracing to be called")
	}
	deadline, ok := got.Deadline()
	if !ok {
		t.Fatal("expected shutdown context to carry a deadline")
	}
	wantMax := time.Now().Add(bootstrapTracingShutdownTimeout + time.Second)
	if deadline.After(wantMax) {
		t.Fatalf("deadline too far in future: %v", deadline)
	}
}

func TestDeferBootstrapTracingShutdown_warnsOnError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	shutdownErr := errors.New("shutdown failed")
	res := &runtimebundle.BootstrapResult{
		Logger: log,
		ShutdownTracing: func(context.Context) error {
			return shutdownErr
		},
	}
	deferBootstrapTracingShutdown(context.Background(), res)
	out := buf.String()
	if !strings.Contains(out, "lipstd: tracing shutdown") {
		t.Fatalf("missing message in log: %s", out)
	}
	if !strings.Contains(out, "shutdown failed") {
		t.Fatalf("missing error in log: %s", out)
	}
}

func TestDeferBootstrapTracingShutdown_noLoggerOnError(t *testing.T) {
	t.Parallel()
	res := &runtimebundle.BootstrapResult{
		Logger: nil,
		ShutdownTracing: func(context.Context) error {
			return fmt.Errorf("fail")
		},
	}
	deferBootstrapTracingShutdown(context.Background(), res)
}
