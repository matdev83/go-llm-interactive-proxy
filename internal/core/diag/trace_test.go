package diag_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

func TestTraceID_emptyContext(t *testing.T) {
	t.Parallel()
	if got := diag.TraceID(nil); got != "" { //nolint:staticcheck // nil ctx is an explicit contract surface
		t.Fatalf("TraceID(nil) = %q, want empty", got)
	}
	if got := diag.TraceID(context.Background()); got != "" {
		t.Fatalf("TraceID(Background) = %q, want empty", got)
	}
	if got := diag.ALegID(nil); got != "" { //nolint:staticcheck // nil ctx is an explicit contract surface
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

func TestWithCallDiag_roundTrip(t *testing.T) {
	t.Parallel()
	ctx := diag.WithCallDiag(context.Background(), "tid-2", "aleg-2")
	if diag.TraceID(ctx) != "tid-2" {
		t.Fatalf("TraceID = %q", diag.TraceID(ctx))
	}
	if diag.ALegID(ctx) != "aleg-2" {
		t.Fatalf("ALegID = %q", diag.ALegID(ctx))
	}
}

func TestTraceIDGenerator_deterministicSequence(t *testing.T) {
	t.Parallel()
	g := diag.NewTraceIDGenerator()
	a := g.Next()
	b := g.Next()
	if a != "t_00000001" {
		t.Fatalf("first trace id = %q, want t_00000001", a)
	}
	if b != "t_00000002" {
		t.Fatalf("second trace id = %q, want t_00000002", b)
	}
}
