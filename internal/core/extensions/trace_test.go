package extensions

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestStartSpan_ok(t *testing.T) {
	t.Parallel()
	ctx, end := startSpan(context.Background(), "lip.extension.test_span")
	end(nil)
	_ = ctx
}

func TestStartSpan_withErr(t *testing.T) {
	t.Parallel()
	_, end := startSpan(context.Background(), "lip.extension.test_span_err")
	end(context.Canceled)
}

func TestStartSpan_nilParent(t *testing.T) {
	t.Parallel()
	//nolint:staticcheck // nil parent: startSpan must coerce to a usable context
	ctx, end := startSpan(nil, "lip.extension.nil_parent")
	defer end(nil)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
}

func TestStartSpan_setsAttributes(t *testing.T) {
	t.Parallel()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	const spanName = "lip.extension.attr_test"
	wantKey := attribute.Key("lip.extension.test_key")
	_, end := startSpan(context.Background(), spanName, attribute.String(string(wantKey), "v"))
	end(nil)

	var target sdktrace.ReadWriteSpan
	for _, s := range rec.Started() {
		if s.Name() == spanName {
			target = s
			break
		}
	}
	if target == nil {
		t.Fatalf("span %q not found among %d started spans", spanName, len(rec.Started()))
	}
	var found bool
	for _, a := range target.Attributes() {
		if a.Key == wantKey && a.Value.AsString() == "v" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing attribute %s on span", wantKey)
	}
}
