package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/logging"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestNewLogger_JSONContainsMessageAndLevel(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log, err := logging.NewLogger(config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "k", "v")
	line := strings.TrimSpace(buf.String())
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("json: %v\nline: %s", err, line)
	}
	if m["msg"] != "hello" {
		t.Fatalf("msg: %v", m["msg"])
	}
	if m["level"] != "INFO" && m["level"] != "info" {
		t.Fatalf("level: %v", m["level"])
	}
}

func TestNewLogger_recordsErrorField(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log, err := logging.NewLogger(config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Error("boom", "error", errSample{})
	line := strings.TrimSpace(buf.String())
	if !strings.Contains(line, `"msg":"boom"`) && !strings.Contains(line, `"msg": "boom"`) {
		t.Fatalf("expected msg in line: %s", line)
	}
	if !strings.Contains(line, "error") {
		t.Fatalf("expected error key in line: %s", line)
	}
}

type errSample struct{}

func (errSample) Error() string { return "sample" }

func TestNewLogger_withOTELTraceAttrs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log, err := logging.NewLogger(config.LoggingConfig{Level: "info", Format: "json"}, &buf,
		logging.WithOTELTraceAttrs(true))
	if err != nil {
		t.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider()
	tr := tp.Tracer("t")
	ctx, span := tr.Start(context.Background(), "op")
	defer span.End()
	log.InfoContext(ctx, "otel")
	line := strings.TrimSpace(buf.String())
	wantT := `"trace_id"`
	wantS := `"span_id"`
	if !strings.Contains(line, wantT) || !strings.Contains(line, wantS) {
		t.Fatalf("missing trace correlation in log line: %s", line)
	}
}
