package stdhttp

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (rr *responseRecorder) WriteHeader(code int) {
	if rr.status == 0 {
		rr.status = code
	}
	rr.ResponseWriter.WriteHeader(code)
}

// accessLogMiddleware logs one structured line per request after Trace + RequestID
// middleware have run, so trace_id is available on r.Context().
func accessLogMiddleware(cfg *config.Config, log *slog.Logger, next http.Handler) http.Handler {
	if cfg == nil || log == nil || !cfg.Logging.AccessLog {
		return next
	}
	skips := cfg.Logging.AccessLogSkipPaths
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		for _, p := range skips {
			if p != "" && strings.HasPrefix(path, p) {
				next.ServeHTTP(w, r)
				return
			}
		}
		start := time.Now()
		rr := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rr, r)
		dur := time.Since(start)
		status := rr.status
		if status == 0 {
			status = http.StatusOK
		}
		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("path", path),
			slog.Int("status", status),
			slog.Int64("duration_ms", dur.Milliseconds()),
		}
		if tid := diag.TraceID(r.Context()); tid != "" {
			attrs = append(attrs, slog.String("trace_id", tid))
		}
		log.LogAttrs(r.Context(), slog.LevelInfo, "http.access", attrs...)
	})
}
