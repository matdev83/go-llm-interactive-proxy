package diag

import (
	"context"
	"log/slog"
)

// AttrOpts selects optional slog attributes beyond trace and A-leg from ctx.
type AttrOpts struct {
	CallID string
	BLegID string
}

// Attrs builds stable slog attributes for orchestration and lineage logs (Req 13.2, 13.3).
func Attrs(ctx context.Context, o AttrOpts) []slog.Attr {
	var out []slog.Attr
	if tid := TraceID(ctx); tid != "" {
		out = append(out, slog.String("trace_id", tid))
	}
	if aid := ALegID(ctx); aid != "" {
		out = append(out, slog.String("a_leg_id", aid))
	}
	if o.BLegID != "" {
		out = append(out, slog.String("b_leg_id", o.BLegID))
	}
	if o.CallID != "" {
		out = append(out, slog.String("call_id", o.CallID))
	}
	return out
}

// LogDecision emits a structured info log when log is non-nil (Req 13.3).
func LogDecision(ctx context.Context, log *slog.Logger, msg string, o AttrOpts, extra ...slog.Attr) {
	if log == nil {
		return
	}
	base := Attrs(ctx, o)
	attrs := make([]slog.Attr, 0, len(base)+len(extra))
	attrs = append(attrs, base...)
	attrs = append(attrs, extra...)
	log.LogAttrs(ctx, slog.LevelInfo, msg, attrs...)
}
