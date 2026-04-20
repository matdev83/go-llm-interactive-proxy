package diag_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

func TestTraceID_emptyContext(t *testing.T) {
	t.Parallel()
	if got := diag.TraceID(nil); got != "" {
		t.Fatalf("TraceID(nil) = %q, want empty", got)
	}
	if got := diag.TraceID(context.Background()); got != "" {
		t.Fatalf("TraceID(Background) = %q, want empty", got)
	}
	if got := diag.ALegID(nil); got != "" {
		t.Fatalf("ALegID(nil) = %q, want empty", got)
	}
}

func TestTraceID_roundTrip(t *testing.T) {
	t.Parallel()
	ctx := diag.WithTraceID(context.Background(), "tid-1")
	if diag.TraceID(ctx) != "tid-1" {
		t.Fatalf("TraceID = %q", diag.TraceID(ctx))
	}
	ctx = diag.WithALeg(ctx, "a_1")
	if diag.ALegID(ctx) != "a_1" {
		t.Fatalf("ALegID = %q", diag.ALegID(ctx))
	}
}

func TestNewTraceID_nonEmptyAndDistinct(t *testing.T) {
	t.Parallel()
	a := diag.NewTraceID()
	b := diag.NewTraceID()
	if a == "" || b == "" {
		t.Fatal("expected non-empty ids")
	}
	if a == b {
		t.Fatal("expected distinct ids")
	}
}
