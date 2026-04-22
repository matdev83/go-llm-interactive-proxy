package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	slogformatter "github.com/samber/slog-formatter"
	slogmulti "github.com/samber/slog-multi"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// NewLogger builds a slog.Logger from validated [config.LoggingConfig] using a
// slog-multi Pipe with slog-formatter error normalization over JSON or text output.
func NewLogger(cfg config.LoggingConfig, w io.Writer) (*slog.Logger, error) {
	if w == nil {
		w = os.Stdout
	}
	lvl, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	format := strings.ToLower(strings.TrimSpace(cfg.Format))
	opts := &slog.HandlerOptions{
		Level:     lvl,
		AddSource: cfg.AddSource,
	}
	var base slog.Handler
	switch format {
	case "json":
		base = slog.NewJSONHandler(w, opts)
	case "text":
		base = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("logging: unsupported format %q", cfg.Format)
	}
	formatter := slogformatter.NewFormatterMiddleware(slogformatter.ErrorFormatter("error"))
	h := slogmulti.Pipe(formatter).Handler(base)
	return slog.New(h), nil
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
