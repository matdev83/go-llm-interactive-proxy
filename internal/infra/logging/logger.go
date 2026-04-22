package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	slogformatter "github.com/samber/slog-formatter"
	slogmulti "github.com/samber/slog-multi"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// Option configures [NewLogger].
type Option func(*loggerSettings)

type loggerSettings struct {
	otelTraceAttrs bool
}

// WithOTELTraceAttrs wraps the handler so each record includes trace_id and span_id
// from the OpenTelemetry span in context when tracing is enabled.
func WithOTELTraceAttrs(enabled bool) Option {
	return func(s *loggerSettings) {
		s.otelTraceAttrs = enabled
	}
}

// NewLogger builds a slog.Logger from validated [config.LoggingConfig] using a
// slog-multi Pipe with slog-formatter error normalization over JSON or text output.
func NewLogger(cfg config.LoggingConfig, w io.Writer, opts ...Option) (*slog.Logger, error) {
	var ls loggerSettings
	for _, o := range opts {
		if o != nil {
			o(&ls)
		}
	}

	if w == nil {
		w = os.Stdout
	}
	lvl, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	format := strings.ToLower(strings.TrimSpace(cfg.Format))
	handlerOpts := &slog.HandlerOptions{
		Level:     lvl,
		AddSource: cfg.AddSource,
	}
	var base slog.Handler
	switch format {
	case "json":
		base = slog.NewJSONHandler(w, handlerOpts)
	case "text":
		base = slog.NewTextHandler(w, handlerOpts)
	default:
		return nil, fmt.Errorf("logging: unsupported format %q", cfg.Format)
	}
	formatter := slogformatter.NewFormatterMiddleware(slogformatter.ErrorFormatter("error"))
	h := slogmulti.Pipe(formatter).Handler(base)
	if ls.otelTraceAttrs {
		h = otelTraceFieldsHandler{next: h}
	}
	return slog.New(h), nil
}

// otelTraceFieldsHandler adds OpenTelemetry trace and span ids to structured log output
// when the record context carries a valid span (stdout/JSON correlation with traces).
type otelTraceFieldsHandler struct {
	next slog.Handler
}

func (h otelTraceFieldsHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h otelTraceFieldsHandler) Handle(ctx context.Context, r slog.Record) error {
	if span := oteltrace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		sc := span.SpanContext()
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.next.Handle(ctx, r)
}

func (h otelTraceFieldsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return otelTraceFieldsHandler{next: h.next.WithAttrs(attrs)}
}

func (h otelTraceFieldsHandler) WithGroup(name string) slog.Handler {
	return otelTraceFieldsHandler{next: h.next.WithGroup(name)}
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging: unknown level %q", s)
	}
}
